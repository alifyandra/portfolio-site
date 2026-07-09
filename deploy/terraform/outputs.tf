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

output "wa_ecr_repository_url" {
  description = "ECR repo the WhatsApp sidecar image is pushed to (Slice B)."
  value       = aws_ecr_repository.wa_sidecar.repository_url
}

output "wa_ecs_cluster" {
  description = "ECS cluster name the sidecar task runs in."
  value       = aws_ecs_cluster.wa.name
}

output "wa_task_definition" {
  description = "ECS task-definition family the backend calls RunTask with."
  value       = aws_ecs_task_definition.wa_sidecar.family
}

output "wa_sidecar_security_group_id" {
  description = "Security group attached to the sidecar Fargate task."
  value       = aws_security_group.wa_sidecar.id
}

output "wa_subnet_ids" {
  description = "Public subnet IDs the sidecar task can be launched into."
  value       = aws_subnet.public[*].id
}

output "app_private_ip" {
  description = "Private IP of the app host. Bake this into DIGEST_DATABASE_URL when seeding it (ADR 13)."
  value       = aws_instance.app.private_ip
}

output "digest_ecr_repository_url" {
  description = "ECR repo the digest image is pushed to (Slice E)."
  value       = aws_ecr_repository.digest.repository_url
}

output "digest_task_definition" {
  description = "ECS task-definition family the worker calls RunTask with for digest.build."
  value       = aws_ecs_task_definition.digest.family
}

output "digest_log_group" {
  description = "CloudWatch log group for the digest task."
  value       = aws_cloudwatch_log_group.digest.name
}

output "jobs_dlq_arn" {
  description = "Dead-letter queue ARN for poison job messages."
  value       = aws_sqs_queue.jobs_dlq.arn
}

output "digest_schedule_name" {
  description = "EventBridge Scheduler schedule that enqueues digest.build."
  value       = aws_scheduler_schedule.digest_build.name
}
