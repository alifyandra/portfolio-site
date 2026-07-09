# 13. Scheduled heavy-compute jobs on the async platform

Date: 2026-07-09
Status: Accepted

## Context

The async seam from ADR 7 is an SQS queue plus a long-running worker. It has
carried one job type since it shipped: `contact.notify`, which is event
triggered (a Contact Message arrives) and light enough to run inline on the host.

A future personal dashboard needs a different kind of background work: fetch
external data on a schedule, summarize it with an LLM, and store the result for
later reading (later, the same shape over finances, email, and calendar). That
work is **scheduled**, not event triggered, and the heavier future variants
(headless scraping, image and PDF rendering) will not fit the 1 GB `t4g.micro`
(ADR 6).

ADR 11 already runs a Fargate task, but in a different mode: it is launched
synchronously from a request and connected to over a private IP so the browser
can watch a live stream (run and connect). Scheduled batch work has no browser
watching and no live connection to hold open.

## Decision

Extend the existing Job seam rather than build a second one alongside it.

- A **Job** is now triggered either by an **event** (a producer, e.g.
  `contact.notify`) or by a **schedule**. The schedule is an **EventBridge
  Scheduler** rule that enqueues a Job message onto SQS on a cron.
- The **worker becomes a dispatcher**. It consumes the queue and routes by
  weight: light Jobs run inline on the host (as today), heavy Jobs are launched
  on a dedicated **Fargate task that runs to completion and exits**. This is a
  second Fargate mode next to ADR 11's run and connect: the worker never dials
  the task, it only cares whether the task exited cleanly.
- The first scheduled Job is `digest.build`, producing a **Digest** (see
  `CONTEXT.md`). The task fetches a fixed set of public, unauthenticated
  **Source**s, summarizes them with the Anthropic API, and writes a Digest row.

Completion and retry:

- The worker **blocks and polls** `DescribeTasks` until the task reaches
  `STOPPED`, reads the container exit code, and acks SQS only on exit 0.
- **SQS redelivery is the retry**: a small `maxReceiveCount` then a **DLQ** for
  poison messages. The schedule is a backstop (a run that dead-letters is retried
  fresh by the next cron).
- The task has a hard runtime cap and the queue **visibility timeout is set above
  that cap**, so a slow run is never redelivered and duplicated mid-flight.
- `digest.build` is **idempotent by date** (upsert the Digest for its date), so a
  redelivery cannot create a duplicate.

Secrets and cost:

- The Anthropic API key lives in **SSM Parameter Store** and is injected via the
  task definition `secrets`, never baked into the image (the ADR 11 pattern).
- Cost is bounded by a small summarization model, a max-tokens limit, one LLM
  call per run, and a per-day run cap. The task exits after the run, so nothing
  is billed at idle (scale to zero).

## Consequences

- One vocabulary. Job covers event and scheduled, inline and Fargate. Batch stays
  separate because it is genuinely different (user triggered, watched live).
- Two Fargate execution modes now exist in the codebase: run and connect
  (WhatsApp) and run to completion (this). They share the task plumbing (cluster,
  networking, IAM, RunTask) but not the connect or stream logic.
- Idle cost stays near zero. The scheduled task runs briefly once a day on arm64
  and exits. The only always-on piece is the existing idle worker poller.

## Deferred, with the trigger to revisit each

These were considered during design and left out on purpose, so they are not
relitigated later:

- **Fan-out and fan-in.** Prove the spine single-task first. Add per-item
  parallelism (one message per Source, a pool, a join) when a workload needs it.
  The real cost is the join and the partial-failure policy, not the parallelism.
  Trigger: the first workload that fans out (many sources, many emails).
- **Priority queues.** One queue now. SQS has no in-queue priority, so the future
  form is two queues (`high`, `bulk`) with the worker polling `high` first.
  Trigger: a user-triggered interactive Job that could otherwise wait behind a
  scheduled batch.
- **Container reuse.** One task per Job now. When fan-out volume makes per-task
  cold starts wasteful, a task that drains several messages before exiting
  amortizes them. (Distinct from WhatsApp session reuse, issue #55, which is
  blocked by a phone-bound session, not cold-start cost.) Trigger: fan-out volume.
- **Event-driven completion.** Block-and-poll now. Move off it when a job type
  runs long or unpredictably, or when concurrency makes a blocked worker slot
  costly. Because Postgres runs on the box (not RDS), the target should be an
  EventBridge rule to a `completions` SQS queue consumed by the same worker, not
  a Lambda (which would need VPC and DB plumbing) or a public API destination.
  The same instinct on the browser side is SSE, not WebSockets: server to client
  push is one directional and fits both the existing NDJSON streaming and the
  Huma/OpenAPI/orval convention (ADR 5).
- **Offline-access OAuth.** The current Google OAuth is identity only (`openid`,
  `email`, `profile`; the token is used once at login and discarded). Personal
  data Sources (Calendar, Gmail) need a separate offline-access token subsystem
  (stored, encrypted, refreshed) plus Google verification for sensitive or
  restricted scopes. The first workload uses public Sources only to keep that out
  of the tracer bullet. Trigger: the first personal-data Job.

## Alternatives rejected

- **A separate "scheduled task" vocabulary and pipeline.** The queue, envelope,
  dispatch, and retry are all shared with existing Jobs. Forking the words would
  fragment one mechanism and force the two to be kept in sync forever.
- **EventBridge Scheduler calling RunTask directly, skipping SQS.** Simpler for a
  single daily job, but it drops the queue that buffering, retry, DLQ, and later
  fan-out all depend on, so the platform would have to be rebuilt for the second
  workload.
- **Running the digest inline on the micro.** The digest alone is light enough
  (an HTTP fetch and an API call). Launching it on Fargate instead validates the
  run-to-completion launcher, the platform's new and unproven part, with the
  first slice, and the heavier future Jobs will not fit the micro anyway.

## Amendment (2026-07-09): Shape B, the worker persists and the task writes to S3

The original build had the Fargate task open Postgres directly (over the box's
private IP) and write the Digest row itself. Wiring that up surfaced a landmine:
the app host bakes `docker-compose.prod.yml` into its `user_data` with
`user_data_replace_on_change = true`, and Postgres lives on the instance root
volume. Publishing Postgres so an off-box task could reach it is a compose change,
which changes `user_data`, which forces an instance replacement, which wipes the
prod database (users, sessions, admin-console data). A gated `terraform apply`
plan confirmed the replacement before anything was applied.

So the data write moves to the worker. The task no longer touches Postgres:

- The worker reads the active Sources on-box and passes them to the task as
  RunTask container overrides (`DIGEST_SOURCES`), alongside `DIGEST_DATE` and a
  per-run `DIGEST_RESULT_KEY`.
- The task fetches those Sources, summarizes them, and writes its Result as JSON
  to S3 under `digest_result_prefix` in the assets bucket. It needs only
  `s3:PutObject` on that prefix, no database reach.
- The worker blocks on the task exit (unchanged launcher), reads the Result back
  from S3, and upserts the Digest row over the docker network (it already owns the
  DB). It then deletes the result object; an S3 lifecycle rule expires any orphan.

Retry and idempotency are unchanged: a content failure writes a failed row
(ON CONFLICT DO NOTHING, never demoting a completed day) and leaves the message
for redelivery, with the daily schedule as the backstop; a clean run acks. The
gain is that the apply is now purely additive (no `docker-compose.prod.yml` change,
no `aws_instance.app` replacement, no `5432` ingress on the web SG, no on-box
Postgres exposure). The cost is one S3 round-trip per run and the worker holding
the DB write, which is a good trade for a daily batch on a box that keeps state on
its root volume.
