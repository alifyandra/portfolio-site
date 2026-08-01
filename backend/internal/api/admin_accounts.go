package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// The one write endpoint on the finance account row (portfolio-site#122). Everything
// else about an account (name, type, class, source, currency, watermark) is owned by
// the ingest and deliberately unreachable from here; only the two owner-authored
// columns the bank cannot know are writable. The finance read API stays GET-only
// (finance_read.go) and the MCP surface stays read-only (ADR 0017), so this is the
// only way description and drawdown_policy ever get set.

// updateAccountInput is a PATCH: both fields are pointers, so a body that omits a key
// leaves that column exactly as it was. A body carrying name/type/class is ignored
// rather than honoured, because those keys simply do not exist on this struct.
type updateAccountInput struct {
	ID   int `path:"id" doc:"Account ID"`
	Body struct {
		Description    *string `json:"description,omitempty" maxLength:"2000" doc:"Owner-authored note on what this account is for; empty string clears it"`
		DrawdownPolicy *string `json:"drawdown_policy,omitempty" enum:"unset,flexible,no_drawdown,emergency_only" doc:"Whether this balance is spendable; unset means not yet declared"`
	}
}

type adminAccountOutput struct {
	Body FinanceAccountDTO
}

func (h *Handler) registerAdminAccounts(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "update-account",
		Method:      http.MethodPatch,
		Path:        "/api/admin/accounts/{id}",
		Summary:     "Set an account's owner-authored description and drawdown policy",
		Description: "Partial update of the two owner-authored columns only. Name, type, class, source and watermark are ingest-owned and cannot be changed here.",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateAccountInput) (*adminAccountOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		upd := h.deps.Ent.Account.UpdateOneID(in.ID)
		if in.Body.Description != nil {
			upd.SetDescription(*in.Body.Description)
		}
		if in.Body.DrawdownPolicy != nil {
			// Validate against the generated enum rather than coercing: an unknown value
			// would pass SQLite in tests and then trip the Postgres check constraint in
			// prod, so it has to be a 422 here.
			policy := account.DrawdownPolicy(*in.Body.DrawdownPolicy)
			if err := account.DrawdownPolicyValidator(policy); err != nil {
				return nil, huma.Error422UnprocessableEntity("drawdown_policy must be one of unset, flexible, no_drawdown, emergency_only")
			}
			upd.SetDrawdownPolicy(policy)
		}
		acc, err := upd.Save(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("account not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update account", err)
		}
		// Re-read through the shared read service so the response is the same shape the
		// dashboard already renders (balance snapshot included), not a second hand-rolled
		// projection that can drift from FinanceAccountDTO.
		views, err := finance.Accounts(ctx, h.deps.Ent)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load account", err)
		}
		for _, v := range views {
			if v.ID != acc.ID {
				continue
			}
			return &adminAccountOutput{Body: toFinanceAccountDTO(v)}, nil
		}
		return nil, huma.Error404NotFound("account not found")
	})
}
