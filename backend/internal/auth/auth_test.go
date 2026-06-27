package auth

import (
	"net/http"
	"testing"
	"time"

	"google.golang.org/api/idtoken"
)

func TestHashTokenIsDeterministicAndHex(t *testing.T) {
	a := hashToken("a-token")
	b := hashToken("a-token")
	if a != b {
		t.Fatalf("hashToken not deterministic: %q != %q", a, b)
	}
	if len(a) != 64 { // sha256 -> 32 bytes -> 64 hex chars
		t.Fatalf("expected 64 hex chars, got %d (%q)", len(a), a)
	}
	if hashToken("a-token") == hashToken("b-token") {
		t.Fatal("different inputs hashed to the same value")
	}
}

func TestRandomTokenUniqueAndURLSafe(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		tok, err := randomToken()
		if err != nil {
			t.Fatalf("randomToken: %v", err)
		}
		if tok == "" {
			t.Fatal("randomToken returned empty string")
		}
		for _, r := range tok {
			ok := (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_'
			if !ok {
				t.Fatalf("token %q contains non-URL-safe rune %q", tok, r)
			}
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token generated: %q", tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestRoleFor(t *testing.T) {
	svc := New(nil, Config{AdminEmails: []string{"Alif@Example.com", "  spaced@example.com "}})

	cases := map[string]string{
		"alif@example.com":    "admin", // case-insensitive match
		"ALIF@EXAMPLE.COM":    "admin",
		"spaced@example.com":  "admin", // allowlist entry was trimmed
		"someone@example.com": "member",
		"":                    "member",
	}
	for email, want := range cases {
		if got := string(svc.roleFor(email)); got != want {
			t.Errorf("roleFor(%q) = %q, want %q", email, got, want)
		}
	}
}

func TestParseClaims(t *testing.T) {
	valid := &idtoken.Payload{
		Subject: "google-sub-123",
		Claims: map[string]interface{}{
			"email":          "person@example.com",
			"email_verified": true,
			"name":           "A Person",
			"picture":        "https://example.com/p.jpg",
		},
	}
	c, err := parseClaims(valid)
	if err != nil {
		t.Fatalf("parseClaims(valid): %v", err)
	}
	if c.sub != "google-sub-123" || c.email != "person@example.com" || c.name != "A Person" || c.picture == "" {
		t.Fatalf("unexpected claims: %+v", c)
	}

	bad := map[string]*idtoken.Payload{
		"missing sub": {Subject: "", Claims: map[string]interface{}{"email": "p@e.com", "email_verified": true}},
		"unverified":  {Subject: "s", Claims: map[string]interface{}{"email": "p@e.com", "email_verified": false}},
		"no email":    {Subject: "s", Claims: map[string]interface{}{"email_verified": true}},
	}
	for name, p := range bad {
		if _, err := parseClaims(p); err == nil {
			t.Errorf("parseClaims(%s): expected error, got nil", name)
		}
	}
}

func TestShouldBump(t *testing.T) {
	now := time.Now()
	fresh := now.Add(sessionDuration)                   // just minted
	stale := now.Add(sessionDuration - 2*bumpThreshold) // well into the window

	if shouldBump(fresh, now) {
		t.Error("a freshly minted session should not be bumped")
	}
	if !shouldBump(stale, now) {
		t.Error("a session past the bump threshold should be bumped")
	}
}

func TestCookieAttributes(t *testing.T) {
	prod := New(nil, Config{CookieDomain: ".aliflabs.dev", CookieSecure: true})
	c := prod.SessionCookie("raw")
	if c.Name != sessionCookieName || c.Value != "raw" {
		t.Fatalf("unexpected name/value: %+v", c)
	}
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Domain != ".aliflabs.dev" || c.Path != "/" {
		t.Fatalf("prod cookie attributes wrong: %+v", c)
	}
	if c.MaxAge <= 0 {
		t.Fatalf("session cookie should have positive MaxAge, got %d", c.MaxAge)
	}

	cleared := prod.ClearSessionCookie()
	if cleared.MaxAge != -1 || cleared.Value != "" {
		t.Fatalf("clear cookie should expire immediately: %+v", cleared)
	}

	local := New(nil, Config{CookieSecure: false})
	lc := local.SessionCookie("raw")
	if lc.Secure {
		t.Error("local cookie should not be Secure over http")
	}
	if lc.Domain != "" {
		t.Errorf("local cookie should be host-only, got domain %q", lc.Domain)
	}
}
