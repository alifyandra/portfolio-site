package jobs

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
)

// newTestClient builds an Ent client on a fresh in-memory SQLite DB with the
// schema migrated (mirrors the digest package's test seam).
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

// stubQueue records enqueues and simulates the queue's configured/unconfigured
// states, so a tick's side effects are observable and controllable.
type stubQueue struct {
	configured bool
	fail       bool
	enqueued   []queue.Job
}

func (s *stubQueue) Configured() bool { return s.configured }

func (s *stubQueue) Enqueue(_ context.Context, job queue.Job) error {
	if s.fail {
		return errors.New("enqueue boom")
	}
	s.enqueued = append(s.enqueued, job)
	return nil
}

// seedJob inserts an enabled daily-midnight ScheduledJob. When nextRunAt is
// non-nil it is set, so the job's due-ness is fully controlled by the test.
func seedJob(t *testing.T, client *ent.Client, key string, enabled bool, nextRunAt *time.Time) *ent.ScheduledJob {
	t.Helper()
	c := client.ScheduledJob.Create().
		SetKey(key).
		SetName(key).
		SetStage(scheduledjob.StageScrape).
		SetEnabled(enabled).
		SetSchedule("0 0 * * *"). // daily at midnight (valid standard cron)
		SetTimezone("UTC")
	if nextRunAt != nil {
		c.SetNextRunAt(*nextRunAt)
	}
	job, err := c.Save(context.Background())
	if err != nil {
		t.Fatalf("seed job %q: %v", key, err)
	}
	return job
}

func countRuns(t *testing.T, client *ent.Client) int {
	t.Helper()
	n, err := client.JobRun.Query().Count(context.Background())
	if err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return n
}

// TestTick_DormantNoEnabledJobs is the dormant guarantee: with zero enabled jobs
// a tick enqueues nothing and creates no JobRun.
func TestTick_DormantNoEnabledJobs(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	// A DISABLED, already-due job must stay inert.
	past := time.Now().Add(-time.Hour)
	seedJob(t, client, "digest.scrape", false, &past)

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil)
	s.tick(ctx, time.Now())

	if len(q.enqueued) != 0 {
		t.Errorf("enqueued %d jobs, want 0 (dormant)", len(q.enqueued))
	}
	if n := countRuns(t, client); n != 0 {
		t.Errorf("created %d JobRuns, want 0 (dormant)", n)
	}
}

// TestTick_NotConfiguredIsNoOp: an unconfigured queue makes tick a no-op even
// with an enabled, due job.
func TestTick_NotConfiguredIsNoOp(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	past := time.Now().Add(-time.Hour)
	seedJob(t, client, "digest.scrape", true, &past)

	q := &stubQueue{configured: false}
	s := NewScheduler(client, q, time.Minute, nil)
	s.tick(ctx, time.Now())

	if len(q.enqueued) != 0 {
		t.Errorf("enqueued %d jobs, want 0 (queue not configured)", len(q.enqueued))
	}
	if n := countRuns(t, client); n != 0 {
		t.Errorf("created %d JobRuns, want 0 (queue not configured)", n)
	}
}

// TestTick_DueJobFiresOnce: one enabled job whose next_run_at is in the past
// produces exactly one queued JobRun with scheduled_for == the canonical tick,
// advances next_run_at, and enqueues exactly one message carrying the run id.
func TestTick_DueJobFiresOnce(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	past := time.Now().Add(-time.Hour)
	job := seedJob(t, client, "digest.scrape", true, &past)

	// The canonical tick is the stored next_run_at, as read back from the DB.
	seeded, err := client.ScheduledJob.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	canonical := *seeded.NextRunAt

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil)
	s.tick(ctx, time.Now())

	// Exactly one run.
	run, err := client.JobRun.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected exactly one JobRun: %v", err)
	}
	if run.Status != jobrun.StatusQueued {
		t.Errorf("run status = %q, want queued", run.Status)
	}
	if run.Trigger != jobrun.TriggerSchedule {
		t.Errorf("run trigger = %q, want schedule", run.Trigger)
	}
	if !run.ScheduledFor.Equal(canonical) {
		t.Errorf("scheduled_for = %s, want the canonical tick %s (not time.Now())", run.ScheduledFor, canonical)
	}

	// next_run_at advanced past the fired tick.
	after, err := client.ScheduledJob.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if after.NextRunAt == nil || !after.NextRunAt.After(canonical) {
		t.Errorf("next_run_at = %v, want advanced past %s", after.NextRunAt, canonical)
	}
	if after.LastRunAt == nil {
		t.Error("last_run_at is nil, want it set to the fire time")
	}

	// Exactly one enqueue carrying the run id and the job key.
	if len(q.enqueued) != 1 {
		t.Fatalf("enqueued %d jobs, want 1", len(q.enqueued))
	}
	if got := q.enqueued[0]; got.Type != "digest.scrape" || got.JobRunID != run.ID {
		t.Errorf("enqueued %+v, want type=digest.scrape JobRunID=%d", got, run.ID)
	}
}

// TestTick_DedupOnDoubleTick: re-firing the SAME (job, scheduled_for) tick is a
// unique-index conflict, so no second JobRun is created and nothing is enqueued
// again. The pointer still advances.
func TestTick_DedupOnDoubleTick(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	past := time.Now().Add(-time.Hour)
	job := seedJob(t, client, "digest.scrape", true, &past)

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil)

	// First tick fires.
	s.tick(ctx, time.Now())
	run, err := client.JobRun.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected one JobRun after first tick: %v", err)
	}

	// Force the job due again for the EXACT same canonical tick (its scheduled_for),
	// so the second insert collides on the unique (job, scheduled_for) index.
	if err := client.ScheduledJob.UpdateOneID(job.ID).SetNextRunAt(run.ScheduledFor).Exec(ctx); err != nil {
		t.Fatalf("reset next_run_at: %v", err)
	}

	s.tick(ctx, time.Now())

	if n := countRuns(t, client); n != 1 {
		t.Errorf("JobRun count = %d, want 1 (dedup collapses the double tick)", n)
	}
	if len(q.enqueued) != 1 {
		t.Errorf("enqueued %d jobs, want 1 (no double-enqueue on conflict)", len(q.enqueued))
	}
}

// TestTick_NewlyEnabledJobInitializesOnly: a job with a null next_run_at gets it
// initialized on the first tick and does NOT fire that tick.
func TestTick_NewlyEnabledJobInitializesOnly(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	job := seedJob(t, client, "digest.scrape", true, nil) // no next_run_at

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil)
	now := time.Now()
	s.tick(ctx, now)

	if len(q.enqueued) != 0 {
		t.Errorf("enqueued %d jobs, want 0 (first tick only initializes)", len(q.enqueued))
	}
	if n := countRuns(t, client); n != 0 {
		t.Errorf("created %d JobRuns, want 0 (first tick only initializes)", n)
	}
	after, err := client.ScheduledJob.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if after.NextRunAt == nil {
		t.Fatal("next_run_at is nil, want it initialized")
	}
	if !after.NextRunAt.After(now) {
		t.Errorf("next_run_at = %s, want a future tick after %s", after.NextRunAt, now)
	}
}

// TestTick_BadCronSkipsJobKeepsGoing: a job with an invalid cron string is logged
// and skipped without killing the tick or starving a healthy job beside it.
func TestTick_BadCronSkipsJobKeepsGoing(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	past := time.Now().Add(-time.Hour)

	// Bad-cron job.
	if _, err := client.ScheduledJob.Create().
		SetKey("bad.job").
		SetName("bad").
		SetStage(scheduledjob.StageScrape).
		SetEnabled(true).
		SetSchedule("not a cron").
		SetTimezone("UTC").
		SetNextRunAt(past).
		Save(ctx); err != nil {
		t.Fatalf("seed bad job: %v", err)
	}
	// Healthy job beside it.
	seedJob(t, client, "digest.scrape", true, &past)

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil)
	s.tick(ctx, time.Now())

	if len(q.enqueued) != 1 {
		t.Fatalf("enqueued %d jobs, want 1 (healthy job fires despite the bad one)", len(q.enqueued))
	}
	if q.enqueued[0].Type != "digest.scrape" {
		t.Errorf("enqueued type = %q, want digest.scrape", q.enqueued[0].Type)
	}
}

// TestValidateCron accepts a valid standard expression and rejects junk.
func TestValidateCron(t *testing.T) {
	if err := ValidateCron("0 0 * * *"); err != nil {
		t.Errorf("ValidateCron(valid) = %v, want nil", err)
	}
	if err := ValidateCron("nope"); err == nil {
		t.Error("ValidateCron(invalid) = nil, want an error")
	}
}
