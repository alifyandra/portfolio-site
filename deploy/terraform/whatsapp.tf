# WhatsApp sidecar on Fargate (ADR 11). The whatsapp-web.js + Chromium sidecar
# is too heavy for the t4g.micro, so it runs as an on-demand Fargate task: the
# backend calls ecs:RunTask when a batch starts, streams to the task's private
# IP on 8081, then stops it. Nothing runs (and nothing bills) while idle, so
# these resources exist unconditionally (no feature flag).

# ---------------------------------------------------------------------------
# ECR: where Slice B pushes the sidecar image
# ---------------------------------------------------------------------------

resource "aws_ecr_repository" "wa_sidecar" {
  name                 = "${var.project}-wa-sidecar"
  image_tag_mutability = "MUTABLE" # the sidecar is pushed and pulled as :latest

  image_scanning_configuration {
    scan_on_push = true
  }
}

# Cost hygiene: drop untagged layers quickly and keep only a few recent images.
resource "aws_ecr_lifecycle_policy" "wa_sidecar" {
  repository = aws_ecr_repository.wa_sidecar.name

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
# ECS cluster + logs
# ---------------------------------------------------------------------------

resource "aws_ecs_cluster" "wa" {
  name = "${var.project}-wa"

  # Container Insights bills per metric; the sidecar's CloudWatch logs are enough.
  setting {
    name  = "containerInsights"
    value = "disabled"
  }
}

resource "aws_cloudwatch_log_group" "wa_sidecar" {
  name              = "/ecs/${var.project}-wa-sidecar"
  retention_in_days = 14
}

# ---------------------------------------------------------------------------
# ECS roles
# ---------------------------------------------------------------------------

# Assume policy shared by both the execution role and the task role.
data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

# Execution role: what the ECS agent uses to pull the image, ship logs, and
# resolve the container's secrets before the sidecar process starts.
resource "aws_iam_role" "wa_exec" {
  name               = "${var.project}-wa-exec"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

# ECR pull + CloudWatch Logs for the awslogs driver.
resource "aws_iam_role_policy_attachment" "wa_exec_managed" {
  role       = aws_iam_role.wa_exec.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

# Resolve the WA_SIDECAR_SECRET SecureString for the container's secrets block.
data "aws_iam_policy_document" "wa_exec_secret" {
  statement {
    sid       = "ReadSidecarSecret"
    effect    = "Allow"
    actions   = ["ssm:GetParameters"]
    resources = [aws_ssm_parameter.secret["WA_SIDECAR_SECRET"].arn]
  }

  # Decrypt the SecureString (AWS-managed aws/ssm key), same pattern as the
  # instance role's DecryptAppEnv statement.
  statement {
    sid       = "DecryptSidecarSecret"
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

resource "aws_iam_role_policy" "wa_exec_secret" {
  name   = "${var.project}-wa-exec-secret"
  role   = aws_iam_role.wa_exec.id
  policy = data.aws_iam_policy_document.wa_exec_secret.json
}

# Task role: the identity of the sidecar process itself. It makes no AWS API
# calls (it only talks to WhatsApp and streams back to the backend), so it gets
# no policies — least privilege.
resource "aws_iam_role" "wa_task" {
  name               = "${var.project}-wa-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
}

# ---------------------------------------------------------------------------
# Sidecar security group
# ---------------------------------------------------------------------------

resource "aws_security_group" "wa_sidecar" {
  name        = "${var.project}-wa-sidecar"
  description = "Sidecar task: 8081 in from the backend only, all out."
  vpc_id      = aws_vpc.main.id

  # The backend streams NDJSON to the task's private IP on 8081; only the
  # backend EC2's SG may reach it (no CIDR, so it stays private to the VPC).
  ingress {
    description     = "Sidecar HTTP from the backend host"
    from_port       = 8081
    to_port         = 8081
    protocol        = "tcp"
    security_groups = [aws_security_group.web.id]
  }

  # All out: WhatsApp traffic, plus ECR image pull / SSM / CloudWatch. The task
  # runs in a public subnet with a public IP (there is no NAT gateway).
  egress {
    description      = "All outbound"
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }

  tags = { Name = "${var.project}-wa-sidecar" }
}

# ---------------------------------------------------------------------------
# Task definition
# ---------------------------------------------------------------------------

resource "aws_ecs_task_definition" "wa_sidecar" {
  family                   = "${var.project}-wa-sidecar"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = "512"
  memory                   = "2048"
  execution_role_arn       = aws_iam_role.wa_exec.arn
  task_role_arn            = aws_iam_role.wa_task.arn

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = "ARM64" # the sidecar image is arm64
  }

  # Fargate cannot set --shm-size, so Chromium's --disable-dev-shm-usage launch
  # flag is baked into the sidecar image (Slice B), not configured here.
  container_definitions = jsonencode([
    {
      name      = "sidecar"
      essential = true
      image     = "${aws_ecr_repository.wa_sidecar.repository_url}:latest"

      portMappings = [
        { containerPort = 8081, protocol = "tcp" },
      ]

      # CHROME_PATH is baked into the image, so it is not set here.
      # WA_SHUTDOWN_AFTER_SESSION makes the sidecar exit(0) after its single
      # session tears down, so an orphaned task self-terminates if the backend
      # dies before it can StopTask (the primary teardown). Orphan backstop.
      environment = [
        { name = "PORT", value = "8081" },
        { name = "WA_MAX_CONCURRENT", value = "1" },
        { name = "WA_SHUTDOWN_AFTER_SESSION", value = "true" },
      ]

      secrets = [
        { name = "WA_SIDECAR_SECRET", valueFrom = aws_ssm_parameter.secret["WA_SIDECAR_SECRET"].arn },
      ]

      logConfiguration = {
        logDriver = "awslogs"
        options = {
          "awslogs-group"         = aws_cloudwatch_log_group.wa_sidecar.name
          "awslogs-region"        = var.aws_region
          "awslogs-stream-prefix" = "sidecar"
        }
      }
    },
  ])
}

# ---------------------------------------------------------------------------
# Backend instance-role additions (WhatsApp/ECS orchestration)
# ---------------------------------------------------------------------------

# A separate inline policy on the existing backend instance role, so the ECS
# orchestration permissions stay isolated from aws_iam_role_policy.instance.
data "aws_iam_policy_document" "instance_wa" {
  statement {
    sid       = "RunSidecarTask"
    effect    = "Allow"
    actions   = ["ecs:RunTask"]
    resources = ["arn:aws:ecs:${var.aws_region}:${local.account_id}:task-definition/${var.project}-wa-sidecar:*"]
    # Limit RunTask to this cluster (RunTask authorizes the task-def resource,
    # the cluster arrives as a condition key).
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [aws_ecs_cluster.wa.arn]
    }
  }

  statement {
    sid       = "ManageSidecarTasks"
    effect    = "Allow"
    actions   = ["ecs:StopTask", "ecs:DescribeTasks"]
    resources = ["arn:aws:ecs:${var.aws_region}:${local.account_id}:task/${var.project}-wa/*"]
  }

  # The backend resolves the running task's private IP from its ENI.
  # DescribeNetworkInterfaces has no resource-level authorization, so it must be
  # its own "*" statement (same as app_deploy's ResolveInstanceByTag).
  statement {
    sid       = "DescribeTaskEni"
    effect    = "Allow"
    actions   = ["ec2:DescribeNetworkInterfaces"]
    resources = ["*"]
  }

  # RunTask hands the exec + task roles to ECS, which requires PassRole scoped
  # to the ecs-tasks service.
  statement {
    sid       = "PassSidecarRoles"
    effect    = "Allow"
    actions   = ["iam:PassRole"]
    resources = [aws_iam_role.wa_exec.arn, aws_iam_role.wa_task.arn]
    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "instance_wa" {
  name   = "${var.project}-instance-wa"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.instance_wa.json
}
