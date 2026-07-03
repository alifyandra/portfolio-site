# 11. WhatsApp sender tool and the whatsapp-web.js sidecar

Date: 2026-06-28
Status: Accepted

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

## Settled

1. **MVP**: a tracer bullet first, a single end-to-end slice (link by QR, one
   template, one recipient list, send, live progress with an aggregate result).
   Multiple templates and lists, batch history, and per-recipient logs are
   fast-follows.
2. **Caps**: 250 recipients per batch and 3 batches per day to start, adjustable.
3. **Sidecar hosting**: a free-tier host, leaning Oracle Cloud Always Free (an
   ARM VM with ample RAM for Chromium, free indefinitely), with Fly.io auto-stop
   as a low-effort near-free alternative. This is a deploy-time concern: the
   sidecar runs locally during development, so the host is finalized before it
   goes live and does not gate the build.

## Amendment (2026-06-30): batch-anchored model and the streaming contract

A design pass before building slices 2 to 5 refined the flow above. The original
Flow paragraph described linking first and creating the Batch afterward. The build
inverts that: the Batch anchors the whole run.

**Batch-anchored flow.** The friend picks a stored Template and Recipient List and
sends. The backend creates a Batch (status `pending`), then asks the sidecar to
start a session for that Batch. The QR, the ready signal, and per-recipient
progress all travel back over a single stream. Status moves `pending` to `linking`
(QR shown) to `running` (linked, sending) to `completed` or `failed`. The user
configures before scanning, so the scan is the last action and sending begins the
moment the device links. This matches the shipped `WaBatch` status enum and
shortens the window in which a ready session can idle out.

**Two transports, both streaming.**

- Backend to sidecar: one dial-out `POST /sessions` carrying the template body and
  the resolved recipients. The sidecar streams newline-delimited JSON events back
  as the chunked response body, then closes and tears the session down. The sidecar
  keeps no public ingress; every connection is dialed by the backend.
- Browser to backend: one streaming `POST /api/wa/batches`, consumed in the browser
  with `fetch` and a `ReadableStream` reader rather than `EventSource`. A send has
  side effects, so it must not ride a GET, where a prefetch, proxy retry, or
  EventSource reconnect could re-fire it. The operation is registered in Huma so it
  stays in `openapi.yaml` (ADR 5), though the frontend reads it with a hand-written
  reader, not a generated React Query hook. Templates and lists keep normal
  orval-generated CRUD.

The full request and event schema lives in `docs/whatsapp-sidecar-contract.md`,
which doubles as the build spec for the private sidecar repo.

**Caps.** 250 recipients per Batch, checked at creation. At most 3 Batches per
rolling 24 hours, counting only Batches that actually sent (`sent + skipped +
failed > 0`), so a failed QR scan does not consume a daily slot.

**Recipients.** Lists are populated by bulk paste. The backend normalizes numbers
to canonical international form against a default country code of `+61` (Australia):
a bare leading `0` is read as an Australian local number, so any non-Australian
number must be pasted with its country code. Invalid lines are rejected per line.

**Sessions.** The sidecar holds no persistent WhatsApp credentials (no `LocalAuth`).
Each session is fresh-linked by QR and destroyed after the Batch or a roughly
10 minute hard cap, with a roughly 90 second window to scan the QR. One live session
per user (a second concurrent Batch gets a 409), plus a small global concurrency cap
as a Chromium memory backstop.

**Entity names.** The shipped Ent entities are `Wa`-prefixed (`WaTemplate`,
`WaRecipientList`, `WaRecipient`, `WaBatch`). The per-recipient `BatchItem` named in
the Decision is deferred: the MVP persists aggregate counts only and relays
per-recipient progress live without storing it.

## Amendment (2026-07-03): ban risk and mitigations

Automating a personal number with an unofficial client carries a real risk of that
number being banned by WhatsApp. This risk cannot be eliminated with this approach,
only reduced; the official WhatsApp Cloud API is the only ban-safe path but it
forbids the free-form, personal-number broadcast this tool exists to do (and adds
business verification, template approval, and per-message cost). We accept the risk
and reduce it, revisiting the Cloud API only if usage moves toward cold or
large-scale marketing.

**Audience assumption.** The intended use is an **opt-in / warm audience** — the
friend messages people who already expect it (e.g. members of an event she is
marketing). Recipient blocks and reports are the dominant ban trigger, so a warm
audience is what makes this acceptable. Cold outreach would change the calculus and
is out of scope.

**Behavioural playbook** (the parts no code enforces, documented for the operator):
the first line should identify the sender and context so recipients recognise it;
recipients ideally have the sender saved as a contact; use a dedicated, already-active
("warmed") number, never a primary/irreplaceable one; and spread large lists over
days, especially for a newer number.

**Tunable caps.** The per-batch (250) and per-day (3) caps are now environment
variables (`WA_MAX_BATCH_RECIPIENTS`, `WA_MAX_BATCHES_PER_DAY`) so a number can start
conservative and be ramped up as it proves stable, without a code change. The
sidecar's inter-message delay defaults were widened to roughly 20-90s for the same
reason. Per-message `{name}` substitution already keeps messages from being
byte-identical. An opt-out ("reply STOP") list is a tracked follow-up for recurring
use.
