package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// The finance dashboard read API (ADR 0017). Seven admin-only GET endpoints over the
// finance ledger, each backed by the pure read service (internal/finance.read.go, plus
// bills.go for recurring commitments) so the same query path serves the /admin dashboard
// and the remote MCP tools. Every operation is cookie-gated and calls requireAdmin as its
// first line: finance is single-tenant (Alif's) and never friend/member-visible. The write
// side stays the token-authed ingest (ADR 0015) and the admin bill endpoints
// (admin_finance_bills.go); nothing here mutates.

// financeReadTags groups the dashboard reads under the shared finance tag.
var financeReadTags = []string{"finance"}

const (
	// dateLayout is the date-only wire form (posted_date, pending date, watermark).
	dateLayout = "2006-01-02"
)

// --- DTOs ---

// FinanceSummaryDTO is the net-worth roll-up. as_of is the freshest snapshot reading
// across accounts (a staleness stamp); null when no account has a snapshot yet.
type FinanceSummaryDTO struct {
	NetWorth     float64 `json:"net_worth"`
	Assets       float64 `json:"assets"`
	Liabilities  float64 `json:"liabilities"`
	Currency     string  `json:"currency"`
	AccountCount int     `json:"account_count"`
	AsOf         *string `json:"as_of" doc:"RFC3339 timestamp of the freshest balance reading; null when no snapshots exist"`
}

// FinanceAccountDTO is one account plus its latest balance. The balance pointers are
// null until the account carries a snapshot.
type FinanceAccountDTO struct {
	ID              int      `json:"id"`
	Name            string   `json:"name"`
	MaskedNumber    string   `json:"masked_number"`
	Type            string   `json:"type" enum:"everyday,savings,credit_card,steppay,investment"`
	Class           string   `json:"class" enum:"asset,liability"`
	Currency        string   `json:"currency"`
	Balance         *float64 `json:"balance" doc:"Latest snapshot balance; null when the account has no snapshot"`
	Available       *float64 `json:"available"`
	CreditLimit     *float64 `json:"credit_limit"`
	BalanceAsOf     *string  `json:"balance_as_of" doc:"RFC3339 reading time of the latest snapshot; null when none"`
	PostedWatermark *string  `json:"posted_watermark" doc:"Date (YYYY-MM-DD) through which posted rows are known complete; null when never synced"`
}

// BalancePointDTO is one point on a balance-history line.
type BalancePointDTO struct {
	AsOf    string  `json:"as_of" doc:"RFC3339 reading time"`
	Balance float64 `json:"balance"`
}

// FinanceTxnDTO is one posted transaction with its account joined in.
type FinanceTxnDTO struct {
	ID           int      `json:"id"`
	AccountID    int      `json:"account_id"`
	AccountName  string   `json:"account_name"`
	PostedDate   string   `json:"posted_date" doc:"YYYY-MM-DD"`
	Amount       float64  `json:"amount" doc:"Signed: money out negative, money in positive"`
	Description  string   `json:"description"`
	Merchant     string   `json:"merchant"`
	BalanceAfter *float64 `json:"balance_after"`
}

// FinancePendingDTO is one pending transaction with its account joined in.
type FinancePendingDTO struct {
	ID          int     `json:"id"`
	AccountID   int     `json:"account_id"`
	AccountName string  `json:"account_name"`
	Date        string  `json:"date" doc:"YYYY-MM-DD"`
	Amount      float64 `json:"amount"`
	Description string  `json:"description"`
	Merchant    string  `json:"merchant"`
}

// FinanceWishlistItemDTO is one wishlist item: something the owner wants to buy or pay
// for once (portfolio-site#123). amount is null when the price is unknown, which is not
// the same as free.
type FinanceWishlistItemDTO struct {
	ID               int      `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Amount           *float64 `json:"amount" doc:"Expected cost; null when the price is unknown (NOT free)"`
	AmountIsEstimate bool     `json:"amount_is_estimate" doc:"True while the amount is a guess rather than a quoted price"`
	Currency         string   `json:"currency"`
	Priority         string   `json:"priority" enum:"low,medium,high"`
	Status           string   `json:"status" enum:"wanted,bought,abandoned"`
	Deadline         *string  `json:"deadline" doc:"Soft want-it-by date (YYYY-MM-DD); null when there is no date"`
	ResolvedAt       *string  `json:"resolved_at" doc:"RFC3339 time the item left wanted (bought or abandoned); null while still wanted"`
	Link             string   `json:"link"`
	ImageKey         string   `json:"image_key" doc:"S3 object key returned by the upload presign endpoint"`
}

// FinanceWishlistTotalsDTO rolls up the items in the same response. known_cost_total
// sums only the non-null amounts denominated in currency; unknown_cost_count reports how
// many rows had no amount, so an unknown price is never counted as zero; and
// currency_mismatch_count reports how many priced rows were excluded for carrying a
// different currency, so a single-currency total never silently absorbs a foreign one.
type FinanceWishlistTotalsDTO struct {
	ItemCount             int     `json:"item_count"`
	KnownCostTotal        float64 `json:"known_cost_total"`
	UnknownCostCount      int     `json:"unknown_cost_count"`
	CurrencyMismatchCount int     `json:"currency_mismatch_count" doc:"Priced rows left out of known_cost_total because their currency differs from currency"`
	Currency              string  `json:"currency"`
}

// FinanceBillDTO is one declared recurring commitment (portfolio-site#125): the stored
// columns plus the part derived on read. next_due is never stored, it is computed from
// (cadence, anchor_date); days_until is negative when a cycle is past due with nothing
// matched. last_paid_* come from the newest reconciled payment's transaction, so
// last_paid_amount differing from expected_amount is the repricing signal, not an error.
type FinanceBillDTO struct {
	ID                 int      `json:"id"`
	Name               string   `json:"name"`
	Payee              string   `json:"payee"`
	ExpectedAmount     float64  `json:"expected_amount" doc:"Expected charge per cycle as a positive magnitude (unlike the signed transaction amount)"`
	Currency           string   `json:"currency"`
	Cadence            string   `json:"cadence" enum:"weekly,fortnightly,monthly,quarterly,annual"`
	AnchorDate         string   `json:"anchor_date" doc:"YYYY-MM-DD; every occurrence steps the cadence from here"`
	AmountVariable     bool     `json:"amount_variable" doc:"True when the amount changes every cycle (utilities): the matcher skips the amount check and expected_amount is an estimate"`
	AmountTolerancePct float64  `json:"amount_tolerance_pct"`
	MatchPattern       string   `json:"match_pattern" doc:"Case-insensitive substring matched against a posted row's description or merchant; empty means never auto-matched"`
	MatchWindowDays    int      `json:"match_window_days"`
	Status             string   `json:"status" enum:"active,paused,ended"`
	EndedOn            *string  `json:"ended_on" doc:"YYYY-MM-DD the commitment stopped; null while it runs"`
	Notes              string   `json:"notes"`
	AccountID          *int     `json:"account_id" doc:"Account the bill is paid from; null when unset (the edge is optional)"`
	AccountName        string   `json:"account_name"`
	NextDue            string   `json:"next_due" doc:"YYYY-MM-DD; the derived due date needing attention (the unsettled past cycle when overdue)"`
	DaysUntil          int      `json:"days_until" doc:"Whole days from today to next_due; negative when overdue"`
	LastPaidDate       *string  `json:"last_paid_date" doc:"YYYY-MM-DD the newest reconciled cycle actually posted; null until one matches"`
	LastPaidAmount     *float64 `json:"last_paid_amount" doc:"Magnitude actually charged for that cycle; null until one matches"`
	Overdue            bool     `json:"overdue" doc:"An active bill whose previous cycle is past its match window with no payment linked"`
	ExpectedMonthly    float64  `json:"expected_monthly" doc:"expected_amount normalised to a per-month figure so mixed cadences can be summed"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

// --- inputs / outputs ---

type financeSummaryOutput struct {
	Body FinanceSummaryDTO
}

type listFinanceAccountsOutput struct {
	Body struct {
		Accounts []FinanceAccountDTO `json:"accounts"`
	}
}

type financeBalanceHistoryInput struct {
	ID   int `path:"id" doc:"Account id"`
	Days int `query:"days" default:"90" doc:"Look-back window in days; 0 returns the full history"`
}

type financeBalanceHistoryOutput struct {
	Body struct {
		Points []BalancePointDTO `json:"points"`
	}
}

type listFinanceTxnInput struct {
	AccountID int    `query:"account_id" doc:"Filter to one account; 0 (default) spans all"`
	From      string `query:"from" doc:"Inclusive lower bound, YYYY-MM-DD"`
	To        string `query:"to" doc:"Inclusive upper bound, YYYY-MM-DD"`
	Limit     int    `query:"limit" default:"50" doc:"Page size (capped at 500)"`
	Offset    int    `query:"offset" doc:"Rows to skip for paging"`
}

type listFinanceTxnOutput struct {
	Body struct {
		Transactions []FinanceTxnDTO `json:"transactions"`
		Total        int             `json:"total" doc:"Total rows matching the filter, before paging"`
	}
}

type listFinancePendingInput struct {
	AccountID int `query:"account_id" doc:"Filter to one account; 0 (default) spans all"`
}

type listFinancePendingOutput struct {
	Body struct {
		Pending []FinancePendingDTO `json:"pending"`
	}
}

type listFinanceWishlistInput struct {
	Status string `query:"status" default:"wanted" enum:"wanted,bought,abandoned,all" doc:"Which items to return; defaults to the still-outstanding ones"`
}

type listFinanceWishlistOutput struct {
	Body struct {
		Items  []FinanceWishlistItemDTO `json:"items"`
		Totals FinanceWishlistTotalsDTO `json:"totals"`
		// Truncated is present only when the read-service row limit actually dropped
		// rows, so its absence means the list and its totals are complete.
		Truncated bool `json:"truncated,omitempty" doc:"True when the row limit dropped the lowest-priority items from this response and its totals"`
	}
}

type listFinanceBillsInput struct {
	Status     string `query:"status" default:"active" enum:"active,paused,ended,all" doc:"Which commitments to include; \"all\" spans every status"`
	WithinDays int    `query:"within_days" doc:"Keep only bills due within this many days from today (overdue bills always pass); 0 (default) returns all"`
	AccountID  int    `query:"account_id" doc:"Filter to bills paid from one account; 0 (default) spans all, and bills with no account are then included"`
}

// listFinanceBillsOutput carries the committed-money roll-up beside the rows, since that
// total is the number the whole feature exists to produce. It counts ACTIVE bills only.
type listFinanceBillsOutput struct {
	Body struct {
		Bills             []FinanceBillDTO `json:"bills"`
		CommittedTotal    float64          `json:"committed_total" doc:"Sum of the returned active bills' expected amounts"`
		MonthlyEquivalent float64          `json:"monthly_equivalent" doc:"The same set normalised to a per-month figure"`
		Count             int              `json:"count" doc:"Number of bills returned"`
	}
}

func (h *Handler) registerFinanceRead(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-finance-summary",
		Method:      http.MethodGet,
		Path:        "/api/finance/summary",
		Summary:     "Net-worth summary across all finance accounts (admin)",
		Tags:        financeReadTags,
		Security:    cookieAuthSecurity,
	}, h.getFinanceSummary)

	huma.Register(api, huma.Operation{
		OperationID: "list-finance-accounts",
		Method:      http.MethodGet,
		Path:        "/api/finance/accounts",
		Summary:     "List finance accounts with their latest balance (admin)",
		Tags:        financeReadTags,
		Security:    cookieAuthSecurity,
	}, h.listFinanceAccounts)

	huma.Register(api, huma.Operation{
		OperationID: "get-finance-balance-history",
		Method:      http.MethodGet,
		Path:        "/api/finance/accounts/{id}/balances",
		Summary:     "Balance history for one account (admin)",
		Tags:        financeReadTags,
		Security:    cookieAuthSecurity,
	}, h.getFinanceBalanceHistory)

	huma.Register(api, huma.Operation{
		OperationID: "list-finance-transactions",
		Method:      http.MethodGet,
		Path:        "/api/finance/transactions",
		Summary:     "List posted transactions, filtered and paged (admin)",
		Tags:        financeReadTags,
		Security:    cookieAuthSecurity,
	}, h.listFinanceTransactions)

	huma.Register(api, huma.Operation{
		OperationID: "list-finance-pending",
		Method:      http.MethodGet,
		Path:        "/api/finance/pending",
		Summary:     "List pending (not-yet-settled) transactions (admin)",
		Tags:        financeReadTags,
		Security:    cookieAuthSecurity,
	}, h.listFinancePending)

	huma.Register(api, huma.Operation{
		OperationID: "list-finance-wishlist",
		Method:      http.MethodGet,
		Path:        "/api/finance/wishlist",
		Summary:     "List wishlist items with a cost roll-up (admin)",
		Tags:        financeReadTags,
		Security:    cookieAuthSecurity,
	}, h.listFinanceWishlist)

	huma.Register(api, huma.Operation{
		OperationID: "list-finance-bills",
		Method:      http.MethodGet,
		Path:        "/api/finance/bills",
		Summary:     "List recurring bills with derived due dates and committed money (admin)",
		Tags:        financeReadTags,
		Security:    cookieAuthSecurity,
	}, h.listFinanceBills)
}

func (h *Handler) getFinanceSummary(ctx context.Context, _ *struct{}) (*financeSummaryOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance is not available")
	}
	s, err := finance.NetWorthSummary(ctx, h.deps.Ent)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load finance summary", err)
	}
	out := &financeSummaryOutput{}
	out.Body = FinanceSummaryDTO{
		NetWorth:     s.NetWorth,
		Assets:       s.Assets,
		Liabilities:  s.Liabilities,
		Currency:     s.Currency,
		AccountCount: s.AccountCount,
		AsOf:         rfc3339Ptr(s.AsOf),
	}
	return out, nil
}

func (h *Handler) listFinanceAccounts(ctx context.Context, _ *struct{}) (*listFinanceAccountsOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance is not available")
	}
	accs, err := finance.Accounts(ctx, h.deps.Ent)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load finance accounts", err)
	}
	out := &listFinanceAccountsOutput{}
	out.Body.Accounts = make([]FinanceAccountDTO, 0, len(accs))
	for _, a := range accs {
		out.Body.Accounts = append(out.Body.Accounts, FinanceAccountDTO{
			ID:              a.ID,
			Name:            a.Name,
			MaskedNumber:    a.MaskedNumber,
			Type:            a.Type,
			Class:           a.Class,
			Currency:        a.Currency,
			Balance:         a.Balance,
			Available:       a.Available,
			CreditLimit:     a.CreditLimit,
			BalanceAsOf:     rfc3339Ptr(a.BalanceAsOf),
			PostedWatermark: dateOnlyPtr(a.PostedWatermark),
		})
	}
	return out, nil
}

func (h *Handler) getFinanceBalanceHistory(ctx context.Context, in *financeBalanceHistoryInput) (*financeBalanceHistoryOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance is not available")
	}
	var from *time.Time
	if in.Days > 0 {
		f := time.Now().UTC().AddDate(0, 0, -in.Days)
		from = &f
	}
	points, err := finance.BalanceHistory(ctx, h.deps.Ent, in.ID, from)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load balance history", err)
	}
	out := &financeBalanceHistoryOutput{}
	out.Body.Points = make([]BalancePointDTO, 0, len(points))
	for _, p := range points {
		out.Body.Points = append(out.Body.Points, BalancePointDTO{
			AsOf:    p.AsOf.UTC().Format(time.RFC3339),
			Balance: p.Balance,
		})
	}
	return out, nil
}

func (h *Handler) listFinanceTransactions(ctx context.Context, in *listFinanceTxnInput) (*listFinanceTxnOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance is not available")
	}
	from, err := parseQueryDate(in.From)
	if err != nil {
		return nil, err
	}
	to, err := parseQueryDate(in.To)
	if err != nil {
		return nil, err
	}
	// The dashboard endpoint never sets ExternalOnly, so truncated is always false here.
	txns, total, _, err := finance.ListTransactions(ctx, h.deps.Ent, finance.TxnFilter{
		AccountID: in.AccountID,
		From:      from,
		To:        to,
		Limit:     in.Limit,
		Offset:    in.Offset,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load transactions", err)
	}
	out := &listFinanceTxnOutput{}
	out.Body.Total = total
	out.Body.Transactions = make([]FinanceTxnDTO, 0, len(txns))
	for _, t := range txns {
		out.Body.Transactions = append(out.Body.Transactions, toFinanceTxnDTO(t))
	}
	return out, nil
}

func (h *Handler) listFinancePending(ctx context.Context, in *listFinancePendingInput) (*listFinancePendingOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance is not available")
	}
	pend, err := finance.Pending(ctx, h.deps.Ent, in.AccountID)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load pending transactions", err)
	}
	out := &listFinancePendingOutput{}
	out.Body.Pending = make([]FinancePendingDTO, 0, len(pend))
	for _, p := range pend {
		out.Body.Pending = append(out.Body.Pending, FinancePendingDTO{
			ID:          p.ID,
			AccountID:   p.AccountID,
			AccountName: p.AccountName,
			Date:        p.Date.UTC().Format(dateLayout),
			Amount:      p.Amount,
			Description: p.Description,
			Merchant:    p.Merchant,
		})
	}
	return out, nil
}

func (h *Handler) listFinanceWishlist(ctx context.Context, in *listFinanceWishlistInput) (*listFinanceWishlistOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance is not available")
	}
	items, totals, truncated, err := finance.Wishlist(ctx, h.deps.Ent, finance.WishlistFilter{Status: in.Status})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load wishlist", err)
	}
	out := &listFinanceWishlistOutput{}
	out.Body.Truncated = truncated
	out.Body.Items = make([]FinanceWishlistItemDTO, 0, len(items))
	for _, w := range items {
		out.Body.Items = append(out.Body.Items, FinanceWishlistItemDTO{
			ID:               w.ID,
			Name:             w.Name,
			Description:      w.Description,
			Amount:           w.Amount,
			AmountIsEstimate: w.AmountIsEstimate,
			Currency:         w.Currency,
			Priority:         w.Priority,
			Status:           w.Status,
			Deadline:         dateOnlyPtr(w.Deadline),
			ResolvedAt:       rfc3339Ptr(w.ResolvedAt),
			Link:             w.Link,
			ImageKey:         w.ImageKey,
		})
	}
	out.Body.Totals = FinanceWishlistTotalsDTO{
		ItemCount:             totals.ItemCount,
		KnownCostTotal:        totals.KnownCostTotal,
		UnknownCostCount:      totals.UnknownCostCount,
		CurrencyMismatchCount: totals.CurrencyMismatchCount,
		Currency:              totals.Currency,
	}
	return out, nil
}

// listFinanceBills serves the dashboard's recurring-bill section. The status/within_days/
// account_id filter goes straight to the shared read service, so this endpoint and the
// list_recurring_bills MCP tool return identical numbers.
func (h *Handler) listFinanceBills(ctx context.Context, in *listFinanceBillsInput) (*listFinanceBillsOutput, error) {
	if _, err := requireAdmin(ctx); err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("finance is not available")
	}
	bills, totals, err := finance.ListRecurringBills(ctx, h.deps.Ent, finance.BillFilter{
		Status:     in.Status,
		WithinDays: in.WithinDays,
		AccountID:  in.AccountID,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to load recurring bills", err)
	}
	out := &listFinanceBillsOutput{}
	out.Body.Bills = toFinanceBillDTOs(bills)
	out.Body.CommittedTotal = totals.CommittedTotal
	out.Body.MonthlyEquivalent = totals.MonthlyEquivalent
	out.Body.Count = totals.Count
	return out, nil
}

// toFinanceBillDTOs maps the read service's bill views to their wire DTOs. Shared by the
// dashboard read endpoint and the admin list, so both render the same derived figures.
func toFinanceBillDTOs(bills []finance.BillView) []FinanceBillDTO {
	out := make([]FinanceBillDTO, 0, len(bills))
	for _, b := range bills {
		dto := FinanceBillDTO{
			ID:                 b.ID,
			Name:               b.Name,
			Payee:              b.Payee,
			ExpectedAmount:     b.ExpectedAmount,
			Currency:           b.Currency,
			Cadence:            b.Cadence,
			AnchorDate:         b.AnchorDate.UTC().Format(dateLayout),
			AmountVariable:     b.AmountVariable,
			AmountTolerancePct: b.AmountTolerancePct,
			MatchPattern:       b.MatchPattern,
			MatchWindowDays:    b.MatchWindowDays,
			Status:             b.Status,
			EndedOn:            dateOnlyPtr(b.EndedOn),
			Notes:              b.Notes,
			AccountName:        b.AccountName,
			NextDue:            b.NextDue.UTC().Format(dateLayout),
			DaysUntil:          b.DaysUntil,
			LastPaidDate:       dateOnlyPtr(b.LastPaidDate),
			LastPaidAmount:     b.LastPaidAmount,
			Overdue:            b.Overdue,
			ExpectedMonthly:    b.ExpectedMonthly,
			CreatedAt:          b.CreatedAt.UTC().Format(http.TimeFormat),
			UpdatedAt:          b.UpdatedAt.UTC().Format(http.TimeFormat),
		}
		if b.AccountID != 0 {
			id := b.AccountID
			dto.AccountID = &id
		}
		out = append(out, dto)
	}
	return out
}

// toFinanceTxnDTO maps a read-service TxnView to its wire DTO (dates to date-only
// strings). Shared by the transactions list handler.
func toFinanceTxnDTO(t finance.TxnView) FinanceTxnDTO {
	return FinanceTxnDTO{
		ID:           t.ID,
		AccountID:    t.AccountID,
		AccountName:  t.AccountName,
		PostedDate:   t.PostedDate.UTC().Format(dateLayout),
		Amount:       t.Amount,
		Description:  t.Description,
		Merchant:     t.Merchant,
		BalanceAfter: t.BalanceAfter,
	}
}

// parseQueryDate reads an optional YYYY-MM-DD query bound into a UTC *time.Time. An
// empty string is no filter (nil, nil); a malformed value is a 422 so a bad request
// fails loudly rather than silently ignoring the filter.
func parseQueryDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("invalid date " + s + " (want YYYY-MM-DD)")
	}
	t = t.UTC()
	return &t, nil
}

// rfc3339Ptr formats an optional instant as an RFC3339 string pointer (nil stays nil).
func rfc3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// dateOnlyPtr formats an optional instant as a YYYY-MM-DD string pointer (nil stays nil).
func dateOnlyPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(dateLayout)
	return &s
}
