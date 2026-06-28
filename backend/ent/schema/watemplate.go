package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// WaTemplate is a reusable WhatsApp message body owned by a User. The body may
// contain a {name} placeholder filled per recipient at send time. See ADR 11.
type WaTemplate struct {
	ent.Schema
}

// Fields of the WaTemplate.
func (WaTemplate) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120),
		field.Text("body").
			NotEmpty().
			Comment("Message text; {name} is substituted per recipient"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the WaTemplate.
func (WaTemplate) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("wa_templates").
			Unique().
			Required(),
		edge.To("batches", WaBatch.Type),
	}
}
