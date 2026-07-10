package digest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
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

// ContentBuilder does the off-box half of digest.build: it fetches the given
// Sources and asks the Anthropic API for one Markdown summary, returning a Result.
// It never touches Postgres, so it runs inside the Fargate task with no database
// access (ADR 0013, Shape B). The worker persists the Result.
type ContentBuilder struct {
	anthropic *Anthropic
	fetcher   sourceFetcher
}

// NewContentBuilder wires the content builder. The Anthropic client and Fetcher are
// cheap to construct (no network), so this never fails.
func NewContentBuilder(anthropic *Anthropic, fetcher sourceFetcher) *ContentBuilder {
	return &ContentBuilder{anthropic: anthropic, fetcher: fetcher}
}

// Configured reports whether a summary can be produced (Anthropic key present).
func (b *ContentBuilder) Configured() bool { return b.anthropic.Configured() }

// Build fetches sources, summarizes them, and returns the Result for date
// (normalized to UTC midnight). It expects at least one source (callers ack early
// when there are none). Returns:
//   - (completed Result, nil) on success;
//   - (failed Result, err) on a content failure (all fetches failed, or the
//     summarize call errored) so the caller both persists the failure and signals
//     a retry (task exits non-zero / SQS redelivery, the schedule being the
//     backstop);
//   - (nil, ErrNotConfigured) when the Anthropic key is absent (caller acks).
func (b *ContentBuilder) Build(ctx context.Context, date time.Time, sources []SourceInput) (*Result, error) {
	day := NormalizeDate(date)
	dayStr := day.Format("2006-01-02")
	log := slog.With("date", dayStr)

	if !b.Configured() {
		log.Warn("digest: anthropic not configured; skipping")
		return nil, ErrNotConfigured
	}

	doc, fetched, failures := b.buildDocument(ctx, sources, log)
	if fetched == 0 {
		// Every source failed: nothing to summarize. Fail so it is retried.
		runErr := fmt.Errorf("digest: all %d sources failed to fetch", len(sources))
		return &Result{Date: dayStr, Status: StatusFailed, Error: runErr.Error()}, runErr
	}

	summary, err := b.anthropic.Summarize(ctx, systemPrompt, doc)
	if err != nil {
		return &Result{Date: dayStr, Status: StatusFailed, Error: err.Error()}, err
	}
	if summary.Truncated {
		log.Warn("digest: summary truncated at max_tokens")
	}

	log.Info("digest: built",
		"sources", fetched, "failed_sources", len(failures), "model", summary.Model,
		"input_tokens", summary.InputTokens, "output_tokens", summary.OutputTokens)
	return &Result{
		Date:         dayStr,
		Status:       StatusCompleted,
		Content:      summary.Text,
		Model:        summary.Model,
		InputTokens:  summary.InputTokens,
		OutputTokens: summary.OutputTokens,
		Truncated:    summary.Truncated,
	}, nil
}

// Submit is the batch counterpart to Build (prod, ADR 0013 Batch API amendment):
// it fetches the sources and submits them as a one-request Message Batch (50%
// cheaper) instead of blocking on inference, returning a pending Result carrying
// the batch id. The worker persists the pending row and the recurring
// digest.collect job resolves it once the batch ends. Returns:
//   - (pending Result, nil) once the batch is submitted;
//   - (failed Result, err) if every fetch failed or the submit call errored (the
//     caller persists the failure and signals a retry, same as Build);
//   - (nil, ErrNotConfigured) when the Anthropic key is absent (caller acks).
func (b *ContentBuilder) Submit(ctx context.Context, date time.Time, sources []SourceInput) (*Result, error) {
	day := NormalizeDate(date)
	dayStr := day.Format("2006-01-02")
	log := slog.With("date", dayStr)

	if !b.Configured() {
		log.Warn("digest: anthropic not configured; skipping")
		return nil, ErrNotConfigured
	}

	doc, fetched, failures := b.buildDocument(ctx, sources, log)
	if fetched == 0 {
		runErr := fmt.Errorf("digest: all %d sources failed to fetch", len(sources))
		return &Result{Date: dayStr, Status: StatusFailed, Error: runErr.Error()}, runErr
	}

	// custom_id is the UTC day: one request per batch, keyed so collect can match it.
	batchID, err := b.anthropic.SubmitBatch(ctx, systemPrompt, doc, dayStr)
	if err != nil {
		return &Result{Date: dayStr, Status: StatusFailed, Error: err.Error()}, err
	}
	log.Info("digest: submitted batch",
		"sources", fetched, "failed_sources", len(failures), "batch", batchID, "model", b.anthropic.model)
	return &Result{
		Date:    dayStr,
		Status:  StatusPending,
		Model:   b.anthropic.model,
		BatchID: batchID,
	}, nil
}

// buildDocument fetches every source and assembles the single Markdown document
// handed to the model, returning it with the count successfully fetched and the
// human-readable failures (also appended as a footnote so the model knows what was
// missing). Shared by Build (synchronous) and Submit (batch). It never touches the
// database — the fetch is the off-box half of digest.build (ADR 0013, Shape B).
func (b *ContentBuilder) buildDocument(ctx context.Context, sources []SourceInput, log *slog.Logger) (doc string, fetched int, failures []string) {
	var sb strings.Builder
	for _, s := range sources {
		content, err := b.fetcher.Fetch(ctx, s.URL)
		if err != nil {
			log.Warn("digest: source fetch failed", "source", s.Name, "url", s.URL, "err", err)
			failures = append(failures, fmt.Sprintf("- %s (%s): %v", s.Name, s.URL, err))
			continue
		}
		fetched++
		sb.WriteString(SourceSection(s.Name, s.Type, s.URL, content))
	}
	if len(failures) > 0 {
		fmt.Fprintf(&sb, "## Sources that could not be fetched\n\n%s\n", strings.Join(failures, "\n"))
	}
	return sb.String(), fetched, failures
}

// SourceSection formats one source's fetched content as a Markdown section for the
// digest document. It is the single source of truth for that format, shared by the
// monolithic buildDocument (digest.build) and the scrape stage (digest.scrape),
// which persists one section per Source as an Artifact so digest.llm can assemble
// the document without re-fetching (ADR 0014).
func SourceSection(name, typ, url, content string) string {
	return fmt.Sprintf("## Source: %s (%s, %s)\n\n%s\n\n", name, typ, url, content)
}

// SubmitAssembled is the batch counterpart to BuildAssembled and the assembled-doc
// analogue of Submit: it submits an already-assembled document (produced by the
// scrape stage's Artifacts, ADR 0014) as a one-request Message Batch and returns a
// pending Result carrying the batch id. Unlike Submit it never fetches; the Fargate
// task uses it in the digest.llm DIGEST_DOC_KEY path. Returns (nil, ErrNotConfigured)
// with no key, (failed Result, err) on an empty doc or a submit error.
func (b *ContentBuilder) SubmitAssembled(ctx context.Context, date time.Time, doc string) (*Result, error) {
	day := NormalizeDate(date)
	dayStr := day.Format("2006-01-02")
	log := slog.With("date", dayStr)

	if !b.Configured() {
		log.Warn("digest: anthropic not configured; skipping")
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(doc) == "" {
		runErr := fmt.Errorf("digest: assembled document is empty; nothing to summarize")
		return &Result{Date: dayStr, Status: StatusFailed, Error: runErr.Error()}, runErr
	}

	batchID, err := b.anthropic.SubmitBatch(ctx, systemPrompt, doc, dayStr)
	if err != nil {
		return &Result{Date: dayStr, Status: StatusFailed, Error: err.Error()}, err
	}
	log.Info("digest: submitted batch (assembled)", "batch", batchID, "model", b.anthropic.model)
	return &Result{
		Date:    dayStr,
		Status:  StatusPending,
		Model:   b.anthropic.model,
		BatchID: batchID,
	}, nil
}

// BuildAssembled is the synchronous (DIGEST_MODE=local) analogue of Build for an
// already-assembled document (from scrape-stage Artifacts, ADR 0014): it summarizes
// the doc in one blocking call and returns a completed Result. It never fetches.
func (b *ContentBuilder) BuildAssembled(ctx context.Context, date time.Time, doc string) (*Result, error) {
	day := NormalizeDate(date)
	dayStr := day.Format("2006-01-02")
	log := slog.With("date", dayStr)

	if !b.Configured() {
		log.Warn("digest: anthropic not configured; skipping")
		return nil, ErrNotConfigured
	}
	if strings.TrimSpace(doc) == "" {
		runErr := fmt.Errorf("digest: assembled document is empty; nothing to summarize")
		return &Result{Date: dayStr, Status: StatusFailed, Error: runErr.Error()}, runErr
	}

	summary, err := b.anthropic.Summarize(ctx, systemPrompt, doc)
	if err != nil {
		return &Result{Date: dayStr, Status: StatusFailed, Error: err.Error()}, err
	}
	if summary.Truncated {
		log.Warn("digest: summary truncated at max_tokens")
	}
	log.Info("digest: built (assembled)",
		"model", summary.Model, "input_tokens", summary.InputTokens, "output_tokens", summary.OutputTokens)
	return &Result{
		Date:         dayStr,
		Status:       StatusCompleted,
		Content:      summary.Text,
		Model:        summary.Model,
		InputTokens:  summary.InputTokens,
		OutputTokens: summary.OutputTokens,
		Truncated:    summary.Truncated,
	}, nil
}

// Builder runs the whole digest.build inline in one process: it reads the active
// Sources from Postgres, builds the content, and persists the row. This is the
// DIGEST_MODE=local path used in dev (no AWS). In prod (DIGEST_MODE=fargate) the
// worker splits the two halves: the Fargate task runs ContentBuilder, the worker
// runs Persist. See ADR 0013.
type Builder struct {
	ent     *ent.Client
	content *ContentBuilder
}

// NewBuilder wires the inline builder.
func NewBuilder(entClient *ent.Client, anthropic *Anthropic, fetcher sourceFetcher) *Builder {
	return &Builder{ent: entClient, content: NewContentBuilder(anthropic, fetcher)}
}

// Configured reports whether a summary can be produced (Anthropic key present).
func (b *Builder) Configured() bool { return b.content.Configured() }

// Run builds and persists the Digest for date, idempotent by date. Returns
// ErrNotConfigured when the Anthropic key is absent (callers ack). A content
// failure persists a failed row (best effort, no-demote) and returns the error.
func (b *Builder) Run(ctx context.Context, date time.Time) error {
	// Detect not-configured first (before any DB work), so a blank key is a clean ack
	// regardless of whether any Sources exist.
	if !b.content.Configured() {
		slog.Warn("digest: anthropic not configured; skipping", "date", NormalizeDate(date).Format("2006-01-02"))
		return ErrNotConfigured
	}

	sources, err := ActiveSourceInputs(ctx, b.ent)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		slog.Warn("digest: no active sources; nothing to build", "date", NormalizeDate(date).Format("2006-01-02"))
		return nil
	}

	result, buildErr := b.content.Build(ctx, date, sources)
	if result != nil {
		if perr := Persist(ctx, b.ent, result); perr != nil {
			return perr
		}
	}
	return buildErr // nil on success; the content error on a persisted failure (retry)
}
