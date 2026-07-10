package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Artifact is one persisted piece of intermediate work between the stages of a
// job: a scrape run writes one Artifact per source, an llm run consumes them.
// Small payloads live inline in Postgres, larger ones in S3. Artifacts carry a
// claim lifecycle so an external runner can lease and complete them over the
// work API. See ADR 0014.
type Artifact struct {
	ent.Schema
}

// Fields of the Artifact.
func (Artifact) Fields() []ent.Field {
	return []ent.Field{
		field.String("label").
			NotEmpty().
			Comment(`Human-and-machine label for this piece of work, e.g. "source:hn"; groups artifacts within a run`),
		field.Enum("storage").
			Values("inline", "s3").
			Comment("Where the bytes live: inline in the content column (small payloads, under ~256 KB) or s3 under s3_key"),
		field.Text("content").
			Optional().
			Comment("Inline payload when storage is inline; empty when the bytes are in S3"),
		field.String("s3_key").
			Optional().
			Comment("Object key when storage is s3; empty when inline"),
		field.String("content_type").
			Optional().
			Comment(`MIME type of the payload, e.g. "text/markdown"`),
		field.Int("size_bytes").
			Default(0).
			NonNegative().
			Comment("Payload size in bytes; drives the inline-vs-S3 threshold and is informational after"),
		field.Enum("status").
			Values("pending", "claimed", "done", "failed", "expired").
			Default("pending").
			Comment("Claim lifecycle: pending until a runner claims it, claimed while leased, then done/failed; expired once its TTL lapses and the sweeper retires it"),
		field.String("claimed_by").
			Optional().
			Comment("Identity of the runner currently holding the claim lease; empty when pending"),
		field.Time("claimed_at").
			Optional().
			Nillable().
			Comment("When the current claim lease was taken; null when pending"),
		field.Time("expires_at").
			Optional().
			Nillable().
			Comment("TTL: when a pending artifact may be swept, or when a claim lease lapses and the artifact becomes reclaimable"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Artifact. produced_by and consumed_by reference two DISTINCT
// JobRun edges (produced_artifacts vs consumed_artifacts), which is what makes
// ent generate two separate FK columns rather than sharing one. consumed_by is
// optional: it is null until an llm run consumes the artifact. Not
// self-referential, so no edge cycle.
func (Artifact) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("job", ScheduledJob.Type).
			Ref("artifacts").
			Unique().
			Required(),
		edge.From("produced_by", JobRun.Type).
			Ref("produced_artifacts").
			Unique().
			Required(),
		edge.From("consumed_by", JobRun.Type).
			Ref("consumed_artifacts").
			Unique(),
	}
}

// Indexes of the Artifact. Supports the claim/sweep path, which scans by status
// and TTL.
func (Artifact) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "expires_at"),
	}
}
