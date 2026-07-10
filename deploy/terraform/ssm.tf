# The production .env, modelled as SSM parameters under /<project>/env. The box
# rebuilds .env from this path on boot (see user_data.sh.tftpl).
#
#   Config values are Terraform-managed (String) and may change here.
#   Secret values are SecureString slots seeded with a placeholder; the real
#   values are pushed once via `aws ssm put-parameter` and never enter state,
#   so ignore_changes keeps Terraform from clobbering them.
#
# Empty-by-design keys (S3_ENDPOINT_URL, SQS_ENDPOINT_URL) are simply absent:
# the app treats an unset endpoint as "use real AWS".

locals {
  # Non-secret configuration. Edit here, apply, replace the box (or re-pull).
  env_config = {
    APP_ENV              = "production"
    PORT                 = "8080"
    AUTO_MIGRATE         = "true"
    POSTGRES_USER        = "portfolio"
    POSTGRES_DB          = "portfolio"
    REDIS_URL            = "redis://redis:6379/0"
    CORS_ALLOWED_ORIGINS = "https://${var.domain},https://www.${var.domain}"
    AWS_REGION           = var.aws_region
    S3_BUCKET            = aws_s3_bucket.assets.bucket
    S3_FORCE_PATH_STYLE  = "false"
    SQS_QUEUE_URL        = aws_sqs_queue.contact.url
    SES_SENDER_EMAIL     = var.ses_sender_email
    CONTACT_NOTIFY_TO    = var.alert_email
    DOMAIN               = local.api_fqdn
    # The limiter may only trust CF-Connecting-IP once the origin SG is locked to
    # Cloudflare's ranges (otherwise the header is spoofable by a direct request),
    # so this rides on lock_origin_to_cloudflare, not proxy_api. Default false.
    TRUST_CLOUDFLARE_IP = var.lock_origin_to_cloudflare ? "true" : "false"

    # Auth / Google OAuth (see ADR 10). Non-secret, derived from the domain.
    # The redirect URL must also be registered on the Google OAuth client. The
    # cookie is scoped to the registrable domain so it is shared across the www
    # frontend and the api subdomain. The post-login landing is the canonical
    # www host (the apex 308s to www).
    GOOGLE_REDIRECT_URL   = "https://${local.api_fqdn}/api/auth/google/callback"
    SESSION_COOKIE_DOMAIN = ".${var.domain}"
    FRONTEND_URL          = "https://www.${var.domain}/account"

    # WhatsApp sidecar on Fargate (ADR 11, see whatsapp.tf). These drive the
    # backend's ECS launcher: at batch start it RunTasks the sidecar task def in
    # this cluster/subnets/SG, streams to the task's private IP, then StopTasks it.
    # WA_SIDECAR_URL is intentionally absent in fargate mode (there is no fixed
    # endpoint; each batch gets a fresh task). AssignPublicIp must be true: the
    # public subnets have no NAT, so the ECS agent needs a public IP to pull the
    # image from ECR and read the WA_SIDECAR_SECRET SecureString from SSM.
    WA_SIDECAR_MODE      = "fargate"
    WA_ECS_CLUSTER       = aws_ecs_cluster.wa.name
    WA_TASK_DEFINITION   = aws_ecs_task_definition.wa_sidecar.family
    WA_SUBNET_IDS        = join(",", aws_subnet.public[*].id)
    WA_SECURITY_GROUP_ID = aws_security_group.wa_sidecar.id
    WA_ASSIGN_PUBLIC_IP  = "true"

    # Digest / scheduled jobs on Fargate (ADR 13, Shape B, see digest.tf). These
    # drive the worker's run-to-completion launcher: on a digest.build job the
    # worker reads the active Sources on-box, RunTasks the digest task def in the
    # shared WA cluster/subnets (passing DIGEST_DATE / DIGEST_SOURCES /
    # DIGEST_RESULT_KEY as overrides), polls DescribeTasks until STOPPED, reads the
    # task's Result from S3 under DIGEST_RESULT_PREFIX, and writes the Digest row
    # itself. The task never touches Postgres. AssignPublicIp must be true (public
    # subnets, no NAT: the ECS agent needs a public IP to pull from ECR and read
    # SSM). DIGEST_MODEL / DIGEST_MAX_TOKENS are also baked into the task def from
    # the same variables, so the two never drift.
    DIGEST_MODE              = "fargate"
    DIGEST_ECS_CLUSTER       = aws_ecs_cluster.wa.name
    DIGEST_TASK_DEFINITION   = aws_ecs_task_definition.digest.family
    DIGEST_SUBNET_IDS        = join(",", aws_subnet.public[*].id)
    DIGEST_SECURITY_GROUP_ID = aws_security_group.digest.id
    DIGEST_ASSIGN_PUBLIC_IP  = "true"
    DIGEST_MODEL             = var.digest_model
    DIGEST_MAX_TOKENS        = var.digest_max_tokens
    DIGEST_RESULT_PREFIX     = var.digest_result_prefix
  }

  # Secret slots. Seeded with a placeholder, then pushed out-of-band.
  # GOOGLE_CLIENT_* are the OAuth credentials; ADMIN_EMAILS / FRIEND_EMAILS are
  # kept here (not in env_config) so personal emails stay out of this public repo.
  env_secrets = [
    "POSTGRES_PASSWORD",
    "DATABASE_URL",
    "SPOTIFY_CLIENT_ID",
    "SPOTIFY_CLIENT_SECRET",
    "SPOTIFY_REFRESH_TOKEN",
    "GOOGLE_CLIENT_ID",
    "GOOGLE_CLIENT_SECRET",
    "ADMIN_EMAILS",
    "FRIEND_EMAILS",
    "WA_SIDECAR_SECRET", # backend<->sidecar shared bearer secret (see whatsapp.tf)
    "ANTHROPIC_API_KEY", # digest LLM key: injected into the Fargate task (submit) and on the box .env for the worker's inline digest.collect (ADR 13 Batch API amendment)
  ]
}

resource "aws_ssm_parameter" "config" {
  for_each = local.env_config

  name  = "${local.ssm_env_path}/${each.key}"
  type  = "String"
  value = each.value
}

resource "aws_ssm_parameter" "secret" {
  for_each = toset(local.env_secrets)

  name  = "${local.ssm_env_path}/${each.value}"
  type  = "SecureString"
  value = "CHANGE_ME" # placeholder; real value pushed via aws ssm put-parameter

  lifecycle {
    ignore_changes = [value]
  }
}

# Cloudflare origin certificate + private key for Caddy, used only when the api
# is proxied (var.proxy_api). Multi-line PEM, so kept off the flat /env path and
# fetched to files by user_data. Seeded with a placeholder; the real PEMs are
# pushed once out-of-band (see deploy/terraform/README.md), e.g.:
#   aws ssm put-parameter --name /portfolio/tls/origin_cert --type SecureString \
#     --overwrite --value file://origin.pem
resource "aws_ssm_parameter" "origin_tls" {
  for_each = toset(["origin_cert", "origin_key"])

  name  = "${local.ssm_tls_path}/${each.value}"
  type  = "SecureString"
  value = "CHANGE_ME" # placeholder; real PEM pushed via aws ssm put-parameter

  lifecycle {
    ignore_changes = [value]
  }
}
