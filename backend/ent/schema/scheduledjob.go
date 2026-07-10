package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// ScheduledJob is a registered unit of recurring work the in-process scheduler
// drives. It replaces EventBridge for these jobs and gives the admin console the
// on/off switch, schedule, and run history that used to live in Terraform/AWS.
// See ADR 0014 (generic scheduled-job platform, amending ADR 13).
type ScheduledJob struct {
	ent.Schema
}

// Fields of the ScheduledJob.
func (ScheduledJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("key").
			NotEmpty().
			Unique().
			Immutable().
			Comment(`Stable machine key the scheduler and work API address the job by, e.g. "digest.scrape". Unique and never reused (immutable, like an identity key)`),
		field.String("name").
			NotEmpty().
			Comment("Human label shown in the /admin Jobs list"),
		field.Enum("stage").
			Values("scrape", "llm").
			Comment("Which half of the two-stage pipeline this job is: scrape fetches raw inputs and persists Artifacts; llm consumes them. See ADR 0014"),
		field.Bool("enabled").
			Default(false).
			Comment("Off by default: the ticker only fires enabled jobs, so a newly registered job stays dormant until an admin turns it on"),
		field.String("schedule").
			NotEmpty().
			Comment("Raw cron expression (robfig/cron standard form) evaluated in timezone to compute next_run_at"),
		field.String("timezone").
			Default("UTC").
			Comment("IANA timezone name the cron schedule is evaluated in; defaults to UTC so a job can be timed in local wall-clock later"),
		field.Enum("runner").
			Values("server", "local", "any").
			Default("server").
			Comment("Where the job may run: server (on-box worker), local (claimed by an external runner over the work API), or any"),
		field.JSON("config", map[string]interface{}{}).
			Optional().
			Comment("Free-form per-job settings read at run time (e.g. source filters, model id); the shape is job-specific"),
		field.Time("last_run_at").
			Optional().
			Nillable().
			Comment("When the most recent run for this job started; null until it has ever run"),
		field.Time("next_run_at").
			Optional().
			Nillable().
			Comment("The next tick the scheduler will fire, computed from schedule/timezone; null when disabled or not yet scheduled"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the ScheduledJob.
func (ScheduledJob) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("runs", JobRun.Type),
		edge.To("artifacts", Artifact.Type),
	}
}
