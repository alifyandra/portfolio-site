package server

import (
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/api"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/digest"
	"github.com/alifyandra/portfolio-site/backend/internal/email"
	"github.com/alifyandra/portfolio-site/backend/internal/fargate"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
	"github.com/alifyandra/portfolio-site/backend/internal/whatsapp"
	"github.com/redis/go-redis/v9"
)

// Deps bundles everything the HTTP handlers need.
type Deps struct {
	Config  *config.Config
	Ent     *ent.Client
	Redis   *redis.Client
	Spotify *spotify.Client
	Storage *storage.Store
	Queue   *queue.Client
	Email   *email.Mailer
	Auth    *auth.Service
	// WA is the WhatsApp sidecar provider (static URL or per-batch Fargate task,
	// selected by WA_SIDECAR_MODE). Built in bootstrap; nil in cmd/spec, which the
	// create-batch handler tolerates (it 503s when the provider is nil).
	WA whatsapp.SidecarProvider
	// Digest builds the dated Digest; the worker runs it inline (DIGEST_MODE=local).
	// Built in bootstrap; nil in cmd/spec. See ADR 0013.
	Digest *digest.Builder
	// DigestLauncher runs digest.build on a run-to-completion Fargate task
	// (DIGEST_MODE=fargate); nil in local mode and in cmd/spec. See ADR 0013.
	DigestLauncher *fargate.Launcher
}

// New builds the Chi router with Huma mounted on it and all routes registered.
// It returns the router and the Huma API (the latter is reused by `cmd/spec`
// to emit the OpenAPI document — single source of truth for orval).
func New(deps *Deps) (http.Handler, huma.API) {
	r := chi.NewMux()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// App-layer rate limit per client IP (a speed bump; Cloudflare is the real
	// DDoS layer in prod). When TrustCloudflareIP is set (only at the proxy
	// cutover, once the origin SG is locked to Cloudflare's ranges) the limiter
	// keys off CF-Connecting-IP, the real visitor IP Cloudflare sets; otherwise it
	// keys off the connecting IP. Gating on an explicit flag rather than
	// IsProduction keeps the spoofable-header path inert until the origin lock is
	// live. RealIP above still normalizes RemoteAddr for logging.
	rateLimitKey := httprate.KeyByIP
	if deps.Config.TrustCloudflareIP {
		rateLimitKey = keyByCloudflareIP
	}
	r.Use(httprate.Limit(100, time.Minute, httprate.WithKeyFuncs(rateLimitKey)))

	// Credentials are enabled so the browser sends the session cookie on
	// cross-subdomain API calls (aliflabs.dev -> api.aliflabs.dev). For that to
	// work the allowed-origins list must stay exact (no wildcards). See ADR 10.
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   deps.Config.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	// Plain health check (not part of the OpenAPI contract).
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	humaCfg := huma.DefaultConfig("Portfolio API", "0.1.0")
	humaCfg.DocsPath = "/docs"
	humaCfg.Info.Description = "Backend API for alifyandra's portfolio site."

	// Register the cookie security scheme so protected operations show a lock in
	// /docs and the generated client knows they need the session cookie (ADR 10).
	if humaCfg.Components.SecuritySchemes == nil {
		humaCfg.Components.SecuritySchemes = map[string]*huma.SecurityScheme{}
	}
	humaCfg.Components.SecuritySchemes["cookieAuth"] = &huma.SecurityScheme{
		Type: "apiKey",
		In:   "cookie",
		Name: "session",
	}
	// Register the scope-only bearer scheme the external-runner work API uses (ADR
	// 0014). It is entirely separate from cookieAuth: work operations gate on token
	// scope, never on the admin/friend role, and a bearer identity is invisible to
	// the cookie-only admin console.
	humaCfg.Components.SecuritySchemes["bearerAuth"] = &huma.SecurityScheme{
		Type:   "http",
		Scheme: "bearer",
	}

	humaAPI := humachi.New(r, humaCfg)

	// Resolve the session cookie to a User on every request (best-effort). Guarded
	// because cmd/spec builds the API with a nil Auth purely to emit the spec.
	if deps.Auth != nil {
		humaAPI.UseMiddleware(deps.Auth.Middleware)
	}

	h := api.New(api.Deps{
		Ent:     deps.Ent,
		Redis:   deps.Redis,
		Spotify: deps.Spotify,
		Storage: deps.Storage,
		Queue:   deps.Queue,
		Auth:    deps.Auth,
		WA:      deps.WA,

		WaMaxBatchRecipients: deps.Config.WaMaxBatchRecipients,
		WaMaxBatchesPerDay:   deps.Config.WaMaxBatchesPerDay,

		FinanceSyncAckToken:    deps.Config.FinanceSyncAckToken,
		FinanceBackfillYears:   deps.Config.FinanceBackfillYears,
		FinanceSyncOverlapDays: deps.Config.FinanceSyncOverlapDays,
	})
	h.Register(humaAPI)

	// OAuth redirect flows are browser navigations, not JSON operations, so they
	// are plain Chi routes rather than Huma operations (no generated client hook).
	if deps.Auth != nil {
		r.Get("/api/auth/google/login", deps.Auth.LoginHandler)
		r.Get("/api/auth/google/callback", deps.Auth.CallbackHandler)
	}

	return r, humaAPI
}

// keyByCloudflareIP rate-limits per real visitor IP when running behind
// Cloudflare. CF sets CF-Connecting-IP to the originating client's address; it
// is only trustworthy once the origin security group is locked to Cloudflare's
// ranges (otherwise a client could spoof the header to evade the limit). When
// the header is absent or not a valid IP (e.g. a direct request) it falls back
// to the connecting IP, so the limiter degrades safely.
func keyByCloudflareIP(r *http.Request) (string, error) {
	// Validate the header is a single IP before trusting it as the limiter key.
	// A malformed or oversized value (spoofed, or just junk) would otherwise
	// bloat the limiter's key map; fall back to the connecting IP when invalid.
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); net.ParseIP(ip) != nil {
		return ip, nil
	}
	return httprate.KeyByIP(r)
}
