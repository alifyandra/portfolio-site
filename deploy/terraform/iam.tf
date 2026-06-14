# IAM: the GitHub OIDC provider plus four roles.
#
#   instance         - assumed by EC2; what the running app may touch (S3, SQS,
#                      SES, SSM core, Parameter Store read).
#   app_deploy       - assumed by deploy-backend.yml over OIDC; sends the SSM
#                      command that pulls the new image and restarts compose.
#                      (The GHCR pull itself runs on the box via GITHUB_TOKEN.)
#   terraform_plan   - assumed on pull requests; read-only + state read.
#   terraform_apply  - assumed on merge to main behind a gated Environment;
#                      manages this stack, so it gets broad write access.

resource "aws_iam_openid_connect_provider" "github" {
  url            = "https://token.actions.githubusercontent.com"
  client_id_list = ["sts.amazonaws.com"]
  thumbprint_list = [
    "6938fd4d98bab03faadb97b34396831e3780aea1",
    "1c58a3a8518e8759bf075b76b750d4f2df264fcd",
  ]
}

# ---------------------------------------------------------------------------
# EC2 instance role
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "instance" {
  name               = "${var.project}-instance"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

# SSM Session Manager + the deploy command channel.
resource "aws_iam_role_policy_attachment" "instance_ssm_core" {
  role       = aws_iam_role.instance.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

data "aws_iam_policy_document" "instance" {
  statement {
    sid    = "AssetsBucket"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:DeleteObject",
      "s3:ListBucket",
    ]
    resources = [
      aws_s3_bucket.assets.arn,
      "${aws_s3_bucket.assets.arn}/*",
    ]
  }

  statement {
    sid    = "BackupsBucket"
    effect = "Allow"
    actions = [
      "s3:GetObject",
      "s3:PutObject",
      "s3:ListBucket",
    ]
    resources = [
      aws_s3_bucket.backups.arn,
      "${aws_s3_bucket.backups.arn}/*",
    ]
  }

  statement {
    sid    = "ContactQueue"
    effect = "Allow"
    actions = [
      "sqs:SendMessage",
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:GetQueueAttributes",
      "sqs:GetQueueUrl",
    ]
    resources = [aws_sqs_queue.contact.arn]
  }

  statement {
    sid       = "SendEmail"
    effect    = "Allow"
    actions   = ["ses:SendEmail", "ses:SendRawEmail"]
    resources = ["*"]
  }

  statement {
    sid    = "ReadAppEnv"
    effect = "Allow"
    actions = [
      "ssm:GetParameter",
      "ssm:GetParameters",
      "ssm:GetParametersByPath",
    ]
    # GetParameter[s] authorize against the individual parameter ARNs (.../env/*),
    # but GetParametersByPath authorizes against the path ARN itself (.../env).
    # The /tls/* params hold the Cloudflare origin cert + key (read by user_data).
    resources = [
      "arn:aws:ssm:${var.aws_region}:${local.account_id}:parameter${local.ssm_env_path}",
      "arn:aws:ssm:${var.aws_region}:${local.account_id}:parameter${local.ssm_env_path}/*",
      "arn:aws:ssm:${var.aws_region}:${local.account_id}:parameter${local.ssm_tls_path}/*",
    ]
  }

  # Decrypt the SecureString app secrets (AWS-managed aws/ssm key).
  statement {
    sid       = "DecryptAppEnv"
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

resource "aws_iam_role_policy" "instance" {
  name   = "${var.project}-instance"
  role   = aws_iam_role.instance.id
  policy = data.aws_iam_policy_document.instance.json
}

resource "aws_iam_instance_profile" "instance" {
  name = "${var.project}-instance"
  role = aws_iam_role.instance.name
}

# ---------------------------------------------------------------------------
# GitHub OIDC trust documents
# ---------------------------------------------------------------------------

# Trust for a specific git ref (push to main / workflow_dispatch on main).
data "aws_iam_policy_document" "oidc_main" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    effect  = "Allow"
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["${local.oidc_sub}:ref:refs/heads/main"]
    }
  }
}

# Trust for any pull request in the repo (plan only).
data "aws_iam_policy_document" "oidc_pr" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    effect  = "Allow"
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["${local.oidc_sub}:pull_request"]
    }
  }
}

# Trust gated by the protected GitHub Environment (apply).
data "aws_iam_policy_document" "oidc_apply_env" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    effect  = "Allow"
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = ["${local.oidc_sub}:environment:${var.github_apply_environment}"]
    }
  }
}

# ---------------------------------------------------------------------------
# app-deploy role (SSM send-command)
# ---------------------------------------------------------------------------

resource "aws_iam_role" "app_deploy" {
  name               = "${var.project}-app-deploy"
  assume_role_policy = data.aws_iam_policy_document.oidc_main.json
}

data "aws_iam_policy_document" "app_deploy" {
  # The deploy workflow resolves the live instance ID by tag at runtime (so a box
  # replacement doesn't strand the deploy on a stale ID). DescribeInstances has no
  # resource-level authorization, so it must be its own "*" statement.
  statement {
    sid       = "ResolveInstanceByTag"
    effect    = "Allow"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"]
  }

  statement {
    sid     = "SendDeployCommand"
    effect  = "Allow"
    actions = ["ssm:SendCommand"]
    resources = [
      "arn:aws:ec2:${var.aws_region}:${local.account_id}:instance/${aws_instance.app.id}",
      "arn:aws:ssm:${var.aws_region}::document/AWS-RunShellScript",
    ]
  }

  statement {
    sid    = "ReadCommandResult"
    effect = "Allow"
    actions = [
      "ssm:GetCommandInvocation",
      "ssm:ListCommands",
      "ssm:ListCommandInvocations",
    ]
    resources = ["*"]
  }
}

resource "aws_iam_role_policy" "app_deploy" {
  name   = "${var.project}-app-deploy"
  role   = aws_iam_role.app_deploy.id
  policy = data.aws_iam_policy_document.app_deploy.json
}

# ---------------------------------------------------------------------------
# Terraform CI roles
# ---------------------------------------------------------------------------

resource "aws_iam_role" "terraform_plan" {
  name               = "${var.project}-terraform-plan"
  assume_role_policy = data.aws_iam_policy_document.oidc_pr.json
}

resource "aws_iam_role_policy_attachment" "terraform_plan_readonly" {
  role       = aws_iam_role.terraform_plan.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

# The plan role reads the remote state, plus puts/deletes the native-locking
# .tflock object. S3 native locking (use_lockfile, TF >= 1.10) writes that lock
# object even on a read-only plan, so without these the plan job 403s at lock
# acquisition. Scope the write to the lock file only — plan never writes state.
data "aws_iam_policy_document" "state_read" {
  statement {
    effect  = "Allow"
    actions = ["s3:ListBucket", "s3:GetObject"]
    resources = [
      "arn:aws:s3:::aliflabs-terraform-state",
      "arn:aws:s3:::aliflabs-terraform-state/${var.project}/*",
    ]
  }
  statement {
    effect  = "Allow"
    actions = ["s3:PutObject", "s3:DeleteObject"]
    resources = [
      "arn:aws:s3:::aliflabs-terraform-state/${var.project}/terraform.tfstate.tflock",
    ]
  }
}

resource "aws_iam_role_policy" "terraform_plan_state" {
  name   = "${var.project}-tf-state-read"
  role   = aws_iam_role.terraform_plan.id
  policy = data.aws_iam_policy_document.state_read.json
}

resource "aws_iam_role" "terraform_apply" {
  name               = "${var.project}-terraform-apply"
  assume_role_policy = data.aws_iam_policy_document.oidc_apply_env.json
}

# Apply manages this whole stack (VPC, IAM, S3, SSM, EC2, ...). Scoping that to
# least privilege is more churn than it is worth for a single-box account; the
# blast radius is bounded by the gated Environment instead.
resource "aws_iam_role_policy_attachment" "terraform_apply_admin" {
  role       = aws_iam_role.terraform_apply.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

data "aws_iam_policy_document" "state_write" {
  statement {
    effect    = "Allow"
    actions   = ["s3:ListBucket"]
    resources = ["arn:aws:s3:::aliflabs-terraform-state"]
  }
  statement {
    effect    = "Allow"
    actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
    resources = ["arn:aws:s3:::aliflabs-terraform-state/${var.project}/*"]
  }
}

resource "aws_iam_role_policy" "terraform_apply_state" {
  name   = "${var.project}-tf-state-write"
  role   = aws_iam_role.terraform_apply.id
  policy = data.aws_iam_policy_document.state_write.json
}
