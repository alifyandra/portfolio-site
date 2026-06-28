package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// WaBatch is one send run: a WaTemplate applied to a WaRecipientList, with a
// status and aggregate counts. Per-recipient logs are a later addition (ADR 11).
type WaBatch struct {
	ent.Schema
}

// Fields of the WaBatch.
func (WaBatch) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("status").
			Values("pending", "linking", "running", "completed", "failed").
			Default("pending"),
		field.Int("total").
			Default(0).
			NonNegative(),
		field.Int("sent").
			Default(0).
			NonNegative(),
		field.Int("failed").
			Default(0).
			NonNegative(),
		field.Int("skipped").
			Default(0).
			NonNegative().
			Comment("Numbers not registered on WhatsApp, skipped at send time"),
		field.String("error").
			Optional(),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the WaBatch. template/list are optional so deleting one later does
// not erase batch history (the aggregate counts stand on their own).
func (WaBatch) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("wa_batches").
			Unique().
			Required(),
		edge.From("template", WaTemplate.Type).
			Ref("batches").
			Unique(),
		edge.From("list", WaRecipientList.Type).
			Ref("batches").
			Unique(),
	}
}
