package api

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// newAuthTestAPI wires only the auth operations onto a humatest API, backed by
// an auth service with no DB. The paths exercised here (logout with no cookie,
// anonymous me) never touch the database.
func newAuthTestAPI(t *testing.T) humatest.TestAPI {
	t.Helper()
	_, api := humatest.New(t)
	svc := auth.New(nil, auth.Config{})
	// Wire the real auth middleware so the harness mirrors production (server.go).
	// The cases here send no session cookie, so the middleware no-ops without
	// touching the DB; DB-backed flows are covered in internal/auth's tests.
	api.UseMiddleware(svc.Middleware)
	h := New(Deps{Auth: svc})
	h.registerAuth(api)
	return api
}

// TestLogoutClearsSessionCookie verifies the response actually emits a valid,
// parseable Set-Cookie that expires the session cookie (guards against the
// header being rendered as a Go struct rather than a cookie string).
func TestLogoutClearsSessionCookie(t *testing.T) {
	api := newAuthTestAPI(t)
	resp := api.Post("/api/auth/logout")
	if resp.Code != http.StatusOK {
		t.Fatalf("logout status = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}

	var session *http.Cookie
	for _, c := range resp.Result().Cookies() {
		if c.Name == "session" {
			session = c
		}
	}
	if session == nil {
		t.Fatalf("no parseable session cookie cleared; raw Set-Cookie=%q", resp.Header().Get("Set-Cookie"))
	}
	if session.MaxAge >= 0 {
		t.Fatalf("session cookie not expired: MaxAge=%d", session.MaxAge)
	}
}

// TestMeRequiresAuth verifies an anonymous request to the protected endpoint is
// rejected (no middleware sets a user, so the handler must 401).
func TestMeRequiresAuth(t *testing.T) {
	api := newAuthTestAPI(t)
	resp := api.Get("/api/auth/me")
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("me status = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
}

// TestUpdateMeRequiresAuth verifies PATCH /api/auth/me is gated: a valid body
// clears input validation, so the 401 proves the auth check ran (not a validation
// short-circuit) and no anonymous write reaches the DB.
func TestUpdateMeRequiresAuth(t *testing.T) {
	api := newAuthTestAPI(t)
	resp := api.Patch("/api/auth/me", map[string]any{"default_country_code": "62"})
	if resp.Code != http.StatusUnauthorized {
		t.Fatalf("patch me status = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
}
