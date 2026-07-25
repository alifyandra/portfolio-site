package oauth

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/go-chi/chi/v5"

	"github.com/alifyandra/portfolio-site/backend/ent"
	entuser "github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/mcp"
)

const (
	testBaseURL  = "https://api.aliflabs.dev"
	testResource = "https://api.aliflabs.dev/mcp"
)

// harness bundles an isolated in-memory DB, the auth + oauth services, a chi router
// with the OAuth routes mounted, and a couple of pre-made users/sessions.
type harness struct {
	t       *testing.T
	ctx     context.Context
	client  *ent.Client
	authSvc *auth.Service
	svc     *Service
	router  chi.Router

	admin        *ent.User
	member       *ent.User
	adminCookie  *http.Cookie
	memberCookie *http.Cookie
}

func newHarness(t *testing.T, enabled bool) *harness {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	authSvc := auth.New(client, auth.Config{})
	svc := New(client, authSvc, Config{Enabled: enabled, BaseURL: testBaseURL})
	r := chi.NewRouter()
	svc.Register(r)

	h := &harness{t: t, ctx: ctx, client: client, authSvc: authSvc, svc: svc, router: r}
	h.admin = h.mkUser("admin@x.dev", entuser.RoleAdmin)
	h.member = h.mkUser("member@x.dev", entuser.RoleMember)
	h.adminCookie = h.session(h.admin)
	h.memberCookie = h.session(h.member)
	return h
}

func (h *harness) mkUser(email string, role entuser.Role) *ent.User {
	h.t.Helper()
	u, err := h.client.User.Create().SetEmail(email).SetRole(role).Save(h.ctx)
	if err != nil {
		h.t.Fatalf("create user %s: %v", email, err)
	}
	return u
}

// session inserts a Session row for u and returns the raw-token cookie. Mirrors the
// auth service's at-rest hashing (sha256 hex) so no auth internals are needed.
func (h *harness) session(u *ent.User) *http.Cookie {
	h.t.Helper()
	raw := "sess-" + u.Email + "-" + time.Now().String()
	sum := sha256.Sum256([]byte(raw))
	_, err := h.client.Session.Create().
		SetTokenHash(hex.EncodeToString(sum[:])).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetOwner(u).
		Save(h.ctx)
	if err != nil {
		h.t.Fatalf("create session: %v", err)
	}
	return &http.Cookie{Name: "session", Value: raw}
}

func (h *harness) get(path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func (h *harness) postForm(path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

// --- PKCE + query helpers ---

func pkce() (verifier, challenge string) {
	verifier = "verifier-0123456789012345678901234567890123456789"
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return
}

func authzQuery(challenge string) url.Values {
	return url.Values{
		"response_type":         {"code"},
		"client_id":             {publicClientID},
		"redirect_uri":          {claudeRedirectURI},
		"scope":                 {"finance.read offline_access"},
		"state":                 {"state-xyz"},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
		"resource":              {testResource},
	}
}

func cookieFrom(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	resp := http.Response{Header: rec.Header()}
	for _, c := range resp.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// mintCode drives the consent flow (GET then approve POST) and returns the raw code
// from the redirect. It fails the test if the flow does not produce a code.
func (h *harness) mintCode(q url.Values, sess *http.Cookie) string {
	h.t.Helper()
	getRec := h.get("/oauth/authorize?"+q.Encode(), sess)
	if getRec.Code != http.StatusOK {
		h.t.Fatalf("consent GET: got %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	csrf := cookieFrom(getRec, csrfCookieName)
	if csrf == nil || csrf.Value == "" {
		h.t.Fatalf("consent GET did not set a csrf cookie")
	}
	form := url.Values{}
	for k := range q {
		form.Set(k, q.Get(k))
	}
	form.Set("csrf", csrf.Value)
	form.Set("action", "approve")
	postRec := h.postForm("/oauth/authorize", form, sess, &http.Cookie{Name: csrfCookieName, Value: csrf.Value})
	if postRec.Code != http.StatusFound {
		h.t.Fatalf("consent POST: got %d, want 302; body=%s", postRec.Code, postRec.Body.String())
	}
	loc, _ := url.Parse(postRec.Header().Get("Location"))
	code := loc.Query().Get("code")
	if code == "" {
		h.t.Fatalf("consent POST redirect had no code: %s", postRec.Header().Get("Location"))
	}
	return code
}

func (h *harness) tokenRequest(form url.Values) *httptest.ResponseRecorder {
	return h.postForm("/oauth/token", form)
}

// ============================ discovery / flag ============================

func TestDiscovery_FlagOn(t *testing.T) {
	h := newHarness(t, true)

	rec := h.get("/.well-known/oauth-protected-resource")
	if rec.Code != http.StatusOK {
		t.Fatalf("protected-resource: got %d, want 200", rec.Code)
	}
	var pr map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &pr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pr["resource"] != testResource {
		t.Errorf("resource = %v, want %v", pr["resource"], testResource)
	}
	if servers, _ := pr["authorization_servers"].([]any); len(servers) != 1 || servers[0] != testBaseURL {
		t.Errorf("authorization_servers = %v, want [%s]", pr["authorization_servers"], testBaseURL)
	}

	// The /mcp subpath variant must also serve it (Claude probes both).
	if rec := h.get("/.well-known/oauth-protected-resource/mcp"); rec.Code != http.StatusOK {
		t.Errorf("protected-resource/mcp: got %d, want 200", rec.Code)
	}

	rec = h.get("/.well-known/oauth-authorization-server")
	if rec.Code != http.StatusOK {
		t.Fatalf("auth-server metadata: got %d, want 200", rec.Code)
	}
	var as map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &as)
	if as["issuer"] != testBaseURL {
		t.Errorf("issuer = %v, want %v", as["issuer"], testBaseURL)
	}
	if as["authorization_endpoint"] != testBaseURL+"/oauth/authorize" {
		t.Errorf("authorization_endpoint = %v", as["authorization_endpoint"])
	}
	if as["token_endpoint"] != testBaseURL+"/oauth/token" {
		t.Errorf("token_endpoint = %v", as["token_endpoint"])
	}
	if methods, _ := as["code_challenge_methods_supported"].([]any); len(methods) != 1 || methods[0] != "S256" {
		t.Errorf("code_challenge_methods_supported = %v, want [S256]", as["code_challenge_methods_supported"])
	}
}

// TestFlagOff_DiscoveryAndOAuth404 asserts the whole OAuth surface is dark when the
// flag is off: discovery + /oauth/* all 404.
func TestFlagOff_DiscoveryAndOAuth404(t *testing.T) {
	h := newHarness(t, false)
	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
		"/.well-known/oauth-authorization-server",
		"/oauth/authorize?client_id=" + publicClientID,
	} {
		if rec := h.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s (flag off): got %d, want 404", path, rec.Code)
		}
	}
	if rec := h.postForm("/oauth/token", url.Values{"grant_type": {"authorization_code"}}); rec.Code != http.StatusNotFound {
		t.Errorf("POST /oauth/token (flag off): got %d, want 404", rec.Code)
	}
}

// ============================ authorize / consent ============================

// TestAuthorize_GetNeverMints: a GET renders consent but must NOT create a code.
func TestAuthorize_GetNeverMints(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()
	rec := h.get("/oauth/authorize?"+authzQuery(challenge).Encode(), h.adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("consent GET: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Approve") {
		t.Errorf("consent page missing Approve button")
	}
	if n := h.client.OAuthAuthCode.Query().CountX(h.ctx); n != 0 {
		t.Errorf("GET minted %d codes, want 0", n)
	}
}

// TestConsent_PostApproveMints: only an explicit approve POST with a valid CSRF
// token mints a code.
func TestConsent_PostApproveMints(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()
	code := h.mintCode(authzQuery(challenge), h.adminCookie)
	if code == "" {
		t.Fatal("no code minted")
	}
	if n := h.client.OAuthAuthCode.Query().CountX(h.ctx); n != 1 {
		t.Errorf("want exactly 1 code row, got %d", n)
	}
}

// TestConsent_CSRFRequired: a POST without a matching CSRF token is refused and
// mints nothing — a logged-in session alone is not consent.
func TestConsent_CSRFRequired(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()
	form := authzQuery(challenge)
	form.Set("action", "approve")
	// No csrf cookie, no csrf field.
	rec := h.postForm("/oauth/authorize", form, h.adminCookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("consent POST without CSRF: got %d, want 403", rec.Code)
	}
	if n := h.client.OAuthAuthCode.Query().CountX(h.ctx); n != 0 {
		t.Errorf("forged POST minted %d codes, want 0", n)
	}
	// A mismatched token (cookie != field) is also refused.
	rec = h.postForm("/oauth/authorize", withCSRF(form, "field-token"), h.adminCookie, &http.Cookie{Name: csrfCookieName, Value: "cookie-token"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("consent POST with mismatched CSRF: got %d, want 403", rec.Code)
	}
}

func withCSRF(base url.Values, token string) url.Values {
	out := url.Values{}
	for k := range base {
		out.Set(k, base.Get(k))
	}
	out.Set("csrf", token)
	return out
}

// TestAuthorize_NonAdminRefused: a member session is refused outright (403), not
// redirected to login (which would loop), and mints nothing.
func TestAuthorize_NonAdminRefused(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()
	rec := h.get("/oauth/authorize?"+authzQuery(challenge).Encode(), h.memberCookie)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member GET authorize: got %d, want 403 (no login redirect loop)", rec.Code)
	}
	// A member cannot mint via a direct POST either.
	form := authzQuery(challenge)
	rec = h.postForm("/oauth/authorize", withCSRF(form, "t"), h.memberCookie, &http.Cookie{Name: csrfCookieName, Value: "t"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("member POST authorize: got %d, want 403", rec.Code)
	}
	if n := h.client.OAuthAuthCode.Query().CountX(h.ctx); n != 0 {
		t.Errorf("member minted %d codes, want 0", n)
	}
}

// TestAuthorize_AnonymousRedirectsToLogin: no session -> 302 to the Google login
// with a safe internal return_to back to the authorize request.
func TestAuthorize_AnonymousRedirectsToLogin(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()
	rec := h.get("/oauth/authorize?" + authzQuery(challenge).Encode())
	if rec.Code != http.StatusFound {
		t.Fatalf("anonymous GET authorize: got %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Path != "/api/auth/google/login" {
		t.Fatalf("redirect path = %q, want /api/auth/google/login", loc.Path)
	}
	rt := loc.Query().Get("return_to")
	if !strings.HasPrefix(rt, "/oauth/authorize") {
		t.Errorf("return_to = %q, want an internal /oauth/authorize path", rt)
	}
}

// TestAuthorize_RejectsBadRedirectURI: a non-allowlisted redirect_uri is a hard 400
// (never a redirect), so it can never be an open redirect.
func TestAuthorize_RejectsBadRedirectURI(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()
	q := authzQuery(challenge)
	q.Set("redirect_uri", "https://evil.example/callback")
	rec := h.get("/oauth/authorize?"+q.Encode(), h.adminCookie)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad redirect_uri: got %d, want 400", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Errorf("bad redirect_uri produced a redirect to %q (open-redirect risk)", loc)
	}
}

// TestAuthorize_RejectsMissingOrWrongPKCEMethod: absent or non-S256
// code_challenge_method bounces back with invalid_request.
func TestAuthorize_RejectsMissingOrWrongPKCEMethod(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()

	cases := map[string]func(url.Values){
		"missing method":    func(q url.Values) { q.Del("code_challenge_method") },
		"plain method":      func(q url.Values) { q.Set("code_challenge_method", "plain") },
		"missing challenge": func(q url.Values) { q.Del("code_challenge") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			q := authzQuery(challenge)
			mutate(q)
			rec := h.get("/oauth/authorize?"+q.Encode(), h.adminCookie)
			if rec.Code != http.StatusFound {
				t.Fatalf("got %d, want 302 error redirect", rec.Code)
			}
			loc, _ := url.Parse(rec.Header().Get("Location"))
			if loc.Query().Get("error") != "invalid_request" {
				t.Errorf("error = %q, want invalid_request", loc.Query().Get("error"))
			}
		})
	}
}

// TestAuthorize_RejectsWrongResource: resource != MCP URL bounces with invalid_target.
func TestAuthorize_RejectsWrongResource(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()
	q := authzQuery(challenge)
	q.Set("resource", "https://api.aliflabs.dev/other")
	rec := h.get("/oauth/authorize?"+q.Encode(), h.adminCookie)
	if rec.Code != http.StatusFound {
		t.Fatalf("wrong resource: got %d, want 302", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("error") != "invalid_target" {
		t.Errorf("error = %q, want invalid_target", loc.Query().Get("error"))
	}
}

// ============================ token endpoint ============================

// TestToken_HappyPath: a redeemed code yields an access token (+ refresh when
// offline_access), and the code is one-time (a second redemption fails).
func TestToken_HappyPath(t *testing.T) {
	h := newHarness(t, true)
	verifier, challenge := pkce()
	code := h.mintCode(authzQuery(challenge), h.adminCookie)

	rec := h.tokenRequest(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {claudeRedirectURI},
		"resource":      {testResource},
		"client_id":     {publicClientID},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("token: got %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var tok map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	if tok["token_type"] != "Bearer" || tok["access_token"] == "" {
		t.Errorf("bad token response: %v", tok)
	}
	if tok["refresh_token"] == nil || tok["refresh_token"] == "" {
		t.Errorf("offline_access was requested but no refresh_token issued")
	}

	// One-time: redeeming the same code again fails.
	rec2 := h.tokenRequest(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {claudeRedirectURI},
		"resource":      {testResource},
		"client_id":     {publicClientID},
	})
	assertTokenError(t, rec2, "invalid_grant")
}

// TestToken_PKCEMismatch: a wrong code_verifier is rejected.
func TestToken_PKCEMismatch(t *testing.T) {
	h := newHarness(t, true)
	_, challenge := pkce()
	code := h.mintCode(authzQuery(challenge), h.adminCookie)
	rec := h.tokenRequest(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {"the-WRONG-verifier-000000000000000000000000"},
		"redirect_uri":  {claudeRedirectURI},
		"resource":      {testResource},
		"client_id":     {publicClientID},
	})
	assertTokenError(t, rec, "invalid_grant")
}

// TestToken_RejectsJSONBody: the token endpoint speaks only form-encoding.
func TestToken_RejectsJSONBody(t *testing.T) {
	h := newHarness(t, true)
	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(`{"grant_type":"authorization_code"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	assertTokenError(t, rec, "invalid_request")
}

// TestToken_RefreshRotation: refreshing rotates the token; the old refresh dies and
// the new one works.
func TestToken_RefreshRotation(t *testing.T) {
	h := newHarness(t, true)
	verifier, challenge := pkce()
	code := h.mintCode(authzQuery(challenge), h.adminCookie)
	first := h.tokenRequest(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {claudeRedirectURI},
		"resource":      {testResource},
		"client_id":     {publicClientID},
	})
	var t1 map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &t1)
	refresh1, _ := t1["refresh_token"].(string)
	if refresh1 == "" {
		t.Fatal("no refresh token from initial grant")
	}

	// Rotate.
	rotate := h.tokenRequest(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh1},
		"client_id":     {publicClientID},
	})
	if rotate.Code != http.StatusOK {
		t.Fatalf("refresh: got %d, want 200; body=%s", rotate.Code, rotate.Body.String())
	}
	var t2 map[string]any
	_ = json.Unmarshal(rotate.Body.Bytes(), &t2)
	refresh2, _ := t2["refresh_token"].(string)
	if refresh2 == "" || refresh2 == refresh1 {
		t.Fatalf("rotation did not issue a new refresh token (r1=%q r2=%q)", refresh1, refresh2)
	}

	// Old refresh token is now dead.
	deadRec := h.tokenRequest(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh1},
		"client_id":     {publicClientID},
	})
	assertTokenError(t, deadRec, "invalid_grant")

	// New refresh token still works.
	liveRec := h.tokenRequest(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refresh2},
		"client_id":     {publicClientID},
	})
	if liveRec.Code != http.StatusOK {
		t.Fatalf("rotated refresh token rejected: got %d", liveRec.Code)
	}
}

// TestToken_CodeExpires: a code past its TTL is rejected.
func TestToken_CodeExpires(t *testing.T) {
	h := newHarness(t, true)
	verifier, challenge := pkce()
	code := h.mintCode(authzQuery(challenge), h.adminCookie)

	// Advance the server clock past the 60s code TTL.
	h.svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }

	rec := h.tokenRequest(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {claudeRedirectURI},
		"resource":      {testResource},
		"client_id":     {publicClientID},
	})
	assertTokenError(t, rec, "invalid_grant")
}

// TestToken_UnknownClient: a wrong client_id is invalid_client.
func TestToken_UnknownClient(t *testing.T) {
	h := newHarness(t, true)
	rec := h.tokenRequest(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"whatever"},
		"client_id":     {"not-our-client"},
	})
	assertTokenError(t, rec, "invalid_client")
}

func assertTokenError(t *testing.T, rec *httptest.ResponseRecorder, wantErr string) {
	t.Helper()
	if rec.Code == http.StatusOK {
		t.Fatalf("expected an OAuth error %q, got 200: %s", wantErr, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v (%s)", err, rec.Body.String())
	}
	if body["error"] != wantErr {
		t.Errorf("error = %v, want %v", body["error"], wantErr)
	}
}

// ============================ redirect allowlist unit ============================

func TestRedirectURIAllowed(t *testing.T) {
	allow := []string{
		claudeRedirectURI,
		"http://127.0.0.1:53210/callback",
		"http://localhost:8080/cb",
		"http://localhost/",
	}
	deny := []string{
		"https://claude.ai/evil",
		"https://claude.ai/api/mcp/auth_callback/extra",
		"https://evil.example/callback",
		"http://evil.example/cb",
		"https://127.0.0.1/cb", // https loopback is not in the loopback rule (http only)
		"ftp://localhost/cb",
		"",
		"http://localhost#frag",
	}
	for _, u := range allow {
		if !redirectURIAllowed(u) {
			t.Errorf("redirectURIAllowed(%q) = false, want true", u)
		}
	}
	for _, u := range deny {
		if redirectURIAllowed(u) {
			t.Errorf("redirectURIAllowed(%q) = true, want false", u)
		}
	}
}

func TestBaseURLFromRedirect(t *testing.T) {
	cases := map[string]string{
		"https://api.aliflabs.dev/api/auth/google/callback": "https://api.aliflabs.dev",
		"http://localhost:8080/api/auth/google/callback":    "http://localhost:8080",
		"":          "https://fallback.dev",
		"not a url": "https://fallback.dev",
	}
	for in, want := range cases {
		if got := BaseURLFromRedirect(in, "https://fallback.dev"); got != want {
			t.Errorf("BaseURLFromRedirect(%q) = %q, want %q", in, got, want)
		}
	}
}

// ============================ MCP resource server ============================

// mcpHandler builds the /mcp handler wired to this harness's services, in the given
// OAuth mode. Mirrors server.New's wiring.
func (h *harness) mcpHandler(oauthEnabled bool) http.Handler {
	return mcp.Handler(mcp.Deps{
		Ent:                          h.client,
		Auth:                         h.authSvc,
		OAuthEnabled:                 oauthEnabled,
		ChallengeResourceMetadataURL: h.svc.ResourceMetadataURL(),
		VerifyOAuthToken:             h.svc.VerifyMCPAccess,
	})
}

func mcpInitRequest(bearer string) *http.Request {
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return req
}

// staticBearer mints a finance.read ApiToken for the admin and returns the raw token.
func (h *harness) staticBearer() string {
	h.t.Helper()
	raw, _, err := h.authSvc.MintApiToken(h.ctx, h.admin.ID, "test", "test-runner", []string{"finance.read"}, nil)
	if err != nil {
		h.t.Fatalf("mint api token: %v", err)
	}
	return raw
}

// oauthAccessToken runs the full flow and returns a live OAuth access token.
func (h *harness) oauthAccessToken() string {
	h.t.Helper()
	verifier, challenge := pkce()
	code := h.mintCode(authzQuery(challenge), h.adminCookie)
	rec := h.tokenRequest(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"redirect_uri":  {claudeRedirectURI},
		"resource":      {testResource},
		"client_id":     {publicClientID},
	})
	var tok map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &tok)
	at, _ := tok["access_token"].(string)
	if at == "" {
		h.t.Fatalf("no access token: %s", rec.Body.String())
	}
	return at
}

func serve(hnd http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	hnd.ServeHTTP(rec, req)
	return rec
}

// TestMCP_FlagOff: static finance.read bearer works; challenge stays bearer-only
// (realm), never advertising resource_metadata.
func TestMCP_FlagOff(t *testing.T) {
	h := newHarness(t, false)
	hnd := h.mcpHandler(false)

	// Unauthenticated -> 401 with the legacy realm challenge.
	rec := serve(hnd, mcpInitRequest(""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}
	ch := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(ch, `realm="mcp"`) || strings.Contains(ch, "resource_metadata") {
		t.Errorf("flag-off challenge = %q, want realm form without resource_metadata", ch)
	}

	// Static finance.read bearer STILL works when the flag is off.
	if rec := serve(hnd, mcpInitRequest(h.staticBearer())); rec.Code != http.StatusOK {
		t.Errorf("static bearer (flag off): got %d, want 200", rec.Code)
	}
}

// TestMCP_FlagOn: the challenge advertises resource_metadata; BOTH the static
// finance.read bearer AND a live OAuth access token are accepted.
func TestMCP_FlagOn(t *testing.T) {
	h := newHarness(t, true)
	hnd := h.mcpHandler(true)

	rec := serve(hnd, mcpInitRequest(""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}
	ch := rec.Header().Get("WWW-Authenticate")
	if !strings.Contains(ch, "resource_metadata=") || !strings.Contains(ch, h.svc.ResourceMetadataURL()) {
		t.Errorf("flag-on challenge = %q, want resource_metadata form", ch)
	}

	// Static bearer still works alongside OAuth.
	if rec := serve(hnd, mcpInitRequest(h.staticBearer())); rec.Code != http.StatusOK {
		t.Errorf("static bearer (flag on): got %d, want 200", rec.Code)
	}
	// A live OAuth access token is accepted.
	if rec := serve(hnd, mcpInitRequest(h.oauthAccessToken())); rec.Code != http.StatusOK {
		t.Errorf("oauth token (flag on): got %d, want 200", rec.Code)
	}
}

// TestMCP_AudienceMismatchRejected: an OAuth token minted for a different audience
// is rejected at /mcp even when the flag is on.
func TestMCP_AudienceMismatchRejected(t *testing.T) {
	h := newHarness(t, true)
	hnd := h.mcpHandler(true)

	// Mint a token bound to the WRONG resource (audience) directly via the store.
	pair, err := h.svc.issueTokens(h.ctx, h.admin.ID, publicClientID, "https://evil.example/mcp", "finance.read")
	if err != nil {
		t.Fatalf("issue tokens: %v", err)
	}
	rec := serve(hnd, mcpInitRequest(pair.AccessToken))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong-audience token: got %d, want 401", rec.Code)
	}
}

// TestMCP_RejectsRandomBearer: a bearer that is neither a static token nor an OAuth
// token is rejected.
func TestMCP_RejectsRandomBearer(t *testing.T) {
	h := newHarness(t, true)
	hnd := h.mcpHandler(true)
	if rec := serve(hnd, mcpInitRequest("not-a-real-token")); rec.Code != http.StatusUnauthorized {
		t.Errorf("random bearer: got %d, want 401", rec.Code)
	}
}
