package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/incomereceipt"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// Recurring Income (ADR 0019): the owner's declared repeating receipts. The read sits at
// GET /api/finance/income beside the bill read, and the writes under
// /api/admin/finance/income, the same split the bills use. Every route is admin-only,
// with requireAdmin as each handler's first line, and the finance.sync ingest token can
// never reach them, so no ingest run touches a declaration.
//
// Both halves call finance.ListRecurringIncome, so a write answers with exactly the
// derived figures the list reports.

// incomeNameMaxBytes mirrors the schema's MaxLen(120), which Ent measures in BYTES while
// Huma's maxLength counts RUNES. Without this pre-check a 120-character name with accents
// clears Huma, fails Ent, and surfaces as a 500 carrying the raw validator text.
const incomeNameMaxBytes = 120

// FinanceIncomeDTO is one declared recurring income: the stored columns plus the part
// derived on read. next_expected is never stored, it is computed from (cadence,
// anchor_date); last_received_* come from the newest linked receipt's transaction.
type FinanceIncomeDTO struct {
	ID                 int      `json:"id"`
	Name               string   `json:"name"`
	ExpectedAmount     float64  `json:"expected_amount" doc:"Expected receipt per cycle as a positive magnitude (unlike the signed transaction amount)"`
	Currency           string   `json:"currency"`
	Cadence            string   `json:"cadence" enum:"weekly,fortnightly,monthly,quarterly,annual"`
	AnchorDate         string   `json:"anchor_date" doc:"YYYY-MM-DD; every pay date steps the cadence from here"`
	Status             string   `json:"status" enum:"active,paused,ended"`
	EndedOn            *string  `json:"ended_on" doc:"YYYY-MM-DD the income stopped; null while it runs"`
	Notes              string   `json:"notes"`
	AccountID          *int     `json:"account_id" doc:"Account the income is paid into; null when unset (the edge is optional)"`
	AccountName        string   `json:"account_name"`
	NextExpected       *string  `json:"next_expected" doc:"YYYY-MM-DD; the next derived pay date on or after today and not after ended_on. Null when none is left (the income ended before its next pay date), never a past date"`
	DaysUntil          *int     `json:"days_until" doc:"Whole days from today to next_expected, never negative; null with it"`
	LastReceivedDate   *string  `json:"last_received_date" doc:"YYYY-MM-DD the newest linked cycle actually posted; null until one is linked"`
	LastReceivedAmount *float64 `json:"last_received_amount" doc:"Amount actually received for that cycle; null until one is linked"`
	ExpectedMonthly    float64  `json:"expected_monthly" doc:"expected_amount normalised to a per-month figure so mixed cadences can be summed"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

// IncomeReceiptDTO is one hand-made link: which cycle of which income a posted credit
// paid. No amount on purpose; it lives on the transaction, one hop away.
type IncomeReceiptDTO struct {
	ID             int    `json:"id"`
	IncomeID       int    `json:"income_id"`
	TransactionID  int    `json:"transaction_id"`
	OccurrenceDate string `json:"occurrence_date" doc:"YYYY-MM-DD pay date this receipt settles (not the posted date)"`
	CreatedAt      string `json:"created_at"`
}

type listFinanceIncomeInput struct {
	Status     string `query:"status" default:"active" enum:"active,paused,ended,all" doc:"Which incomes to include; \"all\" spans every status"`
	WithinDays int    `query:"within_days" doc:"Keep only incomes whose next pay date is within this many days from today; 0 (default) returns all"`
	AccountID  int    `query:"account_id" doc:"Filter to incomes paid into one account; 0 (default) spans all, and incomes with no account are then included"`
}

type listFinanceIncomeOutput struct {
	Body struct {
		Income            []FinanceIncomeDTO `json:"income"`
		ExpectedTotal     float64            `json:"expected_total" doc:"Sum of the returned active incomes' expected amounts"`
		MonthlyEquivalent float64            `json:"monthly_equivalent" doc:"The same set normalised to a per-month figure"`
		Count             int                `json:"count" doc:"Number of incomes returned"`
	}
}

type financeIncomeOutput struct {
	Body FinanceIncomeDTO
}

type createIncomeInput struct {
	Body struct {
		Name           string  `json:"name" minLength:"1" maxLength:"120" doc:"Label and human identity key, at most 120 bytes; must be unique"`
		ExpectedAmount float64 `json:"expected_amount" exclusiveMinimum:"0" doc:"Expected receipt per cycle as a positive magnitude"`
		Currency       string  `json:"currency,omitempty" maxLength:"3" doc:"ISO currency code; defaults to AUD"`
		Cadence        string  `json:"cadence,omitempty" enum:"weekly,fortnightly,monthly,quarterly,annual" doc:"Defaults to monthly"`
		AnchorDate     string  `json:"anchor_date" doc:"One known pay date, YYYY-MM-DD; every occurrence steps the cadence from here"`
		Status         string  `json:"status,omitempty" enum:"active,paused,ended" doc:"Defaults to active; ended requires ended_on"`
		EndedOn        string  `json:"ended_on,omitempty" doc:"YYYY-MM-DD the income stopped; required with status ended and refused with any other status"`
		Notes          string  `json:"notes,omitempty"`
		AccountID      *int    `json:"account_id,omitempty" doc:"Account the income is paid into; optional"`
	}
}

// updateIncomeInput is a PATCH: every body field is a pointer so an omitted field leaves
// the column untouched. ended_on:"" clears the end date and account_id:0 detaches the
// account, as on a bill.
type updateIncomeInput struct {
	ID   int `path:"id" doc:"Recurring income ID"`
	Body struct {
		Name           *string  `json:"name,omitempty" minLength:"1" maxLength:"120"`
		ExpectedAmount *float64 `json:"expected_amount,omitempty" exclusiveMinimum:"0"`
		Currency       *string  `json:"currency,omitempty" maxLength:"3"`
		Cadence        *string  `json:"cadence,omitempty" enum:"weekly,fortnightly,monthly,quarterly,annual" doc:"A change is refused with 409 while the income has receipts"`
		AnchorDate     *string  `json:"anchor_date,omitempty" doc:"YYYY-MM-DD; a change is refused with 409 while the income has receipts"`
		Status         *string  `json:"status,omitempty" enum:"active,paused,ended" doc:"After the patch, ended requires ended_on and any other status refuses it"`
		EndedOn        *string  `json:"ended_on,omitempty" doc:"YYYY-MM-DD, or \"\" to clear it"`
		Notes          *string  `json:"notes,omitempty"`
		AccountID      *int     `json:"account_id,omitempty" doc:"Account id, or 0 to detach the account"`
	}
}

type incomeIDInput struct {
	ID int `path:"id" doc:"Recurring income ID"`
}

type createIncomeReceiptInput struct {
	ID   int `path:"id" doc:"Recurring income ID"`
	Body struct {
		TransactionID  int    `json:"transaction_id" minimum:"1" doc:"Posted credit that paid the cycle: not already a receipt of any income, and posted to the income's account when it has one"`
		OccurrenceDate string `json:"occurrence_date,omitempty" doc:"Pay date, YYYY-MM-DD; must be one of the income's derived occurrences, on or after anchor_date, on or before today and on or before ended_on. Defaults to the cycle nearest the transaction's posted date"`
	}
}

type incomeReceiptOutput struct {
	Body IncomeReceiptDTO
}

// checkIncomeEnd is the status / ended_on cross-check: ended_on is set exactly when the
// income is ended. An active income with an end date would count toward the totals past
// it, and an ended one without a date has no point its cycles stop. On a patch it runs on
// the merged result, so ending takes both fields in one request.
func checkIncomeEnd(status recurringincome.Status, endedOn *time.Time) error {
	switch {
	case status == recurringincome.StatusEnded && endedOn == nil:
		return huma.Error422UnprocessableEntity("status ended requires ended_on (YYYY-MM-DD)")
	case status != recurringincome.StatusEnded && endedOn != nil:
		return huma.Error422UnprocessableEntity("ended_on is only allowed with status ended; send ended_on \"\" to clear it")
	}
	return nil
}

// checkIncomeGridFree refuses a change to anchor_date or cadence while the income has
// receipts. Each receipt settles one derived pay date, so moving the grid would leave
// them pointing at dates that are no longer cycles, and the "already received" check
// would stop seeing them. Removing the receipts first is the explicit way through.
func (h *Handler) checkIncomeGridFree(ctx context.Context, incomeID int) error {
	n, err := h.deps.Ent.IncomeReceipt.Query().
		Where(incomereceipt.HasIncomeWith(recurringincome.IDEQ(incomeID))).
		Count(ctx)
	if err != nil {
		return huma.Error500InternalServerError("failed to check the income's receipts", err)
	}
	if n > 0 {
		return huma.Error409Conflict(fmt.Sprintf(
			"this income has %d linked receipt(s), and changing anchor_date or cadence would move its pay dates out from under them; remove the receipts first", n))
	}
	return nil
}

// checkIncomeName is the byte pre-check behind incomeNameMaxBytes.
func checkIncomeName(name string) error {
	if len(name) > incomeNameMaxBytes {
		return huma.Error422UnprocessableEntity("name is too long: at most 120 bytes, and accented or emoji characters cost more than one byte each")
	}
	return nil
}

func (h *Handler) registerFinanceIncome(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-finance-income",
		Method:      http.MethodGet,
		Path:        "/api/finance/income",
		Summary:     "List recurring income with derived pay dates and expected money (admin)",
		Tags:        financeReadTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *listFinanceIncomeInput) (*listFinanceIncomeOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		incomes, totals, err := finance.ListRecurringIncome(ctx, h.deps.Ent, finance.IncomeFilter{
			Status:     in.Status,
			WithinDays: in.WithinDays,
			AccountID:  in.AccountID,
		})
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load recurring income", err)
		}
		out := &listFinanceIncomeOutput{}
		out.Body.Income = toFinanceIncomeDTOs(incomes)
		out.Body.ExpectedTotal = totals.ExpectedTotal
		out.Body.MonthlyEquivalent = totals.MonthlyEquivalent
		out.Body.Count = totals.Count
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-finance-income",
		Method:        http.MethodPost,
		Path:          "/api/admin/finance/income",
		Summary:       "Declare a recurring income",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createIncomeInput) (*financeIncomeOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		if err := checkIncomeName(in.Body.Name); err != nil {
			return nil, err
		}
		anchor, err := parseBillDate("anchor_date", in.Body.AnchorDate)
		if err != nil {
			return nil, err
		}
		endedOn, err := parseQueryDate(in.Body.EndedOn)
		if err != nil {
			return nil, err
		}
		status := recurringincome.DefaultStatus
		if in.Body.Status != "" {
			status = recurringincome.Status(in.Body.Status)
		}
		if err := checkIncomeEnd(status, endedOn); err != nil {
			return nil, err
		}

		create := h.deps.Ent.RecurringIncome.Create().
			SetName(in.Body.Name).
			SetExpectedAmount(in.Body.ExpectedAmount).
			SetAnchorDate(anchor).
			SetNotes(in.Body.Notes).
			SetNillableEndedOn(endedOn)
		// Omitted optionals fall through to the schema defaults (AUD, monthly, active).
		if in.Body.Currency != "" {
			create.SetCurrency(in.Body.Currency)
		}
		if in.Body.Cadence != "" {
			create.SetCadence(recurringincome.Cadence(in.Body.Cadence))
		}
		if in.Body.Status != "" {
			create.SetStatus(status)
		}
		if in.Body.AccountID != nil && *in.Body.AccountID != 0 {
			create.SetAccountID(*in.Body.AccountID)
		}

		inc, err := create.Save(ctx)
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("a recurring income with that name already exists, or the account does not")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to create recurring income", err)
		}
		return h.incomeResponse(ctx, inc.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-finance-income",
		Method:      http.MethodPatch,
		Path:        "/api/admin/finance/income/{id}",
		Summary:     "Update a recurring income (partial; includes pause/resume via status)",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateIncomeInput) (*financeIncomeOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		cur, err := h.deps.Ent.RecurringIncome.Get(ctx, in.ID)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("recurring income not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load recurring income", err)
		}
		// status and ended_on are cross-checked on the merged result, so start from what
		// is stored and let the patch overwrite it.
		status, endedOn := cur.Status, cur.EndedOn
		upd := h.deps.Ent.RecurringIncome.UpdateOneID(in.ID)
		if in.Body.Name != nil {
			if err := checkIncomeName(*in.Body.Name); err != nil {
				return nil, err
			}
			upd.SetName(*in.Body.Name)
		}
		if in.Body.ExpectedAmount != nil {
			upd.SetExpectedAmount(*in.Body.ExpectedAmount)
		}
		if in.Body.Currency != nil {
			upd.SetCurrency(*in.Body.Currency)
		}
		// gridMoved is a real change to the pay-date grid, not the same value resent: the
		// edit form sends every field on each save.
		gridMoved := false
		if in.Body.Cadence != nil {
			gridMoved = gridMoved || recurringincome.Cadence(*in.Body.Cadence) != cur.Cadence
			upd.SetCadence(recurringincome.Cadence(*in.Body.Cadence))
		}
		if in.Body.AnchorDate != nil {
			anchor, err := parseBillDate("anchor_date", *in.Body.AnchorDate)
			if err != nil {
				return nil, err
			}
			gridMoved = gridMoved || anchor.Format(dateLayout) != cur.AnchorDate.UTC().Format(dateLayout)
			upd.SetAnchorDate(anchor)
		}
		if in.Body.Status != nil {
			status = recurringincome.Status(*in.Body.Status)
			upd.SetStatus(status)
		}
		if in.Body.EndedOn != nil {
			if *in.Body.EndedOn == "" {
				endedOn = nil
				upd.ClearEndedOn()
			} else {
				ended, err := parseBillDate("ended_on", *in.Body.EndedOn)
				if err != nil {
					return nil, err
				}
				endedOn = &ended
				upd.SetEndedOn(ended)
			}
		}
		if err := checkIncomeEnd(status, endedOn); err != nil {
			return nil, err
		}
		if gridMoved {
			if err := h.checkIncomeGridFree(ctx, in.ID); err != nil {
				return nil, err
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

		inc, err := upd.Save(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("recurring income not found")
		}
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("a recurring income with that name already exists, or the account does not")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update recurring income", err)
		}
		return h.incomeResponse(ctx, inc.ID)
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-finance-income",
		Method:        http.MethodDelete,
		Path:          "/api/admin/finance/income/{id}",
		Summary:       "Delete a recurring income and its receipt links",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *incomeIDInput) (*struct{}, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Ent == nil {
			return nil, huma.Error503ServiceUnavailable("finance is not available")
		}
		// The receipts carry a required FK to the income, so they go first, in one
		// transaction. They are links only; the ledger rows they point at are untouched.
		tx, err := h.deps.Ent.Tx(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to delete recurring income", err)
		}
		if _, err := tx.IncomeReceipt.Delete().
			Where(incomereceipt.HasIncomeWith(recurringincome.IDEQ(in.ID))).
			Exec(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to delete recurring income", err)
		}
		if err := tx.RecurringIncome.DeleteOneID(in.ID).Exec(ctx); err != nil {
			_ = tx.Rollback()
			if ent.IsNotFound(err) {
				return nil, huma.Error404NotFound("recurring income not found")
			}
			return nil, huma.Error500InternalServerError("failed to delete recurring income", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, huma.Error500InternalServerError("failed to delete recurring income", err)
		}
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-finance-income-receipt",
		Method:        http.MethodPost,
		Path:          "/api/admin/finance/income/{id}/receipts",
		Summary:       "Link a posted credit to one pay cycle of an income by hand",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createIncomeReceiptInput) (*incomeReceiptOutput, error) {
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
		r, err := finance.LinkIncomeReceipt(ctx, h.deps.Ent, in.ID, in.Body.TransactionID, occ)
		switch {
		case ent.IsNotFound(err):
			return nil, huma.Error404NotFound("recurring income or transaction not found")
		case errors.Is(err, finance.ErrNotAPayDate), errors.Is(err, finance.ErrNotACredit),
			errors.Is(err, finance.ErrCycleOutOfRange), errors.Is(err, finance.ErrWrongAccount):
			return nil, huma.Error422UnprocessableEntity(err.Error())
		case errors.Is(err, finance.ErrIncomeCycleReceived), errors.Is(err, finance.ErrTransactionReceived):
			return nil, huma.Error409Conflict(err.Error())
		case ent.IsConstraintError(err):
			// LinkIncomeReceipt re-checks both unique rules after a constraint error and
			// names the one that fired, so reaching here means neither row is visible.
			return nil, huma.Error409Conflict("the receipt conflicts with another link; reload and try again")
		case err != nil:
			return nil, huma.Error500InternalServerError("failed to link the receipt", err)
		}
		return &incomeReceiptOutput{Body: IncomeReceiptDTO{
			ID:             r.ID,
			IncomeID:       in.ID,
			TransactionID:  in.Body.TransactionID,
			OccurrenceDate: r.OccurrenceDate.UTC().Format(dateLayout),
			CreatedAt:      r.CreatedAt.UTC().Format(http.TimeFormat),
		}}, nil
	})
}

// incomeResponse reloads an income through the shared read service after a write, so a
// create or update answers with the same derived fields the list reports.
func (h *Handler) incomeResponse(ctx context.Context, id int) (*financeIncomeOutput, error) {
	incomes, _, err := finance.ListRecurringIncome(ctx, h.deps.Ent, finance.IncomeFilter{Status: "all"})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load the recurring income", err)
	}
	for _, inc := range incomes {
		if inc.ID == id {
			return &financeIncomeOutput{Body: toFinanceIncomeDTOs([]finance.IncomeView{inc})[0]}, nil
		}
	}
	return nil, huma.Error404NotFound("recurring income not found")
}

// toFinanceIncomeDTOs maps the read service's income views to their wire DTOs.
func toFinanceIncomeDTOs(incomes []finance.IncomeView) []FinanceIncomeDTO {
	out := make([]FinanceIncomeDTO, 0, len(incomes))
	for _, v := range incomes {
		dto := FinanceIncomeDTO{
			ID:                 v.ID,
			Name:               v.Name,
			ExpectedAmount:     v.ExpectedAmount,
			Currency:           v.Currency,
			Cadence:            v.Cadence,
			AnchorDate:         v.AnchorDate.UTC().Format(dateLayout),
			Status:             v.Status,
			EndedOn:            dateOnlyPtr(v.EndedOn),
			Notes:              v.Notes,
			AccountName:        v.AccountName,
			NextExpected:       dateOnlyPtr(v.NextExpected),
			DaysUntil:          v.DaysUntil,
			LastReceivedDate:   dateOnlyPtr(v.LastReceivedDate),
			LastReceivedAmount: v.LastReceivedAmount,
			ExpectedMonthly:    v.ExpectedMonthly,
			CreatedAt:          v.CreatedAt.UTC().Format(http.TimeFormat),
			UpdatedAt:          v.UpdatedAt.UTC().Format(http.TimeFormat),
		}
		if v.AccountID != 0 {
			id := v.AccountID
			dto.AccountID = &id
		}
		out = append(out, dto)
	}
	return out
}
