# S3 assets bucket, S3 backups bucket (with lifecycle expiry), and the SQS
# queue the worker drains for contact-form email.

resource "aws_s3_bucket" "assets" {
  bucket = "${var.project}-assets-${local.account_id}"
}

resource "aws_s3_bucket_public_access_block" "assets" {
  bucket                  = aws_s3_bucket.assets.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "assets" {
  bucket = aws_s3_bucket.assets.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "assets" {
  bucket = aws_s3_bucket.assets.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket" "backups" {
  bucket = "${var.project}-backups-${local.account_id}"
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket                  = aws_s3_bucket.backups.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    id     = "expire-old-backups"
    status = "Enabled"

    filter {
      prefix = ""
    }

    expiration {
      days = var.backup_retention_days
    }
  }
}

# The shared jobs queue (ADR 13). Despite the -contact-notify name (kept to
# avoid a destructive rename), it now carries every Job type: contact.notify
# (event-triggered, run inline on the box) and digest.build (scheduled, run on
# Fargate). The worker dispatches by type.
resource "aws_sqs_queue" "contact" {
  name                      = "${var.project}-contact-notify"
  message_retention_seconds = 345600 # 4 days

  # Must exceed the digest launcher's hard runtime cap so a slow run is never
  # redelivered and duplicated mid-flight (ADR 13). The cap is 15m/900s
  # (maxRunToCompletion in internal/fargate), enforced by the worker via
  # StopTask; visibility (default 1200s) is set above it. Raising this from the
  # old 60s also affects contact.notify redelivery timing — acceptable, since
  # contact.notify runs inline in well under a second.
  visibility_timeout_seconds = var.jobs_visibility_timeout_seconds

  # Retry is SQS redelivery; after a small number of failed receives the message
  # dead-letters (ADR 13). The schedule is a backstop: a dead-lettered digest is
  # retried fresh by the next cron.
  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.jobs_dlq.arn
    maxReceiveCount     = var.jobs_max_receive_count
  })
}

# Dead-letter queue for poison job messages. Longer retention than the source so
# there is time to inspect a failure before it expires. Not consumed by the
# worker; a maintainer redrives or inspects it out-of-band (console/CLI).
resource "aws_sqs_queue" "jobs_dlq" {
  name                      = "${var.project}-jobs-dlq"
  message_retention_seconds = 1209600 # 14 days
}
