# Digest / scheduled heavy-compute jobs on the async platform (ADR 13). A daily
# EventBridge Scheduler cron enqueues a {"type":"digest.build"} message onto the
# shared jobs queue (aws_sqs_queue.contact, see storage.tf). The on-box worker
# consumes it and launches this run-to-completion Fargate task (the cmd/digest
# container), which fetches public Sources and submits them to the Anthropic
# Message Batches API (50% cheaper; ADR 13 Batch API amendment), writes a pending
# Result to S3, and exits. A second, recurring digest.collect cron (below) polls
# the in-flight batches and persists the completed digests. Nothing runs (and
# nothing bills) while idle, so these resources exist unconditionally; the
# schedules are toggled with var.enable_digest_schedule /
# var.enable_digest_collect_schedule.
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

# Resolve the SecureString injected via the container's secrets block:
# ANTHROPIC_API_KEY (the LLM key). The task takes no DB DSN — it never touches
# Postgres (ADR 13, Shape B), it writes its result to S3 for the worker to persist.
data "aws_iam_policy_document" "digest_exec_secret" {
  statement {
    sid     = "ReadDigestSecrets"
    effect  = "Allow"
    actions = ["ssm:GetParameters"]
    resources = [
      aws_ssm_parameter.secret["ANTHROPIC_API_KEY"].arn,
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

# Task role: the identity of the digest process itself. It fetches public Sources
# and calls api.anthropic.com over the internet (no AWS API for those), and writes
# its Result to S3 — so its only AWS permission is PutObject on the result prefix.
resource "aws_iam_role" "digest_task" {
  name               = "${var.project}-digest-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

# S3 result channel (ADR 13, Shape B): the task writes its Result JSON under the
# digest-results/ prefix of the assets bucket; the on-box worker reads and deletes
# it (the instance role already has GetObject/DeleteObject on the assets bucket).
# The task needs only PutObject on that prefix — least privilege, no DB reach.
data "aws_iam_policy_document" "digest_task_s3" {
  statement {
    sid       = "PutDigestResult"
    effect    = "Allow"
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.assets.arn}/${var.digest_result_prefix}*"]
  }
}

resource "aws_iam_role_policy" "digest_task_s3" {
  name   = "${var.project}-digest-task-s3"
  role   = aws_iam_role.digest_task.id
  policy = data.aws_iam_policy_document.digest_task_s3.json
}

# ---------------------------------------------------------------------------
# Digest task security group
# ---------------------------------------------------------------------------

# Egress-only: nobody dials the task (run-to-completion, no stream to connect
# to), so no ingress. All out covers the ECR image pull, SSM secret resolution,
# CloudWatch logs, api.anthropic.com, the public Source fetches, and the S3 PutObject
# for the result. The task never connects to Postgres (ADR 13, Shape B). It runs in
# a public subnet with a public IP (no NAT).
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

      # DIGEST_DATE, DIGEST_SOURCES, and DIGEST_RESULT_KEY are NOT baked here — the
      # worker passes them as RunTask container overrides at launch (the date decided
      # by the worker clock, the active Sources read on-box, and the per-run result
      # key). DIGEST_MODEL / DIGEST_MAX_TOKENS are also mirrored into SSM env_config
      # (see ssm.tf), baked here from the same variables so the two never drift.
      # S3_BUCKET / AWS_REGION let the task construct its S3 client to write the
      # result (ADR 13, Shape B).
      environment = [
        { name = "DIGEST_MODEL", value = var.digest_model },
        { name = "DIGEST_MAX_TOKENS", value = var.digest_max_tokens },
        { name = "S3_BUCKET", value = aws_s3_bucket.assets.bucket },
        { name = "AWS_REGION", value = var.aws_region },
      ]

      # ANTHROPIC_API_KEY is the LLM key. No DB secret: the task never touches
      # Postgres (ADR 13, Shape B) — it writes its result to S3 for the worker.
      secrets = [
        { name = "ANTHROPIC_API_KEY", valueFrom = aws_ssm_parameter.secret["ANTHROPIC_API_KEY"].arn },
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

# The recurring digest.collect trigger (ADR 13, Batch API amendment). digest.build
# now submits an async Anthropic Message Batch (50% cheaper) instead of a
# synchronous call, so a second, more frequent schedule polls the in-flight batches
# and persists the completed digests. It reuses the same scheduler role and jobs
# queue; the worker handles it inline (the box already has ANTHROPIC_API_KEY and
# owns Postgres — no Fargate task and no new IAM). collect drains ALL pending
# digests each run, so a missed tick self-heals on the next one. Must be ENABLED
# for batch-mode digests to ever leave the pending state.
resource "aws_scheduler_schedule" "digest_collect" {
  name  = "${var.project}-digest-collect"
  state = var.enable_digest_collect_schedule ? "ENABLED" : "DISABLED"

  flexible_time_window {
    mode = "OFF"
  }

  schedule_expression          = var.digest_collect_schedule_expression
  schedule_expression_timezone = var.digest_collect_schedule_timezone

  target {
    arn      = aws_sqs_queue.contact.arn
    role_arn = aws_iam_role.scheduler.arn
    input    = jsonencode({ type = "digest.collect", payload = {} })
  }
}
