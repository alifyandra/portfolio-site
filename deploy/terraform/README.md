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
   gh secret set EC2_INSTANCE_ID       --body "$(terraform output -raw instance_id)"
   gh secret set AWS_DEPLOY_ROLE_ARN   --body "$(terraform output -raw app_deploy_role_arn)"
   gh secret set TF_PLAN_ROLE_ARN      --body "$(terraform output -raw terraform_plan_role_arn)"
   gh secret set TF_APPLY_ROLE_ARN     --body "$(terraform output -raw terraform_apply_role_arn)"
   gh secret set CLOUDFLARE_API_TOKEN  --body "$CLOUDFLARE_API_TOKEN"
   gh secret set CLOUDFLARE_ZONE_ID    --body "$TF_VAR_cloudflare_zone_id"
   ```

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
Cloudflare is gated by two flags (both default `false`, so the committed config
is the pre-proxy posture). Caddy switches from Let's Encrypt/ACME to a Cloudflare
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
