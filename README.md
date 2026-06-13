# Portfolio Site

Personal portfolio for Ahmad Alifyandra (Alif). It is a Go backend with a
Next.js frontend, built to grow past a static page: there is an async queue and
room to add LLM features later.

## Stack

| Layer | Choice | Why |
|-------|--------|-----|
| Frontend | Next.js (App Router) + Tailwind | On Vercel ([ADR 3](docs/adr/0003-vercel-frontend-aws-backend-split.md)) |
| Backend | Go + Chi + [Huma](https://huma.rocks) | Concurrency model, single-binary deploy ([ADR 1](docs/adr/0001-go-backend.md)) |
| ORM | [Ent](https://entgo.io) (codegen) | Typed queries, no hand-written SQL ([ADR 4](docs/adr/0004-ent-orm.md)) |
| API contract | Huma to OpenAPI to [orval](https://orval.dev) hooks | Code-first and type-safe ([ADR 5](docs/adr/0005-contract-first-codegen.md)) |
| DB / cache | PostgreSQL / Redis | |
| Async | AWS SQS (worker seam) | [ADR 7](docs/adr/0007-sqs-async-queue.md) |
| Storage | S3 (MinIO locally) | |
| Prod | EC2 t4g.micro + docker compose + Caddy | Cheap to run ([ADR 6](docs/adr/0006-ec2-compose-over-fargate.md)) |

For more detail, see [`CONTEXT.md`](CONTEXT.md) for the domain language,
[`docs/adr/`](docs/adr) for the decisions,
[`docs/design/color-palette.md`](docs/design/color-palette.md) for the palette,
[`docs/deployment.md`](docs/deployment.md) for deploy steps, and
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

You need Docker and Node 22+. A local Go install is optional, since the backend
builds run in Docker.

```bash
make setup     # copies .env, installs frontend deps, runs codegen
make up        # Postgres + Redis + MinIO + API at http://localhost:8080
make seed      # insert starter projects
make fe-dev    # Next.js at http://localhost:3000 (separate terminal)
```

- API docs (Huma): http://localhost:8080/docs
- Health: http://localhost:8080/healthz
- Run `make up-async` to also start the worker and local SQS (ElasticMQ).

## The codegen pipeline

The API contract flows one way and stays type-safe the whole way:

```
Go handlers --(make generate-spec)--> backend/openapi.yaml --(make codegen)--> frontend hooks
```

After you change a handler, run `make generate`. It regenerates Ent, the spec,
and the frontend hooks. CI fails if `openapi.yaml` is out of date.

## Deployment

- Frontend goes to Vercel. Set the Root Directory to `frontend` and
  `NEXT_PUBLIC_API_URL` to the API domain. It auto-deploys on every push.
- Backend goes to EC2. GitHub Actions builds the image, pushes it to GHCR, and
  redeploys over SSM. See [`.github/workflows`](.github/workflows) and the
  one-time setup in [`docs/deployment.md`](docs/deployment.md).

## License

Personal project. All rights reserved unless stated otherwise.
