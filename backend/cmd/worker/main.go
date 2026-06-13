// Command worker consumes jobs from SQS. No real job types exist yet — this is
// the async seam for future LLM/notification work (see ADR 0007).
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alifyandra/portfolio-site/backend/internal/bootstrap"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
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
			if err := handle(ctx, m.Job); err != nil {
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
func handle(_ context.Context, job queue.Job) error {
	switch job.Type {
	// case "contact.notify": ...
	// case "llm.summarise": ...
	default:
		slog.Warn("no handler for job type", "type", job.Type)
		return nil
	}
}
