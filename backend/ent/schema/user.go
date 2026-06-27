package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// User is a person who has authenticated. See CONTEXT.md and ADR 10.
type User struct {
	ent.Schema
}

// Fields of the User.
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("email").
			NotEmpty().
			MaxLen(254).
			Unique().
			Comment("Canonical email, re-asserted from the verified provider email on each login"),
		field.String("name").
			Optional(),
		field.String("avatar_url").
			Optional(),
		field.Enum("role").
			Values("admin", "member").
			Default("member").
			Comment("admin is conferred by the ADMIN_EMAILS allowlist, re-checked on every login"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the User. Identities and Sessions are deleted with the User by the
// auth service's account-deletion transaction (app-level cascade, see ADR 10).
func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("identities", Identity.Type),
		edge.To("sessions", Session.Type),
	}
}
