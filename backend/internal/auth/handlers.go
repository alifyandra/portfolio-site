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
	http.Redirect(w, r, s.frontendURL, http.StatusFound)
}
