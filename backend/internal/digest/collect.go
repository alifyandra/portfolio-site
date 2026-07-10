package digest

import (
	"context"
	"fmt"
	"log/slog"

	entdigest "github.com/alifyandra/portfolio-site/backend/ent/digest"
)

// Collect drives the async half of the batch pipeline (ADR 0013, Batch API
// amendment): it finds every Digest still pending with a batch in flight, polls
// each batch, and persists the outcome. A batch still in flight is left for the
// next run; a completed batch upserts the finished Digest; a terminally failed
// batch (errored/expired/canceled/refused/404) demotes the still-pending row to
// failed and clears its batch id so it stops being polled. The daily digest.build
// remains the backstop for any day left without a completed digest.
//
// Collect is safe to run in any mode: local (synchronous) mode never creates
// pending rows, so there is nothing to drain. A blank Anthropic key is a no-op
// ack, mirroring the rest of the package. It touches the database only on the
// worker (which owns Postgres) — never in the Fargate task (ADR 0013, Shape B).
//
// Per-batch errors are transient (network, 5xx) and are logged, not returned: the
// row stays pending and the next scheduled run retries it. Collect returns an
// error only when the pending query itself fails, so the job redelivers.
func (b *Builder) Collect(ctx context.Context) error {
	if !b.content.Configured() {
		slog.Warn("digest: anthropic not configured; collect is a no-op")
		return nil
	}

	pending, err := b.ent.Digest.Query().
		Where(
			entdigest.StatusEQ(entdigest.StatusPending),
			entdigest.BatchIDNEQ(""),
		).
		All(ctx)
	if err != nil {
		return fmt.Errorf("digest: query pending digests: %w", err)
	}
	if len(pending) == 0 {
		return nil
	}

	var completed, failed, stillPending int
	for _, d := range pending {
		day := d.Date.Format("2006-01-02")
		outcome, err := b.content.anthropic.CollectBatch(ctx, d.BatchID, day)
		if err != nil {
			// Transient (network/5xx): leave the row pending, retry next run.
			slog.Error("digest: collect batch", "date", day, "batch", d.BatchID, "err", err)
			continue
		}

		switch outcome.State {
		case BatchProcessing:
			stillPending++

		case BatchSucceeded:
			// Persist upserts pending -> completed and clears batch_id.
			r := &Result{
				Date:         day,
				Status:       StatusCompleted,
				Content:      outcome.Summary.Text,
				Model:        outcome.Summary.Model,
				InputTokens:  outcome.Summary.InputTokens,
				OutputTokens: outcome.Summary.OutputTokens,
				Truncated:    outcome.Summary.Truncated,
			}
			if err := Persist(ctx, b.ent, r); err != nil {
				slog.Error("digest: persist collected digest", "date", day, "err", err)
				continue
			}
			completed++

		case BatchFailed:
			// Demote only a still-pending row (the status guard), so a race that already
			// completed the digest is never overwritten; clearing batch_id stops the poll.
			n, err := b.ent.Digest.Update().
				Where(
					entdigest.DateEQ(d.Date),
					entdigest.StatusEQ(entdigest.StatusPending),
				).
				SetStatus(entdigest.StatusFailed).
				SetError(outcome.Reason).
				SetBatchID("").
				Save(ctx)
			if err != nil {
				slog.Error("digest: mark batch failed", "date", day, "batch", d.BatchID, "err", err)
				continue
			}
			if n > 0 {
				slog.Warn("digest: batch failed", "date", day, "batch", d.BatchID, "reason", outcome.Reason)
			}
			failed++
		}
	}

	slog.Info("digest: collect swept",
		"pending_seen", len(pending), "completed", completed, "failed", failed, "still_pending", stillPending)
	return nil
}
