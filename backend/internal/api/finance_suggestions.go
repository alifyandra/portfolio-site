package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// Recurrence Suggestions (ADR 0019): the owner's review queue for runs the detection
// pass found. Every route is admin-only with requireAdmin as the handler's first line,
// and the finance.sync ingest token reaches none of them. The pass only suggests; the
// accept route is the owner creating a declaration, never the pass.

// FinanceSuggestionEvidenceDTO is one transaction a suggestion rests on.
type FinanceSuggestionEvidenceDTO struct {
	TransactionID int     `json:"transaction_id"`
	PostedDate    string  `json:"posted_date" doc:"YYYY-MM-DD"`
	Amount        float64 `json:"amount" doc:"Signed, as on the transaction (money out negative), rounded to the cent"`
	Description   string  `json:"description"`
}

// FinanceSuggestionDTO is one stored suggestion with its evidence, newest first.
type FinanceSuggestionDTO struct {
	ID             int                            `json:"id"`
	Kind           string                         `json:"kind" enum:"bill,income" doc:"What accepting creates: a recurring bill (a run of debits) or a recurring income (a run of credits)"`
	Label          string                         `json:"label" doc:"Suggested name; accepting can replace it"`
	MatchPattern   string                         `json:"match_pattern" doc:"Suggested match pattern, shown to the owner to confirm, edit or clear when accepting (it is never copied unseen); empty when the run shares no distinctive text"`
	Cadence        string                         `json:"cadence" enum:"weekly,fortnightly,monthly,quarterly,annual"`
	ExpectedAmount float64                        `json:"expected_amount" doc:"Median magnitude of the evidence, rounded to the cent"`
	AnchorDate     string                         `json:"anchor_date" doc:"YYYY-MM-DD; the grid that best fits the evidence, on its oldest cycle"`
	NextExpected   string                         `json:"next_expected" doc:"YYYY-MM-DD; the grid's next date on or after today"`
	Gaps           int                            `json:"gaps" doc:"Cycles the run skipped between its oldest and newest evidence row; 0 for an unbroken run"`
	Status         string                         `json:"status" enum:"pending,accepted,dismissed"`
	DecidedAt      *string                        `json:"decided_at" doc:"When the owner accepted or dismissed it; null while pending"`
	AccountID      *int                           `json:"account_id" doc:"The account every evidence row posted to"`
	AccountName    string                         `json:"account_name"`
	BillID         *int                           `json:"bill_id" doc:"The recurring bill accepting created; null otherwise. Deleting that bill reopens the suggestion as pending"`
	IncomeID       *int                           `json:"income_id" doc:"The recurring income accepting created; null otherwise. Deleting that income reopens the suggestion as pending"`
	Evidence       []FinanceSuggestionEvidenceDTO `json:"evidence" doc:"The run's transactions, newest first: the newest 5 in the list, all of them from GET /api/admin/finance/suggestions/{id}"`
	EvidenceCount  int                            `json:"evidence_count" doc:"How many transactions the run rests on, whether or not evidence lists them all"`
	EvidenceSince  string                         `json:"evidence_since" doc:"YYYY-MM-DD the run's oldest transaction posted"`
	CreatedAt      string                         `json:"created_at"`
	UpdatedAt      string                         `json:"updated_at"`
}

type listFinanceSuggestionsInput struct {
	Status string `query:"status" default:"pending" enum:"pending,accepted,dismissed,all" doc:"Which suggestions to include"`
}

type listFinanceSuggestionsOutput struct {
	Body struct {
		Suggestions []FinanceSuggestionDTO `json:"suggestions"`
		Count       int                    `json:"count"`
	}
}

type scanFinanceSuggestionsOutput struct {
	Body struct {
		RowsScanned int `json:"rows_scanned" doc:"Posted rows inside the lookback window"`
		RunsFound   int `json:"runs_found" doc:"Runs the detection rule found"`
		Created     int `json:"created" doc:"New pending suggestions"`
		Refreshed   int `json:"refreshed" doc:"Pending suggestions updated with the current amount, grid and evidence"`
		Declared    int `json:"declared" doc:"Runs left alone because a live declaration already covers them (an ended one covers only up to its ended_on plus a week)"`
		Dismissed   int `json:"dismissed" doc:"Runs left alone because the owner dismissed them before"`
		Reopened    int `json:"reopened" doc:"Accepted suggestions put back to pending because their declaration ended and the run carried on"`
		Dropped     int `json:"dropped" doc:"Pending suggestions removed because their run is gone or now declared"`
	}
}

type suggestionIDInput struct {
	ID int `path:"id" doc:"Recurrence suggestion ID"`
}

type acceptFinanceSuggestionInput struct {
	ID   int `path:"id" doc:"Recurrence suggestion ID"`
	Body struct {
		Name         string `json:"name,omitempty" maxLength:"120" doc:"Name for the new declaration, at most 120 bytes; defaults to the suggestion's label"`
		MatchPattern string `json:"match_pattern" maxLength:"200" doc:"The owner's match pattern for the new declaration, at most 200 bytes: the suggestion's match_pattern as the owner confirmed or edited it, or \"\" to link payments by hand. Required, and never defaulted from the suggestion, because a declaration's match pattern is owner-typed"`
	}
}

type acceptFinanceSuggestionOutput struct {
	Body struct {
		Kind     string `json:"kind" enum:"bill,income"`
		BillID   *int   `json:"bill_id" doc:"The recurring bill created, for kind bill"`
		IncomeID *int   `json:"income_id" doc:"The recurring income created, for kind income"`
		Linked   int    `json:"linked" doc:"Evidence rows linked as the declaration's payments or receipts (method auto)"`
		Skipped  int    `json:"skipped" doc:"Evidence rows left unlinked: claimed elsewhere since the scan, or outside the cycle rules"`
	}
}

type financeSuggestionOutput struct {
	Body FinanceSuggestionDTO
}

func intPtrOrNil(v int) *int {
	if v == 0 {
		return nil
	}
	return &v
}

func toFinanceSuggestionDTO(v finance.SuggestionView) FinanceSuggestionDTO {
	dto := FinanceSuggestionDTO{
		ID:             v.ID,
		Kind:           v.Kind,
		Label:          v.Label,
		MatchPattern:   v.MatchPattern,
		Cadence:        v.Cadence,
		ExpectedAmount: roundCents(v.ExpectedAmount),
		AnchorDate:     v.AnchorDate.Format(dateLayout),
		NextExpected:   v.NextExpected.Format(dateLayout),
		Gaps:           v.Gaps,
		Status:         v.Status,
		AccountID:      intPtrOrNil(v.AccountID),
		AccountName:    v.AccountName,
		BillID:         intPtrOrNil(v.BillID),
		IncomeID:       intPtrOrNil(v.IncomeID),
		Evidence:       make([]FinanceSuggestionEvidenceDTO, 0, len(v.Evidence)),
		EvidenceCount:  v.EvidenceCount,
		EvidenceSince:  v.EvidenceSince.Format(dateLayout),
		CreatedAt:      v.CreatedAt.UTC().Format(http.TimeFormat),
		UpdatedAt:      v.UpdatedAt.UTC().Format(http.TimeFormat),
	}
	if v.DecidedAt != nil {
		s := v.DecidedAt.UTC().Format(http.TimeFormat)
		dto.DecidedAt = &s
	}
	for _, e := range v.Evidence {
		dto.Evidence = append(dto.Evidence, FinanceSuggestionEvidenceDTO{
			TransactionID: e.TransactionID,
			PostedDate:    e.PostedDate.Format(dateLayout),
			Amount:        roundCents(e.Amount),
			Description:   e.Description,
		})
	}
	return dto
}

func (h *Handler) registerFinanceSuggestions(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-finance-suggestions",
		Method:      http.MethodGet,
		Path:        "/api/admin/finance/suggestions",
		Summary:     "List recurrence suggestions with their newest evidence",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *listFinanceSuggestionsInput) (*listFinanceSuggestionsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		views, err := finance.ListSuggestions(ctx, h.deps.Ent, in.Status)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load suggestions", err)
		}
		out := &listFinanceSuggestionsOutput{}
		out.Body.Suggestions = make([]FinanceSuggestionDTO, 0, len(views))
		for _, v := range views {
			out.Body.Suggestions = append(out.Body.Suggestions, toFinanceSuggestionDTO(v))
		}
		out.Body.Count = len(views)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-finance-suggestion",
		Method:      http.MethodGet,
		Path:        "/api/admin/finance/suggestions/{id}",
		Summary:     "Get one recurrence suggestion with all of its evidence",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *suggestionIDInput) (*financeSuggestionOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		return h.suggestionResponse(ctx, in.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "scan-finance-suggestions",
		Method:      http.MethodPost,
		Path:        "/api/admin/finance/suggestions/scan",
		Summary:     "Scan the posted ledger for recurring runs and refresh the suggestions",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*scanFinanceSuggestionsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		sum, err := finance.ScanRecurrences(ctx, h.deps.Ent)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to scan for recurring runs", err)
		}
		out := &scanFinanceSuggestionsOutput{}
		out.Body.RowsScanned = sum.RowsScanned
		out.Body.RunsFound = sum.RunsFound
		out.Body.Created = sum.Created
		out.Body.Refreshed = sum.Refreshed
		out.Body.Declared = sum.Declared
		out.Body.Dismissed = sum.Dismissed
		out.Body.Reopened = sum.Reopened
		out.Body.Dropped = sum.Dropped
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "accept-finance-suggestion",
		Method:      http.MethodPost,
		Path:        "/api/admin/finance/suggestions/{id}/accept",
		Summary:     "Accept a suggestion: create the recurring bill or income and link its evidence",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *acceptFinanceSuggestionInput) (*acceptFinanceSuggestionOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		name := strings.TrimSpace(in.Body.Name)
		if err := checkIncomeName(name); err != nil { // the bill name carries the same 120-byte cap
			return nil, err
		}
		pattern := strings.TrimSpace(in.Body.MatchPattern)
		if err := checkIncomeMatchPattern(pattern); err != nil { // and the same 200-byte pattern cap
			return nil, err
		}
		res, err := finance.AcceptSuggestion(ctx, h.deps.Ent, in.ID, name, pattern)
		switch {
		case ent.IsNotFound(err):
			return nil, huma.Error404NotFound("suggestion not found")
		case errors.Is(err, finance.ErrSuggestionDecided), errors.Is(err, finance.ErrDeclarationNameTaken):
			return nil, huma.Error409Conflict(err.Error())
		case err != nil:
			return nil, huma.Error500InternalServerError("failed to accept the suggestion", err)
		}
		out := &acceptFinanceSuggestionOutput{}
		out.Body.Kind = res.Kind
		out.Body.BillID = intPtrOrNil(res.BillID)
		out.Body.IncomeID = intPtrOrNil(res.IncomeID)
		out.Body.Linked = res.Linked
		out.Body.Skipped = res.Skipped
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "dismiss-finance-suggestion",
		Method:      http.MethodPost,
		Path:        "/api/admin/finance/suggestions/{id}/dismiss",
		Summary:     "Dismiss a suggestion; the same run is not suggested again",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *suggestionIDInput) (*financeSuggestionOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		err := finance.DismissSuggestion(ctx, h.deps.Ent, in.ID)
		switch {
		case ent.IsNotFound(err):
			return nil, huma.Error404NotFound("suggestion not found")
		case errors.Is(err, finance.ErrSuggestionDecided):
			return nil, huma.Error409Conflict(err.Error())
		case err != nil:
			return nil, huma.Error500InternalServerError("failed to dismiss the suggestion", err)
		}
		return h.suggestionResponse(ctx, in.ID)
	})
}

// suggestionResponse reads one suggestion with all of its evidence as the response body.
func (h *Handler) suggestionResponse(ctx context.Context, id int) (*financeSuggestionOutput, error) {
	v, err := finance.GetSuggestion(ctx, h.deps.Ent, id)
	if ent.IsNotFound(err) {
		return nil, huma.Error404NotFound("suggestion not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load the suggestion", err)
	}
	return &financeSuggestionOutput{Body: toFinanceSuggestionDTO(v)}, nil
}
