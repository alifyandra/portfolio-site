package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Session is one logged-in device. The cookie carries a random token; only its
// hash is stored here, so a database leak does not yield usable sessions. See
// ADR 10.
type Session struct {
	ent.Schema
}

// Fields of the Session.
func (Session) Fields() []ent.Field {
	return []ent.Field{
		field.String("token_hash").
			NotEmpty().
			Unique().
			Sensitive().
			Comment("SHA-256 of the opaque session token; the raw token lives only in the cookie"),
		field.String("user_agent").
			Optional().
			Comment("Captured at sign-in to help a user recognise the device"),
		field.Time("expires_at").
			Comment("30-day sliding window, bumped on use"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the Session.
func (Session) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("sessions").
			Unique().
			Required(),
	}
}
