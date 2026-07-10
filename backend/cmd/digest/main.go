// Command digest is the run-to-completion Fargate task for the digest.build Job
// (ADR 0013, Shape B + Batch API amendment). It receives the target date, the
// active Sources, and an S3 result key as container-env overrides from the worker;
// fetches each Source and submits them to the Anthropic Message Batches API (50%
// cheaper than a synchronous call), then writes a pending Result (carrying the
// batch id) as JSON to S3. It never touches Postgres — the worker reads the Result
// back and writes the pending Digest row, and the recurring digest.collect job
// resolves the batch later. Exit 0 once the batch is submitted, non-zero on a
// content or infrastructure failure (the worker acks SQS only on exit 0).
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

	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/digest"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
)

func main() {
	if err := run(); err != nil {
		slog.Error("digest failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Inputs arrive as container-env overrides set by the worker at RunTask.
	date, err := digest.ParseDate(os.Getenv("DIGEST_DATE"))
	if err != nil {
		return err
	}
	resultKey := os.Getenv("DIGEST_RESULT_KEY")
	if resultKey == "" {
		return fmt.Errorf("digest: DIGEST_RESULT_KEY is required (set by the worker)")
	}
	var sources []digest.SourceInput
	if raw := os.Getenv("DIGEST_SOURCES"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &sources); err != nil {
			return fmt.Errorf("digest: bad DIGEST_SOURCES: %w", err)
		}
	}
	if len(sources) == 0 {
		// The worker only launches when there is work, so this is anomalous. Exit 0
		// (nothing to summarize, nothing to write) rather than fail-loop.
		slog.Warn("digest: no sources provided; nothing to build")
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := storage.New(ctx, cfg)
	if err != nil {
		return err
	}

	builder := digest.NewContentBuilder(
		digest.NewAnthropic(cfg.AnthropicAPIKey, cfg.DigestModel, cfg.DigestMaxTokens),
		digest.NewFetcher(),
	)

	result, buildErr := builder.Submit(ctx, date, sources)
	if errors.Is(buildErr, digest.ErrNotConfigured) {
		// No Anthropic key: exit 0 without writing a result. The worker reads no
		// object and acks (the degrade-without-creds pattern, ADR 0013).
		slog.Warn("digest: anthropic not configured; exiting 0 without a result")
		return nil
	}

	// Submit returns a Result even on a content failure (status=failed): write it so
	// the worker records the failure reason, then propagate buildErr as a non-zero
	// exit so the worker leaves the message for redelivery. On success it is a
	// pending Result carrying the batch id for the worker to persist.
	if result != nil {
		body, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("digest: marshal result: %w", err)
		}
		if err := store.PutObject(ctx, resultKey, "application/json", body); err != nil {
			return fmt.Errorf("digest: write result to s3: %w", err)
		}
		slog.Info("digest: wrote result to s3", "key", resultKey, "status", result.Status)
	}
	return buildErr
}
