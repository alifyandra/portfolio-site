# CLAUDE.md

Orientation for Claude Code working in this repo. Keep it short; deeper rationale
lives in the linked docs.

## What this is

Personal portfolio for Ahmad Alifyandra (Alif). Monorepo: **Go backend** + **Next.js
frontend**, built as a scalable foundation (async queue + LLM-ready), not a static page.

- Domain language / glossary: [`CONTEXT.md`](CONTEXT.md)
- Decisions (read before changing architecture): [`docs/adr/`](docs/adr)
- Deploy steps: [`docs/deployment.md`](docs/deployment.md)
- Security runbook: [`docs/security.md`](docs/security.md)
- Palette: [`docs/design/color-palette.md`](docs/design/color-palette.md)

## Stack (see ADRs for why)

Go · Chi · **Huma** (code-first OpenAPI) · **Ent** (codegen ORM) · pgx/Postgres ·
Redis (cache only) · S3 · SQS (worker) · SES (email). Frontend: Next.js + Tailwind +
**orval** React Query hooks. Prod: EC2 `t4g.micro` + docker compose + Caddy; frontend
on Vercel.

## Layout

```
backend/   cmd/{api,worker,seed,spec}, internal/{api,server,config,storage,queue,cache,spotify,email,bootstrap}
           ent/schema/ (entities), openapi.yaml (generated)
frontend/  Next app; src/lib/api/ = orval-generated (gitignored)
docs/      ADRs + deployment + security + design
```

## Running locally

No local Go needed — backend tasks run in Docker. **Docker must be running.**

```bash
make setup     # .env + frontend npm install + codegen
make up        # Postgres + Redis + MinIO + API at :8080
make seed      # starter projects
make fe-dev    # Next.js at :3000 (separate terminal)
```

`make generate` (Ent + OpenAPI spec + frontend hooks), `make test`, `make help`.

## Conventions & gotchas (important)

- **Codegen pipeline:** Go handlers → `make generate-spec` → `backend/openapi.yaml`
  → `make codegen` → frontend hooks. **After changing any handler, run `make generate`
  and commit `openapi.yaml`** — CI fails if the committed spec is stale.
- **Go 1.25 required** (Huma v2 needs it). Generated Ent code and frontend hooks are
  **gitignored** — regenerated on build/CI. Schema + `openapi.yaml` are committed.
- **Ent generation must run BEFORE `go mod tidy`** (else the not-yet-generated
  `ent/*` packages look like missing remote modules). The Dockerfile/CI do this order.
- DB is opened via pgx stdlib + `entsql.OpenDB(dialect.Postgres, db)` — **not**
  `ent.Open("pgx", …)` (that fails at runtime).
- **No public write endpoints** — projects are seed-only until auth exists. Don't
  re-add an unauthenticated `POST /api/projects`.
- Contact form has a honeypot (`website` field) + per-IP rate limit. SES/email and
  SQS degrade gracefully when unconfigured (message still stored).
- Graceful-degradation pattern: Spotify/SES/queue all no-op cleanly without creds.

## State (as of last session)

- ✅ Backend + frontend scaffolded; CI green; repo **private** (`alifyandra/portfolio-site`).
- ✅ SES email-on-contact via the SQS worker seam; LinkedIn link; security hardening
  (honeypot, rate limit, headers, govulncheck, Dependabot).
- ✅ Résumé PDF removed and **scrubbed from git history**; `*.pdf` gitignored.
- ⏳ **Not yet done:** actual EC2 deploy (needs AWS secrets — see deployment.md),
  Spotify creds, Cloudflare setup (see security.md), and **auth** (deferred for v1).
- The `Deploy Backend` workflow's final SSM step fails until AWS secrets are set — expected.
