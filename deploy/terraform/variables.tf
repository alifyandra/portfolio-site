variable "aws_region" {
  description = "AWS region for all resources."
  type        = string
  default     = "ap-southeast-2"
}

variable "project" {
  description = "Short name used to prefix resources and the SSM parameter path."
  type        = string
  default     = "portfolio"
}

variable "domain" {
  description = "Apex domain managed in Cloudflare."
  type        = string
  default     = "aliflabs.dev"
}

variable "api_subdomain" {
  description = "Subdomain label for the backend API (joined with domain)."
  type        = string
  default     = "api"
}

variable "instance_type" {
  description = "EC2 instance type. arm64 / Graviton."
  type        = string
  default     = "t4g.micro"
}

variable "root_volume_gb" {
  description = "Root EBS volume size in GB."
  type        = number
  default     = 20
}

variable "vpc_cidr" {
  description = "CIDR block for the VPC."
  type        = string
  default     = "10.0.0.0/16"
}

variable "public_subnet_cidrs" {
  description = "CIDR blocks for the public subnets (one per AZ, two AZs)."
  type        = list(string)
  default     = ["10.0.1.0/24", "10.0.2.0/24"]
}

variable "github_repo" {
  description = "owner/repo that GitHub OIDC roles trust."
  type        = string
  default     = "alifyandra/portfolio-site"
}

variable "github_apply_environment" {
  description = "GitHub Environment that gates terraform apply (required reviewer)."
  type        = string
  default     = "production"
}

variable "cloudflare_zone_id" {
  description = "Cloudflare zone ID for the domain. Set via TF_VAR_cloudflare_zone_id or tfvars."
  type        = string
}

variable "alert_email" {
  description = "Email for budget alerts and the default contact recipient."
  type        = string
  default     = "alifyandra@gmail.com"
}

variable "ses_sender_email" {
  description = "From address for outbound SES mail (must be on the verified domain)."
  type        = string
  default     = "noreply@aliflabs.dev"
}

variable "budget_amount_usd" {
  description = "Monthly cost budget in USD (AWS Budgets only supports USD for this account). ~17 USD tracks the intended ~25 AUD ceiling."
  type        = string
  default     = "17"
}

variable "backup_retention_days" {
  description = "Days to keep pg_dump backups in S3 before expiry."
  type        = number
  default     = 30
}

# --- Digest / scheduled jobs (ADR 13, see digest.tf) ---------------------------

variable "enable_digest_schedule" {
  description = "Enable the daily EventBridge Scheduler cron that enqueues digest.build. Enabled 2026-07-09 after the slice was applied, the image pushed, the secrets seeded, and a manual run verified end-to-end in prod (ADR 13). Flipping it is an in-place state update on the schedule, not a resource churn."
  type        = bool
  default     = true
}

variable "digest_schedule_expression" {
  description = "Cron for the daily digest.build trigger (EventBridge Scheduler syntax). Default 18:00 UTC = ~04:00-05:00 Melbourne, off-peak."
  type        = string
  default     = "cron(0 18 * * ? *)"
}

variable "digest_schedule_timezone" {
  description = "IANA timezone the digest cron is evaluated in."
  type        = string
  default     = "UTC"
}

variable "digest_model" {
  description = "Anthropic model the digest task summarizes with (small/cheap by default to bound cost, ADR 13)."
  type        = string
  default     = "claude-haiku-4-5"
}

variable "digest_max_tokens" {
  description = "Max output tokens per digest LLM call (cost bound, ADR 13). String: it is passed straight through as an env var. Matches the backend config default."
  type        = string
  default     = "4096"
}

variable "digest_result_prefix" {
  description = "S3 key prefix (in the assets bucket) the digest Fargate task writes its Result JSON under and the worker reads back (ADR 13, Shape B). Trailing slash. A lifecycle rule expires objects here; the worker also deletes each after reading. Matches the backend config default."
  type        = string
  default     = "digest-results/"
}

variable "jobs_visibility_timeout_seconds" {
  description = "SQS visibility timeout on the shared jobs queue. MUST exceed the digest launcher's hard runtime cap (maxRunToCompletion = 15m / 900s in internal/fargate) so a slow run is not redelivered mid-flight (ADR 13). Set above 900s to leave poll/teardown margin. Also affects contact.notify (fast, so harmless)."
  type        = number
  default     = 1200
}

variable "jobs_max_receive_count" {
  description = "Receives before a job message dead-letters. Small, so poison messages fail fast to the DLQ; the daily schedule is the backstop for a dead-lettered digest (ADR 13)."
  type        = number
  default     = 3
}

# --- Cloudflare proxy cutover (two-step; see docs/security.md) -----------------
# Default off = today's posture (grey-cloud api record, origin open to the
# internet, Caddy runs ACME). Flip these on deliberately, in order, during the
# proxy cutover:
#   1. proxy_api = true               -> orange-cloud the api record
#   2. lock_origin_to_cloudflare=true -> restrict the SG to Cloudflare's ranges
# Do step 2 LAST, only after the proxy + origin TLS are verified. SSM Session
# Manager remains the way onto the box, so a wrong SG never locks you out.

variable "proxy_api" {
  description = "Route api.<domain> through the Cloudflare proxy (orange cloud). Requires the CF origin cert installed in Caddy + SSL/TLS mode Full (strict) first."
  type        = bool
  default     = true # cutover complete; set false to roll back the proxy
}

variable "lock_origin_to_cloudflare" {
  description = "Restrict the web SG (80/443) to Cloudflare's published IP ranges instead of the public internet. Turn on only after proxy_api is live and verified."
  type        = bool
  default     = true # cutover complete; set false to roll back the origin lock
}
