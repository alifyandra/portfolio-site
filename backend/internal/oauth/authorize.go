package oauth

import (
	"crypto/subtle"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/alifyandra/portfolio-site/backend/ent/user"
)

// csrfCookieName is the host-only, single-use double-submit cookie that binds the
// consent POST to the consent GET that rendered the form.
const csrfCookieName = "oauth_consent_csrf"

// authzParams is the parsed /oauth/authorize request.
type authzParams struct {
	responseType        string
	clientID            string
	redirectURI         string
	scope               string
	state               string
	codeChallenge       string
	codeChallengeMethod string
	resource            string
}

// handleAuthorize serves GET /oauth/authorize. It validates the request, resolves
// the session, and — only for a live ADMIN session — renders the consent page. It
// NEVER mints a code: a code is only minted by the explicit consent POST. Anonymous
// requests are sent to the site's Google login with a signed-safe internal return
// path back to this exact authorize request; a logged-in NON-admin is refused
// outright (a 403 page, not a redirect, so there is no login loop).
func (s *Service) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if !s.enabled {
		http.NotFound(w, r)
		return
	}
	p, ok := s.validateAuthz(w, r, r.URL.Query().Get)
	if !ok {
		return
	}

	u, err := s.auth.AuthenticateRequest(r)
	if err != nil {
		slog.ErrorContext(r.Context(), "oauth: session lookup failed", "err", err)
		s.errorPage(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	if u == nil {
		// Anonymous: bounce through the existing backend-owned Google login, asking
		// it to return to THIS authorize request (path+query) so the flow resumes
		// after sign-in. return_to is an internal path only (login validates it),
		// so this can never be an open redirect.
		loc := "/api/auth/google/login?return_to=" + url.QueryEscape(r.URL.RequestURI())
		http.Redirect(w, r, loc, http.StatusFound)
		return
	}
	if u.Role != user.RoleAdmin {
		// Owner-only: a member/friend session is refused, not redirected (redirecting
		// a logged-in non-admin back to login would loop). See ADR 0018.
		s.errorPage(w, http.StatusForbidden, "Only the site owner may authorize this connection.")
		return
	}

	// Fresh CSRF token for the consent form (double-submit cookie).
	csrf, err := randomToken()
	if err != nil {
		s.errorPage(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	http.SetCookie(w, s.csrfCookie(csrf))
	s.renderConsent(w, consentView{
		ClientID:      p.clientID,
		RedirectURI:   p.redirectURI,
		Scope:         p.scope,
		ScopeLabel:    grantedScope(p.scope),
		State:         p.state,
		CodeChallenge: p.codeChallenge,
		Resource:      p.resource,
		UserEmail:     u.Email,
		CSRF:          csrf,
	})
}

// handleConsent serves POST /oauth/authorize: the explicit approval that mints a
// code. It re-validates every parameter (never trusting the hidden form fields),
// re-checks the admin session, verifies the CSRF double-submit token, requires an
// explicit action=approve, and only then mints a one-time code and redirects back.
func (s *Service) handleConsent(w http.ResponseWriter, r *http.Request) {
	if !s.enabled {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, http.StatusBadRequest, "Malformed form.")
		return
	}
	p, ok := s.validateAuthz(w, r, r.PostForm.Get)
	if !ok {
		return
	}

	u, err := s.auth.AuthenticateRequest(r)
	if err != nil {
		slog.ErrorContext(r.Context(), "oauth: session lookup failed", "err", err)
		s.errorPage(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}
	if u == nil {
		s.errorPage(w, http.StatusUnauthorized, "Sign in required.")
		return
	}
	if u.Role != user.RoleAdmin {
		s.errorPage(w, http.StatusForbidden, "Only the site owner may authorize this connection.")
		return
	}

	// CSRF: the form token must equal the cookie set when the page was rendered.
	// A session alone is NOT consent — this is what stops a logged-in browser from
	// silently yielding a code to a forged cross-site POST.
	cookie, cerr := r.Cookie(csrfCookieName)
	formCSRF := r.PostForm.Get("csrf")
	if cerr != nil || cookie.Value == "" || formCSRF == "" ||
		subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(formCSRF)) != 1 {
		s.errorPage(w, http.StatusForbidden, "Invalid or missing CSRF token.")
		return
	}
	// The CSRF token is single-use; clear it whatever the outcome below.
	http.SetCookie(w, s.clearCSRFCookie())

	if r.PostForm.Get("action") != "approve" {
		s.redirectError(w, r, p, "access_denied", "the request was not approved")
		return
	}

	scope := grantedScope(p.scope)
	rawCode, err := s.mintAuthCode(r.Context(), codeGrant{
		userID:        u.ID,
		clientID:      p.clientID,
		redirectURI:   p.redirectURI,
		codeChallenge: p.codeChallenge,
		resource:      p.resource,
		scope:         scope,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "oauth: mint auth code failed", "err", err)
		s.errorPage(w, http.StatusInternalServerError, "Something went wrong.")
		return
	}

	redir, _ := url.Parse(p.redirectURI)
	q := redir.Query()
	q.Set("code", rawCode)
	if p.state != "" {
		q.Set("state", p.state)
	}
	redir.RawQuery = q.Encode()
	http.Redirect(w, r, redir.String(), http.StatusFound)
}

// validateAuthz validates the request parameters, shared by GET and POST. On any
// failure it writes the response itself and returns ok=false: an untrustworthy
// client_id or redirect_uri yields a 400 page (we NEVER redirect to an unvalidated
// URI — that would be an open redirect), while a bad parameter on an otherwise
// valid request bounces back to the validated redirect_uri with an OAuth error.
func (s *Service) validateAuthz(w http.ResponseWriter, r *http.Request, get func(string) string) (authzParams, bool) {
	p := authzParams{
		responseType:        get("response_type"),
		clientID:            get("client_id"),
		redirectURI:         get("redirect_uri"),
		scope:               get("scope"),
		state:               get("state"),
		codeChallenge:       get("code_challenge"),
		codeChallengeMethod: get("code_challenge_method"),
		resource:            get("resource"),
	}
	if p.clientID != publicClientID {
		s.errorPage(w, http.StatusBadRequest, "Unknown client.")
		return p, false
	}
	if !redirectURIAllowed(p.redirectURI) {
		s.errorPage(w, http.StatusBadRequest, "Invalid redirect_uri.")
		return p, false
	}
	// redirect_uri is trusted from here: remaining failures bounce back to it.
	if p.responseType != "code" {
		s.redirectError(w, r, p, "unsupported_response_type", "only response_type=code is supported")
		return p, false
	}
	if p.codeChallenge == "" || p.codeChallengeMethod != "S256" {
		s.redirectError(w, r, p, "invalid_request", "PKCE with code_challenge_method=S256 is required")
		return p, false
	}
	if p.resource != s.resource {
		s.redirectError(w, r, p, "invalid_target", "resource must be the MCP URL")
		return p, false
	}
	return p, true
}

// redirectError bounces back to the (already validated) redirect_uri carrying an
// OAuth error and the original state.
func (s *Service) redirectError(w http.ResponseWriter, r *http.Request, p authzParams, code, desc string) {
	u, err := url.Parse(p.redirectURI)
	if err != nil { // unreachable: redirectURIAllowed already parsed it
		s.errorPage(w, http.StatusBadRequest, "Invalid redirect_uri.")
		return
	}
	q := u.Query()
	q.Set("error", code)
	if desc != "" {
		q.Set("error_description", desc)
	}
	if p.state != "" {
		q.Set("state", p.state)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// --- cookies ---

// csrfCookie builds the single-use, host-only consent CSRF cookie. Secure follows
// the base URL scheme (https in prod, plain in local dev). SameSite=Lax is enough:
// the consent form posts same-origin.
func (s *Service) csrfCookie(value string) *http.Cookie {
	return &http.Cookie{
		Name:     csrfCookieName,
		Value:    value,
		Path:     "/oauth/authorize",
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600, // 10 minutes to complete consent
	}
}

func (s *Service) clearCSRFCookie() *http.Cookie {
	c := s.csrfCookie("")
	c.MaxAge = -1
	return c
}

// --- HTML rendering ---

// consentView is the data the consent page renders. Every field is auto-escaped by
// html/template.
type consentView struct {
	ClientID      string
	RedirectURI   string
	Scope         string
	ScopeLabel    string
	State         string
	CodeChallenge string
	Resource      string
	UserEmail     string
	CSRF          string
}

var consentTmpl = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Authorize finance access</title>
<style>
  body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;color:#111}
  .card{border:1px solid #ddd;border-radius:12px;padding:1.5rem}
  h1{font-size:1.25rem;margin:0 0 .75rem}
  .muted{color:#666;font-size:.9rem}
  code{background:#f4f4f5;padding:.1rem .3rem;border-radius:4px}
  .actions{display:flex;gap:.75rem;margin-top:1.5rem}
  button{font:inherit;padding:.6rem 1.2rem;border-radius:8px;border:1px solid #ccc;cursor:pointer}
  button.approve{background:#111;color:#fff;border-color:#111}
  @media(prefers-color-scheme:dark){body{background:#0b0b0c;color:#eee}.card{border-color:#333}code{background:#1c1c1f}button{background:#1c1c1f;color:#eee;border-color:#333}button.approve{background:#eee;color:#111}}
</style></head>
<body>
<div class="card">
  <h1>Authorize read-only finance access</h1>
  <p><code>{{.ClientID}}</code> is requesting <strong>read-only</strong> access to your finance data
  (scope <code>{{.ScopeLabel}}</code>).</p>
  <p class="muted">Signed in as {{.UserEmail}}. Approving lets this connection read your accounts,
  transactions and balances through the finance MCP server. It cannot make changes.</p>
  <form method="post" action="/oauth/authorize">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <input type="hidden" name="response_type" value="code">
    <input type="hidden" name="client_id" value="{{.ClientID}}">
    <input type="hidden" name="redirect_uri" value="{{.RedirectURI}}">
    <input type="hidden" name="scope" value="{{.Scope}}">
    <input type="hidden" name="state" value="{{.State}}">
    <input type="hidden" name="code_challenge" value="{{.CodeChallenge}}">
    <input type="hidden" name="code_challenge_method" value="S256">
    <input type="hidden" name="resource" value="{{.Resource}}">
    <div class="actions">
      <button class="approve" type="submit" name="action" value="approve">Approve</button>
      <button type="submit" name="action" value="deny">Deny</button>
    </div>
  </form>
</div>
</body></html>`))

func (s *Service) renderConsent(w http.ResponseWriter, v consentView) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The consent page must never be framed (clickjacking of the Approve button).
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
	if err := consentTmpl.Execute(w, v); err != nil {
		slog.Error("oauth: render consent failed", "err", err)
	}
}

// errorPage writes a minimal, un-styled HTML error with the given status.
func (s *Service) errorPage(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("<!doctype html><meta charset=\"utf-8\"><title>Error</title><p>" +
		template.HTMLEscapeString(msg) + "</p>"))
}
