package digest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/alifyandra/portfolio-site/backend/ent"
	entdigest "github.com/alifyandra/portfolio-site/backend/ent/digest"
	"github.com/alifyandra/portfolio-site/backend/ent/source"
)

// Status values a Result (and the persisted Digest row) can carry.
const (
	// StatusPending: a batch has been submitted and is in flight (prod batch mode).
	// The Result carries the BatchID; digest.collect resolves it later. See ADR 0013.
	StatusPending   = "pending"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// SourceInput is a Source projected to just the fields the content build needs.
// The worker reads active Sources from Postgres and hands these to the off-box
// Fargate task, so the task never touches the database (ADR 0013, Shape B).
type SourceInput struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Type string `json:"type"`
}

// Result is what the content build produces and the worker persists. In fargate
// mode the task marshals it to JSON in S3; the worker reads it back and upserts
// the Digest row. In local mode the same struct is passed in-process.
type Result struct {
	Date         string `json:"date"`   // YYYY-MM-DD, the UTC day (idempotency key)
	Status       string `json:"status"` // StatusPending | StatusCompleted | StatusFailed
	Content      string `json:"content,omitempty"`
	Model        string `json:"model,omitempty"`
	BatchID      string `json:"batch_id,omitempty"` // the in-flight batch id when Status is pending
	Error        string `json:"error,omitempty"`
	InputTokens  int    `json:"input_tokens,omitempty"`
	OutputTokens int    `json:"output_tokens,omitempty"`
	Truncated    bool   `json:"truncated,omitempty"`
}

// ActiveSourceInputs loads the active Sources as SourceInputs, ordered stably by
// id. Shared by the worker (which hands them to the Fargate task) and local mode.
func ActiveSourceInputs(ctx context.Context, client *ent.Client) ([]SourceInput, error) {
	rows, err := client.Source.Query().
		Where(source.ActiveEQ(true)).
		Order(ent.Asc(source.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("digest: load sources: %w", err)
	}
	out := make([]SourceInput, 0, len(rows))
	for _, s := range rows {
		out = append(out, SourceInput{Name: s.Name, URL: s.URL, Type: s.Type.String()})
	}
	return out, nil
}

// Persist writes the Digest row from a Result (worker side, on-box Postgres).
//
// A completed result upserts (UpdateNewValues), so a genuine failed->completed
// re-run is self-healing. A failed result uses ON CONFLICT DO NOTHING, NOT
// UpdateNewValues: a redelivery of a date that already succeeded can carry a
// transient failure, and demoting an existing completed digest back to failed
// would destroy a good day's briefing. So a failed row is only inserted when no
// row exists for the date yet.
func Persist(ctx context.Context, client *ent.Client, r *Result) error {
	day, err := ParseDate(r.Date)
	if err != nil {
		return fmt.Errorf("digest: persist: bad date %q: %w", r.Date, err)
	}
	day = NormalizeDate(day)

	if r.Status == StatusPending {
		// Record the in-flight batch as a pending row. ON CONFLICT DO NOTHING: never
		// demote a completed digest back to pending, and on a redelivery keep the first
		// batch in flight (the second is orphaned and expires harmlessly). digest.collect
		// resolves the pending row once the batch ends. See ADR 0013 (Batch API amendment).
		if err := client.Digest.Create().
			SetDate(day).
			SetStatus(entdigest.StatusPending).
			SetModel(r.Model).
			SetBatchID(r.BatchID).
			OnConflictColumns(entdigest.FieldDate).
			DoNothing().
			Exec(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("digest: persist pending digest: %w", err)
		}
		slog.Info("digest: submitted batch", "date", r.Date, "batch", r.BatchID, "model", r.Model)
		return nil
	}

	if r.Status == StatusFailed {
		// ON CONFLICT DO NOTHING: only insert a failed row when the date has none yet,
		// never demote an existing completed digest (ADR 0013). When a row already
		// exists the RETURNING clause yields nothing, so Ent surfaces sql.ErrNoRows —
		// that is the "left it alone" success signal, not an error.
		if err := client.Digest.Create().
			SetDate(day).
			SetStatus(entdigest.StatusFailed).
			SetError(r.Error).
			OnConflictColumns(entdigest.FieldDate).
			DoNothing().
			Exec(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("digest: persist failed digest: %w", err)
		}
		slog.Warn("digest: recorded failed digest", "date", r.Date, "err", r.Error)
		return nil
	}

	if err := client.Digest.Create().
		SetDate(day).
		SetStatus(entdigest.StatusCompleted).
		SetContent(r.Content).
		SetModel(r.Model).
		SetError("").
		SetBatchID(""). // clear the in-flight pointer: the batch is resolved
		OnConflictColumns(entdigest.FieldDate).
		UpdateNewValues().
		Exec(ctx); err != nil {
		return fmt.Errorf("digest: upsert digest: %w", err)
	}
	slog.Info("digest: persisted",
		"date", r.Date, "model", r.Model,
		"input_tokens", r.InputTokens, "output_tokens", r.OutputTokens, "truncated", r.Truncated)
	return nil
}
