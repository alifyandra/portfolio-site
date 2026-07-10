package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// ApiToken is a scope-only bearer credential an external runner uses to claim
// and complete work over the work API. It never grants admin access: the work
// API gates on scope, the admin console stays cookie-only. Only the hash is
// stored, so a database leak yields no usable tokens. See ADR 0014 (Security).
type ApiToken struct {
	ent.Schema
}

// Fields of the ApiToken.
func (ApiToken) Fields() []ent.Field {
	return []ent.Field{
		field.String("token_hash").
			NotEmpty().
			Unique().
			Sensitive().
			Comment("SHA-256 of the opaque bearer token; the raw token is shown once at mint time and never stored"),
		field.String("name").
			NotEmpty().
			Comment(`Human label for the token shown in /admin, e.g. "laptop Claude Code"`),
		field.String("runner").
			NotEmpty().
			Comment(`Runner identity this token authenticates as, e.g. "laptop", "home-finance"; independently revocable per runner`),
		field.JSON("scope", []string{}).
			Optional().
			Comment("Job keys (or scope strings) this token may claim/complete work for; the work API gates on this and it never confers admin access"),
		field.Time("last_used_at").
			Optional().
			Nillable().
			Comment("Bumped when the token authenticates a work-API call; null until first use"),
		field.Time("expires_at").
			Optional().
			Nillable().
			Comment("Optional expiry; null means the token does not expire"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("When the token was minted"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the ApiToken. The FK lives here (on api_tokens), not on users.
func (ApiToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("api_tokens").
			Unique().
			Required(),
	}
}
