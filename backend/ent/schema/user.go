package schema

import (
	"regexp"
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
		field.String("default_country_code").
			Default("61").
			Match(regexp.MustCompile("^[0-9]{1,4}$")).
			Comment("Country code (digits only, no +) that replaces a leading trunk 0 when normalizing WhatsApp numbers. A recipient list's own country_code overrides this per-list; the \"61\" constant is the final fallback. See ADR 11."),
		field.Enum("role").
			Values("admin", "friend", "member").
			Default("member").
			Comment("Access tier, re-checked on every login: admin (ADMIN_EMAILS) and friend (FRIEND_EMAILS) are conferred by allowlists, everyone else is a member. Admin takes precedence over friend"),
		field.String("nickname").
			Optional().
			Nillable().
			MaxRuneLen(40).
			Comment("Self-chosen display name; overrides the provider name for display (precedence nickname ?? name ?? email). Unlike name/role it is User-owned and never re-asserted from Google/env on login; null means unset. Set via PATCH /api/auth/me. See ADR 10."),
		field.Enum("greeted_role").
			Values("admin", "friend", "member").
			Optional().
			Nillable().
			Comment("The Role at which the User was last shown the full Welcome; null means never welcomed. Server-owned state stamped from the caller's current role when they ack the Welcome, never client-supplied. Drives the returning/promotion greeting. Same values as role. See ADR 10."),
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
		// Scheduled-job platform (ADR 0014): scope-only bearer tokens for
		// external runners. FK lives on api_tokens, no new column on users.
		edge.To("api_tokens", ApiToken.Type),
	}
}
