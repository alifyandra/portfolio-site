package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// AccessGrant raises a person's access tier by email, independent of the
// ADMIN_EMAILS / FRIEND_EMAILS environment allowlists. See ADR 10 (tiered
// access): the env allowlist is a permanent floor, so a grant can only RAISE a
// role, never lower it. Email is stored normalized (lowercased and trimmed) by
// the service so lookups match the allowlist normalization.
type AccessGrant struct {
	ent.Schema
}

// Fields of the AccessGrant.
func (AccessGrant) Fields() []ent.Field {
	return []ent.Field{
		field.String("email").
			NotEmpty().
			Unique().
			Comment("Normalized (lowercased, trimmed) email the grant applies to"),
		field.Enum("tier").
			Values("admin", "friend").
			Comment("Access tier conferred; raises but never lowers the env-derived role"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the AccessGrant.
func (AccessGrant) Edges() []ent.Edge {
	return nil
}
