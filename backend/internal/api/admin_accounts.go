package api

import (
	"context"
	"log/slog"
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

// descriptionMaxBytes mirrors the schema's MaxLen(2000), which Ent measures in BYTES.
// Huma's maxLength measures RUNES, so the two disagree for any non-ASCII text and the
// handler has to check bytes itself (see the pre-check below).
const descriptionMaxBytes = 2000

// updateAccountInput is a PATCH: both fields are pointers, so a body that omits a key
// leaves that column exactly as it was. A body carrying name/type/class is refused
// outright, because those keys do not exist on this struct and Huma's schema is closed.
type updateAccountInput struct {
	ID   int `path:"id" doc:"Account ID"`
	Body struct {
		Description    *string `json:"description,omitempty" maxLength:"2000" doc:"Owner-authored note on what this account is for, at most 2000 bytes (accented or emoji characters cost more than one); empty string clears it"`
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
		// Nothing below the application layer will catch either of these. Postgres holds
		// drawdown_policy as a plain varchar with a default and no check constraint, and
		// description as an unbounded varchar, so the column types accept anything. Ent's
		// generated validators are the only other guard, and a validator failure surfaces
		// as an opaque save error, not as a message a caller can act on. Both checks
		// therefore run here, before the write, and both are a 422.
		upd := h.deps.Ent.Account.UpdateOneID(in.ID)
		if in.Body.Description != nil {
			// Bytes, not runes: Ent's MaxLen(2000) is len(v), while Huma's maxLength tag
			// already applied above counts runes. A 2000-character note with accents or
			// emoji clears Huma and would fail Ent, so it has to be caught here.
			if len(*in.Body.Description) > descriptionMaxBytes {
				return nil, huma.Error422UnprocessableEntity("description is too long: at most 2000 bytes, and accented or emoji characters cost more than one byte each")
			}
			upd.SetDescription(*in.Body.Description)
		}
		if in.Body.DrawdownPolicy != nil {
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
			// Logged, not echoed. Huma serialises any error passed here into the response
			// body, and an Ent validator message names internal field paths that a caller
			// can do nothing with.
			slog.ErrorContext(ctx, "update-account: save failed", "account", in.ID, "err", err)
			return nil, huma.Error500InternalServerError("failed to update account")
		}
		// Re-read through the shared read service so the response is the same shape the
		// dashboard already renders (balance snapshot included), not a second hand-rolled
		// projection that can drift from FinanceAccountDTO. Single-id, so one PATCH does
		// not pay for a full list plus a snapshot query per account.
		view, err := finance.AccountByID(ctx, h.deps.Ent, acc.ID)
		if err != nil {
			slog.ErrorContext(ctx, "update-account: read-back failed", "account", in.ID, "err", err)
			return nil, huma.Error500InternalServerError("failed to load account")
		}
		return &adminAccountOutput{Body: toFinanceAccountDTO(view)}, nil
	})
}
