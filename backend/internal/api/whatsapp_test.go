package api

import (
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2/humatest"

	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// newWhatsAppTestAPI wires the WhatsApp operations onto a humatest API with the
// real auth middleware but no DB or sidecar. Every WhatsApp handler runs the
// friend gate before touching any dependency, so anonymous requests are rejected
// without dereferencing the nil Ent/WA deps.
func newWhatsAppTestAPI(t *testing.T) humatest.TestAPI {
	t.Helper()
	_, api := humatest.New(t)
	svc := auth.New(nil, auth.Config{})
	api.UseMiddleware(svc.Middleware)
	h := New(Deps{Auth: svc})
	h.registerWhatsApp(api)
	return api
}

// TestWhatsAppRequiresAuth verifies the friend gate is wired on the WhatsApp
// surface: an anonymous request (no session cookie) is 401 on every endpoint,
// never a 200 or a nil-deref panic. Guards against a new handler shipping ungated.
func TestWhatsAppRequiresAuth(t *testing.T) {
	api := newWhatsAppTestAPI(t)

	// GETs reach the resolver directly (no body to validate first).
	gets := []string{
		"/api/wa/templates",
		"/api/wa/templates/1",
		"/api/wa/lists",
		"/api/wa/lists/1",
		"/api/wa/batches",
	}
	for _, path := range gets {
		resp := api.Get(path)
		if resp.Code != http.StatusUnauthorized {
			t.Errorf("GET %s status = %d, want 401; body=%s", path, resp.Code, resp.Body.String())
		}
	}

	// A write with a valid body clears input validation, so the 401 proves the
	// gate ran (not a validation short-circuit).
	if resp := api.Post("/api/wa/templates", map[string]any{"name": "n", "body": "b"}); resp.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/wa/templates status = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Post("/api/wa/batches", map[string]any{"template_id": 1, "list_id": 1}); resp.Code != http.StatusUnauthorized {
		t.Errorf("POST /api/wa/batches status = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
}
