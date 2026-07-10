package digest

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	entartifact "github.com/alifyandra/portfolio-site/backend/ent/artifact"
	entdigestpkg "github.com/alifyandra/portfolio-site/backend/ent/digest"
	entjobrun "github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	entscheduledjob "github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
)

// mapFetcher answers per-URL so a test can make one source fail while others succeed.
type mapFetcher map[string]struct {
	content string
	err     error
}

func (m mapFetcher) Fetch(_ context.Context, url string) (string, error) {
	r := m[url]
	return r.content, r.err
}

// seedScrapeJobRun creates a ScheduledJob of the given stage plus one queued JobRun
// for it, returning the run id (what the worker passes the stage handlers).
func seedScrapeJobRun(t *testing.T, client *ent.Client, key string, stage entscheduledjob.Stage) int {
	t.Helper()
	ctx := context.Background()
	job := client.ScheduledJob.Create().
		SetKey(key).SetName(key).SetStage(stage).SetSchedule("0 0 * * *").
		SaveX(ctx)
	run := client.JobRun.Create().
		SetTrigger(entjobrun.TriggerSchedule).
		SetScheduledFor(time.Now()).
		SetJobID(job.ID).
		SaveX(ctx)
	return run.ID
}

func seedSource(t *testing.T, client *ent.Client, name, url string) {
	t.Helper()
	client.Source.Create().SetName(name).SetURL(url).SetType("web").SetActive(true).SaveX(context.Background())
}

func TestScrape_WritesOneArtifactPerSource(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedSource(t, client, "HN", "https://hn.example/rss")
	seedSource(t, client, "BBC", "https://bbc.example/rss")
	runID := seedScrapeJobRun(t, client, "digest.scrape", entscheduledjob.StageScrape)

	b := NewBuilder(client, summarizeStub("unused"), stubFetcher{content: "raw body"})
	if err := b.Scrape(ctx, nil, runID, time.Now()); err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	arts := client.Artifact.Query().Order(ent.Asc(entartifact.FieldID)).AllX(ctx)
	if len(arts) != 2 {
		t.Fatalf("want 2 artifacts, got %d", len(arts))
	}
	for _, a := range arts {
		if a.Status != entartifact.StatusPending {
			t.Errorf("artifact %s status = %s, want pending", a.Label, a.Status)
		}
		if a.Storage != entartifact.StorageInline {
			t.Errorf("artifact %s storage = %s, want inline", a.Label, a.Storage)
		}
		if a.ExpiresAt == nil || !a.ExpiresAt.After(time.Now()) {
			t.Errorf("artifact %s expires_at not in the future: %v", a.Label, a.ExpiresAt)
		}
		if run := a.QueryProducedBy().OnlyIDX(ctx); run != runID {
			t.Errorf("artifact %s produced_by = %d, want %d", a.Label, run, runID)
		}
	}
	if arts[0].Label != "source:HN" {
		t.Errorf("first artifact label = %q, want source:HN", arts[0].Label)
	}
}

func TestScrape_OneSourceFailsOthersSucceed(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedSource(t, client, "Good", "https://good.example")
	seedSource(t, client, "Bad", "https://bad.example")
	runID := seedScrapeJobRun(t, client, "digest.scrape", entscheduledjob.StageScrape)

	fetch := mapFetcher{
		"https://good.example": {content: "ok"},
		"https://bad.example":  {err: io.ErrUnexpectedEOF},
	}
	b := NewBuilder(client, summarizeStub("unused"), fetch)
	if err := b.Scrape(ctx, nil, runID, time.Now()); err != nil {
		t.Fatalf("Scrape should succeed when at least one source fetches: %v", err)
	}
	if n := client.Artifact.Query().CountX(ctx); n != 1 {
		t.Fatalf("want 1 artifact (the good source), got %d", n)
	}
}

func TestScrape_AllSourcesFailErrors(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedSource(t, client, "Bad", "https://bad.example")
	runID := seedScrapeJobRun(t, client, "digest.scrape", entscheduledjob.StageScrape)

	b := NewBuilder(client, summarizeStub("unused"), stubFetcher{err: io.ErrUnexpectedEOF})
	if err := b.Scrape(ctx, nil, runID, time.Now()); err == nil {
		t.Fatal("Scrape should error when every source fails")
	}
	if n := client.Artifact.Query().CountX(ctx); n != 0 {
		t.Fatalf("want 0 artifacts, got %d", n)
	}
}

// TestScrape_RetryDoesNotDuplicate: re-running scrape for the SAME run (e.g. a
// re-driven stranded run) clears the run's prior pending artifacts first, so the
// artifact count stays at one-per-source instead of doubling.
func TestScrape_RetryDoesNotDuplicate(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedSource(t, client, "HN", "https://hn.example")
	seedSource(t, client, "BBC", "https://bbc.example")
	runID := seedScrapeJobRun(t, client, "digest.scrape", entscheduledjob.StageScrape)

	b := NewBuilder(client, summarizeStub("unused"), stubFetcher{content: "body"})
	if err := b.Scrape(ctx, nil, runID, time.Now()); err != nil {
		t.Fatalf("first scrape: %v", err)
	}
	if err := b.Scrape(ctx, nil, runID, time.Now()); err != nil {
		t.Fatalf("second scrape (retry): %v", err)
	}

	if n := client.Artifact.Query().CountX(ctx); n != 2 {
		t.Fatalf("want 2 artifacts after a retry (idempotent per run+source), got %d", n)
	}
}

// TestScrape_WriteFailureRollsBackNoPartial: when one artifact write fails mid-loop
// (an over-inline-limit payload with no object store), the whole scrape rolls back —
// no partial set of artifacts is committed — and the run is failed for a retry.
func TestScrape_WriteFailureRollsBackNoPartial(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedSource(t, client, "Small", "https://small.example")
	seedSource(t, client, "Huge", "https://huge.example")
	runID := seedScrapeJobRun(t, client, "digest.scrape", entscheduledjob.StageScrape)

	fetch := mapFetcher{
		"https://small.example": {content: "tiny"},
		"https://huge.example":  {content: strings.Repeat("x", ArtifactInlineMaxBytes+1)},
	}
	// store is nil, so the over-limit "Huge" section cannot be written and its write
	// fails, which must roll back the "Small" write committed just before it.
	b := NewBuilder(client, summarizeStub("unused"), fetch)
	if err := b.Scrape(ctx, nil, runID, time.Now()); err == nil {
		t.Fatal("Scrape should fail when an artifact write fails")
	}
	if n := client.Artifact.Query().CountX(ctx); n != 0 {
		t.Fatalf("want 0 artifacts (write failure rolled back the whole scrape), got %d", n)
	}
}

// TestAssembleFromArtifacts_ScopesToNewestScrapeRun: two scrape runs leave two sets of
// pending source artifacts (the first run's llm never consumed them). Assembly must
// pick ONLY the newest run's artifacts, never folding the older day into the digest.
func TestAssembleFromArtifacts_ScopesToNewestScrapeRun(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedSource(t, client, "HN", "https://hn.example")

	run1 := seedScrapeJobRun(t, client, "digest.scrape.a", entscheduledjob.StageScrape)
	b1 := NewBuilder(client, summarizeStub("unused"), stubFetcher{content: "day one body"})
	if err := b1.Scrape(ctx, nil, run1, time.Now()); err != nil {
		t.Fatalf("scrape run1: %v", err)
	}
	run2 := seedScrapeJobRun(t, client, "digest.scrape.b", entscheduledjob.StageScrape)
	b2 := NewBuilder(client, summarizeStub("unused"), stubFetcher{content: "day two body"})
	if err := b2.Scrape(ctx, nil, run2, time.Now()); err != nil {
		t.Fatalf("scrape run2: %v", err)
	}

	// Two pending artifacts now exist (one per run); assembly must scope to run2 only.
	if n := client.Artifact.Query().Where(entartifact.StatusEQ(entartifact.StatusPending)).CountX(ctx); n != 2 {
		t.Fatalf("want 2 pending artifacts across both runs, got %d", n)
	}

	doc, ids, err := b2.AssembleFromArtifacts(ctx, nil)
	if err != nil {
		t.Fatalf("AssembleFromArtifacts: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("want 1 assembled artifact (newest run only), got %d", len(ids))
	}
	if !strings.Contains(doc, "day two body") {
		t.Errorf("assembled doc missing the newest run's content: %q", doc)
	}
	if strings.Contains(doc, "day one body") {
		t.Errorf("assembled doc folded in the older run's content: %q", doc)
	}
	if run := client.Artifact.GetX(ctx, ids[0]).QueryProducedBy().OnlyIDX(ctx); run != run2 {
		t.Errorf("assembled artifact produced_by = %d, want the newest run %d", run, run2)
	}
}

func TestLlmLocal_AssemblesPersistsAndConsumes(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	seedSource(t, client, "HN", "https://hn.example")
	scrapeRun := seedScrapeJobRun(t, client, "digest.scrape", entscheduledjob.StageScrape)
	llmRun := seedScrapeJobRun(t, client, "digest.llm", entscheduledjob.StageLlm)

	// scrape first so pending artifacts exist
	scraper := NewBuilder(client, summarizeStub("unused"), stubFetcher{content: "headline body"})
	if err := scraper.Scrape(ctx, nil, scrapeRun, time.Now()); err != nil {
		t.Fatalf("Scrape: %v", err)
	}

	date := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	llm := NewBuilder(client, summarizeStub("the briefing"), stubFetcher{content: "unused"})
	if err := llm.LlmLocal(ctx, nil, llmRun, date); err != nil {
		t.Fatalf("LlmLocal: %v", err)
	}

	d := client.Digest.Query().Where(entdigestpkg.DateEQ(NormalizeDate(date))).OnlyX(ctx)
	if d.Status != entdigestpkg.StatusCompleted {
		t.Errorf("digest status = %s, want completed", d.Status)
	}
	if d.Content != "the briefing" {
		t.Errorf("digest content = %q, want %q", d.Content, "the briefing")
	}
	arts := client.Artifact.Query().AllX(ctx)
	for _, a := range arts {
		if a.Status != entartifact.StatusDone {
			t.Errorf("artifact %s status = %s, want done", a.Label, a.Status)
		}
		if run := a.QueryConsumedBy().OnlyIDX(ctx); run != llmRun {
			t.Errorf("artifact %s consumed_by = %d, want %d", a.Label, run, llmRun)
		}
	}
}

func TestLlmLocal_NoPendingIsNoOp(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	llmRun := seedScrapeJobRun(t, client, "digest.llm", entscheduledjob.StageLlm)

	llm := NewBuilder(client, summarizeStub("unused"), stubFetcher{})
	if err := llm.LlmLocal(ctx, nil, llmRun, time.Now()); err != nil {
		t.Fatalf("LlmLocal with no pending artifacts should be a clean no-op: %v", err)
	}
	if n := client.Digest.Query().CountX(ctx); n != 0 {
		t.Fatalf("want 0 digests, got %d", n)
	}
}

func TestSubmitAssembled_ReturnsPending(t *testing.T) {
	cb := &ContentBuilder{anthropic: submitStub("msgbatch_abc"), fetcher: stubFetcher{}}
	got, err := cb.SubmitAssembled(context.Background(), time.Now(), "doc")
	if err != nil {
		t.Fatalf("SubmitAssembled: %v", err)
	}
	if got.Status != StatusPending || got.BatchID != "msgbatch_abc" {
		t.Fatalf("got status=%s batch=%s, want pending/msgbatch_abc", got.Status, got.BatchID)
	}
}

func TestSubmitAssembled_EmptyDocFails(t *testing.T) {
	cb := &ContentBuilder{anthropic: submitStub("x"), fetcher: stubFetcher{}}
	_, err := cb.SubmitAssembled(context.Background(), time.Now(), "   ")
	if err == nil {
		t.Fatal("SubmitAssembled should fail on an empty doc")
	}
}

func TestBuildAssembled_Completed(t *testing.T) {
	cb := &ContentBuilder{anthropic: summarizeStub("briefing text"), fetcher: stubFetcher{}}
	got, err := cb.BuildAssembled(context.Background(), time.Now(), "## Source: X\n\nbody")
	if err != nil {
		t.Fatalf("BuildAssembled: %v", err)
	}
	if got.Status != StatusCompleted || got.Content != "briefing text" {
		t.Fatalf("got status=%s content=%q, want completed/briefing text", got.Status, got.Content)
	}
}

func TestBuildAssembled_NotConfigured(t *testing.T) {
	cb := &ContentBuilder{anthropic: NewAnthropic("", "m", 10), fetcher: stubFetcher{}}
	_, err := cb.BuildAssembled(context.Background(), time.Now(), "doc")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("want ErrNotConfigured, got %v", err)
	}
}
