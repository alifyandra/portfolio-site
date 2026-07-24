// Command worker consumes jobs from SQS. No real job types exist yet — this is
// the async seam for future LLM/notification work (see ADR 0007).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/internal/bootstrap"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/digest"
	"github.com/alifyandra/portfolio-site/backend/internal/email"
	"github.com/alifyandra/portfolio-site/backend/internal/jobs"
	"github.com/alifyandra/portfolio-site/backend/internal/notify"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/server"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
)

// ackURL derives the absolute finance sync ack endpoint from the backend's public
// base. The base is taken from the OAuth redirect URL (the one config value that
// already carries this backend's own scheme+host, e.g.
// https://api.example.dev/api/auth/google/callback -> https://api.example.dev). An
// empty or unparseable redirect yields an empty ack URL, which the notifier treats
// as "send the notification without an action button" (ack by hand). This keeps the
// ADR 0016 env surface to the ntfy + token vars, with no separate public-URL var.
func ackURL(oauthRedirect string) string {
	if oauthRedirect == "" {
		return ""
	}
	u, err := url.Parse(oauthRedirect)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/api/finance/sync/ack"
}

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := bootstrap.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer app.Close()

	q := app.Deps.Queue

	// Refresh-handshake notifier for the ack-gated finance sync (ADR 0016). It
	// no-ops gracefully when ntfy is unconfigured, so local/dev needs no setup. The
	// ack endpoint base is derived from the backend's own public host (the same host
	// Google OAuth redirects to); a blank host just omits the action button.
	notifier := notify.New(notify.Config{
		BaseURL:  cfg.NtfyBaseURL,
		Topic:    cfg.NtfyTopic,
		AckURL:   ackURL(cfg.GoogleRedirectURL),
		AckToken: cfg.FinanceSyncAckToken,
	}, slog.Default())

	// Start the in-process scheduler alongside the poll loop (ADR 0014). It ships
	// dormant: with zero enabled ScheduledJob rows it enqueues nothing, and it
	// no-ops whenever the queue is not configured. It stops on ctx.Done().
	tickInterval := time.Duration(cfg.SchedulerTickSeconds) * time.Second
	scheduler := jobs.NewScheduler(app.Deps.Ent, q, tickInterval, slog.Default(), notifier)
	// One-shot recovery pass before steady-state ticking: re-drive runs left queued,
	// reap runs left running, and expire lapsed artifacts from a prior process, so a
	// worker restart recovers stranded work immediately instead of after a full tick.
	// Inert while dormant (no scheduler-produced runs, no artifacts).
	scheduler.RecoverOnce(ctx)
	go scheduler.Run(ctx)

	slog.Info("worker started, polling for jobs")

	for {
		select {
		case <-ctx.Done():
			slog.Info("worker shutting down")
			return nil
		default:
		}

		msgs, err := q.Receive(ctx, 10)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.Error("receive failed", "err", err)
			continue
		}
		for _, m := range msgs {
			process(ctx, app.Deps, q, m)
		}
	}
}

// process runs one received message. A message with no JobRunID is legacy
// (contact.notify, EventBridge-driven digest.*) and keeps its exact prior
// behavior: run handle, leave on the queue on error, ack on success. A message
// carrying a JobRunID is scheduler-driven (ADR 0014) and is wrapped in the
// JobRun lifecycle.
func process(ctx context.Context, deps *server.Deps, q *queue.Client, m queue.Received) {
	if m.Job.JobRunID == 0 {
		err := handle(ctx, deps, m.Job)
		if errors.Is(err, errNoHandler) {
			// Unrecognized legacy type: ack so it does not redeliver forever (the prior
			// behavior when the default case returned nil).
			slog.Warn("no handler for job type; acking", "type", m.Job.Type)
			ack(ctx, q, m)
			return
		}
		if err != nil {
			slog.Error("job failed", "type", m.Job.Type, "err", err)
			return // leave on queue for redelivery
		}
		ack(ctx, q, m)
		return
	}
	processScheduled(ctx, deps, q, m)
}

// processScheduled runs a scheduler-driven message and records the JobRun
// lifecycle (ADR 0014). The run is transitioned queued->running only if it is
// still queued, so a duplicate or an SQS redelivery no-ops. Terminal outcomes
// always ack: for scheduled jobs the retry is the next scheduled tick, not SQS
// redelivery, so acking a failure avoids a redelivery loop fighting the status
// guard.
func processScheduled(ctx context.Context, deps *server.Deps, q *queue.Client, m queue.Received) {
	runID := m.Job.JobRunID

	// Atomically claim the run: queued -> running, but only while it is still
	// queued. Zero rows affected means it is not queued (a duplicate/redelivery, or
	// already terminal): ack and do not re-run.
	claimed, err := deps.Ent.JobRun.Update().
		Where(jobrun.IDEQ(runID), jobrun.StatusEQ(jobrun.StatusQueued)).
		SetStatus(jobrun.StatusRunning).
		SetStartedAt(time.Now()).
		Save(ctx)
	if err != nil {
		// A DB error transitioning the run is infrastructural, not a job failure: we
		// have not run anything and cannot record a terminal state, so leave the
		// message on the queue for redelivery rather than acking work we did not do.
		slog.Error("scheduled run: claim failed", "run", runID, "type", m.Job.Type, "err", err)
		return
	}
	if claimed == 0 {
		slog.Info("scheduled run: not queued (duplicate or terminal); acking", "run", runID, "type", m.Job.Type)
		ack(ctx, q, m)
		return
	}

	if runErr := handle(ctx, deps, m.Job); runErr != nil {
		slog.Error("scheduled run failed", "run", runID, "type", m.Job.Type, "err", runErr)
		finishRun(ctx, deps, runID, jobrun.StatusFailed, runErr.Error())
		ack(ctx, q, m) // ack anyway: the next scheduled tick is the retry, not SQS
		return
	}
	finishRun(ctx, deps, runID, jobrun.StatusSucceeded, "")
	ack(ctx, q, m)
}

// finishRun writes a terminal status and finished_at onto the run (best-effort:
// a failure to record it is logged, not propagated — the message is acked either
// way).
func finishRun(ctx context.Context, deps *server.Deps, runID int, status jobrun.Status, errText string) {
	upd := deps.Ent.JobRun.UpdateOneID(runID).
		SetStatus(status).
		SetFinishedAt(time.Now())
	if errText != "" {
		upd.SetError(errText)
	}
	if err := upd.Exec(ctx); err != nil {
		slog.Error("scheduled run: record terminal status failed", "run", runID, "status", status, "err", err)
	}
}

// ack deletes the message from the queue, logging a failure.
func ack(ctx context.Context, q *queue.Client, m queue.Received) {
	if err := q.Delete(ctx, m); err != nil {
		slog.Error("ack failed", "err", err)
	}
}

// errNoHandler is returned by handle for an unrecognized job type. The legacy path
// acks it (a stray message should not redeliver forever); the scheduler path records
// the run failed rather than succeeded (ADR 0014: a ScheduledJob whose key has no
// handler must not silently look green).
var errNoHandler = errors.New("no handler for job type")

// handle dispatches a job to its processor. Add cases here as job types appear.
func handle(ctx context.Context, deps *server.Deps, job queue.Job) error {
	switch job.Type {
	case queue.TypeContactNotify:
		return handleContactNotify(ctx, deps, job)
	case queue.TypeDigestBuild:
		return handleDigestBuild(ctx, deps, job)
	case queue.TypeDigestCollect:
		return handleDigestCollect(ctx, deps, job)
	case queue.TypeDigestScrape:
		return handleDigestScrape(ctx, deps, job)
	case queue.TypeDigestLlm:
		return handleDigestLlm(ctx, deps, job)
	case queue.TypeFinanceSync:
		return handleFinanceSync(ctx, deps, job)
	default:
		return errNoHandler
	}
}

// handleFinanceSync is a defense-in-depth no-op (ADR 0016). finance.sync is
// ack-gated: the scheduler creates its run in awaiting_ack and NEVER enqueues it, so
// this case should never actually run. It exists only so that if a finance.sync
// message somehow reaches the queue (a manual mis-enqueue, a leftover message), the
// worker recognizes the type and acks it cleanly rather than failing the run via
// errNoHandler. It does no work and touches no run lifecycle: the finance sync seam
// (claim/ack/complete) owns that.
func handleFinanceSync(_ context.Context, _ *server.Deps, _ queue.Job) error {
	slog.Warn("finance.sync is ack-gated and should never be enqueued; acking no-op")
	return nil
}

// handleContactNotify emails Alif about a new contact-form submission.
func handleContactNotify(ctx context.Context, deps *server.Deps, job queue.Job) error {
	var p queue.ContactNotifyPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		// Unrecoverable: bad payload. Don't redeliver forever — ack by returning nil.
		slog.Error("bad contact.notify payload", "err", err)
		return nil
	}

	if !deps.Email.Configured() {
		slog.Warn("email not configured; skipping contact notification", "id", p.ID)
		return nil // ack — nothing to retry until SES is configured
	}

	subject := fmt.Sprintf("Portfolio contact from %s", p.Name)
	body := fmt.Sprintf("New message via your portfolio:\n\nFrom: %s <%s>\n\n%s\n", p.Name, p.Email, p.Body)

	err := deps.Email.Send(ctx, deps.Email.NotifyTo(), subject, body, p.Email)
	if errors.Is(err, email.ErrNotConfigured) {
		return nil
	}
	if err != nil {
		return err // transient (SES throttle etc.) — let it redeliver
	}
	slog.Info("sent contact notification", "id", p.ID)
	return nil
}

// handleDigestBuild runs the scheduled digest.build Job. It resolves the target
// date (payload.Date or today UTC) and dispatches by DIGEST_MODE: local runs the
// builder inline; fargate launches a run-to-completion task and acks only on a
// clean exit. See ADR 0013.
func handleDigestBuild(ctx context.Context, deps *server.Deps, job queue.Job) error {
	var p queue.DigestBuildPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		slog.Error("bad digest.build payload", "err", err)
		return nil // unrecoverable bad payload — ack
	}
	date, err := digest.ParseDate(p.Date)
	if err != nil {
		slog.Error("bad digest.build date", "date", p.Date, "err", err)
		return nil // unrecoverable — ack
	}

	if deps.Config.DigestMode == "fargate" {
		if deps.DigestLauncher == nil {
			slog.Error("digest.build in fargate mode but launcher is nil; skipping")
			return nil
		}
		return runDigestFargate(ctx, deps, date)
	}

	// local: run inline. An unconfigured Anthropic key is an ack (nothing to do),
	// mirroring handleContactNotify's email.ErrNotConfigured handling.
	err = deps.Digest.Run(ctx, date)
	if errors.Is(err, digest.ErrNotConfigured) {
		return nil
	}
	return err
}

// handleDigestCollect runs the recurring digest.collect Job: it polls every
// in-flight Anthropic Message Batch submitted by digest.build (prod batch mode)
// and persists any that have ended. It carries no payload — it drains all pending
// digests. It runs inline on the worker (the box already has ANTHROPIC_API_KEY and
// owns Postgres; there is no heavy compute to push off-box). A blank key is an ack.
// In local (synchronous) mode there are never any pending rows, so this is a no-op.
// See ADR 0013 (Batch API amendment).
func handleDigestCollect(ctx context.Context, deps *server.Deps, _ queue.Job) error {
	if deps.Digest == nil {
		slog.Error("digest.collect but digest builder is nil; skipping")
		return nil
	}
	err := deps.Digest.Collect(ctx)
	if errors.Is(err, digest.ErrNotConfigured) {
		return nil
	}
	return err // nil after a clean sweep; a query error redelivers (next sweep retries)
}

// handleDigestScrape runs the digest.scrape stage (ADR 0014): fetch the active
// Sources on-box and persist one Artifact per source (never calling Anthropic). It is
// scheduler-driven, so it requires the JobRunID the scheduler stamped on the envelope
// to attribute the artifacts to the producing run. Dormant until a digest.scrape
// ScheduledJob row is enabled.
func handleDigestScrape(ctx context.Context, deps *server.Deps, job queue.Job) error {
	if deps.Digest == nil {
		slog.Error("digest.scrape but digest builder is nil; skipping")
		return nil
	}
	if job.JobRunID == 0 {
		return fmt.Errorf("digest.scrape requires a JobRunID (it is scheduler-driven)")
	}
	return deps.Digest.Scrape(ctx, deps.Storage, job.JobRunID, time.Now())
}

// handleDigestLlm runs the digest.llm stage (ADR 0014): assemble the pending scrape
// Artifacts and summarize them into the dated Digest. The job's runner decides where
// the work runs: server runs it on this box (inline in local mode, or on the Fargate
// task in fargate mode); an external runner (local/any) leaves it claimable for the
// work API (P5), so the box does nothing. Scheduler-driven.
func handleDigestLlm(ctx context.Context, deps *server.Deps, job queue.Job) error {
	if deps.Digest == nil {
		slog.Error("digest.llm but digest builder is nil; skipping")
		return nil
	}
	if job.JobRunID == 0 {
		return fmt.Errorf("digest.llm requires a JobRunID (it is scheduler-driven)")
	}

	run, err := deps.Ent.JobRun.Query().Where(jobrun.IDEQ(job.JobRunID)).WithJob().Only(ctx)
	if err != nil {
		return fmt.Errorf("digest.llm: load run %d: %w", job.JobRunID, err)
	}
	// Normalize to the UTC calendar day BEFORE keying the Digest, matching every other
	// date-keying site (the live build, LlmLocal, Persist). ScheduledFor is stored as
	// whatever instant the tick fired; a job timed in a non-UTC zone near a day boundary
	// would format to the wrong UTC day and create a duplicate Digest row. Compute the
	// day once and feed it to both the local and fargate paths.
	day := digest.NormalizeDate(run.ScheduledFor)
	runner := ""
	if run.Edges.Job != nil {
		runner = string(run.Edges.Job.Runner)
	}

	// An external runner pulls the work over the work API (P5); the box leaves the
	// artifacts pending/claimable and does nothing here.
	if runner != "" && runner != "server" {
		slog.Info("digest.llm: job runner is not server; leaving claimable", "runner", runner, "run", job.JobRunID)
		return nil
	}

	if deps.Config.DigestMode == "fargate" {
		if deps.DigestLauncher == nil {
			slog.Error("digest.llm in fargate mode but launcher is nil; skipping")
			return nil
		}
		return runDigestLlmFargate(ctx, deps, job.JobRunID, day)
	}

	err = deps.Digest.LlmLocal(ctx, deps.Storage, job.JobRunID, day)
	if errors.Is(err, digest.ErrNotConfigured) {
		return nil
	}
	return err
}

// runDigestLlmFargate is the digest.llm fargate path (ADR 0014): the worker assembles
// the document from the pending scrape Artifacts on-box, writes it to S3, launches the
// task to read that doc (DIGEST_DOC_KEY) and submit a batch, then reads the pending
// Result back and persists it. The doc goes via S3 because the ECS task-override env
// is size-limited; the task never touches Postgres (Shape B). digest.collect resolves
// the batch. The consumed artifacts are marked done only after a clean submit + persist,
// so a failed submit leaves them pending for the next run.
func runDigestLlmFargate(ctx context.Context, deps *server.Deps, runID int, date time.Time) error {
	day := date.Format("2006-01-02")

	doc, ids, err := deps.Digest.AssembleFromArtifacts(ctx, deps.Storage)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		slog.Warn("digest.llm: no pending artifacts; nothing to summarize", "run", runID)
		return nil // ack: nothing to do
	}

	docKey := deps.Config.DigestResultPrefix + "doc-" + day + "-" + uuid.NewString() + ".md"
	if err := deps.Storage.PutObject(ctx, docKey, "text/markdown", []byte(doc)); err != nil {
		return fmt.Errorf("digest.llm: write assembled doc to s3: %w", err)
	}
	resultKey := deps.Config.DigestResultPrefix + day + "-" + uuid.NewString() + ".json"

	runErr := deps.DigestLauncher.RunToCompletion(ctx, map[string]string{
		"DIGEST_DATE":       day,
		"DIGEST_DOC_KEY":    docKey,
		"DIGEST_RESULT_KEY": resultKey,
	})

	result := readDigestResult(ctx, deps.Storage, resultKey)
	_ = deps.Storage.DeleteObject(ctx, resultKey) // best-effort; lifecycle backstops
	_ = deps.Storage.DeleteObject(ctx, docKey)

	if runErr != nil {
		if result != nil {
			if perr := digest.Persist(ctx, deps.Ent, result); perr != nil {
				slog.Error("digest.llm: persist failed result", "date", day, "err", perr)
			}
		}
		return runErr
	}
	if result == nil {
		return fmt.Errorf("digest.llm: task for %s exited 0 but wrote no result to %s", day, resultKey)
	}
	if err := digest.Persist(ctx, deps.Ent, result); err != nil {
		return err
	}
	return deps.Digest.MarkConsumed(ctx, ids, runID)
}

// runDigestFargate is the DIGEST_MODE=fargate path (ADR 0013, Shape B + Batch API
// amendment): the worker reads the active Sources on-box, launches the Fargate task
// to fetch them off-box and submit a Message Batch (the task writes its pending
// Result JSON — carrying the batch id — to S3 and never touches Postgres), then
// reads that Result back and writes the pending Digest row itself. The recurring
// digest.collect job resolves the batch later. The worker owns the database; the
// task owns only the compute.
func runDigestFargate(ctx context.Context, deps *server.Deps, date time.Time) error {
	day := date.Format("2006-01-02")

	sources, err := digest.ActiveSourceInputs(ctx, deps.Ent)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		slog.Warn("digest: no active sources; nothing to build", "date", day)
		return nil // ack: nothing to do
	}
	srcJSON, err := json.Marshal(sources)
	if err != nil {
		slog.Error("digest: marshal sources", "err", err)
		return nil // unrecoverable (our own data) — ack rather than loop
	}

	// Unique per run so a crashed prior run for the same day can never leave a stale
	// Result the worker would misread; the worker deletes it after reading and an S3
	// lifecycle rule expires any orphan (see digest.tf).
	key := deps.Config.DigestResultPrefix + day + "-" + uuid.NewString() + ".json"

	// Pass the resolved day (worker clock decides, avoiding a task midnight race), the
	// Sources, and the result key as container-env overrides on the task.
	runErr := deps.DigestLauncher.RunToCompletion(ctx, map[string]string{
		"DIGEST_DATE":       day,
		"DIGEST_SOURCES":    string(srcJSON),
		"DIGEST_RESULT_KEY": key,
	})

	// The task writes its Result before exiting and RunToCompletion blocks until the
	// task STOPs, so the object (if any) is present now. Read it regardless of exit
	// code: a content failure exits non-zero but still records a failure reason.
	result := readDigestResult(ctx, deps.Storage, key)
	_ = deps.Storage.DeleteObject(ctx, key) // best-effort cleanup; lifecycle backstops

	if runErr != nil {
		// Task failed (content failure -> non-zero exit, or an infra failure). Persist
		// the failed Result if the task wrote one (no-demote), then leave the message
		// for redelivery; the daily schedule is the backstop.
		if result != nil {
			if perr := digest.Persist(ctx, deps.Ent, result); perr != nil {
				slog.Error("digest: persist failed result", "date", day, "err", perr)
			}
		}
		return runErr
	}

	// Clean exit: expect a pending Result (the batch was submitted). Persist writes
	// the pending row; digest.collect resolves it once the batch ends. A missing
	// object here is anomalous (task exited 0 but produced nothing) — fail so it
	// redelivers rather than silently acking an empty run.
	if result == nil {
		return fmt.Errorf("digest: task for %s exited 0 but wrote no result to %s", day, key)
	}
	return digest.Persist(ctx, deps.Ent, result)
}

// readDigestResult fetches and decodes the task's Result from S3. A missing object
// (the task wrote nothing, e.g. Anthropic not configured) or any read/decode error
// yields nil: the caller decides what a nil means given the task's exit code.
func readDigestResult(ctx context.Context, store *storage.Store, key string) *digest.Result {
	data, err := store.GetObject(ctx, key)
	if err != nil {
		if !errors.Is(err, storage.ErrObjectNotFound) {
			slog.Error("digest: read result from s3", "key", key, "err", err)
		}
		return nil
	}
	var r digest.Result
	if err := json.Unmarshal(data, &r); err != nil {
		slog.Error("digest: decode result from s3", "key", key, "err", err)
		return nil
	}
	return &r
}
