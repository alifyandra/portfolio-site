# ADR 0016: Finance sync orchestration and refresh handshake

Status: Accepted (slices 1-3 implemented; slices 4-5 outstanding)
Date: 2026-07-24
Relates to: ADR 0014 (scheduled job platform), ADR 0015 (finance data source). Closes #93, #96.

## Context

ADR 0015 established a personal-finance ledger fed by a private external source (the
"Finance Source") that pushes sanitized rows to a token-authed ingest endpoint
(`POST /api/finance/ingest`, shipped in #84). #88 added a per-account posted
watermark and a pure `ComputeWindow`, so the backend can compute the exact window a
sync should cover (full backfill when the watermark is nil, else
`(watermark - overlap)..today`).

Two pieces remain to turn a manual sync into a scheduled one:

- **#93** a daily coordinated **refresh handshake**: the source can only refresh with a
  one-off human approval, so a scheduled run must pause for an explicit ack before it
  becomes runnable.
- **#96** the source runner must get its window and report completion through a
  first-class seam, not a manual invocation.

ADR 0014's generic **work API** (`GET /api/work/claim`, `POST /api/work/complete`) was
the presumed seam for #96, but it is shaped for the **digest** pattern: the server
produces an Artifact, an external runner claims it and returns content. Finance is the
inverse. The Finance Source is a **producer**: it acquires rows and delivers them to
the existing ingest endpoint. "Claim an artifact and return content" does not fit, and
the JobRun lifecycle has no human-gate state.

## Decision

### 1. A finance-specific claim/complete seam (not the generic work API)

The Finance Source uses a small `finance.sync`-scoped seam rather than the artifact-based
work API. The ingest endpoint that already exists **is** the data-delivery path; the
new endpoints only carry the *window instruction* and the *run lifecycle*.

Three endpoints, all bearer-authed on the `finance.sync` scope (the same scope-only
token model as the work API; invisible to admin/friend cookies):

- `GET /api/finance/sync/claim` — if a run is claimable, mark it running with a lease
  and return `{claimed:true, run_id, windows:[{account, from, to, backfill}]}` where
  each window is computed by `ComputeWindow` from that account's watermark. If none is
  claimable, `{claimed:false}`.
- `POST /api/finance/sync/ack` — token-gated, called by the notification action button.
  Flips a run from `awaiting_ack` to claimable. The ack token is a shared secret
  distinct from the runner bearer, so it can live in a notification URL. It **fails
  closed**: an unset `FINANCE_SYNC_ACK_TOKEN` rejects every request (401), so a prod
  that forgot to seed the secret is locked, not open. Dev sets a token to use it.
- `POST /api/finance/sync/complete` — `{run_id, status}` marks the claimed run
  succeeded or failed after the source has delivered all windows via the ingest
  endpoint. State-guarded: only a `running` run may be closed; a re-complete to the
  same terminal status is an idempotent no-op; any other state (unclaimed, or the
  other terminal status) is a 409 so a run cannot be silently overwritten.

The data still flows only through `POST /api/finance/ingest`; watermarks still advance
there (#88). No window is persisted: `claim` computes windows on the fly and the source
echoes `window.from/to` back on each ingest payload.

### 2. A new `awaiting_ack` JobRun state and an ack-gated fire path

JobRun gains one state: `awaiting_ack`. The finance run lifecycle is:

```
scheduler fires  ->  awaiting_ack  --(ack)-->  claimable  --(claim)-->  running
                                                                          |
                                              succeeded/failed  <--(complete)
```

The scheduler is today runner-agnostic: it enqueues every fired run to SQS for the
worker. Finance does no server-side work, so routing it through the worker is a poor
fit (the worker CAS-moves queued->running and expects to finish the run). Instead the
scheduler gains a small **ack-gated kind** concept: a job kind may declare that a fired
run is created directly in `awaiting_ack` and a notification is sent, with no SQS
enqueue. `finance.sync` is the first such kind. This keeps the mechanism explicit and
avoids a pointless worker round-trip; other kinds are unaffected.

"Claimable" is represented by a nullable `claimable_at` timestamp on the run (set by
`ack`, cleared/leased by `claim`), rather than overloading the status enum further.

The in-process reaper (which fails scheduler-driven runs stuck `running` past a short
lease) **exempts ack-gated kinds**: a claimed finance run legitimately stays `running`
from claim to complete for as long as the external source takes (a deep backfill can be
minutes), so the lease-based reaper must not kill it. Its lifecycle is closed by the
`complete` call, not the lease.

### 3. Notification via ntfy, gracefully degrading

A minimal ntfy client posts the handshake notification with an action button whose URL
targets `/api/finance/sync/ack` with the ack token. The absolute callback URL is derived
from the app's configured base (the OAuth redirect host), so no separate base-URL env is
needed; if it cannot be derived the notification is still sent, without the button (ack
by hand). Following the SES/Spotify/queue precedent, ntfy no-ops cleanly when
unconfigured: without an ntfy topic the run still enters `awaiting_ack`, so local/dev is
unaffected (an operator acks by setting a token and calling the endpoint).

Config (all env, all optional so unconfigured degrades):
`NTFY_BASE_URL`, `NTFY_TOPIC`, `FINANCE_SYNC_ACK_TOKEN`, and the `finance.sync` schedule
is a normal ScheduledJob row created in /admin (like digest.scrape/llm).

### 4. Job kind registration

`finance.sync` is registered in the three lockstep places (jobs.kinds, the queue Type
const, and the worker switch) with a no-op worker handler guard, so an accidental
enqueue cannot auto-fail a run via errNoHandler. Its runner is `local`/`any`.

## Consequences

- Two runner protocols coexist: artifact-based (digest) and window-based (finance).
  This is honest: the two runners are genuinely different (consumer vs producer).
- The generic work API is left untouched, so the working digest path carries no risk.
- A stray ack is low-risk: it only makes a run claimable; the source still requires the
  separate human refresh approval before any data moves, and no approval means no data.
- If a second producer-style runner ever appears, revisit generalizing the seam
  (Alternative A) rather than adding a third bespoke one.

## Alternatives considered

- **A: generalize the work API** to carry a window instruction as well as artifacts.
  Rejected for now as YAGNI: a bigger change to a young, working API for a single
  consumer. Reconsider when a second producer-style runner exists.
- **C: fit the artifact model** by emitting a "window instruction" artifact the runner
  claims and completes with empty content. Rejected: semantically an artifact that is
  an instruction, not a product; more confusing than a small dedicated seam.

## Build slices

1. **(AFK) DONE** `awaiting_ack` state + `claimable_at` on JobRun; Ent + regen; scheduler
   ack-gated-kind fire path (non-ack-gated kinds unchanged) + reaper exemption;
   `finance.sync` kind registration + no-op worker guard. Fire-path/transition tests.
2. **(AFK) DONE** the three endpoints (`claim` wired to `ComputeWindow`, `ack`
   fail-closed, `complete` state-guarded) with `finance.sync` scope auth; handler + api
   tests; regen openapi.
3. **(AFK) DONE** the ntfy client + config + graceful no-op; wired into the fire path.
4. **(HITL, needs Alif)** create the `finance.sync` schedule row in /admin, set the
   ntfy topic + ack token in SSM, and verify one end-to-end refresh (notification ->
   ack -> claim -> ingest -> complete) against the live source.
5. **(separate, private repo)** teach the source runner to poll `claim`, honor the
   returned windows, and call `complete` (currently a manual one-off). Tracked outside
   this repo.
