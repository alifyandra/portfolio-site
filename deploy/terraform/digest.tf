# Digest / scheduled heavy-compute jobs on the async platform (ADR 13). A daily
# EventBridge Scheduler cron enqueues a {"type":"digest.build"} message onto the
# shared jobs queue (aws_sqs_queue.contact, see storage.tf). The on-box worker
# consumes it and launches this run-to-completion Fargate task (the cmd/digest
# container), which fetches public Sources, summarizes them with the Anthropic
# API, writes a Digest row to Postgres, and exits. Nothing runs (and nothing
# bills) while idle, so these resources exist unconditionally; the schedule
# itself is toggled with var.enable_digest_schedule.
#
# This is the run-to-completion Fargate mode (ADR 13), distinct from the WhatsApp
# run-and-connect mode (ADR 11). It REUSES the ECS cluster, VPC/networking, and
# the ecs-tasks assume-role doc from whatsapp.tf; only the workload-specific
# resources are new here.

# ---------------------------------------------------------------------------
# ECR: where Slice E pushes the digest image
# ---------------------------------------------------------------------------

resource "aws_ecr_repository" "digest" {
  name                 = "${var.project}-digest"
  image_tag_mutability = "MUTABLE" # the image is pushed and pulled as :latest

  image_scanning_configuration {
    scan_on_push = true
  }
}

# Cost hygiene: drop untagged layers quickly and keep only a few recent images.
resource "aws_ecr_lifecycle_policy" "digest" {
  repository = aws_ecr_repository.digest.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Expire untagged images after 7 days"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = 7
        }
        action = { type = "expire" }
      },
      {
        rulePriority = 2
        description  = "Keep only the last 5 images"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = 5
        }
        action = { type = "expire" }
      },
    ]
  })
}

# ---------------------------------------------------------------------------
# Logs (the ECS cluster is shared with the WhatsApp sidecar, see whatsapp.tf)
# ---------------------------------------------------------------------------

resource "aws_cloudwatch_log_group" "digest" {
  name              = "/ecs/${var.project}-digest"
  retention_in_days = 14
}

# ---------------------------------------------------------------------------
# ECS roles (the ecs-tasks assume-role doc lives in whatsapp.tf)
# ---------------------------------------------------------------------------

# Execution role: what the ECS agent uses to pull the image, ship logs, and
# resolve the container's secrets before the digest process starts.
resource "aws_iam_role" "digest_exec" {
  name               = "${var.project}-digest-exec"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

# ECR pull + CloudWatch Logs for the awslogs driver.
resource "aws_iam_role_policy_attachment" "digest_exec_managed" {
  role       = aws_iam_role.digest_exec.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# Resolve the SecureStrings injected via the container's secrets block:
# ANTHROPIC_API_KEY (LLM key) and DIGEST_DATABASE_URL (on-box Postgres DSN).
data "aws_iam_policy_document" "digest_exec_secret" {
  statement {
    sid     = "ReadDigestSecrets"
    effect  = "Allow"
    actions = ["ssm:GetParameters"]
    resources = [
      aws_ssm_parameter.secret["ANTHROPIC_API_KEY"].arn,
      aws_ssm_parameter.secret["DIGEST_DATABASE_URL"].arn,
    ]
  }

  # Decrypt the SecureStrings (AWS-managed aws/ssm key), same pattern as the
  # instance role's DecryptAppEnv and the wa_exec role's DecryptSidecarSecret.
  statement {
    sid       = "DecryptDigestSecrets"
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = ["*"]
    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${var.aws_region}.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "digest_exec_secret" {
  name   = "${var.project}-digest-exec-secret"
  role   = aws_iam_role.digest_exec.id
  policy = data.aws_iam_policy_document.digest_exec_secret.json
}

# Task role: the identity of the digest process itself. It talks to Postgres
# over the network and to api.anthropic.com over the internet, and makes no AWS
# API calls, so it gets no policies (like wa_task) — least privilege.
resource "aws_iam_role" "digest_task" {
  name               = "${var.project}-digest-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

# ---------------------------------------------------------------------------
# Digest task security group
# ---------------------------------------------------------------------------

# Egress-only: nobody dials the task (run-to-completion, no stream to connect
# to), so no ingress. All out covers the ECR image pull, SSM secret resolution,
# CloudWatch logs, api.anthropic.com, and the outbound connection to Postgres on
# the box (inbound to the box is authorized by the 5432 rule on the web SG, see
# network.tf). The task runs in a public subnet with a public IP (no NAT).
resource "aws_security_group" "digest" {
  name        = "${var.project}-digest"
  description = "Digest task: no ingress, all egress."
  vpc_id      = aws_vpc.main.id

  egress {
    description      = "All outbound"
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }

  tags = { Name = "${var.project}-digest" }
}

# ---------------------------------------------------------------------------
# Task definition
# ---------------------------------------------------------------------------

resource "aws_ecs_task_definition" "digest" {
  family                   = "${var.project}-digest"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "512"
  memory                   = "1024"
  execution_role_arn       = aws_iam_role.digest_exec.arn
  task_role_arn            = aws_iam_role.digest_task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64" # the digest image cross-compiles to arm64
  }

  container_definitions = jsonencode([
    {
      name      = "digest"
      essential = true
      image     = "${aws_ecr_repository.digest.repository_url}:latest"

      # The pushed image is the shared backend image (backend/Dockerfile, which
      # builds api/worker/seed/digest side by side). Select the digest binary
      # here; without this the task would run the image's default entrypoint.
      command = ["digest"]

      # DIGEST_DATE is NOT baked here — the worker passes it as a RunTask
      # container override at launch (idempotent-by-date upsert, ADR 13).
      # DIGEST_MODEL / DIGEST_MAX_TOKENS are also mirrored into SSM env_config
      # (see ssm.tf) so the worker can pass them as overrides if it prefers;
      # baked here from the same variables so the two never drift.
      environment = [
        { name = "DIGEST_MODEL", value = var.digest_model },
        { name = "DIGEST_MAX_TOKENS", value = var.digest_max_tokens },
      ]

      # DATABASE_URL points at the box's PRIVATE IP (not the docker-network name
      # "postgres"), so it can't reuse the app's DATABASE_URL secret. It comes
      # from a dedicated SecureString the maintainer seeds with the box IP baked
      # in (see the README runbook). ANTHROPIC_API_KEY is the LLM key.
      secrets = [
        { name = "ANTHROPIC_API_KEY", valueFrom = aws_ssm_parameter.secret["ANTHROPIC_API_KEY"].arn },
        { name = "DATABASE_URL", valueFrom = aws_ssm_parameter.secret["DIGEST_DATABASE_URL"].arn },
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.digest.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "digest"
        }
      }
    },
  ])
}

# ---------------------------------------------------------------------------
# Backend instance-role additions (digest RunTask orchestration)
# ---------------------------------------------------------------------------

# A separate inline policy on the existing backend instance role, parallel to
# aws_iam_role_policy.instance_wa, so the digest orchestration permissions stay
# isolated. The worker (running as aws_iam_role.instance) calls RunTask +
# DescribeTasks. No ec2:DescribeNetworkInterfaces is needed: run-to-completion
# never resolves the task's ENI/private IP (unlike the WhatsApp launcher).
data "aws_iam_policy_document" "instance_digest" {
  statement {
    sid       = "RunDigestTask"
    effect    = "Allow"
    actions   = ["ecs:RunTask"]
    resources = ["arn:aws:ecs:${var.aws_region}:${local.account_id}:task-definition/${var.project}-digest:*"]
    # Limit RunTask to the shared cluster (RunTask authorizes the task-def
    # resource; the cluster arrives as a condition key).
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [aws_ecs_cluster.wa.arn]
    }
  }

  # Poll task state until STOPPED and read the exit code; optionally kill a run
  # that overruns the hard cap. Scoped to the shared cluster's task ARNs — this
  # overlaps aws_iam_role_policy.instance_wa's ManageSidecarTasks (same cluster),
  # kept explicit here so the digest slice is self-contained (IAM is additive).
  statement {
    sid       = "ManageDigestTasks"
    effect    = "Allow"
    actions   = ["ecs:DescribeTasks", "ecs:StopTask"]
    resources = ["arn:aws:ecs:${var.aws_region}:${local.account_id}:task/${var.project}-wa/*"]
  }

  # RunTask hands the exec + task roles to ECS, which requires PassRole scoped
  # to the ecs-tasks service.
  statement {
    sid       = "PassDigestRoles"
    effect    = "Allow"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.digest_exec.arn, aws_iam_role.digest_task.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "instance_digest" {
  name   = "${var.project}-instance-digest"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.instance_digest.json
}

# ---------------------------------------------------------------------------
# EventBridge Scheduler: daily cron -> SQS (the shared jobs queue)
# ---------------------------------------------------------------------------

# Scheduler service role: assume scheduler.amazonaws.com and SendMessage to the
# jobs queue only.
data "aws_iam_policy_document" "scheduler_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "scheduler" {
  name               = "${var.project}-scheduler"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume.json
}

data "aws_iam_policy_document" "scheduler" {
  statement {
    sid       = "EnqueueDigestJob"
    effect    = "Allow"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.contact.arn]
  }
}

resource "aws_iam_role_policy" "scheduler" {
  name   = "${var.project}-scheduler"
  role   = aws_iam_role.scheduler.id
  policy = data.aws_iam_policy_document.scheduler.json
}

# The daily digest.build trigger. state is toggled by var.enable_digest_schedule
# (default DISABLED for a gated slice) rather than count, so flipping it on/off
# is an in-place update, not a resource churn. The Input is the Job envelope the
# worker dispatches on (matches the contact.notify envelope shape).
resource "aws_scheduler_schedule" "digest_build" {
  name  = "${var.project}-digest-build"
  state = var.enable_digest_schedule ? "ENABLED" : "DISABLED"

  flexible_time_window {
    mode = "OFF"
  }

  schedule_expression          = var.digest_schedule_expression
  schedule_expression_timezone = var.digest_schedule_timezone

  target {
    arn      = aws_sqs_queue.contact.arn
    role_arn = aws_iam_role.scheduler.arn
    input    = jsonencode({ type = "digest.build", payload = {} })
  }
}
