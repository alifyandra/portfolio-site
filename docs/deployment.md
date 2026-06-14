# Deployment

Two targets: **frontend on Vercel**, **backend on a single EC2 host** running
docker compose (see [ADR 6](adr/0006-ec2-compose-over-fargate.md)).

## Frontend (Vercel)

1. Import the repo in Vercel. Set **Root Directory** = `frontend`.
2. Build command stays the default (`npm run build`); the `prebuild` hook runs
   orval against `../backend/openapi.yaml` automatically.
3. Add env var `NEXT_PUBLIC_API_URL` = `https://api.aliflabs.dev`.
4. Vercel auto-deploys on every push and creates per-PR preview URLs.

## Backend (EC2 + docker compose)

### Provisioning (Terraform)

Host provisioning is now fully codified via Terraform in `deploy/terraform/`
(flat root, one environment, S3 remote state with native locking; see
[ADR 9](adr/0009-terraform-provisioning.md)). The first apply runs locally to
seed the stack; thereafter infra changes flow through CI (plan on PRs, gated
apply on merge to main).

For the one-time bootstrap order (create the state bucket, init, first apply,
push secrets to SSM, set repo secrets, gate the apply environment), see
[`deploy/terraform/README.md`](../deploy/terraform/README.md).

**What Terraform creates:**

- Custom VPC with an Elastic IP (instance replacement does not move DNS)
- `t4g.micro` Amazon Linux 2023 arm64 host; `user_data` installs Docker +
  compose, adds 2 G swap, writes `docker-compose.prod.yml` and the Caddyfile,
  rebuilds `/opt/portfolio/.env` from SSM Parameter Store, and starts the stack
- IAM: GitHub OIDC provider, instance role (S3/SQS/SES/SSM), deploy role, and
  plan/apply roles for CI
- S3 buckets: assets and backups (backups bucket has lifecycle expiry)
- SQS queue
- App config as SSM `String` parameters; secrets as SSM `SecureString`
  parameters (secret values are pushed once via `aws ssm put-parameter` and
  never enter Terraform state)
- Cloudflare DNS: `api.aliflabs.dev` A record (grey-cloud so Caddy can run ACME)
  and SES DKIM CNAMEs
- SES domain identity + Easy DKIM for `aliflabs.dev`
- Cost budget (~17 USD/month, roughly 25 AUD; AWS Budgets only supports USD)
  with email alerts

### CI/CD

**App image deploys** (`deploy-backend.yml`) run on push to `main` for backend
changes:

1. Builds `backend/Dockerfile`, pushes
   `ghcr.io/alifyandra/portfolio-backend:{latest,<sha>}`.
2. Assumes `AWS_DEPLOY_ROLE_ARN` via OIDC and sends an SSM command that pulls
   the new image tag and runs `docker compose up -d`.

**Infrastructure changes** (`deploy/terraform/**`) flow through
`.github/workflows/terraform.yml`:

- `plan` on pull requests (read-only `TF_PLAN_ROLE_ARN` via OIDC)
- `apply` on merge to main, gated behind the `production` GitHub Environment
  (requires a reviewer approval before it runs)

Required GitHub repo **secrets** (set once from `terraform output` after the
first apply; see the Terraform README):

| Secret | Purpose |
|--------|---------|
| `AWS_REGION` | e.g. `ap-southeast-2` |
| `EC2_INSTANCE_ID` | target instance for SSM app deploys |
| `AWS_DEPLOY_ROLE_ARN` | IAM role assumed for SSM container pulls |
| `TF_PLAN_ROLE_ARN` | read-only OIDC role for `terraform plan` in CI |
| `TF_APPLY_ROLE_ARN` | write OIDC role for gated `terraform apply` in CI |
| `CLOUDFLARE_API_TOKEN` | Zone DNS edit token for `aliflabs.dev` |
| `CLOUDFLARE_ZONE_ID` | Zone ID for `aliflabs.dev` |

`GITHUB_TOKEN` (automatic) handles the GHCR push.

### Database backups

Postgres runs on-box. Schedule `pg_dump` to the S3 backups bucket (cron or a
tiny systemd timer); the backups bucket (with lifecycle expiry) is created by
Terraform. A single-box deploy has no managed backups (the tradeoff documented
in ADR 6).

## Email notifications (AWS SES)

The contact form stores every message in Postgres and (when configured) enqueues
a `contact.notify` SQS job; the **worker** sends the email via SES.

SES domain identity and DKIM records for `aliflabs.dev` are codified in
Terraform. DKIM CNAMEs are published automatically via the Cloudflare provider
and verify once DNS propagates. The SQS queue and the instance profile
`ses:SendEmail` permission are also created by Terraform.

To finish enabling email:

1. Set `SES_SENDER_EMAIL` (e.g. `noreply@aliflabs.dev`) in Parameter Store
   (`/portfolio/env/SES_SENDER_EMAIL`), then re-pull on the box.
2. New SES accounts are **sandboxed** (can only send to verified addresses).
   Either verify `CONTACT_NOTIFY_TO`, or request **production access** via the
   SES console (ap-southeast-2) to send freely. Default recipient is
   `alifyandra@gmail.com` (`CONTACT_NOTIFY_TO`).

Until SES and the queue are configured, messages are still stored and the app
logs and skips the email.

## Migrations

`AUTO_MIGRATE=true` runs Ent's schema auto-migration on startup, which is fine
for a single box. Move to versioned migrations before doing anything destructive
to the schema.
