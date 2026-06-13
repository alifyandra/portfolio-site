# 6. EC2 + docker compose over ECS Fargate

Date: 2026-06-13
Status: Accepted

## Context

Budget is ~**$20 AUD/month**. Realistic Sydney (ap-southeast-2) monthly costs:

| Item | Fargate stack | App Runner | EC2 + Compose |
|------|---------------|-----------|----------------|
| Compute | ~$9 | ~$5–7 | t4g.small ~$12 |
| Load balancer | ALB ~$18 | included | none (Caddy) |
| Database | RDS ~$15 + storage | RDS ~$18 | Postgres in compose: $0 |
| **~Total (USD)** | **~$45 (~$65 AUD)** | **~$25 (~$37 AUD)** | **~$12 (~$18 AUD)** |

The ALB alone (~$25 AUD) exceeds the budget before compute or a database. A
managed Fargate + RDS + ALB stack is unattainable under $20 AUD.

## Decision

Run a **single EC2 `t4g.small`** with **docker compose** (Go + Postgres + Redis
+ Caddy for automatic HTTPS), self-hosting the database and cache on the box.
Build images in GitHub Actions → push to **GHCR** → pull on the host.

This also satisfies the "same setup local and prod" goal: the *same*
`docker-compose.yml` (with a prod override) runs in both places.

## Consequences

- We self-manage Postgres backups, patching, TLS (Caddy), and uptime.
- No hands-on Fargate/ALB/RDS reps for now — partial cert-coverage loss,
  mitigated by real **EC2 + SQS + S3 + IAM** practice.
- **Keep everything container-first and infra-as-code** so a later lift to
  Fargate is a config change, not a rewrite — revisit when budget allows.
- Single point of failure (one box); acceptable for a portfolio.

## Alternatives rejected

- ECS Fargate / App Runner: both blow the $20 AUD budget, chiefly via ALB + RDS.
