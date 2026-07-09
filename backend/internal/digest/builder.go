package digest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	entdigest "github.com/alifyandra/portfolio-site/backend/ent/digest"
	"github.com/alifyandra/portfolio-site/backend/ent/source"
)

// ErrNotConfigured is returned when the Anthropic API key is absent. Callers treat
// it as an ack (nothing to summarize), mirroring email.ErrNotConfigured and the
// Spotify degrade-without-creds pattern (see ADR 0013).
var ErrNotConfigured = fmt.Errorf("digest: not configured")

// systemPrompt steers the model toward a tight, readable Markdown briefing.
const systemPrompt = "You are a concise briefing writer. You are given the raw " +
	"contents of several public sources. Produce a single well-structured Markdown " +
	"digest of what is notable across them today: group by source with short " +
	"headings, use bullet points, keep it skimmable, and do not invent facts that " +
	"are not present in the provided material."

// sourceFetcher fetches a Source's content over HTTP. An interface so a test can
// stub the network; *Fetcher is the production implementation.
type sourceFetcher interface {
	Fetch(ctx context.Context, url string) (string, error)
}

// Builder runs digest.build: it reads the active Sources, fetches each, asks the
// Anthropic API for one Markdown summary, and upserts the dated Digest row. This
// is the run-to-completion work — invoked inline by the worker (DIGEST_MODE=local)
// or as the cmd/digest entrypoint inside a Fargate task (fargate). See ADR 0013.
type Builder struct {
	ent       *ent.Client
	anthropic *Anthropic
	fetcher   sourceFetcher
}

// NewBuilder wires the builder. The Anthropic client and Fetcher are cheap to
// construct (no network), so this never fails.
func NewBuilder(entClient *ent.Client, anthropic *Anthropic, fetcher sourceFetcher) *Builder {
	return &Builder{ent: entClient, anthropic: anthropic, fetcher: fetcher}
}

// Configured reports whether a summary can be produced (Anthropic key present).
func (b *Builder) Configured() bool { return b.anthropic.Configured() }

// Run builds the Digest for date (normalized to UTC midnight). It is idempotent by
// date: a redelivery upserts the same day's row. Returns ErrNotConfigured when the
// Anthropic key is absent (callers ack). A fetch/summarize failure writes a failed
// Digest row (best effort) and returns the error so the run fails (exit non-zero /
// SQS redelivery), the schedule being the backstop.
func (b *Builder) Run(ctx context.Context, date time.Time) error {
	day := NormalizeDate(date)
	log := slog.With("date", day.Format("2006-01-02"))

	if !b.Configured() {
		log.Warn("digest: anthropic not configured; skipping")
		return ErrNotConfigured
	}

	sources, err := b.ent.Source.Query().
		Where(source.ActiveEQ(true)).
		Order(ent.Asc(source.FieldID)).
		All(ctx)
	if err != nil {
		return fmt.Errorf("digest: load sources: %w", err)
	}
	if len(sources) == 0 {
		log.Warn("digest: no active sources; nothing to build")
		return nil
	}

	var doc strings.Builder
	var failures []string
	fetched := 0
	for _, s := range sources {
		content, err := b.fetcher.Fetch(ctx, s.URL)
		if err != nil {
			log.Warn("digest: source fetch failed", "source", s.Name, "url", s.URL, "err", err)
			failures = append(failures, fmt.Sprintf("- %s (%s): %v", s.Name, s.URL, err))
			continue
		}
		fetched++
		fmt.Fprintf(&doc, "## Source: %s (%s, %s)\n\n%s\n\n", s.Name, s.Type, s.URL, content)
	}

	// Every source failed: nothing to summarize. Record the failure and let the run
	// fail so it is retried.
	if fetched == 0 {
		runErr := fmt.Errorf("digest: all %d sources failed to fetch", len(sources))
		b.writeFailed(ctx, day, runErr)
		return runErr
	}
	if len(failures) > 0 {
		fmt.Fprintf(&doc, "## Sources that could not be fetched\n\n%s\n", strings.Join(failures, "\n"))
	}

	summary, err := b.anthropic.Summarize(ctx, systemPrompt, doc.String())
	if err != nil {
		b.writeFailed(ctx, day, err)
		return err
	}
	if summary.Truncated {
		log.Warn("digest: summary truncated at max_tokens")
	}

	if err := b.ent.Digest.Create().
		SetDate(day).
		SetStatus(entdigest.StatusCompleted).
		SetContent(summary.Text).
		SetModel(summary.Model).
		SetError("").
		OnConflictColumns(entdigest.FieldDate).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return fmt.Errorf("digest: upsert digest: %w", err)
	}

	log.Info("digest: built",
		"sources", fetched, "failed_sources", len(failures), "model", summary.Model,
		"input_tokens", summary.InputTokens, "output_tokens", summary.OutputTokens)
	return nil
}

// writeFailed records a failed Digest for the day (best effort). It uses ON CONFLICT
// DO NOTHING, NOT UpdateNewValues: a redelivery of a date that already succeeded can
// hit a transient error, and demoting an existing completed digest back to failed
// would destroy a good day's briefing. So a failed row is only inserted when no row
// exists for the date yet; an existing completed (or failed) row is left untouched.
// The success path still upserts (UpdateNewValues), so a genuine failed->completed
// re-run is self-healing.
func (b *Builder) writeFailed(ctx context.Context, day time.Time, cause error) {
	if err := b.ent.Digest.Create().
		SetDate(day).
		SetStatus(entdigest.StatusFailed).
		SetError(cause.Error()).
		OnConflictColumns(entdigest.FieldDate).
		DoNothing().
		Exec(ctx); err != nil {
		slog.Warn("digest: recording failed digest", "date", day.Format("2006-01-02"), "err", err)
	}
}
