# Deployment

Two targets: **frontend on Vercel**, **backend on a single EC2 host** running
docker compose (see [ADR 6](adr/0006-ec2-compose-over-fargate.md)).

## Frontend (Vercel)

1. Import the repo in Vercel. Set **Root Directory** = `frontend`.
2. Build command stays the default (`npm run build`); the `prebuild` hook runs
   orval against `../backend/openapi.yaml` automatically.
3. Add env var `NEXT_PUBLIC_API_URL` = `https://<your-api-domain>`.
4. Vercel auto-deploys on every push and creates per-PR preview URLs.

## Backend (EC2 + docker compose)

### One-time host setup

1. Launch a **`t4g.micro`** (arm64, 1 GB) in **ap-southeast-2**, Amazon Linux
   2023. Attach an **IAM instance profile** allowing the app's S3 bucket, SQS
   queue, and `ses:SendEmail`, plus `AmazonSSMManagedInstanceCore` (so GitHub
   Actions can deploy via SSM). Security group: open **443/80 only**; manage the
   box via **SSM Session Manager**, not a public SSH port.
2. Install Docker + the compose plugin; enable the service. Add a swap file so
   1 GB has headroom under transient spikes:
   ```bash
   sudo fallocate -l 2G /swapfile && sudo chmod 600 /swapfile
   sudo mkswap /swapfile && sudo swapon /swapfile
   echo '/swapfile none swap sw 0 0' | sudo tee -a /etc/fstab
   ```
3. Create the deploy directory and copy the compose + deploy files:
   ```bash
   sudo mkdir -p /opt/portfolio/deploy
   # copy docker-compose.prod.yml and deploy/Caddyfile to /opt/portfolio
   ```
4. Create `/opt/portfolio/.env` from `.env.example` with **production** values:
   - real `POSTGRES_PASSWORD`, `DATABASE_URL` (host `postgres`)
   - `APP_ENV=production`, `S3_ENDPOINT_URL=` (empty → real S3),
     `S3_FORCE_PATH_STYLE=false`, real `SQS_QUEUE_URL`
   - `DOMAIN=api.yourdomain.com` (point its DNS A record at the EC2 IP)
   - omit `AWS_ACCESS_KEY_ID`/`SECRET` if using the instance profile (preferred)
5. Log in to GHCR once (or make the package public) and start it:
   ```bash
   cd /opt/portfolio
   docker compose -f docker-compose.prod.yml up -d
   ```
   Caddy obtains a TLS cert for `$DOMAIN` automatically.

### CI/CD

`.github/workflows/deploy-backend.yml` runs on push to `main` (backend changes):

1. Builds `backend/Dockerfile`, pushes `ghcr.io/<owner>/portfolio-backend:{latest,<sha>}`.
2. Assumes an AWS role via OIDC and sends an SSM command that pulls the new
   image tag and runs `docker compose up -d`.

Required GitHub repo **secrets**:

| Secret | Purpose |
|--------|---------|
| `AWS_DEPLOY_ROLE_ARN` | IAM role GitHub assumes via OIDC (trust the repo) |
| `AWS_REGION` | e.g. `ap-southeast-2` |
| `EC2_INSTANCE_ID` | target instance for SSM |

`GITHUB_TOKEN` (automatic) handles the GHCR push.

### Database backups

Postgres runs on-box. Schedule `pg_dump` to S3 (cron or a tiny systemd timer)
— a single-box deploy has no managed backups (the tradeoff in ADR 6).

## Email notifications (AWS SES)

The contact form stores every message in Postgres and (when configured) enqueues
a `contact.notify` SQS job; the **worker** sends the email via SES. To enable:

1. In SES (ap-southeast-2), **verify a sender** — a domain (preferred) or a
   single email address. Set `SES_SENDER_EMAIL` to it in `/opt/portfolio/.env`.
2. New SES accounts are **sandboxed** (can only send to verified addresses).
   Either verify `CONTACT_NOTIFY_TO`, or request **production access** to send
   freely. Default recipient is `alifyandra@gmail.com` (`CONTACT_NOTIFY_TO`).
3. Give the EC2 instance profile `ses:SendEmail` permission.
4. Create the SQS queue and set `SQS_QUEUE_URL` in `.env`. The `worker` service
   is already in `docker-compose.prod.yml`, so `docker compose -f
   docker-compose.prod.yml up -d` runs it. Until SES + the queue are configured,
   messages are still stored — the app just logs and skips the email.

## Migrations

`AUTO_MIGRATE=true` runs Ent's schema auto-migration on startup — fine for a
single box. Move to versioned migrations before doing anything destructive to
the schema.
