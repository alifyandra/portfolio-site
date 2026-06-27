package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Identity is one external login credential belonging to a User. See ADR 10.
type Identity struct {
	ent.Schema
}

// Fields of the Identity.
func (Identity) Fields() []ent.Field {
	return []ent.Field{
		field.String("provider").
			NotEmpty().
			Comment(`OAuth provider, e.g. "google"`),
		field.String("provider_sub").
			NotEmpty().
			Comment("Provider's stable subject id (Google `sub`). The durable key, never the email."),
		field.String("email").
			Optional().
			Comment("Provider-asserted email at link time; informational and may go stale"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the Identity.
func (Identity) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("identities").
			Unique().
			Required(),
	}
}

// Indexes of the Identity. A provider's subject id is unique within that
// provider; that pair, not the email, identifies the login.
func (Identity) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "provider_sub").Unique(),
	}
}
