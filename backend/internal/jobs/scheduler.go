// Package jobs holds the in-process scheduler that drives ScheduledJob rows
// (ADR 0014). A single ticker goroutine in the worker (there is exactly one
// worker, ADR 6) wakes about once a minute, finds enabled jobs whose next_run_at
// has passed, inserts a JobRun under the unique (job, scheduled_for) dedup index,
// advances the schedule, and enqueues an SQS message carrying the run id.
//
// It ships DORMANT: with zero enabled ScheduledJob rows a tick enqueues nothing
// and creates nothing, so EventBridge stays authoritative for the digest until a
// later gated cutover. It also no-ops entirely when the queue is not configured,
// keeping local `make up` (no worker) and misconfigured environments inert.
package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/artifact"
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
)

// Maintenance thresholds for the re-drive/reaper/sweeper folded into each tick. They
// are constants (not env): the values only need to sit well clear of normal timings,
// and the worker's queued->running claim guard plus the digest's date-idempotency make
// a redundant re-drive or a reap-then-finish race harmless, so there is no operational
// reason to tune them per environment.
const (
	// queuedRedriveAfter is how long a JobRun may sit `queued` before the scheduler
	// re-enqueues it. It recovers a run whose post-commit enqueue failed (the run row
	// was committed but no SQS message exists). Comfortably longer than the worker's
	// claim+process latency so a run merely waiting to be picked up is not re-driven
	// mid-flight; even if it is, the worker's queued->running guard makes the duplicate
	// message a no-op.
	queuedRedriveAfter = 5 * time.Minute
	// runningLease is how long a JobRun may stay `running` before the reaper fails it.
	// It recovers a worker that crashed between claim and terminal. Far longer than any
	// real job (the digest submit is seconds; a Fargate llm run is a couple of minutes)
	// so a healthy long run is never reaped while genuinely in flight.
	runningLease = 30 * time.Minute
)

// enqueuer is the queue dependency the scheduler needs: it enqueues a message and
// reports whether a queue is configured. *queue.Client satisfies it; the test
// stubs it to record calls and to simulate an unconfigured queue.
type enqueuer interface {
	Enqueue(ctx context.Context, job queue.Job) error
	Configured() bool
}

// Scheduler is the ticker that drives ScheduledJob rows. Construct it with
// NewScheduler and run it in a goroutine with Run.
type Scheduler struct {
	ent      *ent.Client
	queue    enqueuer
	interval time.Duration
	log      *slog.Logger
}

// NewScheduler builds a Scheduler. A non-positive interval falls back to one
// minute; a nil logger falls back to slog.Default().
func NewScheduler(client *ent.Client, q enqueuer, interval time.Duration, log *slog.Logger) *Scheduler {
	if interval <= 0 {
		interval = time.Minute
	}
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{ent: client, queue: q, interval: interval, log: log}
}

// Run drives the ticker until ctx is cancelled. It is meant to run in its own
// goroutine alongside the worker's poll loop.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	s.log.Info("scheduler started", "tick", s.interval.String())
	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopping")
			return
		case now := <-t.C:
			s.tick(ctx, now)
		}
	}
}

// tick runs one scheduling pass at wall-clock time now.
func (s *Scheduler) tick(ctx context.Context, now time.Time) {
	// Never let a panic in a scheduling pass take down the worker process (the
	// ticker shares it with the poll loop). Recover, log, and let the next tick try.
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("scheduler: tick panicked; continuing", "panic", r)
		}
	}()

	// Inert when there is nowhere to enqueue: keeps `make up` (no worker) and any
	// environment without a configured queue completely idle.
	if s.queue == nil || !s.queue.Configured() {
		return
	}

	jobs, err := s.ent.ScheduledJob.Query().
		Where(scheduledjob.EnabledEQ(true)).
		All(ctx)
	if err != nil {
		s.log.Error("scheduler: query enabled jobs", "err", err)
		return
	}
	// Dormant guarantee: zero enabled rows -> the loop body never runs, so no
	// JobRun is created and nothing is enqueued.
	for _, job := range jobs {
		// One badly-configured job (e.g. an invalid cron string) must not kill the
		// tick or starve the others: log and skip it.
		if err := s.evaluate(ctx, job, now); err != nil {
			s.log.Error("scheduler: job skipped", "job", job.Key, "err", err)
		}
	}

	// Recover stranded work and retire expired artifacts on the same tick. Inert while
	// dormant: with zero enabled jobs there are no scheduler-produced runs and no
	// artifacts, so all three sweeps match nothing.
	s.maintenance(ctx, now)
}

// maintenance runs the three background sweeps once at wall-clock time now: re-drive
// runs stuck `queued`, reap runs stuck `running`, and expire lapsed artifacts. Shared
// by every tick and by the one-shot RecoverOnce at worker startup.
func (s *Scheduler) maintenance(ctx context.Context, now time.Time) {
	s.redriveQueued(ctx, now)
	s.reapRunning(ctx, now)
	s.sweepArtifacts(ctx, now)
}

// RecoverOnce runs the maintenance sweeps a single time. The worker calls it at
// startup so runs stranded (or artifacts expired) while the worker was down are
// recovered immediately, rather than only after the first full tick interval. It
// respects the same inert-when-unconfigured guard as tick.
func (s *Scheduler) RecoverOnce(ctx context.Context) {
	if s.queue == nil || !s.queue.Configured() {
		return
	}
	s.maintenance(ctx, time.Now())
}

// evaluate advances a single job for this tick: initialize a fresh pointer,
// no-op if not yet due, or fire when due.
func (s *Scheduler) evaluate(ctx context.Context, job *ent.ScheduledJob, now time.Time) error {
	schedule, loc, err := parseSchedule(job.Schedule, job.Timezone)
	if err != nil {
		return err
	}

	// Newly enabled (or never scheduled) job: initialize next_run_at and do NOT
	// fire this tick — it is not due until that time arrives.
	if job.NextRunAt == nil {
		next := schedule.Next(now.In(loc))
		if err := s.ent.ScheduledJob.UpdateOneID(job.ID).SetNextRunAt(next).Exec(ctx); err != nil {
			return fmt.Errorf("initialize next_run_at: %w", err)
		}
		s.log.Info("scheduler: initialized next_run_at", "job", job.Key, "next", next)
		return nil
	}

	// Not due yet.
	if now.Before(*job.NextRunAt) {
		return nil
	}

	// DUE. The canonical tick is the next_run_at value that fired — never
	// time.Now(). scheduled_for MUST equal it byte-for-byte so the unique
	// (job, scheduled_for) index collapses a double-tick or an SQS redelivery to a
	// single run; a jittery now() would defeat the dedup.
	scheduledFor := *job.NextRunAt
	// Advance from now (not from next_run_at): a worker that was down through
	// several ticks fires ONCE on restart and jumps the pointer to the next future
	// activation, rather than backfilling one run per missed tick.
	next := schedule.Next(now.In(loc))

	fresh, runID, err := s.fire(ctx, job, scheduledFor, next, now)
	if err != nil {
		return err
	}
	if !fresh {
		// A run for this tick already exists (unique-index conflict). The pointer
		// was still advanced inside the transaction, so this tick is not revisited.
		// Nothing to enqueue.
		s.log.Info("scheduler: run already exists for tick; advanced only",
			"job", job.Key, "scheduled_for", scheduledFor)
		return nil
	}

	// Enqueue AFTER the commit. At-least-once is acceptable: the worker transitions
	// the run queued->running only if it is still queued, so a redelivery no-ops. If
	// the enqueue fails we log and move on rather than crash the ticker — the run sits
	// queued and the redriveQueued sweep re-enqueues it on a later tick.
	if err := s.enqueueRun(ctx, job, runID); err != nil {
		s.log.Error("scheduler: enqueue failed", "job", job.Key, "run", runID, "err", err)
		return nil
	}
	s.log.Info("scheduler: enqueued run", "job", job.Key, "run", runID, "scheduled_for", scheduledFor)
	return nil
}

// enqueueRun places one run's message on the queue, marshalling the job's config as
// the payload (empty object when the job has none). Shared by the fire path and the
// redriveQueued sweep so both build the envelope identically.
func (s *Scheduler) enqueueRun(ctx context.Context, job *ent.ScheduledJob, runID int) error {
	body := []byte("{}")
	if len(job.Config) > 0 {
		b, err := json.Marshal(job.Config)
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
		body = b
	}
	return s.queue.Enqueue(ctx, queue.Job{
		Type:     job.Key,
		Payload:  json.RawMessage(body),
		JobRunID: runID,
	})
}

// fire inserts the JobRun for this tick and advances the job's pointer inside one
// transaction, then commits. It reports whether a fresh run was inserted (fresh)
// versus a unique-index conflict on (job, scheduled_for) (not fresh), and the new
// run's id when fresh. The enqueue is deliberately left to the caller, after the
// commit.
func (s *Scheduler) fire(ctx context.Context, job *ent.ScheduledJob, scheduledFor, next, now time.Time) (fresh bool, runID int, err error) {
	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return false, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	// Insert the run guarded by the unique (scheduled_for, job) index. ON CONFLICT
	// DO NOTHING returns no row on a conflict, which Ent surfaces as sql.ErrNoRows;
	// that is the "someone already created this tick's run" signal, not a failure.
	// A fresh insert returns the new id.
	id, cerr := tx.JobRun.Create().
		SetStatus(jobrun.StatusQueued).
		SetTrigger(jobrun.TriggerSchedule).
		SetScheduledFor(scheduledFor).
		SetJobID(job.ID).
		OnConflictColumns(jobrun.FieldScheduledFor, jobrun.JobColumn).
		DoNothing().
		ID(ctx)
	fresh = true
	if cerr != nil {
		if errors.Is(cerr, sql.ErrNoRows) {
			fresh = false // conflict: the run for this tick already exists
		} else {
			err = fmt.Errorf("insert job run: %w", cerr)
			return false, 0, err
		}
	}

	// Advance the pointer regardless of fresh/conflict, so a duplicated tick is
	// never revisited. last_run_at records when this tick dispatched.
	if err = tx.ScheduledJob.UpdateOneID(job.ID).
		SetNextRunAt(next).
		SetLastRunAt(now).
		Exec(ctx); err != nil {
		return false, 0, fmt.Errorf("advance next_run_at: %w", err)
	}

	if err = tx.Commit(); err != nil {
		return false, 0, fmt.Errorf("commit: %w", err)
	}
	return fresh, id, nil
}

// redriveQueued re-enqueues scheduler-driven runs that have sat `queued` past
// queuedRedriveAfter. That state means the run row committed but its post-commit
// enqueue never landed (or its message was lost), so there is no worker to pick it up.
// Only trigger=schedule runs are touched, never the legacy JobRunID==0 live-digest
// path (which creates no JobRun rows at all), and re-enqueue is safe because the
// worker's queued->running claim guard collapses any duplicate.
func (s *Scheduler) redriveQueued(ctx context.Context, now time.Time) {
	cutoff := now.Add(-queuedRedriveAfter)
	runs, err := s.ent.JobRun.Query().
		Where(
			jobrun.StatusEQ(jobrun.StatusQueued),
			jobrun.TriggerEQ(jobrun.TriggerSchedule),
			jobrun.CreatedAtLT(cutoff),
		).
		WithJob().
		All(ctx)
	if err != nil {
		s.log.Error("scheduler: redrive query", "err", err)
		return
	}
	for _, run := range runs {
		if run.Edges.Job == nil {
			// A run with no job edge cannot be re-enqueued (we would not know its key);
			// skip rather than guess. Should not happen: the job edge is Required.
			s.log.Error("scheduler: redrive skipped, run has no job", "run", run.ID)
			continue
		}
		if err := s.enqueueRun(ctx, run.Edges.Job, run.ID); err != nil {
			s.log.Error("scheduler: redrive enqueue failed", "run", run.ID, "job", run.Edges.Job.Key, "err", err)
			continue
		}
		s.log.Info("scheduler: re-drove stranded queued run", "run", run.ID, "job", run.Edges.Job.Key)
	}
}

// reapRunning fails scheduler-driven runs stuck `running` past runningLease. That
// state means a worker claimed the run (queued->running) then died before writing a
// terminal status. Failing it (rather than re-queuing) matches the scheduled-job retry
// model: the next scheduled tick is the retry, so a reaped run does not re-run here and
// risk duplicate side effects. Only trigger=schedule runs are touched. The lease is far
// longer than any real job, so a healthy in-flight run is never reaped.
func (s *Scheduler) reapRunning(ctx context.Context, now time.Time) {
	cutoff := now.Add(-runningLease)
	n, err := s.ent.JobRun.Update().
		Where(
			jobrun.StatusEQ(jobrun.StatusRunning),
			jobrun.TriggerEQ(jobrun.TriggerSchedule),
			jobrun.StartedAtLT(cutoff),
		).
		SetStatus(jobrun.StatusFailed).
		SetError("reaped: exceeded running lease").
		SetFinishedAt(now).
		Save(ctx)
	if err != nil {
		s.log.Error("scheduler: reap running", "err", err)
		return
	}
	if n > 0 {
		s.log.Warn("scheduler: reaped stuck running runs", "count", n)
	}
}

// sweepArtifacts expires ONLY pending artifacts whose TTL has lapsed: ones a run never
// consumed (e.g. a prior failed llm day, left behind by the newest-run scoping in
// AssembleFromArtifacts). claimed is deliberately NOT swept. expires_at is overloaded
// (see the schema comment): it is the TTL for a pending artifact but the claim-lease
// expiry for a claimed one, and the P5 work API treats a lease-lapsed claimed artifact
// as reclaimable. Terminally expiring it here would race that reclaim and strand a
// runner's work. The claimed lease/reclaim lifecycle therefore belongs to the work API,
// not this sweeper. On the box-local digest path artifacts go pending->done
// synchronously and never sit claimed, so this scopes cleanly; a claimed artifact
// abandoned forever is a rare, non-corrupting leak (a dedicated lease column is a later
// option if the runner path gets heavy). done/failed/expired rows are left untouched,
// so a consumed (done) artifact stays as retained job output. It sets status=expired
// rather than deleting; the S3 lifecycle rule reclaims any object bytes.
func (s *Scheduler) sweepArtifacts(ctx context.Context, now time.Time) {
	n, err := s.ent.Artifact.Update().
		Where(
			artifact.StatusEQ(artifact.StatusPending),
			artifact.ExpiresAtNotNil(),
			artifact.ExpiresAtLT(now),
		).
		SetStatus(artifact.StatusExpired).
		Save(ctx)
	if err != nil {
		s.log.Error("scheduler: sweep expired artifacts", "err", err)
		return
	}
	if n > 0 {
		s.log.Info("scheduler: expired lapsed artifacts", "count", n)
	}
}

// parseSchedule parses a standard cron expression and resolves the job's timezone.
// The returned Schedule is evaluated in loc by the caller (schedule.Next(now.In(loc))).
func parseSchedule(expr, timezone string) (cron.Schedule, *time.Location, error) {
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, nil, fmt.Errorf("parse cron %q: %w", expr, err)
	}
	if timezone == "" {
		timezone = "UTC"
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, nil, fmt.Errorf("load timezone %q: %w", timezone, err)
	}
	return schedule, loc, nil
}

// ValidateCron reports whether expr is a valid standard cron expression. Exported
// for reuse by the P6 admin endpoints that create/edit ScheduledJob rows.
func ValidateCron(expr string) error {
	_, err := cron.ParseStandard(expr)
	return err
}

// NextRun computes the next activation of a cron schedule strictly after `after`,
// evaluated in timezone. It reuses the ticker's parseSchedule, so the value is exactly
// what evaluate() would compute for a freshly enabled job — a future instant that will
// not fire this tick. Exported so the admin create/update handlers can populate
// next_run_at synchronously (rather than clearing it and waiting up to a full tick for
// the scheduler to fill it in), giving the console an immediate, correct "Next run".
func NextRun(expr, timezone string, after time.Time) (time.Time, error) {
	schedule, loc, err := parseSchedule(expr, timezone)
	if err != nil {
		return time.Time{}, err
	}
	return schedule.Next(after.In(loc)), nil
}
