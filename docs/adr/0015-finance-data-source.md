# 15. Personal finance data source: ingest, read models, and a human-approved refresh

Date: 2026-07-10
Status: Accepted (amends ADR 13, ADR 14)

## Context

Finance is the one personal-dashboard data source (issue #74, under #69) with no
integration yet. The goal is periodic transactions and balances feeding the
dashboard and a read-only MCP server.

The data comes from an external source that cannot be polled unattended: each
refresh needs a human approval. Acquisition runs in a separate private component and
repository; this ADR covers only the cloud side and the trust boundary. Third-party
aggregators were ruled out for a single personal account (they need a registered
legal entity and a multi-month contract).

## Decision

Split trust and reuse the job platform (ADR 14). A private external component
acquires the data and pushes only sanitized, derived rows to the cloud. It stores no
credentials and sends no session material across the boundary. Everything else is
cloud-side: scheduling, the refresh handshake, ingest, the watermark, overnight LLM
enrichment, and the read side. The external component is an external [Runner] that
pulls work over HTTPS (always outbound, NAT-friendly), authenticated by an [API
Token]. The blast radius of a full cloud compromise is transaction history, which is
already the account holder's own data, not a live handle to the source.

### Refresh handshake

Because every refresh needs a human approval, a run is gated on an explicit
acknowledgement rather than fired blindly on a timer. At a set time the scheduler
creates a `finance.sync` [JobRun] in an `awaiting_ack` state and sends a push
notification (ntfy) with an action button. Tapping it calls a token-gated
acknowledgement endpoint, which flips the run to claimable. The runner claims it and
completes the refresh. A stray acknowledgement cannot start a real refresh without
the human approval that follows.

### Backend-owned refresh window

The cloud keeps a per-[Financial Account] watermark, the date through which data is
known complete, and computes the `from` and `to` window for each run, passing it in
the claimed job. The runner is a dumb executor. The first run backfills; later runs
cover `(watermark - overlap)` to today. The overlap recovers any days a missed run
skipped, because the watermark advances only on a fully successful ingest, and it
re-reads recently settling transactions.

### Data model

- A **[Financial Account]** carries a `type` (transaction, savings, credit,
  investment) and an asset-or-liability class, so net worth aggregates correctly and
  amounts normalize per type. Investment accounts are portfolios rather than cash
  ledgers; v1 tracks their balance only.
- **[Posted Transaction]s** are an immutable ledger, upserted idempotently by a
  stable hash of (account, date, amount, description, running balance).
- **[Pending Transaction]s** are volatile, so they are never hashed into the ledger;
  the whole pending set for an account is replaced on each refresh.
- **[Balance Snapshot]s** are read from the source on each refresh and stored with a
  timestamp. Balance is never derived by summing transactions, which would drift and
  cannot establish a true starting point inside the backfill limit.

### Read side

The dashboard and MCP server are read-only over the database, and every response
carries a "data as of" stamp, so a stale source degrades to stale data rather than an
error. The MCP surface is `query_transactions`, `spending_by_category`,
`search_merchant`, `monthly_summary`, and `account_balances`.

### LLM enrichment

Categorization runs overnight and reuses the [Digest]'s Batch API path (submit at end
of day, collect by morning), which is cheaper and where latency does not matter.

## Consequences

The achievable product is one or two refreshes a day, each gated on a human approval.
That is not a zero-touch ideal, but it is a clear step up from manual import, since
rows arrive parsed, categorized, and queryable, and the split keeps the source-side
session inside the private component.

The generic work API is job-platform P5 and is not built yet. The first vertical
slice can stub it with a minimal write-only, token-authenticated ingest endpoint and
a manually triggered run, then graduate to the work API once P5 lands. Local
development runs against the ingest endpoint with fixtures.

Acquisition details live in the private component's repository. This amends ADR 13,
whose Batch API path is reused for the overnight LLM step, and ADR 14, for which the
external component is the second external Runner the platform was designed to serve.
