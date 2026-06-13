package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// ContactMessage is an inbound message from a site visitor. See CONTEXT.md.
type ContactMessage struct {
	ent.Schema
}

// Fields of the ContactMessage.
func (ContactMessage) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120),
		field.String("email").
			NotEmpty().
			MaxLen(254),
		field.Text("body").
			NotEmpty(),
		field.Bool("handled").
			Default(false).
			Comment("Marked true once Alif has dealt with it (future admin)"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the ContactMessage.
func (ContactMessage) Edges() []ent.Edge {
	return nil
}
