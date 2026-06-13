# Portfolio Site

Personal portfolio for **Ahmad Alifyandra (Alif)** — a Go backend + Next.js
frontend, built as a scalable foundation (async queue + LLM-ready) rather than a
static page.

## Stack

| Layer | Choice | Why |
|-------|--------|-----|
| Frontend | Next.js (App Router) + Tailwind | On Vercel ([ADR 3](docs/adr/0003-vercel-frontend-aws-backend-split.md)) |
| Backend | Go + Chi + [Huma](https://huma.rocks) | Career ROI ([ADR 1](docs/adr/0001-go-backend.md)) |
| ORM | [Ent](https://entgo.io) (codegen) | Magic without raw SQL ([ADR 4](docs/adr/0004-ent-orm.md)) |
| API contract | Huma → OpenAPI → [orval](https://orval.dev) hooks | Code-first, type-safe ([ADR 5](docs/adr/0005-contract-first-codegen.md)) |
| DB / cache | PostgreSQL / Redis | — |
| Async | AWS SQS (worker seam) | ([ADR 7](docs/adr/0007-sqs-async-queue.md)) |
| Storage | S3 (MinIO locally) | — |
| Prod | EC2 `t4g.micro` + docker compose + Caddy | Budget ([ADR 6](docs/adr/0006-ec2-compose-over-fargate.md)) |

See [`CONTEXT.md`](CONTEXT.md) for domain language, [`docs/adr/`](docs/adr) for
decisions, [`docs/design/color-palette.md`](docs/design/color-palette.md) for the
palette, [`docs/deployment.md`](docs/deployment.md) for deploy steps, and
[`docs/security.md`](docs/security.md) for the security runbook.

## Repo layout

```
backend/         Go API (cmd/api, cmd/worker, cmd/seed, cmd/spec)
  ent/schema/    Ent entities (Project, ContactMessage)
  internal/      api, server, config, storage(S3), queue(SQS), cache, spotify
  openapi.yaml   generated from handlers; source of truth for the frontend
frontend/        Next.js + Tailwind; orval-generated hooks in src/lib/api
docs/            ADRs + design notes
deploy/          Caddyfile, elasticmq.conf
docker-compose.yml        local stack
docker-compose.prod.yml   single-box production stack
```

## Quick start (local)

Prerequisites: **Docker**, **Node 20+**. A local **Go** install is optional —
backend builds run in Docker.

```bash
make setup     # copies .env, installs frontend deps, runs codegen
make up        # Postgres + Redis + MinIO + API at http://localhost:8080
make seed      # insert starter projects
make fe-dev    # Next.js at http://localhost:3000 (separate terminal)
```

- API docs (Huma): http://localhost:8080/docs
- Health: http://localhost:8080/healthz
- Add `make up-async` for the worker + local SQS (ElasticMQ).

## The codegen pipeline

The API contract flows one way, fully type-safe:

```
Go handlers ──(make generate-spec)──▶ backend/openapi.yaml ──(make codegen)──▶ frontend hooks
```

After changing a handler, run `make generate` (regenerates Ent, the spec, and
the frontend hooks). **CI fails if `openapi.yaml` is out of date.**

## Deployment

- **Frontend → Vercel.** Set Root Directory to `frontend` and
  `NEXT_PUBLIC_API_URL` to the API domain. Auto-deploys per push.
- **Backend → EC2.** GitHub Actions builds the image, pushes to GHCR, and
  redeploys over SSM. See [`.github/workflows`](.github/workflows) and the
  one-time setup in [`docs/deployment.md`](docs/deployment.md).

## License

Personal project — all rights reserved unless stated otherwise.
