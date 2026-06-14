# 9. Provision the EC2 host with Terraform

Date: 2026-06-14
Status: Accepted

## Context

ADR 6 chose a single EC2 host running docker compose and stated the intent to
"keep everything infra-as-code," but no IaC existed. Standing the box up was a
manual runbook in `docs/deployment.md`: launch the instance, attach IAM, create
the security group, hand-write `/opt/portfolio/.env`, copy compose files, run
compose. This ADR records the decision to codify that provisioning.

## Decision

Provision the AWS side with **Terraform**, living in `deploy/terraform/` as a
flat root (one box, one environment, no module indirection). Specifics:

- **State** in a private, versioned, encrypted S3 bucket with native S3 locking
  (no DynamoDB). The bucket is bootstrapped once by hand (chicken-and-egg).
- **Apply model is hybrid.** The first apply runs locally to seed the resources;
  thereafter a CI workflow runs `terraform plan` on pull requests and a gated
  `apply` on merge to main. Infra (Terraform) and app deploys (the existing
  `deploy-backend.yml` SSM container pull) stay separate seams.
- **Host is fully codified.** `user_data` installs Docker and the swap file,
  pulls the app `.env` from SSM Parameter Store, writes it to `/opt/portfolio`,
  and starts compose. The box is reproducible from zero. Secret values are
  pushed once via `aws ssm put-parameter` so they never enter Terraform state.
- **Network:** a minimal custom VPC (one VPC, two public subnets across two AZs,
  IGW, route table, SG open on 80/443 only) plus an Elastic IP, so the instance
  can be replaced without moving DNS. Two subnets, rather than one, pre-satisfy
  the two-AZ DB subnet group a future RDS instance would require.
- **DNS as code** via the Cloudflare provider. The `api` record stays DNS-only
  (grey-cloud) so Caddy can run its own ACME challenge. SES domain identity and
  DKIM records are codified too; exiting the SES sandbox stays a manual request.
- **Postgres remains on-box** (ADR 6 stands). Managed RDS was reconsidered and
  priced out: the floor is about $25 AUD per month, which alone exceeds the
  budget, and the account is past free-tier credits. The `DATABASE_URL` seam
  plus the codified `pg_dump` to S3 backup keep a later move to RDS cheap.

## Considered options

- **CloudFormation or CDK** over Terraform: AWS-native and closer to the SAA
  exam, but less transferable. Terraform exercises the same VPC, IAM, and EBS
  primitives ADR 6 cared about.
- **Default VPC:** zero networking code and functionally identical for one
  public box, but it leaves the network implicit and account-managed rather than
  in code.
- **Terraform in CI for all applies:** cleaner GitOps, but a single rarely
  changed box does not justify the bootstrap chicken-and-egg or the risk of a
  merge silently mutating live infra.
- **GitHub provider to set repo secrets:** rejected to avoid wiring a long-lived
  PAT just to place a handful of non-secret identifiers. Set them once by hand.

## Consequences

- A scoped Cloudflare API token and AWS credentials are needed for the local
  bootstrap. After that, CI uses OIDC roles Terraform creates.
- A short list stays manual: AWS account and billing, the state bucket
  bootstrap, pushing secret values to Parameter Store, the repo secrets, the SES
  production-access request, Vercel, and GHCR package visibility.
- Local and prod still share the compose Postgres today; a future RDS move would
  diverge them (still Postgres, same seam).
