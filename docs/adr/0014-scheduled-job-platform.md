# 14. Scheduled-job platform and external runners

Date: 2026-07-10
Status: Accepted (amends ADR 13)

## Context

ADR 13 made the digest a single scheduled Job: an EventBridge Scheduler rule
enqueues an SQS message on a cron, a Fargate task fetches the Sources and calls
Anthropic in one shot, and the worker persists a date-keyed Digest. Two limits
surface as soon as a second scheduled workload is in view.

First, there is no admin control. The schedule, the on/off switch, and the run
history live in Terraform and AWS, not in the app. Alif wants to toggle a job,
re-time it, force a run, and read run history and statuses from `/admin`, the same
place the rest of the site's dynamic domain data is edited (ADR 12).

Second, the scrape and the LLM call are fused, and the scraped content is never
stored. That makes it impossible to run the LLM step anywhere other than the one
Fargate task, or to re-run it without re-fetching. The intended flexibility is to
run the LLM step on the box (an Anthropic API key, cheap and unattended) or to hand
it to an external runner: Alif's laptop running Claude Code today, a home
finance runner later (issue #74).

The motivating question was whether the LLM step could run on Alif's Claude
*subscription* instead of API pay-as-you-go. The ground truth: subscription (OAuth)
auth is for interactive Claude Code and claude.ai only. Any programmatic or backend
call needs an API key. That is a terms-of-service and credential boundary, not a
personal-versus-public distinction, so a headless job on the box cannot borrow the
subscription. It can, though, run on the laptop where Claude Code is already signed
in, which is what the external-runner path is for.

## Decision

Build a generic scheduled-job platform on top of the existing Job seam (ADR 7,
ADR 13), and make the digest its first two jobs. This **amends ADR 13**: in-process
scheduling replaces EventBridge Scheduler for these jobs.

### In-process scheduler

A ticker goroutine in the single-instance worker (ADR 6) replaces the EventBridge
Scheduler rules. It wakes about once a minute, reads the `ScheduledJob` rows, and
for each enabled job whose `next_run_at` has passed it enqueues an SQS message and
advances `next_run_at` (robfig `cron/v3`, per-job timezone). Why in-process rather
than the AWS scheduler:

- **Admin control.** The schedule now lives in a database row the Admin Console can
  edit, so toggling, re-timing, and forcing a run are ordinary writes, and run
  history is queryable.
- **No leader election.** There is exactly one worker (ADR 6), so a single ticker is
  safe without the coordination a multi-instance scheduler would need.
- **Idempotent against at-least-once delivery.** SQS delivers at least once, so the
  ticker cannot be the only guard. A `JobRun` is inserted under a unique
  `(job, scheduled_for)` constraint, so a double-tick or a redelivery collapses to
  one run; the worker then transitions that run `queued` to `running` only if it is
  still `queued`, so a duplicated message no-ops.
- **Fire once, not N times, after downtime.** `next_run_at` is persisted, so a
  worker that was down through several ticks fires once on restart and moves the
  pointer forward, rather than backfilling one run per missed tick.

The ticker no-ops when the queue is not configured, so local `make up` (no worker)
and misconfigured environments stay inert.

### The generic registry

Four new Ent entities generalize what ADR 13 hard-coded:

- **ScheduledJob** is the definition of a recurring job: a unique `key` (e.g.
  `digest.scrape`), its `stage`, an `enabled` flag, the cron `schedule` and
  `timezone`, the `runner` that should execute it (`server`, `local`, or `any`),
  free-form `config`, and the `last_run_at` / `next_run_at` bookkeeping the ticker
  reads and writes.
- **JobRun** is one execution of a job: `status` (`queued`, `running`, `succeeded`,
  `failed`, `cancelled`), `trigger` (`schedule` or `manual`), the `runner` that ran
  it, the `scheduled_for` tick it represents, timing, an `error`, and `stats`. The
  unique `(job, scheduled_for)` index is the scheduler dedup key.
- **Artifact** is a stored piece of job I/O: for the digest, one per Source. It is
  stored inline in Postgres when small (under about 256 KB) and in S3 otherwise,
  with a content type, a size, a claim/lifecycle status, and an `expires_at` (about
  seven days, swept on the same tick). It serves three purposes: **retention** of
  what was scraped, **re-processing** (re-run the LLM stage over stored artifacts
  without re-scraping), and a **hand-off buffer** for the future home-finance
  runner, whose data lands here for a claimer to pick up.
- **ApiToken** is a scope-only bearer credential for an external runner (see the
  work API below): a hashed token, a runner identity, a scope, and an owner User.

The digest stops being one `digest.build` job and becomes two, `digest.scrape` and
`digest.llm`. Registering a new workload later (the finance runner) is "insert a
`ScheduledJob` row and write a runner," not a re-architecture.

### The scrape/LLM split

`digest.scrape` runs on-box in the worker (inline). It is deterministic: fetch each
active Source and write one Artifact per Source, `pending`, with a TTL. It never
calls Anthropic.

`digest.llm` consumes those artifacts. It keeps the ADR 13 Fargate task behind
`DIGEST_MODE`. This is Alif's explicit call: the LLM stage keeps off-box headroom
for a heavier future model step, even though the current submit phase is light. In
`fargate` mode the worker assembles the document from the artifacts, writes it to
S3, and the task reads it and submits the Batch, preserving Shape B (the task never
touches Postgres). In `local` mode it runs inline. When the job's runner is `local`
or `any`, the worker does not process it at all; the run sits claimable for an
external runner.

Scrape moving on-box is the change from ADR 13. The submit phase there was already
light (fetch a couple of feeds, POST a batch), so on-box scrape costs the micro
little, and it is what makes the artifacts exist to be re-processed or claimed.

### The external-runner work API

The LLM step can run in one of two places, and the design ships both:

- **On the box, with an API key.** Reliable and unattended. The key is already on
  the box (`ANTHROPIC_API_KEY` in SSM), and on a small model (Haiku) a daily digest
  is cents a month.
- **On an external runner that pulls the work.** Alif's laptop, where Claude Code is
  signed in on the subscription, today; the home finance runner (issue #74)
  later. The runner claims a job's artifacts over HTTPS and posts the result back,
  so the topology is "home polls AWS," not "AWS calls home." That is NAT-friendly
  and needs zero inbound access to the runner.

The work API is two operations:

- **`GET /api/work/claim`** atomically moves a job's pending artifacts to `claimed`
  and returns their content (inline, or a presigned S3 GET) plus the JobRun context.
- **`POST /api/work/complete`** writes the result (for the digest, the date-keyed
  Digest, upserted) and closes the JobRun.

Both are authenticated by **scope-only bearer tokens**, never the session cookie. A
bearer token resolves to `(User, scope)` on a **separate request-context key** from
the one the session middleware uses, and the work API is gated on scope, never on
the admin-role check. The Admin Console stays cookie-only, so a leaked runner token
can claim and complete only the work its scope names and can never reach
`/api/admin/*`. Tokens are per-runner (`laptop`, `home-finance`) and independently
revocable, which matters when the home runner starts handling financial data.

## Consequences

- The digest output is unchanged: still a date-keyed Digest row with
  `ON CONFLICT DO NOTHING` idempotency, so a redelivery, a dual-scheduler overlap,
  or a re-run never produces a duplicate or demotes a completed day.
- There is now one place (the `/admin` Jobs section) to see and control every
  scheduled job, and one vocabulary (ScheduledJob, JobRun, Artifact) for any future
  periodic workload.
- Two ways to run an LLM step coexist on purpose: unattended on the box, or
  offloaded to a runner on a machine that is already signed in. The bearer/work API
  is the seam the finance runner reuses without new architecture.
- Local dev no longer needs Fargate for the digest: `digest.scrape` and
  `digest.llm` in `local` mode run entirely on the dev machine.

## Rollout

The platform ships in phases so the live digest is never at risk from a single
change. The registry tables and the scheduler land dormant (no enabled rows,
EventBridge still authoritative) before anything cuts over. The cutover seeds the
enabled `ScheduledJob` rows, verifies the ticker drives the digest, and only then
disables the EventBridge schedules through a gated `terraform apply`. A brief overlap
where both schedulers are live is safe because the digest is date-idempotent. The
EventBridge rules and their IAM are retired for these jobs only after that cutover is
confirmed; the Fargate task, cluster, and its IAM are kept (ADR 13's scale-to-zero
run-to-completion mode stays intact).

## Alternatives rejected

- **Keep EventBridge and add admin control on top.** The schedule would still live
  in AWS, so a toggle or a re-time from `/admin` would mean an app-to-EventBridge
  write path with IAM for it, and run history would still not exist as app data.
  Moving the schedule into a table the app already owns is simpler and gives history
  for free.
- **A multi-instance scheduler with leader election.** There is one worker;
  election machinery would be cost with no benefit. If the worker is ever scaled
  horizontally, the unique `(job, scheduled_for)` constraint already prevents
  duplicate runs, so the ticker can be revisited then.
- **One bearer identity across the admin console and the work API.** Rejected on
  blast radius: a runner token that could reach `/api/admin/*` would turn a leaked
  laptop credential into full admin. Scope-only tokens on a separate context key
  keep the two boundaries apart.
- **Run the LLM step on the Claude subscription from the box.** Not possible within
  terms of service: subscription auth is for interactive Claude Code and claude.ai,
  and a headless backend call needs an API key. The runner path gets the
  subscription benefit legitimately by running on the laptop where Claude Code is
  already signed in.
