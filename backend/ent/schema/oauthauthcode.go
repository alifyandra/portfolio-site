package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// OAuthAuthCode is a one-time PKCE authorization code minted by our own OAuth 2.1
// authorization server (the finance MCP connector flow). It is short-lived (~60s),
// single-use, and bound to the exact {client, redirect_uri, code_challenge,
// resource, scope} the /oauth/authorize request carried, so the code cannot be
// replayed against a different client or resource. Only the SHA-256 hash of the
// code is stored: a database leak yields no usable codes. See ADR 0018.
type OAuthAuthCode struct {
	ent.Schema
}

// Fields of the OAuthAuthCode.
func (OAuthAuthCode) Fields() []ent.Field {
	return []ent.Field{
		field.String("code_hash").
			NotEmpty().
			Unique().
			Sensitive().
			Comment("SHA-256 of the opaque authorization code; the raw code is only ever in the redirect and never stored"),
		field.String("client_id").
			NotEmpty().
			Comment("The public client the code was minted for; the token exchange must present the same client_id"),
		field.String("redirect_uri").
			NotEmpty().
			Comment("The exact redirect_uri the code was bound to; the token exchange must present a byte-identical value"),
		field.String("code_challenge").
			NotEmpty().
			Comment("The PKCE S256 challenge; the token exchange must present a verifier whose SHA-256 base64url equals this"),
		field.String("resource").
			NotEmpty().
			Comment("The RFC 8707 resource indicator (the MCP URL) the code is bound to; token audience is stamped from this"),
		field.String("scope").
			Optional().
			Comment("Space-delimited scopes granted at consent, e.g. \"finance.read offline_access\""),
		field.Time("expires_at").
			Immutable().
			Comment("~60s TTL; a code presented after this is rejected as expired"),
		field.Time("used_at").
			Optional().
			Nillable().
			Comment("Stamped when the code is redeemed; a second redemption sees a non-null value and is refused (one-time use)"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the OAuthAuthCode. The owning User is the admin who approved consent.
func (OAuthAuthCode) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("oauth_auth_codes").
			Unique().
			Required(),
	}
}
