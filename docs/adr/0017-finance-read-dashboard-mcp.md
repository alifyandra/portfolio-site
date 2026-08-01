# ADR 0017: Finance read service, dashboard endpoints, and remote MCP server

Status: Accepted
Date: 2026-07-26
Relates to: ADR 0014 (scheduled job platform / work API), ADR 0015 (finance data source),
ADR 0016 (finance sync orchestration).

## Context

The finance ledger has a complete write side: a token-authed ingest endpoint
(`POST /api/finance/ingest`, ADR 0015) fed by the private broker, plus the scheduled
sync seam (ADR 0016). Data lands in four Ent entities: `Account`, `Transaction` (the
immutable posted ledger), `PendingTransaction` (a volatile side-set), and
`BalanceSnapshot` (append-only balance readings). Nothing reads it back yet.

Two consumers want the same read model:

- an admin dashboard in `/admin` (net worth, per-account balances, a transaction
  browser, a balance-history line, monthly cash flow), and
- an LLM. The point of a personal-finance tool is to ask questions in natural
  language ("how much did I spend at X", "what is my net worth", "show last month's
  cash flow"). The Model Context Protocol is the standard way to expose that.

Both need identical queries over the same tables. Writing the queries twice would let
the dashboard and the LLM drift apart.

## Decision

### 1. A pure read service both consumers call in-process

`internal/finance/read.go` holds query-only functions over the Ent client:
`NetWorthSummary`, `Accounts`, `BalanceHistory`, `ListTransactions` (filter + paging +
total), `Pending`, `SearchMerchant`, and `MonthlySummary`, with `BalanceSeries` (the
bucketing and both derivation bases) alongside in `internal/finance/balance_series.go`.
Aggregation runs in Go rather than SQL: the finance tests run on in-memory sqlite, which has
no `date_trunc`, and the bucket rules are the part that most needs unit tests. They return plain view
structs, round nothing (money stays `float64`; formatting is the caller's job), and
never mutate. Both the HTTP handlers and the MCP tools call these directly, so the
dashboard and the LLM run byte-identical queries.

Net worth is computed from each account's latest `BalanceSnapshot` (max `as_of`):
assets sum the latest balance of asset-class accounts; liabilities sum the ABSOLUTE
latest balance of liability-class accounts (a credit card carried as a negative
balance is reported as a positive amount owed); net worth is assets minus liabilities.
The dataset is single-tenant (Alif's) and small (a handful of accounts), so a
per-account "latest snapshot" query is used rather than a window-function join. Ent's
eager-load `Limit` applies to the whole query, not per parent, so it cannot select
"one latest snapshot per account" in a single load; the per-account query is both
correct and clear at this scale.

The names `NetWorthSummary` and `ListTransactions` avoid a collision with the ingest
side, which already owns the `Summary` tally type and the `Transactions` payload type
in the same package.

### 2. Admin-gated dashboard read endpoints

Six GET operations in `internal/api/finance_read.go`, all Huma, all cookie-authed and
calling `requireAdmin` as their first line. Finance is single-tenant and never
friend/member visible, so there is no tier below admin here.

- `get-finance-summary` `GET /api/finance/summary`
- `list-finance-accounts` `GET /api/finance/accounts`
- `get-finance-balance-history` `GET /api/finance/accounts/{id}/balances` (`days`, 0 = all;
  `step` = ""|day|week|month; `basis` = snapshot|ledger). Omitting `step` and `basis` returns
  the raw per-reading list unchanged. A step buckets to Australia/Melbourne local boundaries
  and reports each bucket's LAST value (close-of-period, since a balance is a stock and not a
  flow), carrying a quiet bucket forward flagged `carried`. `basis=ledger` derives the same
  series from posted transactions and adds per-bucket open/in/out/net plus the reconciliation
  fields. An unknown `step` or `basis` is a 400, never a silent fall back. See the
  [Balance Series] / [Balance Basis] / [Balance Reconciliation] terms in CONTEXT.md.
- `list-finance-transactions` `GET /api/finance/transactions` (`account_id`, `from`, `to`,
  `limit` default 50 capped 500, `offset`)
- `list-finance-pending` `GET /api/finance/pending` (`account_id`)
- `list-finance-wishlist` `GET /api/finance/wishlist` (`status`, default `wanted`; see
  portfolio-site#123). Returns the items plus a cost roll-up, and a `truncated` flag when
  the read limit dropped rows.

Dates on the wire are strings: date-only (`YYYY-MM-DD`) for `posted_date`, pending
`date`, and `posted_watermark`; RFC3339 for snapshot `as_of` and `balance_as_of`. A
malformed `from`/`to` query is a 422 rather than a silently-ignored filter. These five
appear in `openapi.yaml`, so the frontend gets generated React Query hooks.

### 3. A remote MCP server at `/mcp`, hand-rolled, JSON-response mode

`internal/mcp` exposes the read service as MCP tools. It implements the MCP Streamable
HTTP transport in JSON-response mode: a client POSTs one JSON-RPC 2.0 request and gets
one `application/json` response. It handles `initialize`, `notifications/initialized`,
`tools/list`, `tools/call`, and `ping`, negotiates a protocol version (echoing the
client's requested version when supported, else offering the latest, `2025-06-18`), and
returns proper JSON-RPC errors for unknown methods.

**Hand-rolled, not the official Go SDK.** The official `modelcontextprotocol/go-sdk`
was the preferred option, but its Streamable HTTP handler is built around an
SSE-capable session model (event streams, session ids, resumability). For a stateless
read-only surface behind a token, a small spec-correct JSON-RPC handler is fewer moving
parts, keeps the auth check trivially in front, and adds zero dependencies. The whole
transport is about 200 lines. If the tool surface later needs streaming, subscriptions,
or sampling, revisit adopting the SDK.

**JSON mode, no SSE, because of Cloudflare.** `api.aliflabs.dev` sits behind the
Cloudflare proxy (see `docs/security.md`). A plain request/response endpoint proxies
cleanly; a long-lived SSE stream is exactly the shape a proxy buffers or times out. JSON
mode keeps `/mcp` an ordinary POST the proxied origin serves unchanged. The server issues
no `Mcp-Session-Id` (sessions are optional in the spec), so it stays stateless.

**On the raw Chi router, outside Huma.** MCP does not speak the OpenAPI contract and it
authenticates with a bearer token, not the session cookie, so it does not belong in the
Huma API. It is mounted with `r.Handle("/mcp", ...)` (and `/mcp/`) on the Chi mux next to
the other non-Huma routes (`/healthz`, the OAuth redirects). It is therefore
intentionally ABSENT from `openapi.yaml`. Constructing the handler dereferences nothing,
so `cmd/spec` (which builds the router with nil Ent/Auth purely to emit the spec) stays
safe; Ent and Auth are touched only per request.

**Auth: a `finance.read` bearer scope.** The handler reads `Authorization: Bearer`,
resolves it with the existing `AuthenticateBearer` (ADR 0014), and requires
`Allows("finance.read")`. Missing, invalid, or insufficiently-scoped tokens all get a
single 401 with a `WWW-Authenticate: Bearer` challenge; the client cannot tell which. The
session cookie is never accepted here (MCP clients are token-based). `finance.read` is a
distinct scope from `finance.sync` (the ingest/write side), so a read token can never
write and an ingest token can never read. A bearer identity is invisible to
`requireAdmin`, so an MCP token can never reach the admin console.

**Tools** (each calls the read service in-process, returns one JSON text content block):
`get_net_worth`, `list_accounts`, `list_transactions` (`account_id?`, `from?`, `to?`,
`limit?`), `search_merchant` (`query` required, `limit?`), `monthly_summary`
(`account_id?`, `months?`), `balance_history` (`account_id?` omitted = every account as its
own series, `step?` default month, `from?`/`to?` default the last 12 months, `basis?` default
snapshot; a total point budget bounds the answer and going over returns `truncated: true`
with the coarsen-step advice rather than trimming quietly), `list_pending`, `list_wishlist`
(`status?`, `limit?`: the one-off wants plus a known-cost total and a count of items whose
price is unknown, so a purchase can be weighed against what is already queued, see
portfolio-site#123). The tool defaults are deliberately coarser than the dashboard's:
pixels are cheap and tokens are not, so bucketing is what makes balance history affordable
to expose to an LLM at all. Note also that the HTTP endpoint above is admin-cookie-gated,
so a `finance.read` bearer cannot read balance history any other way.
Structural failures (unknown tool, bad
params) are JSON-RPC errors; a domain/validation failure (bad date, missing query) is a
tool-error result (`isError: true`) so the model sees the message, per MCP convention.

Added later: `list_recurring_bills` (`status?`, `within_days?`, `account_id?`), the
committed-money read over declared repeating commitments, with `committed_total` and
`monthly_equivalent` so affordability can be answered against what is already spoken for
rather than the raw balance (portfolio-site#125).

### Amendment (portfolio-site#122): the account read model carries owner intent

The account read model was purely descriptive: type, class, currency, masked number and a
balance. That describes an account's shape, not its purpose, so two accounts identical in
every structural field looked equally spendable to both the dashboard and the model.

`AccountView` now also carries a free-text `description` and a `drawdown_policy` enum
(`unset` / `flexible` / `no_drawdown` / `emergency_only`), threaded to both consumers as
usual. Mostly prose, with exactly one structured field: the prose carries nuance no enum
would capture, while "can this balance go down" changes an arithmetic answer rather than
colouring it, and should not depend on a model reading prose carefully. The
`list_accounts` tool description says as much, including that `unset` means not yet
declared rather than flexible.

Both fields are owner-authored, and this is the first thing on an account the ingest does
not own. The write side stays off the MCP (still read-only) and off the ingest payload
(the broker has no way to send a description). The single new endpoint is
`PATCH /api/admin/accounts/{id}`, admin-gated, accepting those two keys and nothing else.
The ingest's upsert conflict clause lists its columns explicitly so a re-sync cannot
overwrite them, and that guarantee is pinned by a regression test on both account
creation paths.

Deliberately out of scope: nothing consumes the policy arithmetically yet. `get_net_worth`
and the summaries are unchanged, and a spendable-versus-locked split waits until the
fields are populated and their real-world reading is known.

## Consequences

- One query path serves the dashboard and the LLM, so they cannot drift.
- `/mcp` is a plain POST endpoint, safe behind the Cloudflare proxy, with no streaming
  machinery to buffer or time out.
- Two bearer scopes now exist on the same token model: `finance.sync` (write/ingest,
  ADR 0016) and `finance.read` (this ADR). Neither can do the other's job.
- The read endpoints are admin-only. If a read model is ever wanted for a non-admin
  (it is not today), that is a deliberate future change, not an accident.

## Deferred

- **OAuth for the MCP server (web/mobile clients).** MCP's auth spec allows an OAuth 2.1
  flow so a hosted client can obtain a token interactively. This server is bearer-only:
  a token is minted once (ADR 0014 mint path) and pasted into the client config, which
  suits a single-operator desktop client. A full OAuth authorization-server flow is a
  stretch to revisit if a browser or mobile MCP client ever needs to self-provision.
- **Spending by category.** There is deliberately no category tool. The bank exposes no
  bulk category in any web surface (see the finance memory), so categories require a
  cloud enrichment pass (LLM/rules over transaction descriptions) that is not built yet.
  A category tool today would return nothing useful; it waits on that enrichment, which
  reshapes issue #89.

## Alternatives considered

- **Adopt the official Go MCP SDK.** Rejected for now: its Streamable HTTP handler is
  SSE/session-oriented, heavier than a stateless read-only surface needs, and would add
  a dependency for no gain here. Reconsider if streaming/subscriptions are wanted.
- **Run MCP as Huma operations.** Rejected: JSON-RPC over a single POST is not an
  OpenAPI-shaped contract, and MCP authenticates on a bearer scope, not the cookie. It
  would pollute the generated client and the spec with a non-REST endpoint.
- **SSE streaming transport.** Rejected: the Cloudflare proxy in front of the origin
  makes plain request/response the safer shape, and read-only tools have nothing to
  stream.
