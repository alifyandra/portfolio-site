# CORS on the assets bucket so the browser can upload Project images straight to
# S3 via backend-issued presigned PUT URLs (the admin-console upload flow). The
# backend already presigns GET/PUT against this same bucket (S3_BUCKET in ssm.tf),
# but a cross-origin browser PUT/GET is blocked by the S3 CORS preflight until the
# bucket advertises the allowed origins/methods below. Kept in its own file so the
# change lands as a standalone commit and its apply does not entangle other state.

resource "aws_s3_bucket_cors_configuration" "assets" {
  bucket = aws_s3_bucket.assets.id

  cors_rule {
    # PUT is the presigned-upload method that matters; GET/HEAD cover browser
    # reads of the same objects. allowed_headers "*" lets the presigned PUT send
    # Content-Type plus any x-amz-* headers the signature covers. ETag is exposed
    # so the uploader can read it back off the response.
    allowed_methods = ["PUT", "GET", "HEAD"]
    allowed_origins = [
      "https://${var.domain}",
      "https://www.${var.domain}",
      "http://localhost:3000",
    ]
    allowed_headers = ["*"]
    expose_headers  = ["ETag"]
    max_age_seconds = 3000
  }
}
