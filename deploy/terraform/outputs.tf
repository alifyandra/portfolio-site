output "region" {
  description = "AWS region."
  value       = var.aws_region
}

output "instance_id" {
  description = "EC2 instance ID (set as the EC2_INSTANCE_ID repo secret)."
  value       = aws_instance.app.id
}

output "elastic_ip" {
  description = "Public Elastic IP (the api DNS record points here)."
  value       = aws_eip.app.public_ip
}

output "app_deploy_role_arn" {
  description = "Role deploy-backend.yml assumes (set as AWS_DEPLOY_ROLE_ARN)."
  value       = aws_iam_role.app_deploy.arn
}

output "terraform_plan_role_arn" {
  description = "Role the Terraform plan job assumes on PRs."
  value       = aws_iam_role.terraform_plan.arn
}

output "terraform_apply_role_arn" {
  description = "Role the Terraform apply job assumes on merge to main."
  value       = aws_iam_role.terraform_apply.arn
}

output "instance_role_arn" {
  description = "Role attached to the EC2 instance profile."
  value       = aws_iam_role.instance.arn
}

output "github_oidc_provider_arn" {
  description = "GitHub Actions OIDC provider ARN."
  value       = aws_iam_openid_connect_provider.github.arn
}

output "assets_bucket" {
  description = "S3 bucket for application assets."
  value       = aws_s3_bucket.assets.bucket
}

output "backups_bucket" {
  description = "S3 bucket for pg_dump backups."
  value       = aws_s3_bucket.backups.bucket
}

output "sqs_queue_url" {
  description = "Contact-notify SQS queue URL."
  value       = aws_sqs_queue.contact.url
}

output "ses_dkim_tokens" {
  description = "SES DKIM tokens (published as CNAMEs in Cloudflare)."
  value       = aws_ses_domain_dkim.main.dkim_tokens
}
