// Package auth implements backend-owned Google OAuth and opaque, server-side
// sessions. The Go backend runs the whole authorization-code flow, verifies
// Google's ID token, upserts the User and Identity, and sets a session cookie
// whose value is a random token stored only as a hash. See ADR 10.
//
// Like the Spotify/SES/queue seams, auth degrades gracefully: without Google
// credentials the app still boots and the auth endpoints report "not
// configured" rather than crashing.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"
	googleendpoint "golang.org/x/oauth2/google"
	"google.golang.org/api/idtoken"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/identity"
	"github.com/alifyandra/portfolio-site/backend/ent/session"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
)

const (
	providerGoogle    = "google"
	sessionCookieName = "session"
	stateCookieName   = "oauth_state"

	// sessionDuration is the sliding session lifetime.
	sessionDuration = 30 * 24 * time.Hour
	// stateTTL bounds how long a login attempt's state cookie is valid.
	stateTTL = 10 * time.Minute
	// bumpThreshold avoids writing the session row on every request: the expiry
	// is only slid forward once this much of the window has elapsed.
	bumpThreshold = time.Hour
	// tokenBytes is the entropy of session and state tokens.
	tokenBytes = 32
)

// ErrNotConfigured is returned when Google OAuth credentials are absent.
var ErrNotConfigured = errors.New("auth: not configured")

// Config holds the inputs the auth service needs from application config.
type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AdminEmails  []string
	CookieDomain string
	CookieSecure bool
	FrontendURL  string
}

// Service owns the OAuth flow and session lifecycle.
type Service struct {
	ent          *ent.Client
	oauth        *oauth2.Config // nil when not configured
	clientID     string
	adminEmails  map[string]struct{}
	cookieDomain string
	cookieSecure bool
	frontendURL  string
}

// New builds a Service. When client id/secret are blank the OAuth config is left
// nil and Configured reports false.
func New(entClient *ent.Client, cfg Config) *Service {
	s := &Service{
		ent:          entClient,
		clientID:     cfg.ClientID,
		adminEmails:  make(map[string]struct{}, len(cfg.AdminEmails)),
		cookieDomain: cfg.CookieDomain,
		cookieSecure: cfg.CookieSecure,
		frontendURL:  cfg.FrontendURL,
	}
	for _, e := range cfg.AdminEmails {
		if e = normalizeEmail(e); e != "" {
			s.adminEmails[e] = struct{}{}
		}
	}
	if cfg.ClientID != "" && cfg.ClientSecret != "" {
		s.oauth = &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURL,
			Endpoint:     googleendpoint.Endpoint,
			Scopes:       []string{"openid", "email", "profile"},
		}
	}
	return s
}

// Configured reports whether Google OAuth credentials are present.
func (s *Service) Configured() bool { return s.oauth != nil }

// --- token helpers ---

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

// randomToken returns a URL-safe random string with tokenBytes of entropy.
func randomToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is the at-rest representation of a session token.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// roleFor resolves the role for an email against the admin allowlist.
func (s *Service) roleFor(email string) user.Role {
	if _, ok := s.adminEmails[normalizeEmail(email)]; ok {
		return user.RoleAdmin
	}
	return user.RoleMember
}

// shouldBump reports whether a session's sliding expiry is far enough into the
// window to be worth a write.
func shouldBump(expiresAt, now time.Time) bool {
	return expiresAt.Sub(now) < sessionDuration-bumpThreshold
}

// --- cookies ---

func (s *Service) buildCookie(name, value string, maxAge int) http.Cookie {
	c := http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSecure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
	if s.cookieDomain != "" {
		c.Domain = s.cookieDomain
	}
	return c
}

// SessionCookie builds the cookie that carries a raw session token.
func (s *Service) SessionCookie(rawToken string) http.Cookie {
	return s.buildCookie(sessionCookieName, rawToken, int(sessionDuration.Seconds()))
}

// ClearSessionCookie builds a cookie that expires the session cookie.
func (s *Service) ClearSessionCookie() http.Cookie {
	return s.buildCookie(sessionCookieName, "", -1)
}

// --- OAuth flow ---

// AuthCodeURL builds the Google consent URL for a login attempt.
func (s *Service) AuthCodeURL(state string) string {
	return s.oauth.AuthCodeURL(state)
}

// googleClaims is the subset of the ID token the upsert needs.
type googleClaims struct {
	sub     string
	email   string
	name    string
	picture string
}

// exchange runs the authorization-code exchange and verifies the returned ID
// token against the configured client id, then extracts the claims it needs.
func (s *Service) exchange(ctx context.Context, code string) (googleClaims, error) {
	tok, err := s.oauth.Exchange(ctx, code)
	if err != nil {
		return googleClaims{}, err
	}
	rawID, ok := tok.Extra("id_token").(string)
	if !ok || rawID == "" {
		return googleClaims{}, errors.New("auth: no id_token in token response")
	}
	payload, err := idtoken.Validate(ctx, rawID, s.clientID)
	if err != nil {
		return googleClaims{}, err
	}
	return parseClaims(payload)
}

// parseClaims validates and extracts the claims the upsert relies on. The email
// must be present and verified; the durable key is the subject id.
func parseClaims(p *idtoken.Payload) (googleClaims, error) {
	c := googleClaims{sub: p.Subject}
	if c.sub == "" {
		return c, errors.New("auth: id token missing sub")
	}
	emailVerified, _ := p.Claims["email_verified"].(bool)
	c.email, _ = p.Claims["email"].(string)
	if c.email == "" || !emailVerified {
		return c, errors.New("auth: google account email missing or unverified")
	}
	c.name, _ = p.Claims["name"].(string)
	c.picture, _ = p.Claims["picture"].(string)
	return c, nil
}

// upsertUser finds or creates the User and Identity for a set of claims, and
// re-asserts the admin role from the allowlist on every login. Runs in a
// transaction so a partial sign-in never persists.
func (s *Service) upsertUser(ctx context.Context, c googleClaims) (*ent.User, error) {
	role := s.roleFor(c.email)

	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	// Known identity => known user. Refresh the profile and role.
	id, err := tx.Identity.Query().
		Where(identity.Provider(providerGoogle), identity.ProviderSub(c.sub)).
		WithOwner().
		Only(ctx)
	switch {
	case err == nil:
		u, err := tx.User.UpdateOne(id.Edges.Owner).
			SetEmail(c.email).
			SetName(c.name).
			SetAvatarURL(c.picture).
			SetRole(role).
			Save(ctx)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Identity.UpdateOne(id).SetEmail(c.email).Save(ctx); err != nil {
			return nil, err
		}
		return u, tx.Commit()
	case !ent.IsNotFound(err):
		return nil, err
	}

	// No identity yet. Reuse a User with the same email if one exists, else
	// create one, then attach the new identity.
	u, err := tx.User.Query().Where(user.Email(c.email)).Only(ctx)
	switch {
	case err == nil:
		u, err = tx.User.UpdateOne(u).SetName(c.name).SetAvatarURL(c.picture).SetRole(role).Save(ctx)
		if err != nil {
			return nil, err
		}
	case ent.IsNotFound(err):
		u, err = tx.User.Create().
			SetEmail(c.email).
			SetName(c.name).
			SetAvatarURL(c.picture).
			SetRole(role).
			Save(ctx)
		if err != nil {
			return nil, err
		}
	default:
		return nil, err
	}

	if _, err := tx.Identity.Create().
		SetProvider(providerGoogle).
		SetProviderSub(c.sub).
		SetEmail(c.email).
		SetOwner(u).
		Save(ctx); err != nil {
		return nil, err
	}
	return u, tx.Commit()
}

// createSession mints a session for a user and returns the raw token to put in
// the cookie. Only the hash is stored.
func (s *Service) createSession(ctx context.Context, u *ent.User, userAgent string) (string, error) {
	raw, err := randomToken()
	if err != nil {
		return "", err
	}
	if _, err := s.ent.Session.Create().
		SetTokenHash(hashToken(raw)).
		SetUserAgent(userAgent).
		SetExpiresAt(time.Now().Add(sessionDuration)).
		SetOwner(u).
		Save(ctx); err != nil {
		return "", err
	}
	return raw, nil
}

// --- session resolution and revocation ---

// Authenticate resolves a raw session token to its User, sliding the expiry
// forward when due. An absent, unknown, or expired token returns (nil, nil):
// callers treat that as anonymous, not an error.
func (s *Service) Authenticate(ctx context.Context, rawToken string) (*ent.User, error) {
	if rawToken == "" {
		return nil, nil
	}
	sess, err := s.ent.Session.Query().
		Where(session.TokenHash(hashToken(rawToken))).
		WithOwner().
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	now := time.Now()
	if !sess.ExpiresAt.After(now) {
		// Expired: best-effort cleanup, treated as logged out.
		_ = s.ent.Session.DeleteOne(sess).Exec(ctx)
		return nil, nil
	}
	if shouldBump(sess.ExpiresAt, now) {
		if _, err := s.ent.Session.UpdateOne(sess).SetExpiresAt(now.Add(sessionDuration)).Save(ctx); err != nil {
			slog.WarnContext(ctx, "auth: failed to slide session expiry", "err", err)
		}
	}
	return sess.Edges.Owner, nil
}

// RevokeSession deletes the session identified by a raw token (logout). It is
// idempotent: an unknown token is a no-op.
func (s *Service) RevokeSession(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	_, err := s.ent.Session.Delete().Where(session.TokenHash(hashToken(rawToken))).Exec(ctx)
	return err
}

// RevokeAllForUser deletes every session for a user ("log out everywhere").
func (s *Service) RevokeAllForUser(ctx context.Context, userID int) error {
	_, err := s.ent.Session.Delete().Where(session.HasOwnerWith(user.ID(userID))).Exec(ctx)
	return err
}

// IsSoleAdmin reports whether the user is an admin and the only one. Used to
// stop the last admin from deleting their own account.
func (s *Service) IsSoleAdmin(ctx context.Context, u *ent.User) (bool, error) {
	if u.Role != user.RoleAdmin {
		return false, nil
	}
	n, err := s.ent.User.Query().Where(user.RoleEQ(user.RoleAdmin)).Count(ctx)
	if err != nil {
		return false, err
	}
	return n <= 1, nil
}

// DeleteUser hard-deletes a user and cascades to their identities and sessions
// in a single transaction (app-level cascade, see ADR 10).
func (s *Service) DeleteUser(ctx context.Context, userID int) error {
	tx, err := s.ent.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit

	if _, err := tx.Session.Delete().Where(session.HasOwnerWith(user.ID(userID))).Exec(ctx); err != nil {
		return err
	}
	if _, err := tx.Identity.Delete().Where(identity.HasOwnerWith(user.ID(userID))).Exec(ctx); err != nil {
		return err
	}
	if err := tx.User.DeleteOneID(userID).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}
