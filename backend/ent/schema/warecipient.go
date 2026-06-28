package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// WaRecipient is one entry in a WaRecipientList. See ADR 11.
type WaRecipient struct {
	ent.Schema
}

// Fields of the WaRecipient.
func (WaRecipient) Fields() []ent.Field {
	return []ent.Field{
		field.String("phone").
			NotEmpty().
			MaxLen(20).
			Comment("International format, digits only (no +, no leading 0)"),
		field.String("name").
			Optional().
			Comment("Substituted into a template's {name} placeholder"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the WaRecipient.
func (WaRecipient) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("list", WaRecipientList.Type).
			Ref("recipients").
			Unique().
			Required(),
	}
}
