// Command digest is the run-to-completion Fargate task that submits the digest's
// Anthropic Message Batch (ADR 0013 Shape B + Batch API amendment; ADR 0014 split).
// It takes a target date and an S3 result key as container-env overrides, plus one
// of two document inputs: DIGEST_SOURCES (the monolithic digest.build path: fetch
// the Sources here, then submit) or DIGEST_DOC_KEY (the digest.llm path: the worker
// already assembled the document from scrape Artifacts and wrote it to S3, so just
// read and submit). Either way it writes a pending Result (carrying the batch id) as
// JSON to S3. It never touches Postgres; the worker reads the Result back, writes the
// pending Digest row, and the recurring digest.collect job resolves the batch later.
// Exit 0 once the batch is submitted, non-zero on a content or infrastructure failure
// (the worker acks SQS only on exit 0).
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

	result, buildErr := buildResult(ctx, store, builder, date)
	if errors.Is(buildErr, digest.ErrNotConfigured) {
		// No Anthropic key: exit 0 without writing a result. The worker reads no
		// object and acks (the degrade-without-creds pattern, ADR 0013).
		slog.Warn("digest: anthropic not configured; exiting 0 without a result")
		return nil
	}

	// buildResult returns a Result even on a content failure (status=failed): write it
	// so the worker records the failure reason, then propagate buildErr as a non-zero
	// exit so the worker leaves the message for redelivery. On success it is a pending
	// Result carrying the batch id for the worker to persist.
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

// buildResult produces the Result the task writes to S3. It has two input modes,
// selected by which env override the worker set:
//   - DIGEST_DOC_KEY (ADR 0014, digest.llm fargate): the worker already assembled
//     the document from scrape Artifacts and wrote it to S3. Read that doc and submit
//     a batch. No fetching happens in the task.
//   - DIGEST_SOURCES (ADR 0013, digest.build): the monolithic path. Fetch the
//     Sources and submit. Unchanged.
//
// Both submit a one-request Message Batch; only the document's origin differs. A nil
// Result with a nil error means "nothing to build" (exit 0, no write).
func buildResult(ctx context.Context, store *storage.Store, builder *digest.ContentBuilder, date time.Time) (*digest.Result, error) {
	if docKey := os.Getenv("DIGEST_DOC_KEY"); docKey != "" {
		doc, err := store.GetObject(ctx, docKey)
		if err != nil {
			return nil, fmt.Errorf("digest: read assembled doc %s: %w", docKey, err)
		}
		slog.Info("digest: submitting pre-assembled doc", "doc_key", docKey, "bytes", len(doc))
		return builder.SubmitAssembled(ctx, date, string(doc))
	}

	var sources []digest.SourceInput
	if raw := os.Getenv("DIGEST_SOURCES"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &sources); err != nil {
			return nil, fmt.Errorf("digest: bad DIGEST_SOURCES: %w", err)
		}
	}
	if len(sources) == 0 {
		// The worker only launches when there is work, so this is anomalous. Nil/nil
		// means exit 0 (nothing to summarize, nothing to write) rather than fail-loop.
		slog.Warn("digest: no sources provided; nothing to build")
		return nil, nil
	}
	return builder.Submit(ctx, date, sources)
}
