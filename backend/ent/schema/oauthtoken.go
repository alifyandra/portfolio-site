package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// OAuthToken is an issued OAuth 2.1 access token (and optional refresh token) from
// our own authorization server for the finance MCP resource. Every token is stamped
// with an audience (the MCP URL) so the resource server can reject a token minted
// for anything else — there is no token pass-through. Refresh is rotating: a refresh
// grant marks this row rotated and mints a successor, so a captured-then-replayed
// refresh token is dead. Only SHA-256 hashes of the token values are stored. Revoke
// is instant via revoked_at. See ADR 0018.
type OAuthToken struct {
	ent.Schema
}

// Fields of the OAuthToken.
func (OAuthToken) Fields() []ent.Field {
	return []ent.Field{
		field.String("access_token_hash").
			NotEmpty().
			Unique().
			Sensitive().
			Comment("SHA-256 of the opaque access token; the raw token is returned once at the token endpoint and never stored"),
		field.String("refresh_token_hash").
			Optional().
			Nillable().
			Unique().
			Sensitive().
			Comment("SHA-256 of the opaque refresh token; null when offline_access was not granted. Rotated on every refresh grant"),
		field.String("client_id").
			NotEmpty().
			Comment("The public client the token was issued to"),
		field.String("resource").
			NotEmpty().
			Comment("Audience: the MCP URL this token is valid for. The resource server rejects any other audience"),
		field.String("scope").
			Optional().
			Comment("Space-delimited granted scopes, e.g. \"finance.read offline_access\""),
		field.Time("access_expires_at").
			Comment("~1h TTL; the resource server rejects the access token after this"),
		field.Time("refresh_expires_at").
			Optional().
			Nillable().
			Comment("Optional refresh-token expiry; null when there is no refresh token"),
		field.Time("rotated_at").
			Optional().
			Nillable().
			Comment("Stamped when this row's refresh token was rotated into a successor; a rotated refresh token is no longer accepted"),
		field.Time("revoked_at").
			Optional().
			Nillable().
			Comment("Stamped on explicit revoke; a revoked token's access and refresh are both dead immediately"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the OAuthToken. The owning User is the admin who granted consent.
func (OAuthToken) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("owner", User.Type).
			Ref("oauth_tokens").
			Unique().
			Required(),
	}
}
