package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/billpayment"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// Recurring-bill writes (portfolio-site#125). Bills are declared by the owner, so every
// mutation is admin-only: cookie-gated with requireAdmin as the handler's first line, the
// same server-enforced gate the rest of /api/admin/* uses. The finance.sync ingest token
// can never reach these routes, so an ingest run never touches a declaration.
//
// The READ of the same data lives at GET /api/finance/bills beside its dashboard siblings
// (finance_read.go). Reads under /api/finance/* and writes under /api/admin/finance/* is
// deliberate: it matches what is already there rather than harmonising one of them.
//
// The admin list calls the same finance.ListRecurringBills the dashboard read does, so the
// two can never report different derived figures.

// BillPaymentDTO is one reconciliation link: which cycle of which bill a posted
// transaction settled, and whether the matcher or the owner made the link. There is no
// amount here on purpose; the amount lives on the transaction, one hop away.
type BillPaymentDTO struct {
	ID             int    `json:"id"`
	BillID         int    `json:"bill_id"`
	TransactionID  int    `json:"transaction_id"`
	OccurrenceDate string `json:"occurrence_date" doc:"YYYY-MM-DD cycle due date this payment settles (not the posted date)"`
	Method         string `json:"method" enum:"auto,manual"`
	CreatedAt      string `json:"created_at"`
}

type listAdminBillsOutput struct {
	Body struct {
		Bills             []FinanceBillDTO `json:"bills"`
		CommittedTotal    float64          `json:"committed_total"`
		MonthlyEquivalent float64          `json:"monthly_equivalent"`
		Count             int              `json:"count"`
	}
}

type adminBillOutput struct {
	Body FinanceBillDTO
}

type createBillInput struct {
	Body struct {
		Name               string   `json:"name" minLength:"1" maxLength:"120" doc:"Label and human identity key; must be unique"`
		Payee              string   `json:"payee,omitempty" maxLength:"120" doc:"Who actually gets paid; display only, never matched against"`
		ExpectedAmount     float64  `json:"expected_amount" exclusiveMinimum:"0" doc:"Expected charge per cycle as a positive magnitude"`
		Currency           string   `json:"currency,omitempty" maxLength:"3" doc:"ISO currency code; defaults to AUD"`
		Cadence            string   `json:"cadence,omitempty" enum:"weekly,fortnightly,monthly,quarterly,annual" doc:"Defaults to monthly"`
		AnchorDate         string   `json:"anchor_date" doc:"One known due date, YYYY-MM-DD; every occurrence steps the cadence from here"`
		AmountVariable     bool     `json:"amount_variable,omitempty" doc:"True for a bill whose amount changes every cycle: the matcher then skips the amount check"`
		AmountTolerancePct *float64 `json:"amount_tolerance_pct,omitempty" minimum:"0" doc:"Percent either side of expected_amount a posted row may differ; defaults to 10"`
		MatchPattern       string   `json:"match_pattern,omitempty" maxLength:"200" doc:"Case-insensitive substring matched against a posted row's description or merchant; empty means never auto-matched"`
		MatchWindowDays    *int     `json:"match_window_days,omitempty" minimum:"0" doc:"Days either side of an occurrence a posted row may land; defaults to 5"`
		Status             string   `json:"status,omitempty" enum:"active,paused,ended" doc:"Defaults to active"`
		EndedOn            string   `json:"ended_on,omitempty" doc:"YYYY-MM-DD the commitment stopped"`
		Notes              string   `json:"notes,omitempty"`
		AccountID          *int     `json:"account_id,omitempty" doc:"Account the bill is paid from; optional, and when set it narrows auto-match scope"`
	}
}

// updateBillInput is a PATCH: every body field is a pointer so an omitted field leaves the
// column untouched. Two fields clear rather than set on a sentinel, since a PATCH pointer
// cannot otherwise express "unset": ended_on:"" clears the end date, and account_id:0
// detaches the account.
type updateBillInput struct {
	ID   int `path:"id" doc:"Recurring bill ID"`
	Body struct {
		Name               *string  `json:"name,omitempty" minLength:"1" maxLength:"120"`
		Payee              *string  `json:"payee,omitempty" maxLength:"120"`
		ExpectedAmount     *float64 `json:"expected_amount,omitempty" exclusiveMinimum:"0"`
		Currency           *string  `json:"currency,omitempty" maxLength:"3"`
		Cadence            *string  `json:"cadence,omitempty" enum:"weekly,fortnightly,monthly,quarterly,annual"`
		AnchorDate         *string  `json:"anchor_date,omitempty" doc:"YYYY-MM-DD"`
		AmountVariable     *bool    `json:"amount_variable,omitempty"`
		AmountTolerancePct *float64 `json:"amount_tolerance_pct,omitempty" minimum:"0"`
		MatchPattern       *string  `json:"match_pattern,omitempty" maxLength:"200"`
		MatchWindowDays    *int     `json:"match_window_days,omitempty" minimum:"0"`
		Status             *string  `json:"status,omitempty" enum:"active,paused,ended"`
		EndedOn            *string  `json:"ended_on,omitempty" doc:"YYYY-MM-DD, or \"\" to clear it"`
		Notes              *string  `json:"notes,omitempty"`
		AccountID          *int     `json:"account_id,omitempty" doc:"Account id, or 0 to detach the account"`
	}
}

type billIDInput struct {
	ID int `path:"id" doc:"Recurring bill ID"`
}

type reconcileBillsOutput struct {
	Body struct {
		BillsScanned   int `json:"bills_scanned" doc:"Bills with a match pattern and at least one cycle in range"`
		CyclesChecked  int `json:"cycles_checked"`
		PaymentsLinked int `json:"payments_linked" doc:"New links created; a re-run over unchanged data links nothing"`
	}
}

type createBillPaymentInput struct {
	ID   int `path:"id" doc:"Recurring bill ID"`
	Body struct {
		TransactionID  int    `json:"transaction_id" minimum:"1" doc:"Posted transaction that paid the cycle"`
		OccurrenceDate string `json:"occurrence_date,omitempty" doc:"Cycle due date, YYYY-MM-DD; defaults to the cycle nearest the transaction's posted date"`
	}
}

type billPaymentOutput struct {
	Body BillPaymentDTO
}

func toBillPaymentDTO(p *ent.BillPayment, billID, txnID int) BillPaymentDTO {
	return BillPaymentDTO{
		ID:             p.ID,
		BillID:         billID,
		TransactionID:  txnID,
		OccurrenceDate: p.OccurrenceDate.UTC().Format(dateLayout),
		Method:         string(p.Method),
		CreatedAt:      p.CreatedAt.UTC().Format(http.TimeFormat),
	}
}

// parseBillDate reads a required date-only body field into a UTC-midnight time. Empty or
// malformed is a 422 so a bad anchor fails loudly instead of landing as the zero time.
func parseBillDate(field, value string) (time.Time, error) {
	t, err := parseQueryDate(value)
	if err != nil {
		return time.Time{}, err
	}
	if t == nil {
		return time.Time{}, huma.Error422UnprocessableEntity(field + " is required (YYYY-MM-DD)")
	}
	return *t, nil
}

func (h *Handler) registerAdminFinanceBills(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-admin-finance-bills",
		Method:      http.MethodGet,
		Path:        "/api/admin/finance/bills",
		Summary:     "List every recurring bill for management (all statuses)",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listAdminBillsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		bills, totals, err := finance.ListRecurringBills(ctx, h.deps.Ent, finance.BillFilter{Status: "all"})
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list recurring bills", err)
		}
		out := &listAdminBillsOutput{}
		out.Body.Bills = toFinanceBillDTOs(bills)
		out.Body.CommittedTotal = totals.CommittedTotal
		out.Body.MonthlyEquivalent = totals.MonthlyEquivalent
		out.Body.Count = totals.Count
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-finance-bill",
		Method:        http.MethodPost,
		Path:          "/api/admin/finance/bills",
		Summary:       "Declare a recurring bill",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createBillInput) (*adminBillOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		anchor, err := parseBillDate("anchor_date", in.Body.AnchorDate)
		if err != nil {
			return nil, err
		}
		endedOn, err := parseQueryDate(in.Body.EndedOn)
		if err != nil {
			return nil, err
		}

		create := h.deps.Ent.RecurringBill.Create().
			SetName(in.Body.Name).
			SetPayee(in.Body.Payee).
			SetExpectedAmount(in.Body.ExpectedAmount).
			SetAnchorDate(anchor).
			SetAmountVariable(in.Body.AmountVariable).
			SetMatchPattern(in.Body.MatchPattern).
			SetNotes(in.Body.Notes).
			SetNillableEndedOn(endedOn)
		// The omitted optionals fall through to the schema defaults (AUD, monthly, 10%,
		// 5 days, active) rather than being written as a zero value.
		if in.Body.Currency != "" {
			create.SetCurrency(in.Body.Currency)
		}
		if in.Body.Cadence != "" {
			create.SetCadence(recurringbill.Cadence(in.Body.Cadence))
		}
		if in.Body.AmountTolerancePct != nil {
			create.SetAmountTolerancePct(*in.Body.AmountTolerancePct)
		}
		if in.Body.MatchWindowDays != nil {
			create.SetMatchWindowDays(*in.Body.MatchWindowDays)
		}
		if in.Body.Status != "" {
			create.SetStatus(recurringbill.Status(in.Body.Status))
		}
		if in.Body.AccountID != nil && *in.Body.AccountID != 0 {
			create.SetAccountID(*in.Body.AccountID)
		}

		b, err := create.Save(ctx)
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("a recurring bill with that name already exists")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to create recurring bill", err)
		}
		return h.billResponse(ctx, b.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-finance-bill",
		Method:      http.MethodPatch,
		Path:        "/api/admin/finance/bills/{id}",
		Summary:     "Update a recurring bill (partial; includes pause/resume via status)",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateBillInput) (*adminBillOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		upd := h.deps.Ent.RecurringBill.UpdateOneID(in.ID)
		if in.Body.Name != nil {
			upd.SetName(*in.Body.Name)
		}
		if in.Body.Payee != nil {
			upd.SetPayee(*in.Body.Payee)
		}
		if in.Body.ExpectedAmount != nil {
			upd.SetExpectedAmount(*in.Body.ExpectedAmount)
		}
		if in.Body.Currency != nil {
			upd.SetCurrency(*in.Body.Currency)
		}
		if in.Body.Cadence != nil {
			upd.SetCadence(recurringbill.Cadence(*in.Body.Cadence))
		}
		if in.Body.AnchorDate != nil {
			anchor, err := parseBillDate("anchor_date", *in.Body.AnchorDate)
			if err != nil {
				return nil, err
			}
			upd.SetAnchorDate(anchor)
		}
		if in.Body.AmountVariable != nil {
			upd.SetAmountVariable(*in.Body.AmountVariable)
		}
		if in.Body.AmountTolerancePct != nil {
			upd.SetAmountTolerancePct(*in.Body.AmountTolerancePct)
		}
		if in.Body.MatchPattern != nil {
			upd.SetMatchPattern(*in.Body.MatchPattern)
		}
		if in.Body.MatchWindowDays != nil {
			upd.SetMatchWindowDays(*in.Body.MatchWindowDays)
		}
		if in.Body.Status != nil {
			upd.SetStatus(recurringbill.Status(*in.Body.Status))
		}
		if in.Body.EndedOn != nil {
			if *in.Body.EndedOn == "" {
				upd.ClearEndedOn()
			} else {
				ended, err := parseBillDate("ended_on", *in.Body.EndedOn)
				if err != nil {
					return nil, err
				}
				upd.SetEndedOn(ended)
			}
		}
		if in.Body.Notes != nil {
			upd.SetNotes(*in.Body.Notes)
		}
		if in.Body.AccountID != nil {
			if *in.Body.AccountID == 0 {
				upd.ClearAccount()
			} else {
				upd.SetAccountID(*in.Body.AccountID)
			}
		}

		b, err := upd.Save(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("recurring bill not found")
		}
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("a recurring bill with that name already exists")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update recurring bill", err)
		}
		return h.billResponse(ctx, b.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-finance-bill",
		Method:        http.MethodDelete,
		Path:          "/api/admin/finance/bills/{id}",
		Summary:       "Delete a recurring bill and its reconciliation links",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *billIDInput) (*struct{}, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		// The link rows carry a required FK to the bill, so they go first or the delete
		// trips the constraint. They are derived data (the ledger rows they point at are
		// untouched), so dropping them with the declaration is safe.
		tx, err := h.deps.Ent.Tx(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to delete recurring bill", err)
		}
		if _, err := tx.BillPayment.Delete().
			Where(billpayment.HasBillWith(recurringbill.IDEQ(in.ID))).
			Exec(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to delete recurring bill", err)
		}
		if err := tx.RecurringBill.DeleteOneID(in.ID).Exec(ctx); err != nil {
			_ = tx.Rollback()
			if ent.IsNotFound(err) {
				return nil, huma.Error404NotFound("recurring bill not found")
			}
			return nil, huma.Error500InternalServerError("failed to delete recurring bill", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, huma.Error500InternalServerError("failed to delete recurring bill", err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "reconcile-finance-bills",
		Method:      http.MethodPost,
		Path:        "/api/admin/finance/bills/reconcile",
		Summary:     "Re-run the bill matching pass over the stored ledger",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*reconcileBillsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		sum, err := finance.ReconcileBills(ctx, h.deps.Ent)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to reconcile recurring bills", err)
		}
		out := &reconcileBillsOutput{}
		out.Body.BillsScanned = sum.BillsScanned
		out.Body.CyclesChecked = sum.CyclesChecked
		out.Body.PaymentsLinked = sum.PaymentsLinked
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-finance-bill-payment",
		Method:        http.MethodPost,
		Path:          "/api/admin/finance/bills/{id}/payments",
		Summary:       "Link a posted transaction to one cycle of a bill by hand",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createBillPaymentInput) (*billPaymentOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		occ, err := parseQueryDate(in.Body.OccurrenceDate)
		if err != nil {
			return nil, err
		}
		p, err := finance.LinkBillPayment(ctx, h.deps.Ent, in.ID, in.Body.TransactionID, occ)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("recurring bill or transaction not found")
		}
		if errors.Is(err, finance.ErrCycleAlreadyLinked) {
			return nil, huma.Error409Conflict(err.Error())
		}
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("that transaction is already linked to this bill")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to link the payment", err)
		}
		return &billPaymentOutput{Body: toBillPaymentDTO(p, in.ID, in.Body.TransactionID)}, nil
	})
}

// billResponse reloads a bill through the shared read service after a write, so a create
// or update answers with the same derived fields (next_due, days_until, last paid) the
// list endpoints report instead of a second, hand-rolled shape.
func (h *Handler) billResponse(ctx context.Context, id int) (*adminBillOutput, error) {
	bills, _, err := finance.ListRecurringBills(ctx, h.deps.Ent, finance.BillFilter{Status: "all"})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load the recurring bill", err)
	}
	for _, b := range bills {
		if b.ID != id {
			continue
		}
		dtos := toFinanceBillDTOs([]finance.BillView{b})
		return &adminBillOutput{Body: dtos[0]}, nil
	}
	return nil, huma.Error404NotFound("recurring bill not found")
}
