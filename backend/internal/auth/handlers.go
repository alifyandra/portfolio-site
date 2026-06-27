package auth

import (
	"log/slog"
	"net/http"
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
	stateCookie := s.buildCookie(stateCookieName, state, int(stateTTL.Seconds()))
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

	// CSRF defence: the state in the query must match the state cookie we set.
	stateCookie, err := r.Cookie(stateCookieName)
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		http.Error(w, "invalid oauth state", http.StatusBadRequest)
		return
	}
	cleared := s.buildCookie(stateCookieName, "", -1)
	http.SetCookie(w, &cleared)

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing authorization code", http.StatusBadRequest)
		return
	}

	claims, err := s.exchange(ctx, code)
	if err != nil {
		slog.WarnContext(ctx, "auth: oauth exchange/verify failed", "err", err)
		http.Error(w, "sign-in failed", http.StatusBadGateway)
		return
	}

	u, err := s.upsertUser(ctx, claims)
	if err != nil {
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
