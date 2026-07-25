package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// The finance dashboard read API (ADR 0017). Five admin-only GET endpoints over the
// finance ledger, each backed by the pure read service (internal/finance.read.go) so
// the same query path serves the /admin dashboard and the remote MCP tools. Every
// operation is cookie-gated and calls requireAdmin as its first line: finance is
// single-tenant (Alif's) and never friend/member-visible. The write side stays the
// token-authed ingest (ADR 0015); nothing here mutates.

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
	txns, total, err := finance.ListTransactions(ctx, h.deps.Ent, finance.TxnFilter{
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
