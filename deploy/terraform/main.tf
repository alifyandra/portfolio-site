# Root configuration: providers, remote state, and shared data sources.
# Flat root, one box, one environment (see ADR 9).

terraform {
  required_version = ">= 1.7.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.40"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.40"
    }
  }

  # Remote state in a private, versioned, encrypted S3 bucket with native S3
  # locking (use_lockfile, no DynamoDB). The bucket is created once by hand
  # before the first `terraform init` (chicken-and-egg, see README).
  backend "s3" {
    bucket       = "aliflabs-terraform-state"
    key          = "portfolio/terraform.tfstate"
    region       = "ap-southeast-2"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project   = var.project
      ManagedBy = "terraform"
    }
  }
}

# api_token is read from the CLOUDFLARE_API_TOKEN environment variable.
provider "cloudflare" {}

data "aws_caller_identity" "current" {}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  api_fqdn     = "${var.api_subdomain}.${var.domain}"
  account_id   = data.aws_caller_identity.current.account_id
  oidc_sub     = "repo:${var.github_repo}"
  ssm_env_path = "/${var.project}/env"
}
