// Command digest builds the dated Digest and exits: it is the run-to-completion
// Fargate task entrypoint for the digest.build Job (ADR 0013). It reads the active
// Sources, summarizes them with the Anthropic API, upserts the Digest for the
// target date, and exits 0 on success / non-zero on any failure (the worker acks
// SQS only on exit 0). The target date comes from DIGEST_DATE (RFC3339 or
// YYYY-MM-DD), defaulting to today in UTC.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "github.com/jackc/pgx/v5/stdlib" // register the "pgx" database/sql driver

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/digest"
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

	date, err := digest.ParseDate(os.Getenv("DIGEST_DATE"))
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Open Ent directly (pgx stdlib + entsql.OpenDB, never ent.Open): this task only
	// needs Postgres + Anthropic, not the full app (Redis/S3/SQS). Schema migration
	// is owned by the API, so we deliberately do NOT auto-migrate here.
	db, err := sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		return err
	}
	entClient := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	defer entClient.Close()

	builder := digest.NewBuilder(
		entClient,
		digest.NewAnthropic(cfg.AnthropicAPIKey, cfg.DigestModel, cfg.DigestMaxTokens),
		digest.NewFetcher(),
	)

	err = builder.Run(ctx, date)
	if errors.Is(err, digest.ErrNotConfigured) {
		// No Anthropic key: nothing to do. Exit cleanly so the Job is acked rather
		// than redelivered forever (mirrors the worker's contact.notify handling).
		slog.Warn("digest: anthropic not configured; exiting 0")
		return nil
	}
	return err
}
