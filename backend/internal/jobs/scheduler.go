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
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
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
	// the enqueue fails we log and move on rather than crash the ticker — the run
	// sits queued and an admin force-run (P6) is the backstop.
	body := []byte("{}")
	if len(job.Config) > 0 {
		b, err := json.Marshal(job.Config)
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}
		body = b
	}
	if err := s.queue.Enqueue(ctx, queue.Job{
		Type:     job.Key,
		Payload:  json.RawMessage(body),
		JobRunID: runID,
	}); err != nil {
		s.log.Error("scheduler: enqueue failed", "job", job.Key, "run", runID, "err", err)
		return nil
	}
	s.log.Info("scheduler: enqueued run", "job", job.Key, "run", runID, "scheduled_for", scheduledFor)
	return nil
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
