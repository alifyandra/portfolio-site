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
