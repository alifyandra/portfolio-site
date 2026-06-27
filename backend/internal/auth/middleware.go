package auth

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
)

type userCtxKeyType struct{}

var userCtxKey = userCtxKeyType{}

// UserFromContext returns the authenticated user, or nil for an anonymous
// request. Handlers pair this with the guard helpers to enforce access.
func UserFromContext(ctx context.Context) *ent.User {
	u, _ := ctx.Value(userCtxKey).(*ent.User)
	return u
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
			if bumped {
				// The DB expiry slid forward; refresh the cookie's client-side
				// expiry to match so the session actually slides for the user.
				c := s.SessionCookie(raw)
				ctx.AppendHeader("Set-Cookie", c.String())
			}
		}
	}
	next(ctx)
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
