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
- Spotify proxy setup + dead endpoints: [`docs/spotify.md`](docs/spotify.md)
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
- ✅ **Pivot decided:** site becomes **aliflabs** — a tools platform; portfolio
  becomes the about area. Path-based routing (not subdomains). Domain
  **`aliflabs.dev`** bought (Cloudflare, DNSSEC on). Platform shell deferred
  until Tool #1; see memory `aliflab-rebrand`. Hero now reads just "Alif".
- ✅ **About Panels** (new concept, see CONTEXT.md): extensible registry in
  `frontend/src/components/panels/`. Two shipped:
  - **Music** — Spotify live now-playing (+recently-played fallback, LIVE badge),
    top tracks, top artists, hand-curated playlists. **Spotify creds wired**
    (refresh token in `.env`). Setup + dead endpoints: `docs/spotify.md`.
  - **Photography** — static masonry of curated photos (`frontend/public/photos/`
    + `src/lib/photos.ts`); originals in gitignored `pics/`.
- ✅ **Deploy IaC built + applied** ([ADR 9](docs/adr/0009-terraform-provisioning.md),
  `deploy/terraform/`): Terraform flat root, S3 remote state (native locking,
  needs TF >= 1.10), hybrid apply (local bootstrap, then plan-on-PR + gated
  apply-on-merge), fully-codified host (`user_data` + SSM Parameter Store for
  `.env`), minimal custom VPC (2 subnets/2 AZs, EIP), DNS + SES via Cloudflare
  provider, on-box Postgres, `pg_dump`-to-S3 backups bucket.
- ✅ **Backend is LIVE** at `https://api.aliflabs.dev` (EC2 `t4g.micro` arm64,
  EIP `13.236.29.201`, account `363045847720`, ap-southeast-2). Verified
  `/healthz`, `/api/projects`, `/docs`, valid TLS. SSM Session Manager works.
  Bootstrap gotchas hit + fixed (see git): instance policy needs
  `ssm:GetParametersByPath` on the **path** ARN not just `/*`; AMI must be the
  **standard** AL2023 (`al2023-ami-2023.*`), not `minimal` (no SSM agent); the
  GHCR image must be **arm64/multi-arch** (Dockerfile cross-compiles, CI builds
  `linux/amd64,linux/arm64`). `prod` Postgres is empty (not seeded).
- ⏳ **Not yet done:** push this branch to `main` (will trigger gated CI apply —
  create the `production` GitHub Environment w/ required reviewer first); Vercel
  `NEXT_PUBLIC_API_URL=https://api.aliflabs.dev`; SES production-access request;
  Cloudflare proxy/security setup (security.md); **auth** (deferred for v1, the
  unlock for dynamic Photography + curated playlists + admin page).
