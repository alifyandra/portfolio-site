# Minimal custom VPC: two public subnets across two AZs, an IGW, one route
# table, and a security group open on 80/443 only. The second subnet is not
# used by the instance today; it pre-satisfies the two-AZ subnet group a future
# RDS instance would need (ADR 9).

resource "aws_vpc" "main" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true

  tags = { Name = "${var.project}-vpc" }
}

resource "aws_internet_gateway" "main" {
  vpc_id = aws_vpc.main.id

  tags = { Name = "${var.project}-igw" }
}

resource "aws_subnet" "public" {
  count                   = length(var.public_subnet_cidrs)
  vpc_id                  = aws_vpc.main.id
  cidr_block              = var.public_subnet_cidrs[count.index]
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  map_public_ip_on_launch = true

  tags = { Name = "${var.project}-public-${count.index}" }
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.main.id

  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.main.id
  }

  tags = { Name = "${var.project}-public-rt" }
}

resource "aws_route_table_association" "public" {
  count          = length(aws_subnet.public)
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# Cloudflare's published edge IP ranges. Used to lock the origin so the box is
# only reachable through Cloudflare once the api record is proxied (orange cloud)
# and TLS is served via the CF origin cert. Kept current by the provider.
data "cloudflare_ip_ranges" "cloudflare" {}

resource "aws_security_group" "web" {
  name        = "${var.project}-web"
  description = "HTTP/HTTPS in, all out. SSH is via SSM Session Manager only."
  vpc_id      = aws_vpc.main.id

  # When lock_origin_to_cloudflare is true, 80/443 accept traffic only from
  # Cloudflare's ranges (the box is unreachable except via the CF proxy);
  # otherwise they are open to the internet (the pre-proxy default). Flip the
  # flag on only AFTER the proxy + origin cert are verified, or you cut off
  # direct access (and Caddy's ACME) to a box you can still reach via SSM.
  ingress {
    description      = "HTTP (ACME + redirect)"
    from_port        = 80
    to_port          = 80
    protocol         = "tcp"
    cidr_blocks      = var.lock_origin_to_cloudflare ? data.cloudflare_ip_ranges.cloudflare.ipv4_cidr_blocks : ["0.0.0.0/0"]
    ipv6_cidr_blocks = var.lock_origin_to_cloudflare ? data.cloudflare_ip_ranges.cloudflare.ipv6_cidr_blocks : ["::/0"]
  }

  ingress {
    description      = "HTTPS"
    from_port        = 443
    to_port          = 443
    protocol         = "tcp"
    cidr_blocks      = var.lock_origin_to_cloudflare ? data.cloudflare_ip_ranges.cloudflare.ipv4_cidr_blocks : ["0.0.0.0/0"]
    ipv6_cidr_blocks = var.lock_origin_to_cloudflare ? data.cloudflare_ip_ranges.cloudflare.ipv6_cidr_blocks : ["::/0"]
  }

  # No inbound Postgres rule: the digest task never touches the DB (ADR 13,
  # Shape B). The Fargate task fetches + summarizes and writes its result to S3;
  # the on-box worker reads that result and writes the Digest row over the docker
  # network. Nothing off-box reaches Postgres.

  egress {
    description      = "All outbound"
    from_port        = 0
    to_port          = 0
    protocol         = "-1"
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }

  tags = { Name = "${var.project}-web" }
}

# Stable address so the instance can be replaced without moving DNS.
resource "aws_eip" "app" {
  domain = "vpc"

  tags = { Name = "${var.project}-eip" }
}
