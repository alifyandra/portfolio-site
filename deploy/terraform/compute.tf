# The single application host: a t4g.micro on Amazon Linux 2023 (arm64), the
# Elastic IP association, and the cloud-init that brings the box up from zero.

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
    project_dir   = "/opt/portfolio"
    aws_region    = var.aws_region
    ssm_path      = local.ssm_env_path
    compose_b64   = base64encode(file("${path.module}/../../docker-compose.prod.yml"))
    caddyfile_b64 = base64encode(file("${path.module}/../../deploy/Caddyfile"))
  })

  # Re-runs user_data on a fresh instance if the compose/Caddyfile/env path
  # change, replacing the box (the EIP stays put).
  user_data_replace_on_change = true

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
  # password rather than the placeholder.
  depends_on = [aws_ssm_parameter.config, aws_ssm_parameter.secret]

  tags = { Name = "${var.project}-app" }
}

resource "aws_eip_association" "app" {
  instance_id   = aws_instance.app.id
  allocation_id = aws_eip.app.id
}
