# The single application host: a t4g.micro on Amazon Linux 2023 (arm64), the
# Elastic IP association, and the cloud-init that brings the box up from zero.

locals {
  # When the api is proxied (var.proxy_api), Caddy serves the Cloudflare origin
  # cert instead of running ACME, and the cert dir is mounted into the caddy
  # container. With proxy_api = false these render byte-identical to the
  # committed files, so user_data is unchanged and the box is not replaced until
  # the deliberate cutover apply.
  compose_rendered = var.proxy_api ? replace(
    file("${path.module}/../../docker-compose.prod.yml"),
    "      - ./deploy/Caddyfile:/etc/caddy/Caddyfile:ro\n",
    "      - ./deploy/Caddyfile:/etc/caddy/Caddyfile:ro\n      - ./deploy/tls:/etc/caddy/tls:ro\n"
  ) : file("${path.module}/../../docker-compose.prod.yml")

  caddyfile_rendered = var.proxy_api ? replace(
    file("${path.module}/../../deploy/Caddyfile"),
    "\tencode gzip\n",
    "\tencode gzip\n\ttls /etc/caddy/tls/origin.crt /etc/caddy/tls/origin.key\n"
  ) : file("${path.module}/../../deploy/Caddyfile")

  cert_fetch = var.proxy_api ? join("\n", [
    "# --- Cloudflare origin cert for Caddy (api is proxied) ---",
    "mkdir -p ${local.project_dir}/deploy/tls && chmod 700 ${local.project_dir}/deploy/tls",
    "aws ssm get-parameter --name ${local.ssm_tls_path}/origin_cert --with-decryption --region ${var.aws_region} --query Parameter.Value --output text > ${local.project_dir}/deploy/tls/origin.crt",
    "aws ssm get-parameter --name ${local.ssm_tls_path}/origin_key --with-decryption --region ${var.aws_region} --query Parameter.Value --output text > ${local.project_dir}/deploy/tls/origin.key",
    # Fail fast if the params still hold the SSM placeholder (CHANGE_ME) rather
    # than real PEM material — otherwise Caddy would start with a bad cert and
    # take the proxied API down. Seed the certs before cutover (see README.md).
    "grep -q 'BEGIN CERTIFICATE' ${local.project_dir}/deploy/tls/origin.crt || { echo 'FATAL: ${local.ssm_tls_path}/origin_cert is missing/placeholder; seed the Cloudflare origin cert before the proxy cutover' >&2; exit 1; }",
    "grep -q 'PRIVATE KEY' ${local.project_dir}/deploy/tls/origin.key || { echo 'FATAL: ${local.ssm_tls_path}/origin_key is missing/placeholder; seed the Cloudflare origin key before the proxy cutover' >&2; exit 1; }",
    "chmod 600 ${local.project_dir}/deploy/tls/origin.crt ${local.project_dir}/deploy/tls/origin.key",
  ]) : ""
}

data "aws_ami" "al2023_arm64" {
  most_recent = true
  owners      = ["amazon"]

  # Standard AL2023 only. "al2023-ami-2023.*" excludes the "al2023-ami-minimal-*"
  # variant, which omits the SSM agent (and would break Session Manager / the
  # SSM-based deploy path).
  filter {
    name   = "name"
    values = ["al2023-ami-2023.*-arm64"]
  }
  filter {
    name   = "architecture"
    values = ["arm64"]
  }
  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }
}

resource "aws_instance" "app" {
  ami                    = data.aws_ami.al2023_arm64.id
  instance_type          = var.instance_type
  subnet_id              = aws_subnet.public[0].id
  vpc_security_group_ids = [aws_security_group.web.id]
  iam_instance_profile   = aws_iam_instance_profile.instance.name

  user_data = templatefile("${path.module}/user_data.sh.tftpl", {
    project_dir   = local.project_dir
    aws_region    = var.aws_region
    ssm_path      = local.ssm_env_path
    compose_b64   = base64encode(local.compose_rendered)
    caddyfile_b64 = base64encode(local.caddyfile_rendered)
    cert_fetch    = local.cert_fetch
  })

  # Re-runs user_data on a fresh instance if the compose/Caddyfile/env path
  # change, replacing the box (the EIP stays put).
  user_data_replace_on_change = true

  # Pin to the AMI the box launched with. data.aws_ami.al2023_arm64 tracks the
  # newest AL2023 release, so without this every apply would replace the box
  # (downtime + wipes the on-box Postgres root volume) merely because AWS
  # published a newer AMI. Deliberate AMI upgrades are done by tainting the
  # instance (terraform apply -replace=aws_instance.app) after taking a backup.
  lifecycle {
    ignore_changes = [ami]
  }

  metadata_options {
    http_tokens   = "required" # IMDSv2 only
    http_endpoint = "enabled"
  }

  root_block_device {
    volume_size = var.root_volume_gb
    volume_type = "gp3"
    encrypted   = true
  }

  # The box rebuilds .env from these on first boot, so they must exist first.
  # Real secret values are seeded before this instance is created (see the
  # two-phase bootstrap in README.md) so Postgres initialises with the real
  # password rather than the placeholder. origin_tls is included so a fresh
  # proxy_api=true apply can't create the box before cert_fetch's /tls/* params
  # exist (user_data would otherwise fail fetching the Cloudflare origin cert).
  depends_on = [
    aws_ssm_parameter.config,
    aws_ssm_parameter.secret,
    aws_ssm_parameter.origin_tls,
  ]

  tags = { Name = "${var.project}-app" }
}

resource "aws_eip_association" "app" {
  instance_id   = aws_instance.app.id
  allocation_id = aws_eip.app.id
}
