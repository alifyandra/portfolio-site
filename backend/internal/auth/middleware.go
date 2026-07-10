package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
)

type userCtxKeyType struct{}

type tokenCtxKeyType struct{}

type bearerCtxKeyType struct{}

var (
	userCtxKey   = userCtxKeyType{}
	tokenCtxKey  = tokenCtxKeyType{}
	bearerCtxKey = bearerCtxKeyType{}
)

// UserFromContext returns the authenticated user, or nil for an anonymous
// request. Handlers pair this with the guard helpers to enforce access.
func UserFromContext(ctx context.Context) *ent.User {
	u, _ := ctx.Value(userCtxKey).(*ent.User)
	return u
}

// TokenFromContext returns the raw session token for the current request, or ""
// if anonymous. Lets a handler act on the current session (e.g. logout) without
// modeling the HttpOnly cookie as an operation input parameter.
func TokenFromContext(ctx context.Context) string {
	t, _ := ctx.Value(tokenCtxKey).(string)
	return t
}

// BearerFromContext returns the resolved work-API bearer identity for the current
// request, or nil when no valid Authorization: Bearer token was presented. This is
// a SEPARATE slot from UserFromContext on purpose: the work API reads this, while
// requireAdmin/requireFriend read UserFromContext, so a bearer token can never
// reach the admin console or the friend tools (ADR 0014).
func BearerFromContext(ctx context.Context) *BearerIdentity {
	b, _ := ctx.Value(bearerCtxKey).(*BearerIdentity)
	return b
}

// Middleware resolves the session cookie to a User and stores it on the request
// context. It is best-effort: anonymous or invalid requests pass through
// untouched. Protected operations do the actual rejecting in their handlers.
func (s *Service) Middleware(ctx huma.Context, next func(huma.Context)) {
	if raw := readCookie(ctx, sessionCookieName); raw != "" {
		if u, bumped, err := s.Authenticate(ctx.Context(), raw); err != nil {
			slog.WarnContext(ctx.Context(), "auth: session lookup failed", "err", err)
		} else if u != nil {
			ctx = huma.WithValue(ctx, userCtxKey, u)
			ctx = huma.WithValue(ctx, tokenCtxKey, raw)
			// The DB expiry slid forward; refresh the cookie's client-side expiry
			// to match so the session actually slides for the user. Skip this on
			// operations that clear the cookie (logout/delete): a revive header
			// would race the handler's clearing header on the same response.
			if bumped && !clearsSessionCookie(ctx.Operation()) {
				c := s.SessionCookie(raw)
				ctx.AppendHeader("Set-Cookie", c.String())
			}
		}
	} else if bearer := readBearer(ctx); bearer != "" {
		// No session cookie: try a work-API bearer token (ADR 0014). It resolves onto
		// bearerCtxKey, NEVER userCtxKey, so the admin/friend gates cannot see it. This
		// branch only runs when the cookie is absent, so cookie-session behavior for
		// every existing endpoint is unchanged.
		if id, err := s.AuthenticateBearer(ctx.Context(), bearer); err != nil {
			slog.WarnContext(ctx.Context(), "auth: bearer lookup failed", "err", err)
		} else if id != nil {
			ctx = huma.WithValue(ctx, bearerCtxKey, id)
			// Best-effort last_used_at bump; a failure never fails the request the
			// token just authorized.
			if err := s.TouchApiToken(ctx.Context(), id.TokenID); err != nil {
				slog.WarnContext(ctx.Context(), "auth: failed to touch api token", "err", err)
			}
		}
	}
	next(ctx)
}

// clearsSessionCookie reports whether an operation expires the session cookie,
// so the sliding middleware must not emit a competing revive header on the same
// response.
func clearsSessionCookie(op *huma.Operation) bool {
	if op == nil {
		return false
	}
	switch op.OperationID {
	case "logout", "logout-all", "delete-account":
		return true
	}
	return false
}

// readBearer extracts the token from an Authorization: Bearer header, or "" when
// the header is absent or not a Bearer scheme. The scheme match is case-insensitive
// per RFC 6750.
func readBearer(ctx huma.Context) string {
	h := ctx.Header("Authorization")
	if h == "" {
		return ""
	}
	const prefix = "Bearer "
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// readCookie pulls a single cookie value out of a Huma request context by
// reusing net/http's cookie parser.
func readCookie(ctx huma.Context, name string) string {
	header := ctx.Header("Cookie")
	if header == "" {
		return ""
	}
	r := http.Request{Header: http.Header{"Cookie": []string{header}}}
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}
