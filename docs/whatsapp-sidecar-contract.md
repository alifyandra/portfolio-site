# WhatsApp Sender: backend and sidecar contract

The internal API between the Go backend and the private whatsapp-web.js sidecar,
plus the streaming endpoint the backend exposes to the browser. See ADR 11 and its
2026-06-30 amendment for the rationale.

This file lives in the public repo and contains no secrets, so it doubles as the
build spec for the private sidecar repo. Recipient data and the shared secret never
appear here.

## Shape

```
Browser  ──POST /api/wa/batches (stream)──▶  Go backend  ──POST /sessions (stream)──▶  Sidecar
   ▲                                            │                                        │
   └──────── streamed events (relayed) ─────────┴────── streamed NDJSON events ──────────┘
```

Both hops are a single streaming POST. The backend is a thin relay: it creates and
updates the `WaBatch` row, transforms nothing of substance, and forwards events from
the sidecar to the browser. Every connection is dialed by the caller, so the sidecar
needs no public ingress.

## Auth

- Browser to backend: the existing session cookie. The handler enforces the
  friend-or-admin gate (`requireFriend`); anonymous is 401, `member` is 403.
- Backend to sidecar: `Authorization: Bearer <shared secret>`, held in SSM and
  injected as an env var. The sidecar rejects any other caller. No public ingress.

## Internal endpoint: backend to sidecar

### Request

```
POST /sessions
Authorization: Bearer <shared secret>
Content-Type: application/json

{
  "batch_id": 123,
  "template_body": "Hi {name}, ...",
  "recipients": [
    { "phone": "61412345678", "name": "Budi" },
    { "phone": "61498765432", "name": "" }
  ]
}
```

- `phone` is already canonical (digits only, no `+`, no leading zero). The backend
  normalizes; the sidecar does not re-parse.
- `name` may be empty. The sidecar substitutes `{name}` in `template_body` per
  recipient; an empty name substitutes an empty string.
- The backend has already enforced both caps before calling, so `recipients` is at
  most 250 long and this is within the daily limit.

### Response

A `200` whose body is a stream of newline-delimited JSON objects (one event per
line), held open until the run ends, then closed. The sidecar tears the session
down when it closes the stream.

## Event schema

Same events on both hops; the backend relays them. One JSON object per line.

| `type`     | Fields | Meaning |
|------------|--------|---------|
| `qr`       | `value` (string) | The WhatsApp linking payload. May repeat as WhatsApp refreshes it (~20s); render the latest. |
| `ready`    | none | Device linked; sending begins. |
| `progress` | `phone`, `name`, `status` (`sent` \| `skipped` \| `failed`), `reason`?, `error`? | One per recipient. `skipped` carries a `reason` (e.g. `not_registered`); `failed` carries an `error`. |
| `waiting`  | `ms` (int), `next_phone`, `next_name` | The randomized pause before the next recipient. Lets the UI count down `ms` and name who is next. Relayed to the browser only; no batch effect. |
| `done`     | `sent`, `skipped`, `failed` (ints) | Terminal success. Final aggregate. Stream closes. |
| `error`    | `message` (string) | Terminal failure (e.g. link timeout). Stream closes. |

Examples:

```
{"type":"qr","value":"2@abc..."}
{"type":"ready"}
{"type":"progress","phone":"61412345678","name":"Budi","status":"sent"}
{"type":"waiting","ms":23000,"next_phone":"61498765432","next_name":"Siti"}
{"type":"progress","phone":"61498765432","status":"skipped","reason":"not_registered"}
{"type":"progress","phone":"61400000000","status":"failed","error":"send timeout"}
{"type":"done","sent":1,"skipped":1,"failed":1}
```

## Backend handling of events

| Event | `WaBatch` effect |
|-------|------------------|
| created | row written, status `pending` |
| `qr` | status `linking` (first QR only) |
| `ready` | status `running` |
| `progress` | increment the matching aggregate count (`sent` / `skipped` / `failed`) |
| `waiting` | none (relayed to the browser only) |
| `done` | status `completed`, final counts persisted |
| `error` | status `failed`, `error` field set |

Per-recipient `progress` is relayed to the browser for the live view but is not
persisted per recipient. Only the aggregate counts live on `WaBatch` (no
`BatchItem` in the MVP). On reload, the user sees totals, not the per-number log.

## Failure and teardown

- **Scan window**: the sidecar gives each QR roughly 90 seconds to be scanned. If
  it is not, it emits `error` and closes.
- **Session hard cap**: a session lives at most roughly 10 minutes, then is torn
  down regardless.
- **Stream drop**: best effort, no resume. If the backend's relay handler exits
  without a `done`, it marks the Batch `failed` so nothing stays stuck in `running`.
  The sidecar aborts and tears down when a write to the stream errors. Some messages
  may already have been sent; this is an accepted MVP limitation.

## Concurrency

- One live session per user. A second concurrent Batch for the same user gets a
  `409` from the sidecar, which the backend surfaces as "a send is already running".
- A small global concurrency cap on the sidecar bounds Chromium memory on the free
  host. Beyond it, new sessions are rejected.

## Public endpoint: backend to browser

```
POST /api/wa/batches            (session cookie; friend or admin)
Content-Type: application/json

{ "template_id": 7, "list_id": 4 }
```

The backend resolves the Template and Recipient List, enforces the gate and both
caps, creates the `WaBatch`, then opens the internal `POST /sessions` and relays its
events as the streamed response body. The frontend reads this with `fetch` and a
`ReadableStream` reader (not `EventSource`), rendering the QR and the live progress.

The operation is registered in Huma so it appears in `openapi.yaml` (ADR 5). It is
consumed by a hand-written reader rather than a generated React Query hook, because
orval does not model streaming. Template and Recipient List CRUD use normal
orval-generated hooks.

## Recipient normalization (backend, before storage and before the sidecar)

- Strip whitespace and punctuation; drop a leading `+`.
- A leading `0` is replaced with the default country code (`61`, Australia). Any
  non-Australian number must be pasted with its own country code, or it will be
  misread as Australian.
- Reject anything that does not land at 8 to 15 digits. Invalid lines are reported
  per line so the user can fix and resubmit.
- Stored and sent form is digits only, no `+`, no leading zero.
