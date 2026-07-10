package digest

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	entdigest "github.com/alifyandra/portfolio-site/backend/ent/digest"
)

// newTestClient builds an Ent client on a fresh in-memory SQLite DB with the
// schema migrated (mirrors the auth package's test seam).
func newTestClient(t *testing.T) *ent.Client {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return client
}

type stubFetcher struct {
	content string
	err     error
}

func (s stubFetcher) Fetch(context.Context, string) (string, error) { return s.content, s.err }

// summarizeStub returns an Anthropic client whose transport answers every Messages
// call with a canned summary text.
func summarizeStub(text string) *Anthropic {
	body := `{"model":"claude-haiku-4-5","stop_reason":"end_turn","content":[{"type":"text","text":"` + text + `"}]}`
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
	})
	return NewAnthropic("key", "claude-haiku-4-5", 1024, WithHTTPClient(&http.Client{Transport: rt}))
}

// TestRun_IdempotentByDate is the core correctness check: two runs for the same
// day (a redelivery) upsert one Digest row, not two, and the second run's content
// overwrites the first (see ADR 0013).
func TestRun_IdempotentByDate(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	if _, err := client.Source.Create().SetName("A").SetURL("https://a.example").Save(ctx); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	day := time.Date(2026, 7, 9, 13, 0, 0, 0, time.UTC) // afternoon; normalized to midnight

	b1 := NewBuilder(client, summarizeStub("first summary"), stubFetcher{content: "raw"})
	if err := b1.Run(ctx, day); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// A different calendar instant on the SAME UTC day must hit the same row.
	b2 := NewBuilder(client, summarizeStub("second summary"), stubFetcher{content: "raw"})
	if err := b2.Run(ctx, day.Add(3*time.Hour)); err != nil {
		t.Fatalf("second run: %v", err)
	}

	rows, err := client.Digest.Query().All(ctx)
	if err != nil {
		t.Fatalf("query digests: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d digest rows, want 1 (idempotent by date)", len(rows))
	}
	d := rows[0]
	if d.Status != entdigest.StatusCompleted {
		t.Errorf("status = %q, want completed", d.Status)
	}
	if d.Content != "second summary" {
		t.Errorf("content = %q, want it overwritten to \"second summary\"", d.Content)
	}
	if !d.Date.Equal(NormalizeDate(day)) {
		t.Errorf("date = %s, want normalized midnight %s", d.Date, NormalizeDate(day))
	}
}

// TestRun_AllSourcesFailWritesFailed checks the failure path: when every Source
// fails to fetch, a failed Digest is recorded and the run returns an error (so it
// is retried), without ever calling the model.
func TestRun_AllSourcesFailWritesFailed(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	if _, err := client.Source.Create().SetName("A").SetURL("https://a.example").Save(ctx); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	day := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)

	b := NewBuilder(client, summarizeStub("unused"), stubFetcher{err: io.ErrUnexpectedEOF})
	if err := b.Run(ctx, day); err == nil {
		t.Fatal("Run with all sources failing = nil, want an error")
	}
	d, err := client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected one failed digest row: %v", err)
	}
	if d.Status != entdigest.StatusFailed {
		t.Errorf("status = %q, want failed", d.Status)
	}
	if d.Error == "" {
		t.Error("error field is empty, want the failure reason recorded")
	}
}

// TestRun_FailureDoesNotDemoteCompleted verifies the failure path never overwrites
// an already-completed digest for the same date: a redelivery that hits a transient
// error must leave the good briefing intact (ADR 0013 idempotency).
func TestRun_FailureDoesNotDemoteCompleted(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	if _, err := client.Source.Create().SetName("A").SetURL("https://a.example").Save(ctx); err != nil {
		t.Fatalf("seed source: %v", err)
	}
	day := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)

	// First run succeeds -> completed digest with content.
	ok := NewBuilder(client, summarizeStub("good briefing"), stubFetcher{content: "raw"})
	if err := ok.Run(ctx, day); err != nil {
		t.Fatalf("success run: %v", err)
	}

	// A redelivery on the same UTC day now fails (all sources fail to fetch).
	bad := NewBuilder(client, summarizeStub("unused"), stubFetcher{err: io.ErrUnexpectedEOF})
	if err := bad.Run(ctx, day.Add(2*time.Hour)); err == nil {
		t.Fatal("failure run = nil, want an error")
	}

	d, err := client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected exactly one digest row: %v", err)
	}
	if d.Status != entdigest.StatusCompleted {
		t.Errorf("status = %q, want completed (a transient failure must not demote a good digest)", d.Status)
	}
	if d.Content != "good briefing" {
		t.Errorf("content = %q, want the completed briefing preserved", d.Content)
	}
}

// TestRun_NoActiveSources is a clean no-op ack: nothing to build, no row, no error.
func TestRun_NoActiveSources(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	b := NewBuilder(client, summarizeStub("unused"), stubFetcher{content: "raw"})
	if err := b.Run(ctx, time.Now()); err != nil {
		t.Fatalf("Run with no sources = %v, want nil", err)
	}
	n, err := client.Digest.Query().Count(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("digest rows = %d, want 0", n)
	}
}

// TestRun_NotConfigured returns ErrNotConfigured so the caller acks.
func TestRun_NotConfigured(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	b := NewBuilder(client, NewAnthropic("", "m", 10), stubFetcher{content: "raw"})
	if err := b.Run(ctx, time.Now()); err != ErrNotConfigured {
		t.Fatalf("Run without a key = %v, want ErrNotConfigured", err)
	}
}

// --- Batch API (ADR 0013 amendment): Submit + Collect ------------------------

// seedPending inserts a pending Digest row carrying an in-flight batch id, as the
// submit phase would (via Persist) before collect resolves it.
func seedPending(t *testing.T, client *ent.Client, day time.Time, batchID string) {
	t.Helper()
	if _, err := client.Digest.Create().
		SetDate(NormalizeDate(day)).
		SetStatus(entdigest.StatusPending).
		SetModel("claude-haiku-4-5").
		SetBatchID(batchID).
		Save(context.Background()); err != nil {
		t.Fatalf("seed pending digest: %v", err)
	}
}

// TestSubmit_Pending: the batch submit path returns a pending Result carrying the
// batch id, with no DB access (off-box half, Shape B).
func TestSubmit_Pending(t *testing.T) {
	ctx := context.Background()
	b := NewContentBuilder(submitStub("msgbatch_abc"), stubFetcher{content: "raw"})
	day := time.Date(2026, 7, 10, 13, 0, 0, 0, time.UTC)

	r, err := b.Submit(ctx, day, testSources)
	if err != nil {
		t.Fatalf("Submit = %v, want nil", err)
	}
	if r == nil || r.Status != StatusPending {
		t.Fatalf("result = %+v, want a pending Result", r)
	}
	if r.BatchID != "msgbatch_abc" {
		t.Errorf("batch id = %q, want msgbatch_abc", r.BatchID)
	}
	if r.Date != "2026-07-10" {
		t.Errorf("date = %q, want normalized 2026-07-10", r.Date)
	}
	if r.Model == "" {
		t.Error("model is empty, want it recorded on the pending row")
	}
}

// TestSubmit_AllFail: every fetch fails -> a failed Result AND an error, without
// submitting a batch (mirrors Build's all-fail contract).
func TestSubmit_AllFail(t *testing.T) {
	ctx := context.Background()
	b := NewContentBuilder(submitStub("unused"), stubFetcher{err: io.ErrUnexpectedEOF})

	r, err := b.Submit(ctx, time.Now(), testSources)
	if err == nil {
		t.Fatal("Submit with all fetches failing = nil error, want an error")
	}
	if r == nil || r.Status != StatusFailed {
		t.Fatalf("result = %+v, want a failed Result", r)
	}
}

// TestCollect_CompletesPending: a pending row whose batch has ended and succeeded
// is upserted to completed, with the content written and batch_id cleared.
func TestCollect_CompletesPending(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	day := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	seedPending(t, client, day, "msgbatch_1")

	b := NewBuilder(client, collectStub("msgbatch_1", "2026-07-10", "ended", "succeeded", "final briefing"), stubFetcher{})
	if err := b.Collect(ctx); err != nil {
		t.Fatalf("Collect = %v, want nil", err)
	}

	d, err := client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected one digest row: %v", err)
	}
	if d.Status != entdigest.StatusCompleted {
		t.Errorf("status = %q, want completed", d.Status)
	}
	if d.Content != "final briefing" {
		t.Errorf("content = %q, want the collected briefing", d.Content)
	}
	if d.BatchID != "" {
		t.Errorf("batch_id = %q, want it cleared once resolved", d.BatchID)
	}
}

// TestCollect_FailsPending: a pending row whose batch ended in a terminal failure
// is demoted to failed with the reason recorded and batch_id cleared.
func TestCollect_FailsPending(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	day := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	seedPending(t, client, day, "msgbatch_2")

	b := NewBuilder(client, collectStub("msgbatch_2", "2026-07-10", "ended", "expired", ""), stubFetcher{})
	if err := b.Collect(ctx); err != nil {
		t.Fatalf("Collect = %v, want nil", err)
	}

	d, err := client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected one digest row: %v", err)
	}
	if d.Status != entdigest.StatusFailed {
		t.Errorf("status = %q, want failed", d.Status)
	}
	if d.Error == "" {
		t.Error("error is empty, want the failure reason recorded")
	}
	if d.BatchID != "" {
		t.Errorf("batch_id = %q, want it cleared so polling stops", d.BatchID)
	}
}

// TestCollect_LeavesProcessing: a batch still in flight leaves the row pending and
// its batch id intact, to be retried on the next sweep.
func TestCollect_LeavesProcessing(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	day := time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)
	seedPending(t, client, day, "msgbatch_3")

	b := NewBuilder(client, collectStub("msgbatch_3", "2026-07-10", "in_progress", "", ""), stubFetcher{})
	if err := b.Collect(ctx); err != nil {
		t.Fatalf("Collect = %v, want nil", err)
	}

	d, err := client.Digest.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected one digest row: %v", err)
	}
	if d.Status != entdigest.StatusPending {
		t.Errorf("status = %q, want it left pending", d.Status)
	}
	if d.BatchID != "msgbatch_3" {
		t.Errorf("batch_id = %q, want it preserved for the next sweep", d.BatchID)
	}
}

// TestCollect_NoPendingIsNoOp: with nothing pending, Collect is a clean no-op.
func TestCollect_NoPendingIsNoOp(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	b := NewBuilder(client, collectStub("x", "x", "ended", "succeeded", "x"), stubFetcher{})
	if err := b.Collect(ctx); err != nil {
		t.Fatalf("Collect with nothing pending = %v, want nil", err)
	}
}

// TestCollect_NotConfigured: a blank key makes Collect a no-op ack.
func TestCollect_NotConfigured(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedPending(t, client, time.Now(), "msgbatch_x")
	b := NewBuilder(client, NewAnthropic("", "m", 10), stubFetcher{})
	if err := b.Collect(ctx); err != nil {
		t.Fatalf("Collect without a key = %v, want nil", err)
	}
}
