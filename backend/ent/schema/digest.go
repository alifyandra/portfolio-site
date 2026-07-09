package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// Digest is a dated, machine-generated summary of a fixed set of public Sources,
// produced by the scheduled digest.build Job and stored for later reading. See
// CONTEXT.md ("Digest") and ADR 0013 (scheduled heavy-compute jobs).
type Digest struct {
	ent.Schema
}

// Fields of the Digest.
func (Digest) Fields() []ent.Field {
	return []ent.Field{
		field.Time("date").
			Unique().
			Immutable().
			Comment("The day this digest covers, normalized to UTC midnight in Go before writing. The idempotency key: digest.build upserts on this column, so a redelivered Job updates the day's row instead of creating a duplicate"),
		field.Enum("status").
			Values("pending", "completed", "failed").
			Default("pending").
			Comment("Lifecycle: pending on create, completed once the summary is written, failed if the run errored"),
		field.Text("content").
			Optional().
			Comment("The Markdown summary returned by the Anthropic API; empty until the run completes"),
		field.String("model").
			Optional().
			Comment("The Anthropic model id that produced the summary, e.g. \"claude-haiku-4-5\""),
		field.Text("error").
			Optional().
			Comment("The failure reason when status is failed; cleared on a later successful upsert. Text (not String) because it can carry a multi-KB API error body"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("Row creation time"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			Comment("Last write time"),
	}
}

// Edges of the Digest. A Digest stands alone: Sources are a fixed config set the
// build Job reads at run time, not a relation (see ADR 0013).
func (Digest) Edges() []ent.Edge {
	return nil
}
