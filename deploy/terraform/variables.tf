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

variable "budget_amount_aud" {
  description = "Monthly cost budget in AUD."
  type        = string
  default     = "25"
}

variable "backup_retention_days" {
  description = "Days to keep pg_dump backups in S3 before expiry."
  type        = number
  default     = 30
}
