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
  }

  # Secret slots. Seeded with a placeholder, then pushed out-of-band.
  env_secrets = [
    "POSTGRES_PASSWORD",
    "DATABASE_URL",
    "SPOTIFY_CLIENT_ID",
    "SPOTIFY_CLIENT_SECRET",
    "SPOTIFY_REFRESH_TOKEN",
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
