package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// The finance ingest API (portfolio-site#84). A single write-only, idempotent
// endpoint the private home broker POSTs a scraped window to. It reuses the work
// API's scope-only bearer gate (ADR 0014): the token carries the finance.sync
// scope and nothing else, so a leaked ingest credential can only write finance
// data and can never reach the admin console or the friend tools.

// financeTags groups the finance ingest under its own OpenAPI tag.
var financeTags = []string{"finance"}

// financeScope is the ScheduledJob key / scope string the ingest token must carry.
const financeScope = "finance.sync"

type ingestInput struct {
	Body finance.Payload
}

type ingestOutput struct {
	Body finance.Summary
}

func (h *Handler) registerFinance(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "ingest-finance",
		Method:      http.MethodPost,
		Path:        "/api/finance/ingest",
		Summary:     "Ingest a scraped personal-finance window (external broker runner)",
		Tags:        financeTags,
		Security:    bearerAuthSecurity,
	}, h.ingestFinance)
}

// ingestFinance gates on the finance.sync scope, then hands the whole payload to
// finance.Ingest, which persists it in one transaction. requireBearer is the access
// gate: a well-formed request from a token without finance.sync gets 403, and a
// request with no bearer gets 401. Unlike completeWork, this handler does NOT
// guarantee the auth check wins over a body-shape 422: the Payload has required
// fields, enums, and additionalProperties:false, so Huma validates the body before
// the handler runs, and an anonymous request with a malformed body can 422 before
// the auth check. That is harmless: the 422 path performs no mutation, and the
// schema it reveals is already public via /docs. The token owner is not attributed
// onto the rows: v1 finance data is single-tenant (Alif's), so there is no
// per-owner scoping to enforce here.
func (h *Handler) ingestFinance(ctx context.Context, in *ingestInput) (*ingestOutput, error) {
	if _, err := requireBearer(ctx, financeScope); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance ingest is not available")
	}
	sum, err := finance.Ingest(ctx, h.deps.Ent, &in.Body)
	if err != nil {
		// A real (committing) run that failed reconciliation is a 422, not a 500:
		// nothing was persisted (Ingest already rolled back), and the discrepancies
		// tell the broker why the window was refused. A dry run never reaches here
		// with this error (it returns 200 with the unreconciled Summary instead).
		var re *finance.ReconciliationError
		if errors.As(err, &re) {
			return nil, huma.Error422UnprocessableEntity(re.Error())
		}
		return nil, huma.Error500InternalServerError("failed to ingest finance data", err)
	}
	out := &ingestOutput{}
	out.Body = *sum
	return out, nil
}
