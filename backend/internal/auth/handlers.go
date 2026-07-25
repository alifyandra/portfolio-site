package auth

import (
	"errors"
	"log/slog"
	"net/http"

	"golang.org/x/oauth2"
)

// LoginHandler starts the OAuth flow: it sets a short-lived state cookie and
// redirects the browser to Google's consent screen. Registered as a plain Chi
// route because it is a browser navigation, not a JSON API call.
func (s *Service) LoginHandler(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}
	state, err := randomToken()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	stateCookie := s.StateCookie(state)
	http.SetCookie(w, &stateCookie)

	// Optional return_to: a same-origin path to resume after sign-in (e.g. the OAuth
	// /oauth/authorize request, ADR 0018). Only a safeInternalPath is honoured, so a
	// crafted return_to can never turn the callback into an open redirect. Stored in
	// a host-only cookie because Google returns only state+code, nothing else.
	if rt := r.URL.Query().Get("return_to"); safeInternalPath(rt) {
		rtCookie := s.ReturnToCookie(rt)
		http.SetCookie(w, &rtCookie)
	}

	http.Redirect(w, r, s.AuthCodeURL(state), http.StatusFound)
}

// CallbackHandler completes the OAuth flow: it checks the state cookie, exchanges
// the code, verifies the ID token, upserts the user and identity, mints a
// session, sets the session cookie, and redirects back to the frontend.
func (s *Service) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	if !s.Configured() {
		http.Error(w, "auth not configured", http.StatusServiceUnavailable)
		return
	}
	ctx := r.Context()

	// Always invalidate the single-use state cookie, whatever the outcome, so a
	// failed attempt cannot leave a reusable CSRF nonce on the browser.
	cleared := s.ClearStateCookie()
	http.SetCookie(w, &cleared)

	// Read and clear any return_to set at login start. It is re-validated as a
	// safeInternalPath below before use; a tampered value is discarded.
	returnTo := ""
	if rt, rerr := r.Cookie(returnToCookieName); rerr == nil {
		returnTo = rt.Value
		clearedRT := s.ClearReturnToCookie()
		http.SetCookie(w, &clearedRT)
	}

	// CSRF defence: the state in the query must match the state cookie we set.
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	claims, err := s.exchange(ctx, code)
	if err != nil {
		// A rejected authorization code (expired, already used, invalid) is a
		// client-recoverable condition, not a backend outage: don't return 502
		// and trip uptime alarms.
		var rerr *oauth2.RetrieveError
		if errors.As(err, &rerr) {
			slog.InfoContext(ctx, "auth: authorization code rejected", "err", err)
			http.Error(w, "sign-in link expired or invalid, please try again", http.StatusBadRequest)
			return
		}
		slog.WarnContext(ctx, "auth: oauth exchange/verify failed", "err", err)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}

	u, err := s.upsertUser(ctx, claims)
	if err != nil {
		if errors.Is(err, ErrEmailInUse) {
			slog.InfoContext(ctx, "auth: sign-in blocked, email already in use")
			http.Error(w, "an account already exists for this email", http.StatusConflict)
			return
		}
		slog.ErrorContext(ctx, "auth: upsert user failed", "err", err)
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}

	raw, err := s.createSession(ctx, u, r.UserAgent())
	if err != nil {
		slog.ErrorContext(ctx, "auth: create session failed", "err", err)
		http.Error(w, "sign-in failed", http.StatusInternalServerError)
		return
	}

	sessionCookie := s.SessionCookie(raw)
	http.SetCookie(w, &sessionCookie)

	// Resume the return_to path when it is a safe same-origin path, else fall back
	// to the frontend. The guard is defence-in-depth: only a safeInternalPath was
	// ever stored, and it is re-checked here so a tampered cookie cannot redirect
	// off-origin.
	dest := s.frontendURL
	if safeInternalPath(returnTo) {
		dest = returnTo
	}
	http.Redirect(w, r, dest, http.StatusFound)
}
