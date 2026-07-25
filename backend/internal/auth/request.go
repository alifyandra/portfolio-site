package auth

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/alifyandra/portfolio-site/backend/ent"
)

// AuthenticateRequest resolves the session cookie on a raw *http.Request to its
// User, for routes mounted OUTSIDE the Huma middleware (e.g. the OAuth authorize
// endpoint on the raw Chi mux). It returns (nil, nil) for an anonymous or invalid
// session — callers treat that as unauthenticated, not an error. It never slides
// the cookie expiry (a raw browser-navigation route has nowhere to set the revive
// header cleanly); read-only session resolution is all these routes need.
func (s *Service) AuthenticateRequest(r *http.Request) (*ent.User, error) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	u, _, aerr := s.Authenticate(r.Context(), c.Value)
	return u, aerr
}

// safeInternalPath reports whether p is a safe SAME-ORIGIN redirect target: a
// non-empty absolute path ("/...") with no scheme and no host, and not a
// protocol-relative ("//host") or backslash-smuggled ("/\host") URL that a browser
// would resolve to another origin. This is the anti-open-redirect gate for the
// OAuth login return_to.
func safeInternalPath(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	if strings.HasPrefix(p, "//") || strings.HasPrefix(p, "/\\") {
		return false
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return false
	}
	return true
}
