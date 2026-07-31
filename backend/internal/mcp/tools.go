package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// The finance MCP tool surface (ADR 0017). Every tool is read-only and calls the
// shared finance read service (internal/finance.read.go) in-process, so the MCP tools
// and the admin dashboard endpoints run identical queries. Results are returned as a
// single JSON text content block: an LLM reads structured JSON well, and it keeps the
// tool output stable regardless of the client.
//
// Deliberately absent: spending-by-category. Categorisation needs a data-enrichment
// pass (LLM/rules over descriptions) that is not built yet, so a category tool would
// return nothing useful. See ADR 0017.

const dateLayout = "2006-01-02"

// errUnknownTool is the sentinel for a tools/call naming a tool that does not exist;
// the dispatcher maps it to a JSON-RPC -32602 rather than a tool-error result.
var errUnknownTool = errors.New("unknown tool")

// toolCallParams is the params of a tools/call request.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

// --- tool argument shapes ---

type listTxnArgs struct {
	AccountID    int    `json:"account_id"`
	From         string `json:"from"`
	To           string `json:"to"`
	Limit        int    `json:"limit"`
	ExternalOnly bool   `json:"external_only"`
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
	From  string `json:"from"`
	To    string `json:"to"`
}

type monthlyArgs struct {
	AccountID int `json:"account_id"`
	Months    int `json:"months"`
}

type spendingArgs struct {
	From      string `json:"from"`
	To        string `json:"to"`
	AccountID int    `json:"account_id"`
}

type wishlistArgs struct {
	Status string `json:"status"`
	Limit  int    `json:"limit"`
}

// --- tool result shapes (dates rendered as strings for a readable payload) ---

type summaryResult struct {
	NetWorth     float64 `json:"net_worth"`
	Assets       float64 `json:"assets"`
	Liabilities  float64 `json:"liabilities"`
	Currency     string  `json:"currency"`
	AccountCount int     `json:"account_count"`
	AsOf         *string `json:"as_of"`
}

type accountResult struct {
	ID              int      `json:"id"`
	Name            string   `json:"name"`
	MaskedNumber    string   `json:"masked_number"`
	Type            string   `json:"type"`
	Class           string   `json:"class"`
	Currency        string   `json:"currency"`
	Balance         *float64 `json:"balance"`
	Available       *float64 `json:"available"`
	CreditLimit     *float64 `json:"credit_limit"`
	BalanceAsOf     *string  `json:"balance_as_of"`
	PostedWatermark *string  `json:"posted_watermark"`
}

type txnResult struct {
	ID           int      `json:"id"`
	AccountID    int      `json:"account_id"`
	AccountName  string   `json:"account_name"`
	PostedDate   string   `json:"posted_date"`
	Amount       float64  `json:"amount"`
	Description  string   `json:"description"`
	Merchant     string   `json:"merchant"`
	BalanceAfter *float64 `json:"balance_after"`
}

type pendingResult struct {
	ID          int     `json:"id"`
	AccountID   int     `json:"account_id"`
	AccountName string  `json:"account_name"`
	Date        string  `json:"date"`
	Amount      float64 `json:"amount"`
	Description string  `json:"description"`
	Merchant    string  `json:"merchant"`
}

type wishlistResult struct {
	ID               int      `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	Amount           *float64 `json:"amount"`
	AmountIsEstimate bool     `json:"amount_is_estimate"`
	Currency         string   `json:"currency"`
	Priority         string   `json:"priority"`
	Status           string   `json:"status"`
	Deadline         *string  `json:"deadline"`
	ResolvedAt       *string  `json:"resolved_at"`
	Link             string   `json:"link"`
	ImageKey         string   `json:"image_key"`
}

type monthResult struct {
	Month     string  `json:"month"`
	Income    float64 `json:"income"`
	Spend     float64 `json:"spend"`
	Net       float64 `json:"net"`
	Transfers float64 `json:"internal_transfers_excluded"`
}

type spendingResult struct {
	From          string  `json:"from"`
	To            string  `json:"to"`
	ExternalSpend float64 `json:"external_spend"`
	Income        float64 `json:"income"`
	Net           float64 `json:"net"`
	Transfers     float64 `json:"internal_transfers_excluded"`
	TxnCount      int     `json:"txn_count"`
}

// handleToolsCall executes a tools/call and wraps the result. A structural failure
// (bad params, unknown tool) is a JSON-RPC error; a domain/validation failure (bad
// date, missing query, query error) is a tool-error result (isError=true) so the model
// sees the message. A successful call returns one JSON text content block.
func (s *server) handleToolsCall(ctx context.Context, req *rpcRequest) rpcResponse {
	var p toolCallParams
	if len(req.Params) == 0 || json.Unmarshal(req.Params, &p) != nil {
		return errorResponse(req.ID, codeInvalidParams, "invalid params: expected {name, arguments}")
	}
	if s.deps.Ent == nil {
		return errorResponse(req.ID, codeInternalError, "finance data is not available")
	}

	payload, err := s.callTool(ctx, p.Name, p.Arguments)
	if errors.Is(err, errUnknownTool) {
		return errorResponse(req.ID, codeInvalidParams, err.Error())
	}
	if err != nil {
		return resultResponse(req.ID, toolError(err.Error()))
	}
	text, mErr := json.MarshalIndent(payload, "", "  ")
	if mErr != nil {
		return resultResponse(req.ID, toolError("failed to encode result"))
	}
	return resultResponse(req.ID, toolText(string(text)))
}

// callTool routes a named tool to the finance read service and returns the domain
// payload to be JSON-encoded, or an error (errUnknownTool for an unknown name).
func (s *server) callTool(ctx context.Context, name string, rawArgs json.RawMessage) (any, error) {
	switch name {
	case "get_net_worth":
		sum, err := finance.NetWorthSummary(ctx, s.deps.Ent)
		if err != nil {
			return nil, err
		}
		return toSummaryResult(sum), nil

	case "list_accounts":
		accs, err := finance.Accounts(ctx, s.deps.Ent)
		if err != nil {
			return nil, err
		}
		return map[string]any{"accounts": toAccountResults(accs)}, nil

	case "list_transactions":
		var a listTxnArgs
		if err := decodeArgs(rawArgs, &a); err != nil {
			return nil, err
		}
		from, err := parseToolDate(a.From)
		if err != nil {
			return nil, err
		}
		to, err := parseToolDate(a.To)
		if err != nil {
			return nil, err
		}
		txns, total, truncated, err := finance.ListTransactions(ctx, s.deps.Ent, finance.TxnFilter{
			AccountID:    a.AccountID,
			From:         from,
			To:           to,
			Limit:        a.Limit,
			ExternalOnly: a.ExternalOnly,
		})
		if err != nil {
			return nil, err
		}
		res := map[string]any{"transactions": toTxnResults(txns), "total": total}
		if truncated {
			// Only present when the external_only safety cap actually dropped older rows, so
			// its absence means a complete answer (no-silent-caps signal).
			res["truncated"] = true
		}
		return res, nil

	case "search_merchant":
		var a searchArgs
		if err := decodeArgs(rawArgs, &a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Query) == "" {
			return nil, errors.New("query is required and must be a non-empty string")
		}
		from, err := parseToolDate(a.From)
		if err != nil {
			return nil, err
		}
		to, err := parseToolDate(a.To)
		if err != nil {
			return nil, err
		}
		txns, err := finance.SearchMerchant(ctx, s.deps.Ent, a.Query, a.Limit, from, to)
		if err != nil {
			return nil, err
		}
		return map[string]any{"transactions": toTxnResults(txns)}, nil

	case "spending_summary":
		var a spendingArgs
		if err := decodeArgs(rawArgs, &a); err != nil {
			return nil, err
		}
		from, err := parseToolDate(a.From)
		if err != nil {
			return nil, err
		}
		to, err := parseToolDate(a.To)
		if err != nil {
			return nil, err
		}
		fromT, toT := spendingWindow(from, to)
		bucket, err := finance.SpendingSummary(ctx, s.deps.Ent, a.AccountID, fromT, toT)
		if err != nil {
			return nil, err
		}
		return toSpendingResult(bucket), nil

	case "monthly_summary":
		var a monthlyArgs
		if err := decodeArgs(rawArgs, &a); err != nil {
			return nil, err
		}
		buckets, err := finance.MonthlySummary(ctx, s.deps.Ent, a.AccountID, a.Months)
		if err != nil {
			return nil, err
		}
		return map[string]any{"months": toMonthResults(buckets)}, nil

	case "list_pending":
		pend, err := finance.Pending(ctx, s.deps.Ent, 0)
		if err != nil {
			return nil, err
		}
		return map[string]any{"pending": toPendingResults(pend)}, nil

	case "list_wishlist":
		var a wishlistArgs
		if err := decodeArgs(rawArgs, &a); err != nil {
			return nil, err
		}
		// An unknown status is rejected by the read service (a domain error the model
		// sees), not by the input schema, so the allowed set lives in one place.
		items, totals, err := finance.Wishlist(ctx, s.deps.Ent, finance.WishlistFilter{
			Status: a.Status,
			Limit:  a.Limit,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"items":              toWishlistResults(items),
			"item_count":         totals.ItemCount,
			"known_cost_total":   totals.KnownCostTotal,
			"unknown_cost_count": totals.UnknownCostCount,
		}, nil

	default:
		return nil, fmt.Errorf("%w: %s", errUnknownTool, name)
	}
}

// toolDefinitions is the tools/list payload: each tool's name, description and JSON
// input schema. additionalProperties:false on every schema so a client that fat-fingers
// an argument name is told, rather than silently ignored.
func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "get_net_worth",
			"description": "Current net worth in AUD: total assets, total liabilities (reported as a positive amount owed), net worth (assets minus liabilities), the number of accounts, and the freshest balance reading time.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "list_accounts",
			"description": "List every finance account with its type, class (asset or liability), currency, and latest balance/available/credit-limit snapshot.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "list_transactions",
			"description": "List posted (settled) transactions, newest first. Optionally filter by account_id and an inclusive from/to date range (YYYY-MM-DD). Set external_only=true to exclude internal money moves (transfers between the owner's own accounts, credit-card payments and StepPay repayments, identified from the description) so only payments that actually leave the bank remain; the returned total then counts external rows only. Returns up to `limit` rows (default 50, max 500) plus the total matching count. In external_only mode, if you omit `from` and the ledger is larger than an internal safety cap, the response carries \"truncated\": true and total/rows cover only the newest scanned rows; pass a `from` date to guarantee a complete answer.",
			"inputSchema": objectSchema(map[string]any{
				"account_id":    intProp("Restrict to one account id; omit or 0 for all accounts."),
				"from":          strProp("Inclusive lower bound date, YYYY-MM-DD."),
				"to":            strProp("Inclusive upper bound date, YYYY-MM-DD."),
				"limit":         intProp("Maximum rows to return (default 50, capped at 500)."),
				"external_only": boolProp("When true, drop internal transfers, card payments and StepPay repayments, leaving only real external spend/income. Default false."),
			}, nil),
		},
		{
			"name":        "search_merchant",
			"description": "Find posted transactions whose merchant OR description contains the query (case-insensitive substring), newest first. Optionally restrict to an inclusive from/to date range (YYYY-MM-DD).",
			"inputSchema": objectSchema(map[string]any{
				"query": strProp("Substring to match against merchant or description (required)."),
				"limit": intProp("Maximum rows to return (default 50, capped at 500)."),
				"from":  strProp("Inclusive lower bound date, YYYY-MM-DD."),
				"to":    strProp("Inclusive upper bound date, YYYY-MM-DD."),
			}, []string{"query"}),
		},
		{
			"name":        "monthly_summary",
			"description": "Per-calendar-month income, spend (positive figure) and net for the last N months (default 6). Income and spend count only EXTERNAL money: internal transfers between the owner's own accounts, credit-card payments and StepPay repayments are excluded from both (identified from the transaction description), and the excluded volume is reported as internal_transfers_excluded. A transfer to someone else still counts as spend. Optionally scope to one account_id.",
			"inputSchema": objectSchema(map[string]any{
				"account_id": intProp("Restrict to one account id; omit or 0 for all accounts."),
				"months":     intProp("Number of trailing calendar months to bucket (default 6)."),
			}, nil),
		},
		{
			"name":        "spending_summary",
			"description": "External income, spend and net over one arbitrary date window in a single figure, so \"how much did I spend last week/month\" is one call instead of summing a row list. Income and external_spend count only money that actually leaves or enters the bank: internal transfers between the owner's own accounts, credit-card payments and StepPay repayments are excluded from both (their volume is reported as internal_transfers_excluded); a transfer to another person still counts as spend. external_spend is a positive figure; net = income - external_spend; txn_count is every posted row in the window. Defaults to the last 30 days when `from` is omitted, and to today when `to` is omitted. Optionally scope to one account_id.",
			"inputSchema": objectSchema(map[string]any{
				"from":       strProp("Inclusive window start, YYYY-MM-DD. Defaults to 30 days before `to`."),
				"to":         strProp("Inclusive window end, YYYY-MM-DD. Defaults to today (UTC)."),
				"account_id": intProp("Restrict to one account id; omit or 0 for all accounts."),
			}, nil),
		},
		{
			"name":        "list_pending",
			"description": "List pending (not-yet-settled) transactions across all accounts, newest first. Pending rows are volatile and are replaced on each sync.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "list_wishlist",
			"description": "The owner's wishlist: things he wants to buy or pay for once, such as a new computer, a bag, or a pending car service payment. Read this before answering whether a purchase is a good idea, what he is saving toward, or how one want compares in cost and urgency to everything already queued. Each item has a status (wanted, bought, abandoned), a priority (low, medium, high), an amount in AUD, an optional deadline (YYYY-MM-DD), an optional link, and a description. amount_is_estimate=true means the price is the owner's guess, not a quote, so treat it as approximate. amount is null when the cost is unknown: that means unknown, NOT free, so say the cost is unrecorded rather than assuming zero. Defaults to status=wanted, which is what is still outstanding; pass status=all to include bought and abandoned items, which is how you check whether something was already bought or already decided against. Items are ordered by priority, then nearest deadline, then newest. Also returns totals: item_count, known_cost_total (the sum of the non-null amounts in this response) and unknown_cost_count. This list is a set of intentions, not a budget and not a commitment: to judge affordability combine it with get_net_worth and spending_summary rather than reasoning from the wishlist alone.",
			"inputSchema": objectSchema(map[string]any{
				"status": strProp("Which items to return: \"wanted\" (default, still outstanding), \"bought\", \"abandoned\", or \"all\"."),
				"limit":  intProp("Maximum rows to return (default 100, capped at 500)."),
			}, nil),
		},
	}
}

// --- mappers ---

func toSummaryResult(s finance.SummaryView) summaryResult {
	return summaryResult{
		NetWorth:     s.NetWorth,
		Assets:       s.Assets,
		Liabilities:  s.Liabilities,
		Currency:     s.Currency,
		AccountCount: s.AccountCount,
		AsOf:         fmtRFC3339Ptr(s.AsOf),
	}
}

func toAccountResults(accs []finance.AccountView) []accountResult {
	out := make([]accountResult, 0, len(accs))
	for _, a := range accs {
		out = append(out, accountResult{
			ID:              a.ID,
			Name:            a.Name,
			MaskedNumber:    a.MaskedNumber,
			Type:            a.Type,
			Class:           a.Class,
			Currency:        a.Currency,
			Balance:         a.Balance,
			Available:       a.Available,
			CreditLimit:     a.CreditLimit,
			BalanceAsOf:     fmtRFC3339Ptr(a.BalanceAsOf),
			PostedWatermark: fmtDateOnlyPtr(a.PostedWatermark),
		})
	}
	return out
}

func toTxnResults(txns []finance.TxnView) []txnResult {
	out := make([]txnResult, 0, len(txns))
	for _, t := range txns {
		out = append(out, txnResult{
			ID:           t.ID,
			AccountID:    t.AccountID,
			AccountName:  t.AccountName,
			PostedDate:   t.PostedDate.UTC().Format(dateLayout),
			Amount:       t.Amount,
			Description:  t.Description,
			Merchant:     t.Merchant,
			BalanceAfter: t.BalanceAfter,
		})
	}
	return out
}

func toPendingResults(pend []finance.PendingView) []pendingResult {
	out := make([]pendingResult, 0, len(pend))
	for _, p := range pend {
		out = append(out, pendingResult{
			ID:          p.ID,
			AccountID:   p.AccountID,
			AccountName: p.AccountName,
			Date:        p.Date.UTC().Format(dateLayout),
			Amount:      p.Amount,
			Description: p.Description,
			Merchant:    p.Merchant,
		})
	}
	return out
}

// toWishlistResults maps wishlist rows to the model-facing shape: the deadline as a
// plain date, resolved_at as an instant, and amount left null when the price is unknown
// so the model cannot read it as zero.
func toWishlistResults(items []finance.WishlistView) []wishlistResult {
	out := make([]wishlistResult, 0, len(items))
	for _, w := range items {
		out = append(out, wishlistResult{
			ID:               w.ID,
			Name:             w.Name,
			Description:      w.Description,
			Amount:           w.Amount,
			AmountIsEstimate: w.AmountIsEstimate,
			Currency:         w.Currency,
			Priority:         w.Priority,
			Status:           w.Status,
			Deadline:         fmtDateOnlyPtr(w.Deadline),
			ResolvedAt:       fmtRFC3339Ptr(w.ResolvedAt),
			Link:             w.Link,
			ImageKey:         w.ImageKey,
		})
	}
	return out
}

func toMonthResults(buckets []finance.MonthBucket) []monthResult {
	out := make([]monthResult, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, monthResult{Month: b.Month, Income: b.Income, Spend: b.Spend, Net: b.Net, Transfers: b.Transfers})
	}
	return out
}

func toSpendingResult(b finance.SpendBucket) spendingResult {
	return spendingResult{
		From:          b.From.UTC().Format(dateLayout),
		To:            b.To.UTC().Format(dateLayout),
		ExternalSpend: b.Spend,
		Income:        b.Income,
		Net:           b.Net,
		Transfers:     b.Transfers,
		TxnCount:      b.TxnCount,
	}
}

// --- small helpers ---

// decodeArgs unmarshals tool arguments into v. Absent arguments (a no-arg tool call)
// are fine and leave v at its zero value.
func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("invalid arguments: %v", err)
	}
	return nil
}

// parseToolDate reads an optional YYYY-MM-DD argument into a UTC *time.Time; empty is
// no filter, malformed is a domain error surfaced to the model.
func parseToolDate(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		return nil, fmt.Errorf("invalid date %q (want YYYY-MM-DD)", s)
	}
	t = t.UTC()
	return &t, nil
}

// spendingWindow resolves the spending_summary window from optional parsed bounds: `to`
// defaults to today (UTC midnight), and `from` to 30 days before the effective `to`, so a
// bare call means "the last 30 days". Both bounds are inclusive UTC dates.
func spendingWindow(from, to *time.Time) (time.Time, time.Time) {
	now := time.Now().UTC()
	toT := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if to != nil {
		toT = *to
	}
	fromT := toT.AddDate(0, 0, -30)
	if from != nil {
		fromT = *from
	}
	return fromT, toT
}

func fmtRFC3339Ptr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

func fmtDateOnlyPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(dateLayout)
	return &s
}

// toolText wraps a successful tool result as a single JSON text content block.
func toolText(s string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": s}},
		"isError": false,
	}
}

// toolError wraps a tool-level failure so the model sees the message (MCP reports
// execution errors in the result with isError, not as JSON-RPC protocol errors).
func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

// objectSchema builds a JSON Schema object with the given properties and required
// names; nil props means a no-argument tool.
func objectSchema(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
