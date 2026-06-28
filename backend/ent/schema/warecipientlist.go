package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// WaRecipientList is a named set of WhatsApp recipients owned by a User. See
// ADR 11. Recipient data is PII and lives only in the database, never in git.
type WaRecipientList struct {
	ent.Schema
}

// Fields of the WaRecipientList.
func (WaRecipientList) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the WaRecipientList.
func (WaRecipientList) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("wa_recipient_lists").
			Unique().
			Required(),
		edge.To("recipients", WaRecipient.Type),
		edge.To("batches", WaBatch.Type),
	}
}
