# 11. WhatsApp sender tool and the whatsapp-web.js sidecar

Date: 2026-06-28
Status: Proposed

## Context

Auth is live (ADR 10 plus the friend tier), which unlocks the first **Tool** in
the platform sense (CONTEXT.md: an app a user operates, distinct from a read-only
About Panel). The first Tool is a WhatsApp bulk-message sender for a friend-tier
user: she links her own WhatsApp, stores message templates and recipient lists,
and triggers personalized batch sends.

A working CLI prototype already exists (a whatsapp-web.js bulk sender). The
capability she needs, free-form personalized broadcast from a personal number, is
exactly what the official WhatsApp Cloud and Business APIs restrict: those require
pre-approved message templates, recipient opt-in, and a per-message cost. So the
tool must use the unofficial whatsapp-web.js, which automates a linked WhatsApp
Web device.

That automation violates WhatsApp's Terms of Service and can get a number banned.
The risk falls on the linking user's own number (not Alif's), access is gated to
the friend tier, and the engine is isolated from the rest of the platform.

## Decision

The tool is three parts.

1. **Frontend**: a `/whatsapp` route in the existing Next.js app, gated to friend
   and admin via the Phase 1 auth context. The route guard is UX only; the
   backend enforces. It shows the link QR, a template editor, a recipient-list
   manager, and send progress.
2. **Go backend** (the existing API): owns auth and the friend/admin
   authorization gate, owns the durable data as Ent entities, orchestrates the
   sidecar, and proxies the QR and progress streams to the browser. It never runs
   Chromium.
3. **A separate, private whatsapp-web.js sidecar** (Node plus headless Chromium),
   in its own private repository, deployed off the t4g.micro. It holds at most one
   linked-device session per user, created on link and destroyed after the batch
   or an idle timer. It exposes an internal authenticated API that only the Go
   backend calls; it is never publicly reachable.

Data model (Ent, in the main backend's Postgres):

- **WaTemplate**: owner User, name, body (with a `{name}` placeholder), timestamps.
- **RecipientList**: owner User, name. **Recipient**: list, phone in international
  format, optional name.
- **Batch**: owner, template, list, status, counts (sent / failed / skipped),
  timestamps. **BatchItem**: batch, recipient, status, error, for per-recipient
  results.

Recipient data is PII and lives only in private Postgres, never in git.

Flow: the friend opens `/whatsapp`; the backend asks the sidecar to start a link;
the sidecar emits a QR; the backend relays it to the browser over SSE; the friend
scans it; the session becomes ready; the friend picks a stored template and list
and sends; the backend creates a Batch and hands the template plus resolved
recipients to the sidecar; the sidecar sends with randomized delays, skips numbers
not on WhatsApp, and reports per-recipient progress back; the backend updates the
Batch and BatchItems and relays progress; on completion or idle timeout the
sidecar destroys the session. The next visit re-links via a fresh QR.

Security and abuse control:

- Friend/admin gate, enforced in the backend (frontend guard is cosmetic).
- The internal sidecar API is authenticated by a shared secret held in SSM; the
  sidecar has no public ingress.
- Per-user caps (recipients per batch, batches per day) plus the existing rate
  limiter, to bound both misuse and ban risk.
- Ephemeral sessions: no long-lived linked WhatsApp account is held server-side.
- The whatsapp-web.js engine and Chromium live in a private repo, which keeps
  ToS-violating code out of the public, name-bearing portfolio repo, and off the
  micro, where Chromium would exhaust the 1 GB of RAM.

Public/private split: the UI and the Go data/orchestration stay in the public
repo (no secrets; they call an internal API). The sidecar is private.

## Consequences

- A new private repo, a new deploy target for the sidecar, and an internal API
  contract between the backend and the sidecar.
- whatsapp-web.js tracks WhatsApp Web; breakage and bans are operational
  realities. The tool is best-effort and the linking user accepts the risk on
  their own number.
- The Go backend gains its first write-capable, authorization-gated feature
  surface (templates, lists, batches), which exercises the auth system and the
  codegen pipeline (new handlers to openapi.yaml to frontend hooks).
- Establishes the gated-tool pattern (tier-gated route plus entities plus an
  optional sidecar) for future Tools.

## Alternatives rejected

- **Official WhatsApp Cloud/Business API**: no way to link a personal number by
  QR, restricts free-form broadcast to approved templates and opt-in, and costs
  per message. It does not deliver the capability the user needs.
- **Lift-and-shift the CLI into the public repo or onto the micro**: drags a
  ToS-violating engine and a session-credential risk into a public, name-bearing
  repo, and Chromium would exhaust the 1 GB box.
- **Persistent server-side linked session**: holding a live WhatsApp account open
  indefinitely is fragile and widens the blast radius; per-batch ephemeral
  sessions are safer.
- **Run the engine on the existing SQS worker seam**: the worker is Go on the
  micro, but the send must run in the Node/Chromium sidecar, so the queue seam
  does not fit the engine (the backend may still use a Job to kick off a batch).

## Open questions (to settle before Accepted)

1. **Sidecar hosting**: where the Node plus Chromium service runs. Occasional use
   favours a cheap, scale-to-zero host; a small dedicated VPS or a second EC2 are
   alternatives with an always-on cost.
2. **MVP cut**: a tracer bullet first (link, one template, one list, send, live
   progress with an aggregate result), with multiple templates and lists, batch
   history, and per-recipient logs as fast-follows.
3. **Caps**: concrete per-batch and per-day limits.
