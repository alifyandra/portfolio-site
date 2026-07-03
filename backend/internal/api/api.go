package api

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/redis/go-redis/v9"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
	"github.com/alifyandra/portfolio-site/backend/internal/whatsapp"
)

// Deps are the dependencies the API handlers need.
type Deps struct {
	Ent     *ent.Client
	Redis   *redis.Client
	Spotify *spotify.Client
	Storage *storage.Store
	Queue   *queue.Client
	Auth    *auth.Service
	WA      *whatsapp.Client
	// WhatsApp send caps (ADR 11), sourced from config so they can be tuned per
	// environment. Zero means the default is not wired; server.New always sets them.
	WaMaxBatchRecipients int
	WaMaxBatchesPerDay   int
}

// Handler holds dependencies and registers operations against a Huma API.
type Handler struct {
	deps Deps
}

// New builds an API Handler.
func New(deps Deps) *Handler {
	return &Handler{deps: deps}
}

// Register wires every operation onto the Huma API. Huma derives the OpenAPI
// spec from the input/output struct types registered here (see ADR 0005).
func (h *Handler) Register(api huma.API) {
	h.registerProjects(api)
	h.registerContact(api)
	h.registerSpotify(api)
	h.registerAuth(api)
	h.registerWhatsApp(api)
}
