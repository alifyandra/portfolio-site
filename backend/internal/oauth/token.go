package oauth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
)

// handleToken serves POST /oauth/token. It speaks application/x-www-form-urlencoded
// ONLY (per RFC 6749): a non-form body is an invalid_request. It supports the
// authorization_code and refresh_token grants for the single public client, and
// returns the standard OAuth JSON on success or an OAuth error JSON on failure.
func (s *Service) handleToken(w http.ResponseWriter, r *http.Request) {
	if !s.enabled {
		http.NotFound(w, r)
		return
	}
	// The token endpoint is form-encoded. Reject a JSON (or other) body outright so
	// a client that posts the wrong content type gets a clear, deterministic error
	// rather than silently-empty form values.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
		s.tokenError(w, http.StatusBadRequest, "invalid_request", "token endpoint requires application/x-www-form-urlencoded")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.tokenError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}

	// The single public client authenticates with method "none": no secret, but the
	// client_id must still name our one registered client.
	if r.PostForm.Get("client_id") != publicClientID {
		s.tokenError(w, http.StatusBadRequest, "invalid_client", "unknown client")
		return
	}

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.grantAuthorizationCode(w, r)
	case "refresh_token":
		s.grantRefreshToken(w, r)
	default:
		s.tokenError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// grantAuthorizationCode handles grant_type=authorization_code.
func (s *Service) grantAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	code := r.PostForm.Get("code")
	verifier := r.PostForm.Get("code_verifier")
	redirectURI := r.PostForm.Get("redirect_uri")
	resource := r.PostForm.Get("resource")
	clientID := r.PostForm.Get("client_id")

	if code == "" || verifier == "" {
		s.tokenError(w, http.StatusBadRequest, "invalid_request", "code and code_verifier are required")
		return
	}

	pair, err := s.redeemAuthCode(r.Context(), code, verifier, redirectURI, clientID, resource)
	if errors.Is(err, errBadGrant) {
		s.tokenError(w, http.StatusBadRequest, "invalid_grant", "the authorization code is invalid, expired, or already used")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "oauth: redeem auth code failed", "err", err)
		s.tokenError(w, http.StatusInternalServerError, "server_error", "could not issue token")
		return
	}
	s.writeTokens(w, pair)
}

// grantRefreshToken handles grant_type=refresh_token with rotation.
func (s *Service) grantRefreshToken(w http.ResponseWriter, r *http.Request) {
	refresh := r.PostForm.Get("refresh_token")
	clientID := r.PostForm.Get("client_id")
	if refresh == "" {
		s.tokenError(w, http.StatusBadRequest, "invalid_request", "refresh_token is required")
		return
	}
	pair, err := s.rotateRefresh(r.Context(), refresh, clientID)
	if errors.Is(err, errBadGrant) {
		s.tokenError(w, http.StatusBadRequest, "invalid_grant", "the refresh token is invalid, expired, revoked, or already used")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "oauth: rotate refresh failed", "err", err)
		s.tokenError(w, http.StatusInternalServerError, "server_error", "could not issue token")
		return
	}
	s.writeTokens(w, pair)
}

// writeTokens renders a successful token response. A no-store cache directive keeps
// the token out of intermediary caches (RFC 6749 §5.1).
func (s *Service) writeTokens(w http.ResponseWriter, pair tokenPair) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	body := map[string]any{
		"access_token": pair.AccessToken,
		"token_type":   "Bearer",
		"expires_in":   pair.ExpiresIn,
		"scope":        pair.Scope,
	}
	if pair.RefreshToken != "" {
		body["refresh_token"] = pair.RefreshToken
	}
	writeJSON(w, http.StatusOK, body)
}

// tokenError writes a standard OAuth error response.
func (s *Service) tokenError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Cache-Control", "no-store")
	body := map[string]any{"error": code}
	if desc != "" {
		body["error_description"] = desc
	}
	writeJSON(w, status, body)
}
