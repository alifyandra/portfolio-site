// Package oauth is a minimal, self-contained OAuth 2.1 authorization server AND
// the resource-server checks for the finance MCP endpoint (ADR 0018). It exists so
// claude.ai (web + mobile) can connect the remote MCP server at
// <base>/mcp: Claude probes the RFC 9728 protected-resource metadata, discovers
// this authorization server via RFC 8414, runs an authorization-code + PKCE flow
// against /oauth/authorize and /oauth/token, and presents the resulting bearer to
// /mcp.
//
// We are our OWN authorization server: the browser consent step is gated on the
// site's existing admin session (backend-owned Google login), but Google is never
// proxied as an OAuth AS — Claude only ever talks to us. Only ONE person (the admin
// owner) may ever approve a connection, so there is no dynamic client registration
// and no multi-user consent: a single hardcoded public client is accepted.
//
// The whole surface is behind a feature flag (FINANCE_MCP_OAUTH_ENABLED, default
// off). When the flag is off every route here 404s and the /mcp challenge stays the
// legacy bearer-only form; the static finance.read bearer token keeps working in
// BOTH modes. Construction dereferences nothing (ent/auth may be nil, as in
// cmd/spec); the clients are touched only per request, and only when enabled.
package oauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	"github.com/go-chi/chi/v5"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

const (
	// publicClientID is the single, pre-registered public client this server
	// accepts. There is NO dynamic client registration: claude.ai is configured
	// with this fixed client_id via the connector's Advanced settings, and any
	// other client_id is rejected. It is public (no secret): the flow's security
	// rests on PKCE + the admin-gated consent, not on a client credential.
	publicClientID = "aliflabs-finance-mcp"

	// The scopes this server understands. financeReadScope is the only resource
	// scope (it maps 1:1 to the MCP finance.read gate); offlineAccessScope opts the
	// client into a refresh token.
	financeReadScope   = "finance.read"
	offlineAccessScope = "offline_access"

	// TTLs. Auth codes are single-use and expire fast; access tokens last ~1h;
	// refresh tokens are long-lived but rotate on every use.
	authCodeTTL     = 60 * time.Second
	accessTokenTTL  = time.Hour
	refreshTokenTTL = 90 * 24 * time.Hour

	// defaultBaseURL is the production origin, used when no configured redirect URL
	// reveals the backend's own scheme+host.
	defaultBaseURL = "https://api.aliflabs.dev"

	// claudeRedirectURI is the exact (byte-for-byte) redirect the claude.ai web +
	// mobile connector posts back to. It is matched exactly; loopback callbacks
	// (Claude Code) are matched separately by host, see redirectURIAllowed.
	claudeRedirectURI = "https://claude.ai/api/mcp/auth_callback"
)

// Config is the slice of app configuration the OAuth server needs.
type Config struct {
	// Enabled is the FINANCE_MCP_OAUTH_ENABLED flag. Off => every route 404s.
	Enabled bool
	// BaseURL is this backend's external origin (scheme+host, no trailing slash),
	// e.g. https://api.aliflabs.dev. It is the OAuth issuer and the base for the
	// authorization/token endpoints and the MCP resource URL.
	BaseURL string
	// Dialect is the ent database dialect (e.g. dialect.Postgres, dialect.SQLite).
	// It selects whether rotateRefresh may take a SELECT ... FOR UPDATE family lock:
	// Postgres supports it, SQLite does not (and the single-connection test DB
	// serializes transactions anyway). Empty is treated as lock-capable so a
	// production misconfiguration fails closed (keeps the lock) rather than silently
	// dropping the reuse-race protection.
	Dialect string
}

// Service is the OAuth authorization + resource server. It is safe to construct
// with nil ent/auth (cmd/spec); handlers guard on enabled and on nil clients.
type Service struct {
	ent      *ent.Client
	auth     *auth.Service
	enabled  bool
	baseURL  string // scheme+host, no trailing slash
	resource string // baseURL + "/mcp"; the audience every token is stamped with
	secure   bool   // set Secure on cookies (true when the base URL is https)
	rowLock  bool   // take a FOR UPDATE family lock in rotateRefresh (off on SQLite)
	now      func() time.Time
}

// New builds the Service. now is injectable purely for tests; production uses
// time.Now.
func New(entClient *ent.Client, authSvc *auth.Service, cfg Config) *Service {
	base := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = defaultBaseURL
	}
	return &Service{
		ent:      entClient,
		auth:     authSvc,
		enabled:  cfg.Enabled,
		baseURL:  base,
		resource: base + "/mcp",
		secure:   strings.HasPrefix(base, "https://"),
		rowLock:  cfg.Dialect != dialect.SQLite,
		now:      time.Now,
	}
}

// Enabled reports whether the OAuth surface is live.
func (s *Service) Enabled() bool { return s.enabled }

// ResourceMetadataURL is the RFC 9728 protected-resource metadata URL advertised
// in the /mcp WWW-Authenticate challenge when the flag is on.
func (s *Service) ResourceMetadataURL() string {
	return s.baseURL + "/.well-known/oauth-protected-resource"
}

// BaseURLFromRedirect derives this backend's external origin (scheme+host) from a
// configured OAuth redirect URL, the one config value that already carries the
// backend's own scheme+host (mirrors notify.AckURL). An empty or unparseable
// redirect falls back to fallback.
func BaseURLFromRedirect(redirectURL, fallback string) string {
	u, err := url.Parse(strings.TrimSpace(redirectURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimRight(fallback, "/")
	}
	return u.Scheme + "://" + u.Host
}

// Register mounts the discovery + OAuth endpoints on the raw Chi router, alongside
// /mcp. Every handler 404s when the flag is off, so registering is unconditional
// and cmd/spec (flag off, nil clients) stays inert. The routes inherit the root
// mux's per-IP rate limiter.
func (s *Service) Register(r chi.Router) {
	r.Get("/.well-known/oauth-protected-resource", s.handleProtectedResource)
	r.Get("/.well-known/oauth-protected-resource/mcp", s.handleProtectedResource)
	r.Get("/.well-known/oauth-authorization-server", s.handleAuthServerMetadata)
	r.Get("/oauth/authorize", s.handleAuthorize)
	r.Post("/oauth/authorize", s.handleConsent)
	r.Post("/oauth/token", s.handleToken)
}

// handleProtectedResource serves the RFC 9728 protected-resource metadata that
// points Claude at this authorization server and the scopes the MCP resource wants.
func (s *Service) handleProtectedResource(w http.ResponseWriter, r *http.Request) {
	if !s.enabled {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 s.resource,
		"authorization_servers":    []string{s.baseURL},
		"scopes_supported":         []string{financeReadScope},
		"bearer_methods_supported": []string{"header"},
	})
}

// handleAuthServerMetadata serves the RFC 8414 authorization-server metadata.
func (s *Service) handleAuthServerMetadata(w http.ResponseWriter, r *http.Request) {
	if !s.enabled {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                s.baseURL,
		"authorization_endpoint":                s.baseURL + "/oauth/authorize",
		"token_endpoint":                        s.baseURL + "/oauth/token",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{financeReadScope, offlineAccessScope},
	})
}

// redirectURIAllowed reports whether a redirect_uri is permitted. The claude.ai
// connector URI is matched EXACTLY (byte-for-byte). Loopback callbacks (Claude
// Code and other local MCP clients) are matched by host only — http scheme, a
// loopback host (127.0.0.1 / localhost / ::1), any port — per RFC 8252, which is
// safe because a loopback address is only reachable on the user's own machine and
// so cannot be an attacker-controlled destination. Everything else is rejected:
// no wildcard, no substring, no open redirect.
func redirectURIAllowed(raw string) bool {
	if raw == claudeRedirectURI {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	// A fragment is never valid in a redirect_uri (RFC 6749 §3.1.2).
	if u.Fragment != "" {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	default:
		return false
	}
}

// writeJSON encodes v as an application/json response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
