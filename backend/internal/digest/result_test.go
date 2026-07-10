package digest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	entdigest "github.com/alifyandra/portfolio-site/backend/ent/digest"
)

var testSources = []SourceInput{{Name: "A", URL: "https://a.example", Type: "web"}}

// TestContentBuild_Completed checks the off-box half (ADR 0013 Shape B): Build
// returns a completed Result carrying the summary, with no error and no DB access.
func TestContentBuild_Completed(t *testing.T) {
	ctx := context.Background()
	b := NewContentBuilder(summarizeStub("the briefing"), stubFetcher{content: "raw"})
	day := time.Date(2026, 7, 9, 13, 0, 0, 0, time.UTC)

	r, err := b.Build(ctx, day, testSources)
	if err != nil {
		t.Fatalf("Build = %v, want nil", err)
	}
	if r == nil {
		t.Fatal("Build returned nil result")
	}
	if r.Status != StatusCompleted {
		t.Errorf("status = %q, want completed", r.Status)
	}
	if r.Content != "the briefing" {
		t.Errorf("content = %q, want the summary", r.Content)
	}
	if r.Date != "2026-07-09" {
		t.Errorf("date = %q, want normalized 2026-07-09", r.Date)
	}
	if r.Model == "" {
		t.Error("model is empty, want it recorded")
	}
}

// TestContentBuild_AllFail: every fetch fails -> a failed Result AND a non-nil
// error (so the task exits non-zero), without calling the model.
func TestContentBuild_AllFail(t *testing.T) {
	ctx := context.Background()
	b := NewContentBuilder(summarizeStub("unused"), stubFetcher{err: io.ErrUnexpectedEOF})

	r, err := b.Build(ctx, time.Now(), testSources)
	if err == nil {
		t.Fatal("Build with all fetches failing = nil error, want an error")
	}
	if r == nil || r.Status != StatusFailed {
		t.Fatalf("result = %+v, want a failed Result", r)
	}
	if r.Error == "" {
		t.Error("result.Error is empty, want the failure reason")
	}
}

// TestContentBuild_NotConfigured: no key -> (nil, ErrNotConfigured), so the task
// exits 0 without writing a result and the worker acks.
func TestContentBuild_NotConfigured(t *testing.T) {
	ctx := context.Background()
	b := NewContentBuilder(NewAnthropic("", "m", 10), stubFetcher{content: "raw"})

	r, err := b.Build(ctx, time.Now(), testSources)
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Build without a key = %v, want ErrNotConfigured", err)
	}
	if r != nil {
		t.Errorf("result = %+v, want nil when not configured", r)
	}
}

// TestPersist_RoundTrip exercises the worker half: a Result that has been through
// JSON (as it is via S3) persists to a completed row, and a later failed Result for
// the same date does not demote it (no-demote guarantee, ADR 0013).
func TestPersist_RoundTrip(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	completed := mustRoundTrip(t, &Result{
		Date: "2026-07-09", Status: StatusCompleted, Content: "briefing", Model: "claude-haiku-4-5",
	})
	if err := Persist(ctx, client, completed); err != nil {
		t.Fatalf("persist completed: %v", err)
	}

	// A redelivery for the same day that failed must not overwrite the good row.
	failed := mustRoundTrip(t, &Result{Date: "2026-07-09", Status: StatusFailed, Error: "transient"})
	if err := Persist(ctx, client, failed); err != nil {
		t.Fatalf("persist failed: %v", err)
	}

	d, err := client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected exactly one digest row: %v", err)
	}
	if d.Status != entdigest.StatusCompleted {
		t.Errorf("status = %q, want completed (failed re-run must not demote)", d.Status)
	}
	if d.Content != "briefing" {
		t.Errorf("content = %q, want the completed briefing preserved", d.Content)
	}
}

// TestPersist_Pending: the batch submit phase persists a pending row carrying the
// batch id; a later completed Result for the same date upserts to completed and
// clears batch_id (ADR 0013, Batch API amendment).
func TestPersist_Pending(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	pending := mustRoundTrip(t, &Result{
		Date: "2026-07-10", Status: StatusPending, Model: "claude-haiku-4-5", BatchID: "msgbatch_9",
	})
	if err := Persist(ctx, client, pending); err != nil {
		t.Fatalf("persist pending: %v", err)
	}
	d, err := client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected one digest row: %v", err)
	}
	if d.Status != entdigest.StatusPending {
		t.Errorf("status = %q, want pending", d.Status)
	}
	if d.BatchID != "msgbatch_9" {
		t.Errorf("batch_id = %q, want msgbatch_9", d.BatchID)
	}

	// The collect phase upserts the same date to completed.
	completed := mustRoundTrip(t, &Result{Date: "2026-07-10", Status: StatusCompleted, Content: "briefing", Model: "claude-haiku-4-5"})
	if err := Persist(ctx, client, completed); err != nil {
		t.Fatalf("persist completed: %v", err)
	}
	d, err = client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected exactly one digest row after completion: %v", err)
	}
	if d.Status != entdigest.StatusCompleted {
		t.Errorf("status = %q, want completed", d.Status)
	}
	if d.Content != "briefing" {
		t.Errorf("content = %q, want the completed briefing", d.Content)
	}
	if d.BatchID != "" {
		t.Errorf("batch_id = %q, want it cleared once completed", d.BatchID)
	}
}

// TestPersist_PendingDoesNotDemoteCompleted: a pending re-submit for a date that
// already completed (a redelivery) must not overwrite the good digest.
func TestPersist_PendingDoesNotDemoteCompleted(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)

	completed := mustRoundTrip(t, &Result{Date: "2026-07-10", Status: StatusCompleted, Content: "good briefing", Model: "m"})
	if err := Persist(ctx, client, completed); err != nil {
		t.Fatalf("persist completed: %v", err)
	}
	pending := mustRoundTrip(t, &Result{Date: "2026-07-10", Status: StatusPending, Model: "m", BatchID: "msgbatch_late"})
	if err := Persist(ctx, client, pending); err != nil {
		t.Fatalf("persist pending: %v", err)
	}

	d, err := client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected exactly one digest row: %v", err)
	}
	if d.Status != entdigest.StatusCompleted {
		t.Errorf("status = %q, want completed (a late pending must not demote a good digest)", d.Status)
	}
	if d.Content != "good briefing" {
		t.Errorf("content = %q, want the completed briefing preserved", d.Content)
	}
}

// mustRoundTrip marshals and unmarshals a Result, simulating the S3 hop between the
// task and the worker, so tests exercise the same bytes that cross the wire.
func mustRoundTrip(t *testing.T, r *Result) *Result {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out Result
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return &out
}
