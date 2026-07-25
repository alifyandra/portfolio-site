package api

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/redis/go-redis/v9"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
	"github.com/alifyandra/portfolio-site/backend/internal/storage"
	"github.com/alifyandra/portfolio-site/backend/internal/whatsapp"
)

// enqueuer is the queue dependency the handlers need: place a job on the queue and
// report whether a queue is configured. *queue.Client satisfies it. Kept an interface
// (rather than the concrete *queue.Client) so the admin force-start path is unit
// testable without a live SQS, mirroring the scheduler's enqueuer seam. Nil-tolerant
// at the call sites (a nil enqueuer means "no worker").
type enqueuer interface {
	Enqueue(ctx context.Context, job queue.Job) error
	Configured() bool
}

// notifier sends the finance refresh-handshake notification (ADR 0016) when an
// ack-gated job is force-started from the admin console. *notify.Client satisfies it
// (and no-ops gracefully when ntfy is unconfigured); a nil notifier is tolerated — the
// forced run still sits awaiting_ack and can be acked directly. Mirrors the scheduler's
// notifier seam, so "Run now" exercises the same handshake the cron does.
type notifier interface {
	NotifyRefresh(ctx context.Context, runID int, jobName string) error
}

// Deps are the dependencies the API handlers need.
type Deps struct {
	Ent      *ent.Client
	Redis    *redis.Client
	Spotify  *spotify.Client
	Storage  *storage.Store
	Queue    enqueuer
	Notifier notifier
	Auth     *auth.Service
	WA       whatsapp.SidecarProvider
	// WhatsApp send caps (ADR 11), sourced from config so they can be tuned per
	// environment. Zero means the default is not wired; server.New always sets them.
	WaMaxBatchRecipients int
	WaMaxBatchesPerDay   int

	// Finance sync seam settings (ADR 0016), sourced from config. FinanceSyncAckToken
	// gates the ack endpoint (constant-time compare); FinanceBackfillYears bounds a
	// full backfill window; FinanceSyncOverlapDays is the incremental re-scan overlap
	// ComputeWindow applies.
	FinanceSyncAckToken    string
	FinanceBackfillYears   int
	FinanceSyncOverlapDays int
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
	h.registerAdmin(api)
	h.registerWork(api)
	h.registerFinance(api)
	h.registerFinanceRead(api)
	h.registerFinanceSync(api)
}
