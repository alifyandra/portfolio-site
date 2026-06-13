package api

import (
	"github.com/danielgtaylor/huma/v2"
	"github.com/redis/go-redis/v9"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
)

// Deps are the dependencies the API handlers need.
type Deps struct {
	Ent     *ent.Client
	Redis   *redis.Client
	Spotify *spotify.Client
	Storage *storage.Store
	Queue   *queue.Client
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
}
