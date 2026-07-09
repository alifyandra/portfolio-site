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

	"github.com/alifyandra/portfolio-site/backend/internal/bootstrap"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/digest"
	"github.com/alifyandra/portfolio-site/backend/internal/email"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/server"
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
		// Pass the resolved day so the task is not subject to a midnight-boundary
		// race: the worker's clock decides the day, not the task's.
		return deps.DigestLauncher.RunToCompletion(ctx, map[string]string{
			"DIGEST_DATE": date.Format("2006-01-02"),
		})
	}

	// local: run inline. An unconfigured Anthropic key is an ack (nothing to do),
	// mirroring handleContactNotify's email.ErrNotConfigured handling.
	err = deps.Digest.Run(ctx, date)
	if errors.Is(err, digest.ErrNotConfigured) {
		return nil
	}
	return err
}
