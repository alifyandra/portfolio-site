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
  needs TF >= 1.10), fully-codified host (`user_data` + SSM Parameter Store for
  `.env`), minimal custom VPC (2 subnets/2 AZs, EIP), DNS + SES via Cloudflare
  provider, on-box Postgres, `pg_dump`-to-S3 backups bucket. CI: `plan` on PRs,
  **manual two-stage (`plan-dispatch` → `apply`) `workflow_dispatch`** gated by a
  required-reviewer rule on the `production` Environment (free on public repos;
  self-review allowed for the solo maintainer). Bootstrap is two-phase (seed SSM
  secrets before the host boots) — see `deploy/terraform/README.md`.
- ✅ **Backend is LIVE** at `https://api.aliflabs.dev` (EC2 `t4g.micro` arm64 on
  a stable EIP, ap-southeast-2; IDs/IPs via `terraform output`). Verified
  `/healthz`, `/api/projects`, `/docs`, valid TLS. SSM works; the **full CI/CD
  seam is validated** (deploy-backend.yml builds multi-arch + redeploys over
  SSM, green). The deploy job **resolves the live instance by `tag:Name=
  portfolio-app` at runtime** (the `app-deploy` role carries `ec2:DescribeInstances`),
  so a box replacement never strands the deploy on a stale ID — there is no
  `EC2_INSTANCE_ID` secret. Bootstrap gotchas hit + fixed (see git): instance policy needs
  `ssm:GetParametersByPath` on the **path** ARN not just `/*`; AMI must be the
  **standard** AL2023 (`al2023-ami-2023.*`), not `minimal` (no SSM agent); the
  GHCR image must be **arm64/multi-arch** (Dockerfile cross-compiles, CI builds
  `linux/amd64,linux/arm64`). `prod` Postgres is empty (not seeded).
- ✅ **Repo is now PUBLIC** (`alifyandra/portfolio-site`); git history was
  rewritten to gmail authorship before going public; GHCR package public.
- ✅ **Cloudflare proxy cutover complete** (see `docs/security.md`,
  `deploy/terraform/README.md`): `api.aliflabs.dev` is behind the CF proxy
  (orange-cloud) with the origin security group locked to Cloudflare's published
  IP ranges; Caddy serves a CF **Origin certificate** (stored in `/portfolio/tls/*`
  SSM, fetched on boot) instead of ACME; SSL/TLS mode is **Full (strict)**;
  `TRUST_CLOUDFLARE_IP=true` so the rate limiter keys off the real visitor IP.
  Direct (non-CF) origin access is dropped at the SG; SSM Session Manager is still
  the way onto the box. Flags `proxy_api` + `lock_origin_to_cloudflare` (both now
  default `true`) are applied through a gated two-stage **`plan-dispatch → apply`**
  `workflow_dispatch` with per-flag inputs, so a cutover can stage without
  committing first or putting CF creds in a local shell.
- ✅ Vercel `NEXT_PUBLIC_API_URL=https://api.aliflabs.dev` confirmed live (baked
  into the prod bundle; frontend auto-deploys from `main`).
- ⏳ **Not yet done:** SES production-access request (DKIM already verifying);
  remaining Cloudflare freebies (Bot Fight Mode is a dashboard toggle; the
  `/api/contact` edge rate-limit rule is codified in Terraform; managed WAF
  rulesets need a paid plan); optional `prod` seed.
- ✅ **Auth is LIVE in prod** ([ADR 10](docs/adr/0010-authentication-session-model.md)):
  backend-owned Google OAuth, **open registration**, opaque server-side sessions
  in Postgres (`User`/`Identity`/`Session`). **Tiered access** (ADR 10 amendment):
  `admin` (`ADMIN_EMAILS`) / `friend` (`FRIEND_EMAILS`) / `member`, allowlists
  re-asserted each login with admin precedence. Frontend wiring + `/account` page
  shipped; verified end-to-end in prod (admin login confirmed, `friend` set for
  Nayla). Auth env lives in SSM, and **`deploy-backend.yml` now rebuilds `.env`
  from SSM on every deploy** (config ships with code); the instance `ami` is
  pinned (`ignore_changes`) so an apply never silently replaces the box.
  **Outstanding (external):** add Nayla (`munarohmantab99@gmail.com`) as a Google
  OAuth **test user** if the consent screen is in Testing, else she cannot sign in.
- 🚧 **Phase 2 — WhatsApp Sender, the first Tool** (feature-complete, not yet
  deployed) ([ADR 11](docs/adr/0011-whatsapp-sender-tool.md), friend-gated):
  branch `feat/whatsapp-tool` (draft PR #53, **CI green**). Gated `/whatsapp`
  route + Go backend (data + orchestration) + a separate **private**
  `whatsapp-web.js` sidecar (Node + Chromium, off the micro) with ephemeral
  QR-linked sessions. Batch-anchored flow (create Batch → QR-link → send);
  browser↔backend and backend↔sidecar both stream NDJSON over POST (not SSE).
  Tracer-bullet MVP, caps 250/batch + 3/day. Contract:
  `docs/whatsapp-sidecar-contract.md`.
  - ✅ **Slice 1** Ent data model (`WaTemplate`/`WaRecipientList`/`WaRecipient`/
    `WaBatch`). ✅ **Slice 2** backend: friend gate, owner-scoped CRUD,
    `POST /api/wa/batches` streaming orchestration + caps (`internal/whatsapp/`,
    `internal/api/whatsapp*.go`). ✅ **Slice 3** sidecar: private repo
    **`alifyandra/whatsapp-sidecar`** (Docker image verified — streams a real QR
    from containerized Chromium). ✅ **Slice 4** frontend: `/whatsapp` panels +
    `qrcode.react` QR + streaming reader (`src/lib/wa-stream.ts`).
  - ⏳ **Slice 5 remains (needs Alif):** a live QR scan to confirm delivery, then
    provision the free host (Oracle Always Free) + deploy the sidecar + set prod
    `WA_SIDECAR_URL/SECRET` in SSM. Local wiring seam done (sidecar
    `docker compose up` + backend at `host.docker.internal:8081`).
  See memory `whatsapp-sender-tool`.
