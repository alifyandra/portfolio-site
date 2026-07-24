package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// JobRun is one execution of a ScheduledJob, whether scheduler-triggered or an
// admin force-start. It carries the run's lifecycle status, timing, and the
// artifacts it produced or consumed. See ADR 0014.
type JobRun struct {
	ent.Schema
}

// Fields of the JobRun.
func (JobRun) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").
			Values("queued", "running", "succeeded", "failed", "cancelled", "awaiting_ack").
			Default("queued").
			Comment("Lifecycle: queued on insert (enqueued to the worker), running once a worker or runner picks it up, then a terminal succeeded/failed/cancelled. awaiting_ack is the ack-gated variant (ADR 0016): the run is created directly in awaiting_ack, not enqueued, and only becomes claimable once a human approves the refresh (claimable_at is set)"),
		field.Enum("trigger").
			Values("schedule", "manual").
			Comment("How the run was created: schedule (the ticker) or manual (an admin force-start)"),
		field.String("runner").
			Optional().
			Comment(`Named identity of whoever executed the run, e.g. "server", "laptop", "home-finance"; empty until a runner claims a claimable run`),
		field.Time("scheduled_for").
			Immutable().
			Comment("The scheduler tick this run represents. Together with the job edge it forms the unique dedup key, so a double-tick or an SQS redelivery cannot create two runs for the same tick. Immutable: it is an idempotency key (like Digest.date)"),
		field.Time("started_at").
			Optional().
			Nillable().
			Comment("When execution began; null while still queued"),
		field.Time("claimable_at").
			Optional().
			Nillable().
			Comment("For an ack-gated run (ADR 0016): when the refresh was approved, making the run claimable by the external finance source. Null while still awaiting_ack; set by /api/finance/sync/ack, then leased (status -> running) by /api/finance/sync/claim. Not a status: a run stays awaiting_ack until it is claimed, this only records that it MAY be claimed"),
		field.Time("finished_at").
			Optional().
			Nillable().
			Comment("When the run reached a terminal state; null until then"),
		field.Text("error").
			Optional().
			Comment("Failure reason when status is failed; Text (not String) because it can carry a multi-KB API error body"),
		field.JSON("stats", map[string]interface{}{}).
			Optional().
			Comment("Structured run metrics (counts, durations, model usage); the shape is job-specific"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("When the run row was inserted (enqueue time); gives run history a stable ordering independent of scheduled_for"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the JobRun. produced_artifacts and consumed_artifacts are distinct
// edges (distinct Ref names on Artifact) so they generate two separate FK
// columns, not one shared column.
func (JobRun) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("job", ScheduledJob.Type).
			Ref("runs").
			Unique().
			Required(),
		edge.To("produced_artifacts", Artifact.Type),
		edge.To("consumed_artifacts", Artifact.Type),
	}
}

// Indexes of the JobRun. The scheduler dedup key: at most one run per job per
// tick, enforced with OnConflict...DoNothing at insert time (see ADR 0014).
func (JobRun) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("scheduled_for").Edges("job").Unique(),
	}
}
