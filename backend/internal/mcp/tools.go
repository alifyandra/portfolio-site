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
	AccountID int    `json:"account_id"`
	From      string `json:"from"`
	To        string `json:"to"`
	Limit     int    `json:"limit"`
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

type monthlyArgs struct {
	AccountID int `json:"account_id"`
	Months    int `json:"months"`
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

type monthResult struct {
	Month  string  `json:"month"`
	Income float64 `json:"income"`
	Spend  float64 `json:"spend"`
	Net    float64 `json:"net"`
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
		txns, total, err := finance.ListTransactions(ctx, s.deps.Ent, finance.TxnFilter{
			AccountID: a.AccountID,
			From:      from,
			To:        to,
			Limit:     a.Limit,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{"transactions": toTxnResults(txns), "total": total}, nil

	case "search_merchant":
		var a searchArgs
		if err := decodeArgs(rawArgs, &a); err != nil {
			return nil, err
		}
		if strings.TrimSpace(a.Query) == "" {
			return nil, errors.New("query is required and must be a non-empty string")
		}
		txns, err := finance.SearchMerchant(ctx, s.deps.Ent, a.Query, a.Limit)
		if err != nil {
			return nil, err
		}
		return map[string]any{"transactions": toTxnResults(txns)}, nil

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
			"description": "List posted (settled) transactions, newest first. Optionally filter by account_id and an inclusive from/to date range (YYYY-MM-DD). Returns up to `limit` rows (default 50, max 500) plus the total matching count.",
			"inputSchema": objectSchema(map[string]any{
				"account_id": intProp("Restrict to one account id; omit or 0 for all accounts."),
				"from":       strProp("Inclusive lower bound date, YYYY-MM-DD."),
				"to":         strProp("Inclusive upper bound date, YYYY-MM-DD."),
				"limit":      intProp("Maximum rows to return (default 50, capped at 500)."),
			}, nil),
		},
		{
			"name":        "search_merchant",
			"description": "Find posted transactions whose merchant OR description contains the query (case-insensitive substring), newest first.",
			"inputSchema": objectSchema(map[string]any{
				"query": strProp("Substring to match against merchant or description (required)."),
				"limit": intProp("Maximum rows to return (default 50, capped at 500)."),
			}, []string{"query"}),
		},
		{
			"name":        "monthly_summary",
			"description": "Per-calendar-month income (sum of money in), spend (sum of money out as a positive figure) and net, for the last N months (default 6). Optionally scope to one account_id.",
			"inputSchema": objectSchema(map[string]any{
				"account_id": intProp("Restrict to one account id; omit or 0 for all accounts."),
				"months":     intProp("Number of trailing calendar months to bucket (default 6)."),
			}, nil),
		},
		{
			"name":        "list_pending",
			"description": "List pending (not-yet-settled) transactions across all accounts, newest first. Pending rows are volatile and are replaced on each sync.",
			"inputSchema": objectSchema(nil, nil),
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

func toMonthResults(buckets []finance.MonthBucket) []monthResult {
	out := make([]monthResult, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, monthResult{Month: b.Month, Income: b.Income, Spend: b.Spend, Net: b.Net})
	}
	return out
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
