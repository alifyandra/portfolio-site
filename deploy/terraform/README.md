# Terraform: AWS host + DNS + SES

Provisions the AWS side of the deploy: a custom VPC, one `t4g.micro` host on an
Elastic IP, IAM (GitHub OIDC roles + instance role), S3 buckets, an SQS queue,
the app `.env` as SSM parameters, Cloudflare DNS, SES domain identity, and a
cost budget. Flat root, one box, one environment (see
[ADR 9](../../docs/adr/0009-terraform-provisioning.md)).

The host is codified end to end: `user_data` installs Docker + compose, adds
swap, writes `docker-compose.prod.yml` and the Caddyfile, rebuilds `.env` from
SSM Parameter Store, and starts the stack. Replacing the box keeps the EIP, so
DNS does not move.

## Layout

| File | What it holds |
|------|---------------|
| `main.tf` | providers, S3 backend, shared data/locals |
| `network.tf` | VPC, 2 public subnets/2 AZs, IGW, route table, SG, EIP |
| `compute.tf` | AMI lookup, instance, EIP association, `user_data` |
| `iam.tf` | GitHub OIDC provider, instance + deploy + plan/apply roles |
| `storage.tf` | assets bucket, backups bucket (+lifecycle), SQS queue |
| `ssm.tf` | `/portfolio/env/*` parameters (config + secret slots) + `/portfolio/tls/*` origin cert/key |
| `dns.tf` | Cloudflare `api` A record (proxy via `proxy_api`) + SES DKIM CNAMEs |
| `ses.tf` | SES domain identity + Easy DKIM |
| `budget.tf` | ~25 AUD/month budget with email alerts |
| `variables.tf` / `outputs.tf` | inputs and exported ARNs/IDs |

## One-time bootstrap

Run from this directory with AWS credentials that can create the stack (an admin
user, locally) plus a scoped Cloudflare API token.

1. **Create the state bucket** (chicken-and-egg: the backend can't create its own
   bucket). Name must match `backend "s3"` in `main.tf`:

   ```bash
   aws s3api create-bucket \
     --bucket aliflabs-terraform-state \
     --region ap-southeast-2 \
     --create-bucket-configuration LocationConstraint=ap-southeast-2
   aws s3api put-bucket-versioning \
     --bucket aliflabs-terraform-state \
     --versioning-configuration Status=Enabled
   aws s3api put-bucket-encryption \
     --bucket aliflabs-terraform-state \
     --server-side-encryption-configuration \
     '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
   ```

2. **Set credentials** for this shell:

   ```bash
   export AWS_PROFILE=...                 # or AWS_ACCESS_KEY_ID / SECRET
   export CLOUDFLARE_API_TOKEN=...        # Zone:DNS:Edit on aliflabs.dev
   export TF_VAR_cloudflare_zone_id=...   # zone id for aliflabs.dev
   ```

3. **Init and seed the secret slots first.** Apply just the SecureString
   parameters so they exist before the box that reads them:

   ```bash
   terraform init
   terraform apply -target=aws_ssm_parameter.secret
   ```

4. **Push the real secret values** to Parameter Store, overwriting the
   `CHANGE_ME` placeholders (these never enter Terraform state). Do this *before*
   the full apply: the instance rebuilds `.env` from SSM on first boot, and
   Postgres initialises its volume with whatever `POSTGRES_PASSWORD` it sees
   then, so the real value has to be in place first.

   ```bash
   for k in POSTGRES_PASSWORD DATABASE_URL SPOTIFY_CLIENT_ID \
            SPOTIFY_CLIENT_SECRET SPOTIFY_REFRESH_TOKEN; do
     read -rsp "$k: " v; echo
     aws ssm put-parameter --name "/portfolio/env/$k" \
       --type SecureString --value "$v" --overwrite --region ap-southeast-2
   done
   ```

   `DATABASE_URL` for the on-box Postgres looks like
   `postgres://portfolio:<POSTGRES_PASSWORD>@postgres:5432/portfolio?sslmode=disable`.

5. **Full apply.** Creates the rest, including the host, which now boots with the
   real secrets:

   ```bash
   terraform apply
   ```

6. **Set the repo secrets** GitHub Actions needs (one-off; not done in Terraform
   on purpose, see ADR 9). Read the ARNs straight from outputs:

   ```bash
   gh secret set AWS_REGION            --body "$(terraform output -raw region)"
   gh secret set AWS_DEPLOY_ROLE_ARN   --body "$(terraform output -raw app_deploy_role_arn)"
   gh secret set TF_PLAN_ROLE_ARN      --body "$(terraform output -raw terraform_plan_role_arn)"
   gh secret set TF_APPLY_ROLE_ARN     --body "$(terraform output -raw terraform_apply_role_arn)"
   gh secret set CLOUDFLARE_API_TOKEN  --body "$CLOUDFLARE_API_TOKEN"
   gh secret set CLOUDFLARE_ZONE_ID    --body "$TF_VAR_cloudflare_zone_id"
   ```

   There is no `EC2_INSTANCE_ID` secret: `deploy-backend.yml` resolves the live
   instance by `tag:Name=portfolio-app` at runtime (the `app-deploy` role carries
   `ec2:DescribeInstances`), so a box replacement never strands the deploy on a
   stale ID. If an old `EC2_INSTANCE_ID` secret exists from a prior bootstrap, it
   is unused and can be removed (`gh secret delete EC2_INSTANCE_ID`).

7. **Create the `production` Environment.** In repo Settings > Environments,
   create `production`. It pins the apply role's OIDC trust
   (sub `...:environment:production`) and, now that the repo is public, carries a
   **required-reviewer** rule: a dispatched `terraform apply` pauses for an
   explicit approval before it runs (self-review is allowed for the solo
   maintainer). The apply workflow is also manual (`workflow_dispatch`), so infra
   never mutates unattended. See [ADR 9](../../docs/adr/0009-terraform-provisioning.md)
   and the `terraform.yml` header.

8. **Request SES production access.** New accounts are sandboxed (can only send
   to verified addresses). In the SES console (ap-southeast-2), submit the
   production-access request. DKIM verification of `aliflabs.dev` happens
   automatically once the CNAMEs propagate.

9. **Make the GHCR package pullable.** The box pulls
   `ghcr.io/alifyandra/portfolio-backend` with no AWS credentials. Set the
   package to public, or add a GHCR login step to `user_data`.

After this, `deploy/terraform/**` changes flow through CI: `plan` on PRs,
gated `apply` on merge to main. App image deploys stay on the separate
`deploy-backend.yml` SSM seam.

## Day-2 notes

- Config in `ssm.tf` (`env_config`) is Terraform-managed; edit, apply, then
  re-pull on the box. Secret slots (`env_secrets`) are ignored after creation.
- After changing a secret value, push it with `aws ssm put-parameter
  --overwrite` and force a re-pull on the box:
  ```bash
  aws ssm send-command --instance-ids "$(terraform output -raw instance_id)" \
    --document-name AWS-RunShellScript --region ap-southeast-2 \
    --parameters 'commands=["cd /opt/portfolio","docker compose -f docker-compose.prod.yml up -d --force-recreate"]'
  ```
- CI runs `terraform fmt -check` and `validate` on every PR.

## Cloudflare proxy cutover (origin lock)

Putting `api.<domain>` behind the Cloudflare proxy and locking the origin to
Cloudflare is gated by two flags (`proxy_api`, `lock_origin_to_cloudflare`). The
cutover below is complete, so their defaults are now `true`; set a flag to `false`
(via the dispatch input or by editing the default) to roll back. Caddy switches
from Let's Encrypt/ACME to a Cloudflare
**origin certificate** so it never needs to reach the public internet once the
security group is locked. SSM Session Manager is always available, so a wrong SG
never locks you out. Do the steps in order.

**How the flags get applied.** Stage each flag through the **`Terraform` workflow's
`workflow_dispatch`** (Actions tab → Run workflow), setting the `proxy_api` /
`lock_origin_to_cloudflare` inputs. The dispatch runs gated `plan-dispatch` →
`apply` jobs: the first approval lets the plan run (read it), the second applies
it. Inputs are passed as `-var` only when set, so they stage a flag *without*
committing it first — no local creds, no per-step PR. (`*.tfvars` is gitignored,
so committed config lives in the variable defaults.) Once both flags are live and
verified, **reconcile**: a small PR flipping the two `default = false` to `true`
in `variables.tf` so committed config matches reality (its CI plan should show no
changes). Roll back by dispatching with the input(s) set to `false`.

1. **Generate a Cloudflare Origin Certificate** (dashboard → SSL/TLS → Origin
   Server → Create Certificate). Cover `aliflabs.dev` and `*.aliflabs.dev`. Save
   the certificate PEM and the private key.

2. **Push the cert + key** to the `/portfolio/tls/*` SecureString slots (real
   values never enter Terraform state):

   ```bash
   aws ssm put-parameter --name /portfolio/tls/origin_cert --type SecureString \
     --overwrite --region ap-southeast-2 --value file://origin.pem
   aws ssm put-parameter --name /portfolio/tls/origin_key --type SecureString \
     --overwrite --region ap-southeast-2 --value file://origin.key
   ```

3. **Set SSL/TLS mode to Full (strict)** in the Cloudflare dashboard (never
   Flexible). The origin cert satisfies strict validation.

4. **Proxy the api + apply.** Dispatch the `Terraform` workflow with
   `proxy_api = true` (leave `lock_origin_to_cloudflare = unchanged`). This
   orange-clouds the api record and **replaces the box** so Caddy boots serving
   the origin cert. The EIP stays, so DNS does not move. Verify
   `https://api.<domain>/healthz` through Cloudflare before continuing.

5. **Lock the origin + apply.** Dispatch again with `proxy_api = true` and
   `lock_origin_to_cloudflare = true` (both, since the committed defaults are
   still `false` until you reconcile). The web SG now accepts 80/443 only from
   `cloudflare_ip_ranges`; the box is unreachable except via Cloudflare (manage
   it via SSM). This is an in-place SG change, not a box replacement.

6. **Reconcile.** Open a PR flipping both `default = false` to `true` in
   `variables.tf`. Its CI plan should report no changes (live already matches),
   confirming committed config now equals reality. After this, a plain dispatch
   keeps the flags on.

To roll back, dispatch with the relevant input(s) set to `false` (lock first,
then proxy), then revert the reconcile defaults.

## WhatsApp sidecar (Fargate) deploy

The WhatsApp Sender tool (ADR 11) runs its `whatsapp-web.js` + Chromium sidecar as
an on-demand Fargate task the backend launches per batch (see the 2026-07-07 ADR 11
amendment and issue #58). The Terraform is in `whatsapp.tf` (ECR repo, ECS cluster,
arm64 task definition, security group, IAM); the backend launcher is gated by
`WA_SIDECAR_MODE=fargate`, set in `ssm.tf` `env_config`. Bring it up once, in order:

1. **Apply the infra.** Dispatch the `Terraform` workflow (gated `plan-dispatch` →
   `apply`) or apply locally. This creates the ECR repo `portfolio-wa-sidecar`, the
   ECS cluster, the task definition, the SG, and the IAM, and sets
   `WA_SIDECAR_MODE=fargate` plus `WA_ECS_CLUSTER` / `WA_TASK_DEFINITION` /
   `WA_SUBNET_IDS` / `WA_SECURITY_GROUP_ID` in SSM.

2. **Seed the shared secret.** Backend and sidecar authenticate with a bearer secret
   in `/portfolio/env/WA_SIDECAR_SECRET` (a placeholder until seeded, so sends stay
   inert). Push a real value:

   ```bash
   aws ssm put-parameter --name /portfolio/env/WA_SIDECAR_SECRET --type SecureString \
     --overwrite --region ap-southeast-2 --value "$(openssl rand -hex 32)"
   ```

3. **Build + push the arm64 image to ECR** (from the private `whatsapp-sidecar`
   repo; the task def pins `arm64`, so the image must be arm64):

   ```bash
   AWS_REGION=ap-southeast-2
   ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
   REPO=$(terraform -chdir=path/to/portfolio-site/deploy/terraform output -raw wa_ecr_repository_url)
   aws ecr get-login-password --region "$AWS_REGION" \
     | docker login --username AWS --password-stdin "$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com"
   docker buildx build --platform linux/arm64 -t "$REPO:latest" --push .
   ```

4. **Redeploy the backend.** `deploy-backend.yml` rebuilds `.env` from SSM on every
   deploy, so a redeploy picks up `WA_SIDECAR_MODE=fargate` and the secret (push to
   `main` after merge, or the manual deploy path).

5. **Verify a live send.** As a friend/admin, open `/whatsapp`, create a template +
   list with a **real** recipient number (mind the `+61` default, issue #54), start a
   batch, and scan the QR. The browser shows a "provisioning" state during the
   ~30-60s Fargate cold start, then the QR; confirm delivery on the recipient phone.

Notes: the task runs in a public subnet with `assignPublicIp=ENABLED` (no NAT) so it
can pull from ECR and read SSM. Teardown is `StopTask` on relay end, backstopped by
the sidecar's `WA_SHUTDOWN_AFTER_SESSION=true` self-exit. Logs land in CloudWatch
`/ecs/portfolio-wa-sidecar`. The first ECR push is manual; sidecar CI (OIDC build +
push) is a later follow-up.

## Digest / scheduled jobs (Fargate) deploy

The digest platform (ADR 13) runs a daily scheduled job: EventBridge Scheduler enqueues
a `digest.build` message onto the shared jobs queue, the on-box worker consumes it and
launches the `cmd/digest` container as a run-to-completion Fargate task, which fetches
public Sources, summarizes them with the Anthropic API, writes a Digest row, and exits.
The Terraform is in `digest.tf` (ECR repo, log group, arm64 task definition, egress-only
SG, exec/task IAM roles, the instance-role RunTask additions, the SQS DLQ + redrive on the
shared queue in `storage.tf`, and the scheduler + its role). It reuses the WhatsApp ECS
cluster, VPC, and subnets. Local dev is unchanged.

This is a gated slice, applied later by the maintainer (same shape as WhatsApp Slice E).
Do the steps in order.

1. **Apply the infra.** Dispatch the `Terraform` workflow (gated `plan-dispatch` -> `apply`)
   or apply locally. This creates the ECR repo `portfolio-digest`, the task definition, the
   log group, the digest SG, the IAM, the `portfolio-jobs-dlq` queue, and the (disabled)
   schedule, raises the jobs-queue visibility timeout to `jobs_visibility_timeout_seconds`
   (default 1200s, above the launcher's 15m/900s cap), and sets `DIGEST_MODE=fargate`
   plus `DIGEST_ECS_CLUSTER` / `DIGEST_TASK_DEFINITION` / `DIGEST_SUBNET_IDS` /
   `DIGEST_SECURITY_GROUP_ID` / `DIGEST_MODEL` / `DIGEST_MAX_TOKENS` in SSM. The schedule
   stays off until step 6 (`enable_digest_schedule` defaults to `false`).

2. **Seed the Anthropic API key.** `ANTHROPIC_API_KEY` is a SecureString placeholder until
   seeded, so digest runs are inert:

   ```bash
   aws ssm put-parameter --name /portfolio/env/ANTHROPIC_API_KEY --type SecureString \
     --overwrite --region ap-southeast-2 --value "sk-ant-..."
   ```

3. **Seed the digest DATABASE_URL with the box private IP.** The digest task reaches Postgres
   over the box's private IP, not the docker-network name `postgres`, so it needs its own DSN.
   Read the private IP from Terraform output and bake it in (reuse the same
   `POSTGRES_USER` / `POSTGRES_PASSWORD` / `POSTGRES_DB` as the app's `DATABASE_URL`):

   ```bash
   IP=$(terraform -chdir=path/to/portfolio-site/deploy/terraform output -raw app_private_ip)
   aws ssm put-parameter --name /portfolio/env/DIGEST_DATABASE_URL --type SecureString \
     --overwrite --region ap-southeast-2 \
     --value "postgres://portfolio:<POSTGRES_PASSWORD>@$IP:5432/portfolio?sslmode=disable"
   ```

   The private IP is stable across reboots but changes if the box is replaced (an AMI bump or
   a `user_data` change). Re-seed this param after any box replacement.

4. **Confirm on-box Postgres listens on the private interface.** This is a runtime/host concern
   Terraform cannot fully assert. The web SG already allows tcp/5432 from the digest SG, but the
   `docker-compose.prod.yml` Postgres service has no host port mapping, so it is only reachable
   on the docker bridge, not the box's private interface. Publish it before the first run, for
   example by adding a `ports` mapping to the postgres service (`"5432:5432"`, or better the
   private IP form `"<private-ip>:5432:5432"` so it is not bound on all interfaces) and
   redeploying. Validate from the box over SSM Session Manager:

   ```bash
   ss -tlnp | grep 5432          # postgres listening on 0.0.0.0:5432 or the private IP, not just 127.0.0.1
   ```

   The SG restricts inbound 5432 to the digest SG regardless, so a `0.0.0.0` bind is not
   internet-exposed, but the private-IP bind is the tighter choice.

5. **Build + push the arm64 image to ECR** (the task def pins `arm64`, so the image must be arm64).
   The digest binary ships in the shared `backend/Dockerfile` (it builds `api`/`worker`/`seed`/`digest`
   side by side); the task def selects it with `command = ["digest"]`, so push that same image to the
   digest ECR repo:

   ```bash
   AWS_REGION=ap-southeast-2
   ACCOUNT=$(aws sts get-caller-identity --query Account --output text)
   REPO=$(terraform -chdir=path/to/portfolio-site/deploy/terraform output -raw digest_ecr_repository_url)
   aws ecr get-login-password --region "$AWS_REGION" \
     | docker login --username AWS --password-stdin "$ACCOUNT.dkr.ecr.$AWS_REGION.amazonaws.com"
   docker buildx build --platform linux/arm64 -t "$REPO:latest" --push -f backend/Dockerfile backend
   ```

6. **Redeploy the backend, then enable the schedule.** `deploy-backend.yml` rebuilds `.env` from
   SSM on every deploy, so a redeploy picks up `DIGEST_MODE=fargate` and the seeded secrets. Once
   the manual test run (step 7) is green, enable the daily cron by dispatching the `Terraform`
   workflow with `enable_digest_schedule = true` (or flip the default and reconcile, per the
   Cloudflare-cutover pattern above).

7. **Trigger a manual test run.** Send one `digest.build` message to the shared queue and watch
   the worker launch the task:

   ```bash
   QURL=$(terraform -chdir=path/to/portfolio-site/deploy/terraform output -raw sqs_queue_url)
   aws sqs send-message --region ap-southeast-2 --queue-url "$QURL" \
     --message-body '{"type":"digest.build","payload":{}}'
   ```

   Confirm a Fargate task appears in the `portfolio-wa` cluster, its logs land in CloudWatch
   `/ecs/portfolio-digest`, it exits 0, and a Digest row is written. A run that fails 3 times
   lands in `portfolio-jobs-dlq`; inspect it with `aws sqs receive-message` on the DLQ URL.

Notes: the task runs in a public subnet with `assignPublicIp=ENABLED` (no NAT) so it can pull
from ECR, read SSM, and reach `api.anthropic.com`. Retry is SQS redelivery (`maxReceiveCount`
`jobs_max_receive_count`, default 3) then the DLQ; the daily schedule is the backstop. The jobs
queue visibility timeout (`jobs_visibility_timeout_seconds`, default 1200s) sits above the digest
launcher's 15m (900s) hard runtime cap so a slow run is never redelivered mid-flight, and applies to `contact.notify`
too (harmless, it runs inline in well under a second). The first ECR push is manual; digest CI is
a later follow-up.
