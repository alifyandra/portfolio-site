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
	"github.com/alifyandra/portfolio-site/backend/ent/artifact"
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

// stubNotifier records refresh notifications so the ack-gated fire path is
// observable (and can be told to fail, to prove a notification error is non-fatal).
type stubNotifier struct {
	fail  bool
	calls []int // run ids notified
}

func (n *stubNotifier) NotifyRefresh(_ context.Context, runID int, _ string) error {
	n.calls = append(n.calls, runID)
	if n.fail {
		return errors.New("notify boom")
	}
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
	s := NewScheduler(client, q, time.Minute, nil, nil)
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
	s := NewScheduler(client, q, time.Minute, nil, nil)
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
	s := NewScheduler(client, q, time.Minute, nil, nil)
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
	s := NewScheduler(client, q, time.Minute, nil, nil)

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
	s := NewScheduler(client, q, time.Minute, nil, nil)
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
	s := NewScheduler(client, q, time.Minute, nil, nil)
	s.tick(ctx, time.Now())

	if len(q.enqueued) != 1 {
		t.Fatalf("enqueued %d jobs, want 1 (healthy job fires despite the bad one)", len(q.enqueued))
	}
	if q.enqueued[0].Type != "digest.scrape" {
		t.Errorf("enqueued type = %q, want digest.scrape", q.enqueued[0].Type)
	}
}

// TestTick_RedrivesStrandedQueuedRun: a scheduler-driven run left `queued` past the
// re-drive threshold (its post-commit enqueue was lost) is re-enqueued on the tick.
// The job itself is not due (next_run_at in the future), so the only enqueue is the
// re-drive.
func TestTick_RedrivesStrandedQueuedRun(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	future := time.Now().Add(time.Hour)
	job := seedJob(t, client, "digest.scrape", true, &future) // enabled but not due

	// A run stranded queued long enough to cross queuedRedriveAfter.
	stale := time.Now().Add(-queuedRedriveAfter - time.Minute)
	run := client.JobRun.Create().
		SetStatus(jobrun.StatusQueued).
		SetTrigger(jobrun.TriggerSchedule).
		SetScheduledFor(time.Now()).
		SetCreatedAt(stale).
		SetJobID(job.ID).
		SaveX(ctx)

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil, nil)
	s.tick(ctx, time.Now())

	if len(q.enqueued) != 1 {
		t.Fatalf("enqueued %d jobs, want 1 (the re-drive)", len(q.enqueued))
	}
	if got := q.enqueued[0]; got.JobRunID != run.ID || got.Type != "digest.scrape" {
		t.Errorf("re-drove %+v, want type=digest.scrape JobRunID=%d", got, run.ID)
	}
}

// TestTick_DoesNotRedriveFreshQueuedRun: a run only just queued (within the threshold)
// is left alone — it is presumably in flight, not stranded.
func TestTick_DoesNotRedriveFreshQueuedRun(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	future := time.Now().Add(time.Hour)
	job := seedJob(t, client, "digest.scrape", true, &future)

	client.JobRun.Create().
		SetStatus(jobrun.StatusQueued).
		SetTrigger(jobrun.TriggerSchedule).
		SetScheduledFor(time.Now()).
		SetJobID(job.ID). // created_at defaults to now: fresh, inside the threshold
		SaveX(ctx)

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil, nil)
	s.tick(ctx, time.Now())

	if len(q.enqueued) != 0 {
		t.Errorf("enqueued %d jobs, want 0 (a fresh queued run is not re-driven)", len(q.enqueued))
	}
}

// TestTick_ReapsStuckRunningRun: a scheduler-driven run stuck `running` past the lease
// (the worker crashed after claiming it) is marked failed, with an error and a
// finished_at. A healthy in-flight run (started recently) is untouched.
func TestTick_ReapsStuckRunningRun(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	job := seedJob(t, client, "digest.scrape", false, nil) // disabled: it never fires

	old := time.Now().Add(-runningLease - time.Minute)
	stuck := client.JobRun.Create().
		SetStatus(jobrun.StatusRunning).
		SetTrigger(jobrun.TriggerSchedule).
		SetScheduledFor(time.Now()).
		SetStartedAt(old).
		SetJobID(job.ID).
		SaveX(ctx)
	fresh := client.JobRun.Create().
		SetStatus(jobrun.StatusRunning).
		SetTrigger(jobrun.TriggerSchedule).
		SetScheduledFor(time.Now()).
		SetStartedAt(time.Now()).
		SetJobID(job.ID).
		SaveX(ctx)

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil, nil)
	s.tick(ctx, time.Now())

	reaped := client.JobRun.GetX(ctx, stuck.ID)
	if reaped.Status != jobrun.StatusFailed {
		t.Errorf("stuck run status = %q, want failed", reaped.Status)
	}
	if reaped.Error == "" {
		t.Error("reaped run has no error message")
	}
	if reaped.FinishedAt == nil {
		t.Error("reaped run has no finished_at")
	}
	if still := client.JobRun.GetX(ctx, fresh.ID); still.Status != jobrun.StatusRunning {
		t.Errorf("fresh run status = %q, want it left running", still.Status)
	}
}

// TestTick_ReapsAckGatedRunningPastExtendedLease: an ack-gated (finance.sync) run stuck
// `running` gets a SEPARATE, much longer lease than a normal kind. Past the ordinary
// runningLease but younger than ackGatedRunningLease it is NOT reaped (a deep backfill
// can legitimately run that long); once it crosses ackGatedRunningLease it IS reaped
// (the runner crashed before /sync/complete). A normal (digest.scrape) run past
// runningLease is still reaped as before.
func TestTick_ReapsAckGatedRunningPastExtendedLease(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	normalJob := seedJob(t, client, "digest.scrape", false, nil) // disabled: never fires
	ackJob := seedJob(t, client, "finance.sync", false, nil)     // ack-gated kind
	now := time.Now()

	// Distinct scheduled_for per run: the (scheduled_for, job) unique index forbids two
	// runs of the same job sharing a tick.
	mkRunning := func(jobID int, trig jobrun.Trigger, scheduledFor, startedAt time.Time) int {
		return client.JobRun.Create().
			SetStatus(jobrun.StatusRunning).
			SetTrigger(trig).
			SetScheduledFor(scheduledFor).
			SetStartedAt(startedAt).
			SetJobID(jobID).
			SaveX(ctx).ID
	}

	// Normal run past the ordinary lease: reaped (bucket a, unchanged).
	normalStuck := mkRunning(normalJob.ID, jobrun.TriggerSchedule, now, now.Add(-runningLease-time.Minute))
	// Ack-gated run past runningLease but well within the extended lease: NOT reaped.
	ackYoung := mkRunning(ackJob.ID, jobrun.TriggerSchedule, now.Add(-time.Second), now.Add(-runningLease-time.Hour)) // ~1.5h old < 4h
	// Ack-gated schedule-fired run past the extended lease: reaped (bucket b).
	ackOld := mkRunning(ackJob.ID, jobrun.TriggerSchedule, now.Add(-2*time.Second), now.Add(-ackGatedRunningLease-time.Minute))
	// Ack-gated MANUAL ("Run now") run past the extended lease: also reaped — a crashed
	// force-started sync must not be exempted by a trigger scope (companion PR #115).
	ackManualOld := mkRunning(ackJob.ID, jobrun.TriggerManual, now.Add(-3*time.Second), now.Add(-ackGatedRunningLease-time.Minute))

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil, nil)
	s.tick(ctx, now)

	if got := client.JobRun.GetX(ctx, normalStuck).Status; got != jobrun.StatusFailed {
		t.Errorf("normal stuck run status = %q, want failed (reaped at runningLease)", got)
	}
	if got := client.JobRun.GetX(ctx, ackYoung).Status; got != jobrun.StatusRunning {
		t.Errorf("young ack-gated run status = %q, want it left running (within extended lease)", got)
	}
	reapedAck := client.JobRun.GetX(ctx, ackOld)
	if reapedAck.Status != jobrun.StatusFailed {
		t.Errorf("old ack-gated run status = %q, want failed (reaped past extended lease)", reapedAck.Status)
	}
	if reapedAck.Error == "" {
		t.Error("reaped ack-gated run has no error message")
	}
	if reapedAck.FinishedAt == nil {
		t.Error("reaped ack-gated run has no finished_at")
	}
	if got := client.JobRun.GetX(ctx, ackManualOld).Status; got != jobrun.StatusFailed {
		t.Errorf("old manual ack-gated run status = %q, want failed (reaped regardless of trigger)", got)
	}
}

// TestTick_ExpiresStaleAwaitingAck: an ack-gated run left `awaiting_ack` (never approved)
// past awaitingAckTTL is moved to a terminal cancelled with an error and finished_at, so
// it stops blocking new runs and does not accumulate. One still inside the TTL is left
// untouched.
func TestTick_ExpiresStaleAwaitingAck(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	ackJob := seedJob(t, client, "finance.sync", false, nil) // disabled: never fires
	now := time.Now()

	// Distinct scheduled_for per run: the (scheduled_for, job) unique index forbids two
	// runs of the same job sharing a tick.
	mkAwaiting := func(trig jobrun.Trigger, scheduledFor, createdAt time.Time) int {
		return client.JobRun.Create().
			SetStatus(jobrun.StatusAwaitingAck).
			SetTrigger(trig).
			SetScheduledFor(scheduledFor).
			SetCreatedAt(createdAt).
			SetJobID(ackJob.ID).
			SaveX(ctx).ID
	}

	stale := mkAwaiting(jobrun.TriggerSchedule, now, now.Add(-awaitingAckTTL-time.Minute))  // never approved, past TTL
	fresh := mkAwaiting(jobrun.TriggerSchedule, now.Add(-time.Second), now.Add(-time.Hour)) // approved-window still open
	// A MANUAL ("Run now") ack-gated run never approved must also expire, else it 409s
	// every future "Run now" (companion PR #115 added this trigger=manual path).
	staleManual := mkAwaiting(jobrun.TriggerManual, now.Add(-2*time.Second), now.Add(-awaitingAckTTL-time.Minute))

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil, nil)
	s.tick(ctx, now)

	expired := client.JobRun.GetX(ctx, stale)
	if expired.Status != jobrun.StatusCancelled {
		t.Errorf("stale awaiting_ack run status = %q, want cancelled (expired past TTL)", expired.Status)
	}
	if expired.Error == "" {
		t.Error("expired awaiting_ack run has no error message")
	}
	if expired.FinishedAt == nil {
		t.Error("expired awaiting_ack run has no finished_at")
	}
	if got := client.JobRun.GetX(ctx, fresh).Status; got != jobrun.StatusAwaitingAck {
		t.Errorf("fresh awaiting_ack run status = %q, want it left awaiting_ack (within TTL)", got)
	}
	if got := client.JobRun.GetX(ctx, staleManual).Status; got != jobrun.StatusCancelled {
		t.Errorf("stale manual awaiting_ack run status = %q, want cancelled (expired regardless of trigger)", got)
	}
}

// TestTick_SweepsExpiredArtifacts: a lapsed pending artifact is expired; a lapsed
// CLAIMED artifact is left untouched (its expires_at is a reclaimable lease the work
// API owns, not a TTL the sweeper may terminally expire); a lapsed done artifact and an
// in-TTL pending artifact are both left alone.
func TestTick_SweepsExpiredArtifacts(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	job := seedJob(t, client, "digest.scrape", false, nil)
	// A terminal run to hang the artifacts off of, so neither the re-drive nor the
	// reaper touches it during the tick.
	run := client.JobRun.Create().
		SetStatus(jobrun.StatusSucceeded).
		SetTrigger(jobrun.TriggerSchedule).
		SetScheduledFor(time.Now()).
		SetJobID(job.ID).
		SaveX(ctx)

	past := time.Now().Add(-time.Hour)
	future := time.Now().Add(time.Hour)
	mk := func(status artifact.Status, expires time.Time) int {
		return client.Artifact.Create().
			SetLabel("source:test").
			SetStorage(artifact.StorageInline).
			SetContent("x").
			SetStatus(status).
			SetExpiresAt(expires).
			SetJobID(job.ID).
			SetProducedByID(run.ID).
			SaveX(ctx).ID
	}
	expiredPending := mk(artifact.StatusPending, past)
	claimedPast := mk(artifact.StatusClaimed, past)
	donePast := mk(artifact.StatusDone, past)
	pendingFuture := mk(artifact.StatusPending, future)

	q := &stubQueue{configured: true}
	s := NewScheduler(client, q, time.Minute, nil, nil)
	s.tick(ctx, time.Now())

	if got := client.Artifact.GetX(ctx, expiredPending).Status; got != artifact.StatusExpired {
		t.Errorf("lapsed pending artifact status = %q, want expired", got)
	}
	if got := client.Artifact.GetX(ctx, claimedPast).Status; got != artifact.StatusClaimed {
		t.Errorf("lapsed claimed artifact status = %q, want it left claimed (reclaim is the work API's job, not the sweeper's)", got)
	}
	if got := client.Artifact.GetX(ctx, donePast).Status; got != artifact.StatusDone {
		t.Errorf("done artifact status = %q, want it left done (not swept)", got)
	}
	if got := client.Artifact.GetX(ctx, pendingFuture).Status; got != artifact.StatusPending {
		t.Errorf("in-TTL pending artifact status = %q, want it left pending", got)
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

// TestNextRun computes the next activation in UTC and in a non-UTC zone, and matches
// the pointer evaluate() would set for a freshly enabled job. The value must be strictly
// after `after` (a future instant, never a stale backfill).
func TestNextRun(t *testing.T) {
	// 2026-01-02 09:00 UTC; the next "0 18 * * *" (18:00 daily) is the same day 18:00.
	after := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)

	next, err := NextRun("0 18 * * *", "UTC", after)
	if err != nil {
		t.Fatalf("NextRun(UTC) err = %v", err)
	}
	want := time.Date(2026, 1, 2, 18, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Errorf("NextRun(UTC) = %v, want %v", next, want)
	}
	if !next.After(after) {
		t.Errorf("NextRun = %v, want strictly after %v", next, after)
	}

	// Same wall-clock cron in Melbourne (UTC+11 in January) resolves to 18:00 local,
	// i.e. 07:00 UTC the same day — proving the timezone is honoured.
	mel, err := NextRun("0 18 * * *", "Australia/Melbourne", after)
	if err != nil {
		t.Fatalf("NextRun(Melbourne) err = %v", err)
	}
	loc, _ := time.LoadLocation("Australia/Melbourne")
	if h := mel.In(loc).Hour(); h != 18 {
		t.Errorf("NextRun(Melbourne) local hour = %d, want 18", h)
	}

	if _, err := NextRun("nope", "UTC", after); err == nil {
		t.Error("NextRun(bad cron) = nil err, want an error")
	}
	if _, err := NextRun("0 18 * * *", "Mars/Phobos", after); err == nil {
		t.Error("NextRun(bad tz) = nil err, want an error")
	}
}

// TestTick_AckGatedFiresAwaitingAckNoEnqueue: an ack-gated kind (finance.sync) fires
// a run directly in awaiting_ack, does NOT enqueue it, and sends the refresh
// notification carrying that run's id (ADR 0016).
func TestTick_AckGatedFiresAwaitingAckNoEnqueue(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	past := time.Now().Add(-time.Hour)
	seedJob(t, client, "finance.sync", true, &past)

	q := &stubQueue{configured: true}
	n := &stubNotifier{}
	s := NewScheduler(client, q, time.Minute, nil, n)
	s.tick(ctx, time.Now())

	run, err := client.JobRun.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected exactly one JobRun: %v", err)
	}
	if run.Status != jobrun.StatusAwaitingAck {
		t.Errorf("run status = %q, want awaiting_ack", run.Status)
	}
	if run.ClaimableAt != nil {
		t.Errorf("claimable_at = %v, want nil until acked", run.ClaimableAt)
	}
	if len(q.enqueued) != 0 {
		t.Errorf("enqueued %d jobs, want 0 (ack-gated kinds are never enqueued)", len(q.enqueued))
	}
	if len(n.calls) != 1 || n.calls[0] != run.ID {
		t.Errorf("notifier calls = %v, want exactly [%d]", n.calls, run.ID)
	}
}

// TestTick_AckGatedNotificationFailureIsNonFatal: a failing notifier does not stop
// the run being created in awaiting_ack (it can be acked directly), and does not
// enqueue anything.
func TestTick_AckGatedNotificationFailureIsNonFatal(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	past := time.Now().Add(-time.Hour)
	seedJob(t, client, "finance.sync", true, &past)

	q := &stubQueue{configured: true}
	n := &stubNotifier{fail: true}
	s := NewScheduler(client, q, time.Minute, nil, n)
	s.tick(ctx, time.Now())

	run, err := client.JobRun.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected exactly one JobRun despite notify failure: %v", err)
	}
	if run.Status != jobrun.StatusAwaitingAck {
		t.Errorf("run status = %q, want awaiting_ack", run.Status)
	}
	if len(q.enqueued) != 0 {
		t.Errorf("enqueued %d jobs, want 0", len(q.enqueued))
	}
}

// TestTick_NonAckGatedUnaffectedByNotifier is the regression guard: a normal kind
// (digest.scrape) still fires queued + enqueues even when a notifier is wired, and
// the notifier is never called. The ack-gated branch must not change the ordinary
// fire path.
func TestTick_NonAckGatedUnaffectedByNotifier(t *testing.T) {
	ctx := context.Background()
	client := newTestClient(t)
	past := time.Now().Add(-time.Hour)
	seedJob(t, client, "digest.scrape", true, &past)

	q := &stubQueue{configured: true}
	n := &stubNotifier{}
	s := NewScheduler(client, q, time.Minute, nil, n)
	s.tick(ctx, time.Now())

	run, err := client.JobRun.Query().Only(ctx)
	if err != nil {
		t.Fatalf("expected exactly one JobRun: %v", err)
	}
	if run.Status != jobrun.StatusQueued {
		t.Errorf("run status = %q, want queued (normal path unchanged)", run.Status)
	}
	if len(q.enqueued) != 1 || q.enqueued[0].JobRunID != run.ID {
		t.Errorf("enqueued = %+v, want exactly one carrying run %d", q.enqueued, run.ID)
	}
	if len(n.calls) != 0 {
		t.Errorf("notifier calls = %v, want none for a non-ack-gated kind", n.calls)
	}
}
