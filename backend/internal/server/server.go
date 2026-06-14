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
	"github.com/alifyandra/portfolio-site/backend/internal/config"
	"github.com/alifyandra/portfolio-site/backend/internal/email"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
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
	// DDoS layer in prod). In production the limiter keys off CF-Connecting-IP
	// (the real visitor IP Cloudflare sets), trustworthy once the origin SG is
	// locked to Cloudflare's ranges; elsewhere it keys off the connecting IP.
	// RealIP above still normalizes RemoteAddr for logging.
	rateLimitKey := httprate.KeyByIP
	if deps.Config.IsProduction() {
		rateLimitKey = keyByCloudflareIP
	}
	r.Use(httprate.Limit(100, time.Minute, httprate.WithKeyFuncs(rateLimitKey)))

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   deps.Config.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Content-Type", "Authorization"},
		AllowCredentials: false,
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
	humaAPI := humachi.New(r, humaCfg)

	h := api.New(api.Deps{
		Ent:     deps.Ent,
		Redis:   deps.Redis,
		Spotify: deps.Spotify,
		Storage: deps.Storage,
		Queue:   deps.Queue,
	})
	h.Register(humaAPI)

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
