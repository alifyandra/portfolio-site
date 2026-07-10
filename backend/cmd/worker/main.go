// Command worker consumes jobs from SQS. No real job types exist yet — this is
// the async seam for future LLM/notification work (see ADR 0007).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"

	"github.com/alifyandra/portfolio-site/backend/internal/bootstrap"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/digest"
	"github.com/alifyandra/portfolio-site/backend/internal/email"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/server"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
)

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
			if err := handle(ctx, app.Deps, m.Job); err != nil {
				slog.Error("job failed", "type", m.Job.Type, "err", err)
				continue // leave on queue for redelivery
			}
			if err := q.Delete(ctx, m); err != nil {
				slog.Error("ack failed", "err", err)
			}
		}
	}
}

// handle dispatches a job to its processor. Add cases here as job types appear.
func handle(ctx context.Context, deps *server.Deps, job queue.Job) error {
	switch job.Type {
	case queue.TypeContactNotify:
		return handleContactNotify(ctx, deps, job)
	case queue.TypeDigestBuild:
		return handleDigestBuild(ctx, deps, job)
	case queue.TypeDigestCollect:
		return handleDigestCollect(ctx, deps, job)
	default:
		slog.Warn("no handler for job type", "type", job.Type)
		return nil
	}
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
