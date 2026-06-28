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
			Comment("Canonical email; re-asserted from the verified provider email on login, unless that value already belongs to another account (then the existing email is kept so the user is not locked out)"),
		field.String("name").
			Optional(),
		field.String("avatar_url").
			Optional(),
		field.Enum("role").
			Values("admin", "friend", "member").
			Default("member").
			Comment("Access tier, re-checked on every login: admin (ADMIN_EMAILS) and friend (FRIEND_EMAILS) are conferred by allowlists, everyone else is a member. Admin takes precedence over friend"),
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
		// WhatsApp Sender tool (ADR 11), friend-tier owned.
		edge.To("wa_templates", WaTemplate.Type),
		edge.To("wa_recipient_lists", WaRecipientList.Type),
		edge.To("wa_batches", WaBatch.Type),
	}
}
