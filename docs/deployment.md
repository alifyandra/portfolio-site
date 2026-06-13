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

1. Launch a **`t4g.small`** (arm64) in **ap-southeast-2**, Amazon Linux 2023.
   Attach an **IAM instance profile** allowing the app's S3 bucket + SQS queue,
   and `AmazonSSMManagedInstanceCore` (so GitHub Actions can deploy via SSM).
2. Install Docker + the compose plugin; enable the service.
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

## Migrations

`AUTO_MIGRATE=true` runs Ent's schema auto-migration on startup — fine for a
single box. Move to versioned migrations before doing anything destructive to
the schema.
