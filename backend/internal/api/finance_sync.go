package api

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// The finance sync seam (ADR 0016). Three small endpoints carry the run lifecycle
// and the window instruction for a scheduled finance refresh; the data itself still
// flows only through POST /api/finance/ingest. claim and complete reuse the work
// API's scope-only bearer gate on the finance.sync scope. ack is deliberately NOT
// bearer-authed: it rides in a notification action URL, so it is gated by a separate
// shared secret (FINANCE_SYNC_ACK_TOKEN) compared in constant time.

// defaultFinanceSource is the source whose accounts claim plans windows for when the
// caller does not name one. Kept generic; the finance source clamps to reality.
const defaultFinanceSource = "commbank"

// --- claim ---

type financeClaimInput struct {
	Source string `query:"source" default:"commbank" doc:"Finance source whose known accounts to plan windows for; defaults to commbank"`
}

// financeWindowDTO is one account's computed sync window. from/to are YYYY-MM-DD;
// backfill is true when the account has never been synced (nil watermark).
type financeWindowDTO struct {
	Account  string `json:"account"`
	From     string `json:"from"`
	To       string `json:"to"`
	Backfill bool   `json:"backfill"`
}

type financeClaimOutput struct {
	Body struct {
		Claimed bool               `json:"claimed" doc:"false when there is no claimable finance sync run"`
		RunID   int                `json:"run_id,omitempty" doc:"the leased run to pass back to /complete"`
		Windows []financeWindowDTO `json:"windows,omitempty" doc:"one window per known account of the source; empty when the source has no accounts yet (it discovers and backfills on first run)"`
	}
}

// --- ack ---

type financeAckInput struct {
	RunID int    `query:"run_id" required:"true" doc:"the awaiting_ack run to approve"`
	Token string `query:"token" doc:"the shared ack secret (FINANCE_SYNC_ACK_TOKEN); rides in the notification action URL"`
}

type financeAckOutput struct {
	Body struct {
		Acked bool `json:"acked" doc:"true once the run is (or already was) approved"`
	}
}

// --- complete ---

type financeCompleteInput struct {
	Body struct {
		// RunID/Status are optional at the schema level so the scope/auth gate wins
		// over a body-shape 422 (as in completeWork); the handler enforces them after
		// requireBearer.
		RunID  int    `json:"run_id,omitempty" doc:"the run returned by /claim, to close"`
		Status string `json:"status,omitempty" enum:"succeeded,failed" doc:"terminal outcome of the sync"`
	}
}

type financeCompleteOutput struct {
	Body struct {
		Done bool `json:"done" doc:"true when the run was marked terminal"`
	}
}

func (h *Handler) registerFinanceSync(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "claim-finance-sync",
		Method:      http.MethodGet,
		Path:        "/api/finance/sync/claim",
		Summary:     "Claim a claimable finance sync run and get its per-account windows (finance source)",
		Tags:        financeTags,
		Security:    bearerAuthSecurity,
	}, h.claimFinanceSync)

	huma.Register(api, huma.Operation{
		OperationID: "ack-finance-sync",
		Method:      http.MethodPost,
		Path:        "/api/finance/sync/ack",
		Summary:     "Approve a scheduled finance refresh, making its run claimable (token-gated)",
		Tags:        financeTags,
	}, h.ackFinanceSync)

	huma.Register(api, huma.Operation{
		OperationID: "complete-finance-sync",
		Method:      http.MethodPost,
		Path:        "/api/finance/sync/complete",
		Summary:     "Close a claimed finance sync run (finance source)",
		Tags:        financeTags,
		Security:    bearerAuthSecurity,
	}, h.completeFinanceSync)
}

// claimFinanceSync leases the oldest claimable finance.sync run and returns the sync
// window for each known account of the source. "Claimable" means status awaiting_ack
// with claimable_at set (a human approved the refresh) and not yet leased. The lease
// is a single conditional UPDATE (WHERE still awaiting_ack), so two concurrent
// claimers never take the same run: the loser matches zero rows and gets claimed=false.
func (h *Handler) claimFinanceSync(ctx context.Context, in *financeClaimInput) (*financeClaimOutput, error) {
	id, err := requireBearer(ctx, financeScope)
	if err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance sync is not available")
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		source = defaultFinanceSource
	}
	now := time.Now()
	out := &financeClaimOutput{}

	tx, err := h.deps.Ent.Tx(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to start claim", err)
	}

	// Oldest claimable finance.sync run.
	run, err := tx.JobRun.Query().
		Where(
			jobrun.HasJobWith(scheduledjob.KeyEQ(financeScope)),
			jobrun.StatusEQ(jobrun.StatusAwaitingAck),
			jobrun.ClaimableAtNotNil(),
		).
		Order(ent.Asc(jobrun.FieldClaimableAt), ent.Asc(jobrun.FieldID)).
		First(ctx)
	if ent.IsNotFound(err) {
		_ = tx.Rollback()
		return out, nil // no claimable run: Claimed stays false
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, huma.Error500InternalServerError("failed to find claimable run", err)
	}

	// Lease it: awaiting_ack -> running, only while still awaiting_ack. Zero rows means
	// another claimer won the race.
	leased, err := tx.JobRun.Update().
		Where(jobrun.IDEQ(run.ID), jobrun.StatusEQ(jobrun.StatusAwaitingAck)).
		SetStatus(jobrun.StatusRunning).
		SetStartedAt(now).
		SetRunner(id.Runner).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, huma.Error500InternalServerError("failed to lease run", err)
	}
	if leased == 0 {
		_ = tx.Rollback()
		return out, nil // lost the race: no work
	}

	// Compute one window per known account of the source. today is UTC-midnight (the
	// watermark's granularity); earliest is today minus the configured backfill span,
	// which the source clamps to what it actually offers.
	accounts, err := tx.Account.Query().
		Where(account.SourceEQ(source)).
		Order(ent.Asc(account.FieldName)).
		All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, huma.Error500InternalServerError("failed to load accounts", err)
	}
	today := now.UTC().Truncate(24 * time.Hour)
	years := h.deps.FinanceBackfillYears
	if years <= 0 {
		years = 8
	}
	earliest := today.AddDate(-years, 0, 0)
	overlap := h.deps.FinanceSyncOverlapDays

	windows := make([]financeWindowDTO, 0, len(accounts))
	for _, acc := range accounts {
		w := finance.ComputeWindow(acc.PostedWatermark, earliest, today, overlap)
		windows = append(windows, financeWindowDTO{
			Account:  acc.Name,
			From:     w.From.Format("2006-01-02"),
			To:       w.To.Format("2006-01-02"),
			Backfill: w.Backfill,
		})
	}

	if err := tx.Commit(); err != nil {
		return nil, huma.Error500InternalServerError("failed to commit claim", err)
	}

	out.Body.Claimed = true
	out.Body.RunID = run.ID
	out.Body.Windows = windows
	return out, nil
}

// ackFinanceSync approves a scheduled refresh, flipping a run awaiting_ack ->
// claimable (claimable_at set). It is gated by the shared ack token (constant-time
// compare), NOT the runner bearer, so it can ride in a notification action URL. It is
// idempotent: a run that is already claimable, or already claimed/terminal, is a
// success no-op. A run that is not a finance.sync run (or does not exist) is 404.
func (h *Handler) ackFinanceSync(ctx context.Context, in *financeAckInput) (*financeAckOutput, error) {
	// Fail CLOSED when unconfigured: an empty configured token means the ack endpoint
	// is not set up, so reject every request (never let an empty presented token match
	// an empty secret via the constant-time compare). This keeps a prod that forgot to
	// seed the secret locked, not open. Only compare when a real secret is configured.
	configured := h.deps.FinanceSyncAckToken
	if configured == "" {
		return nil, huma.Error401Unauthorized("ack endpoint is not configured")
	}
	if subtle.ConstantTimeCompare([]byte(in.Token), []byte(configured)) != 1 {
		return nil, huma.Error401Unauthorized("invalid ack token")
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance sync is not available")
	}

	run, err := h.deps.Ent.JobRun.Query().
		Where(jobrun.IDEQ(in.RunID)).
		WithJob().
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, huma.Error404NotFound("run not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load run", err)
	}
	if run.Edges.Job == nil || run.Edges.Job.Key != financeScope {
		return nil, huma.Error404NotFound("run not found")
	}

	out := &financeAckOutput{}
	// Only an awaiting_ack run needs flipping; anything else is already past this gate,
	// so ack is an idempotent success no-op.
	if run.Status == jobrun.StatusAwaitingAck {
		if err := h.deps.Ent.JobRun.UpdateOneID(run.ID).SetClaimableAt(time.Now()).Exec(ctx); err != nil {
			return nil, huma.Error500InternalServerError("failed to approve refresh", err)
		}
	}
	out.Body.Acked = true
	return out, nil
}

// completeFinanceSync closes a claimed finance.sync run succeeded or failed after the
// source has delivered its windows through the ingest endpoint. Bearer-gated on the
// finance.sync scope; idempotent (re-completing rewrites the same terminal state).
func (h *Handler) completeFinanceSync(ctx context.Context, in *financeCompleteInput) (*financeCompleteOutput, error) {
	id, err := requireBearer(ctx, financeScope)
	if err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance sync is not available")
	}
	if in.Body.RunID == 0 {
		return nil, huma.Error422UnprocessableEntity("run_id is required")
	}
	target := jobrun.StatusSucceeded
	switch in.Body.Status {
	case "succeeded":
		target = jobrun.StatusSucceeded
	case "failed":
		target = jobrun.StatusFailed
	default:
		return nil, huma.Error422UnprocessableEntity("status must be succeeded or failed")
	}

	run, err := h.deps.Ent.JobRun.Query().
		Where(jobrun.IDEQ(in.Body.RunID)).
		WithJob().
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil, huma.Error404NotFound("run not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load run", err)
	}
	if run.Edges.Job == nil || run.Edges.Job.Key != financeScope {
		return nil, huma.Error403Forbidden("run does not belong to the finance sync job")
	}

	// State guard. Only a claimed (running) run may be closed; a re-complete to the
	// SAME terminal status is an idempotent no-op (safe under retry). Any other state
	// -- awaiting_ack (unclaimed), or the OTHER terminal status (e.g. a reaper-failed
	// run being flipped to succeeded) -- is a conflict and mutates nothing. This keeps
	// an unclaimed or already-terminal run from being silently overwritten.
	out := &financeCompleteOutput{}
	switch {
	case run.Status == jobrun.StatusRunning:
		// proceed to close it below
	case run.Status == target:
		out.Body.Done = true // idempotent re-complete
		return out, nil
	default:
		return nil, huma.Error409Conflict("run is not in a completable state (only a claimed, running run can be completed)")
	}

	if err := h.deps.Ent.JobRun.UpdateOneID(run.ID).
		SetStatus(target).
		SetFinishedAt(time.Now()).
		SetRunner(id.Runner).
		Exec(ctx); err != nil {
		return nil, huma.Error500InternalServerError("failed to close run", err)
	}

	out.Body.Done = true
	return out, nil
}
