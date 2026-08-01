package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
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

type billsArgs struct {
	Status     string `json:"status"`
	WithinDays int    `json:"within_days"`
	AccountID  int    `json:"account_id"`
}

type balanceHistoryArgs struct {
	AccountID int    `json:"account_id"`
	Step      string `json:"step"`
	From      string `json:"from"`
	To        string `json:"to"`
	Basis     string `json:"basis"`
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

// accountResult carries the owner-authored description and drawdown_policy alongside
// the bank's own fields. description is omitempty so an unlabelled account does not
// spend tokens on an empty string; drawdown_policy is always present because "unset" is
// itself information (it means the owner has not declared one, NOT that it is flexible).
type accountResult struct {
	ID              int      `json:"id"`
	Name            string   `json:"name"`
	MaskedNumber    string   `json:"masked_number"`
	Type            string   `json:"type"`
	Class           string   `json:"class"`
	Currency        string   `json:"currency"`
	Description     string   `json:"description,omitempty"`
	DrawdownPolicy  string   `json:"drawdown_policy"`
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

// billResult is one declared recurring commitment. expected_amount is the owner's
// declaration for the cycle; last_paid_amount is what the matched posted row actually
// charged, so the two differing is a repricing signal rather than an error.
type billResult struct {
	ID              int      `json:"id"`
	Name            string   `json:"name"`
	Payee           string   `json:"payee,omitempty"`
	Status          string   `json:"status"`
	Cadence         string   `json:"cadence"`
	ExpectedAmount  float64  `json:"expected_amount"`
	ExpectedMonthly float64  `json:"expected_monthly"`
	Currency        string   `json:"currency"`
	AmountVariable  bool     `json:"amount_variable"`
	NextDue         string   `json:"next_due"`
	DaysUntil       int      `json:"days_until"`
	Overdue         bool     `json:"overdue"`
	AutoMatched     bool     `json:"auto_matched"`
	LastPaidDate    *string  `json:"last_paid_date"`
	LastPaidAmount  *float64 `json:"last_paid_amount"`
	AccountID       *int     `json:"account_id"`
	AccountName     string   `json:"account_name,omitempty"`
}

// balancePointResult is one bucket on a balance series. as_of is the bucket START. The
// optional fields are omitempty so a basis=snapshot point stays the two numbers it is, and
// a model is not handed eight nulls per point to read past.
type balancePointResult struct {
	AsOf    string  `json:"as_of"`
	Balance float64 `json:"balance"`
	Carried *bool   `json:"carried,omitempty"`

	Open         *float64 `json:"open,omitempty"`
	Close        *float64 `json:"close,omitempty"`
	In           *float64 `json:"in,omitempty"`
	Out          *float64 `json:"out,omitempty"`
	Net          *float64 `json:"net,omitempty"`
	ExternalIn   *float64 `json:"external_in,omitempty"`
	ExternalOut  *float64 `json:"external_out,omitempty"`
	Txns         *int     `json:"txns,omitempty"`
	Source       *string  `json:"source,omitempty"`
	Drift        *float64 `json:"drift,omitempty"`
	FlowMismatch *bool    `json:"flow_mismatch,omitempty"`
}

// balanceSeriesResult is one account's balance series. first/last/change/change_pct answer
// "is this going up or down" without the model doing arithmetic over the points, and they
// always describe the full requested window even when points were truncated.
type balanceSeriesResult struct {
	AccountID   int    `json:"account_id"`
	AccountName string `json:"account_name"`
	Class       string `json:"class"`
	Currency    string `json:"currency"`

	First     *float64 `json:"first"`
	Last      *float64 `json:"last"`
	Change    *float64 `json:"change"`
	ChangePct *float64 `json:"change_pct"`

	// basis=ledger only.
	Basis           *string  `json:"basis,omitempty"`
	LedgerFrom      *string  `json:"ledger_from,omitempty"`
	StartUnverified *bool    `json:"start_unverified,omitempty"`
	DriftMax        *float64 `json:"drift_max,omitempty"`
	Note            *string  `json:"note,omitempty"`

	Points []balancePointResult `json:"points"`
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

	case "balance_history":
		var a balanceHistoryArgs
		if err := decodeArgs(rawArgs, &a); err != nil {
			return nil, err
		}
		// Enums are policed here, in Go, with a tool error rather than in the JSON schema, so
		// a mistyped value gets a message the model can act on.
		step, err := parseToolStep(a.Step)
		if err != nil {
			return nil, err
		}
		basis, err := finance.ParseBalanceBasis(strings.TrimSpace(a.Basis))
		if err != nil {
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
		fromT, toT := balanceWindow(from, to)
		series, err := finance.BalanceSeries(ctx, s.deps.Ent, finance.BalanceSeriesFilter{
			AccountID: a.AccountID,
			From:      &fromT,
			To:        &toT,
			Step:      step,
			Basis:     basis,
		})
		if err != nil {
			return nil, err
		}
		results, truncated := toBalanceSeriesResults(series, basis)
		res := map[string]any{
			"step":   string(step),
			"basis":  string(basis),
			"from":   fromT.UTC().Format(dateLayout),
			"to":     toT.UTC().Format(dateLayout),
			"series": results,
		}
		if truncated {
			// Only present when the point budget actually dropped buckets, so its absence
			// means a complete answer (no-silent-caps signal).
			res["truncated"] = true
			res["advice"] = "The point budget dropped the oldest buckets from at least one series. Coarsen step (day -> week -> month) or narrow the from/to window for a complete answer. first/last/change still describe the whole requested window."
		}
		return res, nil

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
		items, totals, truncated, err := finance.Wishlist(ctx, s.deps.Ent, finance.WishlistFilter{
			Status: a.Status,
			Limit:  a.Limit,
		})
		if err != nil {
			return nil, err
		}
		res := map[string]any{
			"items":              toWishlistResults(items),
			"item_count":         totals.ItemCount,
			"known_cost_total":   totals.KnownCostTotal,
			"unknown_cost_count": totals.UnknownCostCount,
			// Always present: it qualifies known_cost_total, so the model can see the
			// total is single-currency rather than having to assume it.
			"currency_mismatch_count": totals.CurrencyMismatchCount,
		}
		if truncated {
			// Only present when the limit actually dropped rows, so its absence means a
			// complete answer (no-silent-caps signal, as in list_transactions).
			res["truncated"] = true
		}
		return res, nil

	case "list_recurring_bills":
		var a billsArgs
		if err := decodeArgs(rawArgs, &a); err != nil {
			return nil, err
		}
		// The status enum is policed here, in Go, with a tool error the model can read,
		// rather than as a JSON-schema constraint (the read service rejects it too).
		status := strings.ToLower(strings.TrimSpace(a.Status))
		if status == "" {
			status = "active"
		}
		switch status {
		case "active", "paused", "ended", "all":
		default:
			return nil, fmt.Errorf("invalid status %q (want active, paused, ended or all)", a.Status)
		}
		bills, billTotals, err := finance.ListRecurringBills(ctx, s.deps.Ent, finance.BillFilter{
			Status:     status,
			WithinDays: a.WithinDays,
			AccountID:  a.AccountID,
		})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"bills":              toBillResults(bills),
			"committed_total":    billTotals.CommittedTotal,
			"monthly_equivalent": billTotals.MonthlyEquivalent,
			"count":              billTotals.Count,
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
			"description": "List every finance account with its type, class (asset or liability), currency, and latest balance/available/credit-limit snapshot, plus two owner-authored fields. `description` is a free-text note on what the account is actually for. `drawdown_policy` is one of flexible (money moves in and out), no_drawdown (the balance must not fall), emergency_only (reachable, but not for ordinary spending) or unset. Two accounts of the same type can serve completely different purposes, so read the description before treating a balance as spendable, and never count a no_drawdown balance as available for new spending. `unset` means the owner has not labelled that account yet, NOT that it is flexible: say so rather than assuming. A missing `description` likewise means unwritten.",
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
			"name":        "balance_history",
			"description": "Balance trend over time for one account or every account: the balance as the bank reported it, bucketed into day, week or month steps. Each point is the LAST reading in its bucket (close-of-period), because a balance is a level rather than a sum: points are never averaged or added together. Buckets align to Australia/Melbourne local boundaries, so a month bucket starts on the local 1st and a week bucket on the local Monday, and each point's as_of is the bucket START rendered in that local zone (e.g. 2026-08-01T00:00:00+10:00), so read the date off it directly rather than converting to UTC first. A bucket with no reading repeats the previous close and is marked \"carried\": true, so a missed sync does not read as a balance change; buckets before a series' start are omitted rather than back-filled. Each series also carries first, last, change and change_pct, so \"is this going up or down\" needs no arithmetic over the points. step defaults to \"month\" and the window to the last 12 months, which keeps a year of history cheap to read; ask for \"day\" only over a short window. Omit account_id to get every account as a separate series. Set basis=\"ledger\" to derive the same series from posted transactions instead, which adds per-bucket open, close, in, out, net, external_in, external_out and txns, and reaches back as far as the ledger does rather than as far as the balance readings do (the readings only started accumulating recently, so basis=\"ledger\" is where the depth is). Under basis=\"ledger\" every point carries \"source\": \"balance_after\" means the bank's own running balance at that bucket's last row and cannot drift, \"accumulated\" means arithmetic walked from the newest balance reading and is exact only if the ledger in between is complete, \"carried\" means a repeat of the previous close. Ledger flows use the SAME internal-transfer rule as spending_summary and monthly_summary: in/out are gross over every posted row, external_in/external_out exclude transfers between the owner's own accounts, card payments and StepPay repayments, out and external_out are positive figures, and txns counts every row including internal legs. Each series reports ledger_from (the oldest posted row, so a flat line is distinguishable from a ledger that does not reach that far back), start_unverified when the earliest bucket's opening had to be synthesized, per-bucket drift where a balance reading falls in the same bucket (nonzero means a dropped or duplicated transaction), flow_mismatch when either reconciliation check fails for that bucket (close - open not equalling net, meaning a row is missing from the bucket, OR two consecutive rows whose running balances differ by something other than the intervening amount, which catches offsetting errors that leave the bucket total intact; the offending row ids are logged), and drift_max for the series. investment accounts are excluded from basis=\"ledger\" because their balance moves with the market with no transaction behind it. If a request would exceed the point budget the response carries \"truncated\": true, in which case coarsen step or narrow the window. To ask how much was spent or earned in a period use spending_summary or monthly_summary; this tool answers where the balance stood.",
			"inputSchema": objectSchema(map[string]any{
				"account_id": intProp("Restrict to one account id; omit or 0 for every account as its own series."),
				"step":       strProp("Bucket width: day, week or month. Defaults to month."),
				"from":       strProp("Inclusive window start, YYYY-MM-DD. Defaults to 12 months before `to`."),
				"to":         strProp("Inclusive window end, YYYY-MM-DD. Defaults to today (Australia/Melbourne)."),
				"basis":      strProp("snapshot (default) for the bank's balance readings, or ledger to derive closes and per-period in/out from posted transactions."),
			}, nil),
		},
		{
			"name":        "list_pending",
			"description": "List pending (not-yet-settled) transactions across all accounts, newest first. Pending rows are volatile and are replaced on each sync.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "list_wishlist",
			"description": "The owner's wishlist: things he wants to buy or pay for once, such as a new computer, a bag, or a pending car service payment. Read this before answering whether a purchase is a good idea, what he is saving toward, or how one want compares in cost and urgency to everything already queued. Each item has a status (wanted, bought, abandoned), a priority (low, medium, high), an amount in AUD, an optional deadline (YYYY-MM-DD), an optional link, and a description. amount_is_estimate=true means the price is the owner's guess, not a quote, so treat it as approximate. amount is null when the cost is unknown: that means unknown, NOT free, so say the cost is unrecorded rather than assuming zero. Defaults to status=wanted, which is what is still outstanding; pass status=all to include bought and abandoned items, which is how you check whether something was already bought or already decided against. Items are ordered by priority, then nearest deadline, then newest. Also returns totals: item_count, known_cost_total (the sum of the non-null amounts in this response) and unknown_cost_count. known_cost_total is AUD only, and currency_mismatch_count says how many priced items were left out of it for carrying another currency, so add those separately rather than assuming the total covers them. If the response carries \"truncated\": true the limit dropped the lowest-priority items from both the list and the totals; raise limit (up to 500) for a complete answer, and its absence means the answer is complete. This list is a set of intentions, not a budget and not a commitment: to judge affordability combine it with get_net_worth and spending_summary rather than reasoning from the wishlist alone.",
			"inputSchema": objectSchema(map[string]any{
				"status": strProp("Which items to return: \"wanted\" (default, still outstanding), \"bought\", \"abandoned\", or \"all\"."),
				"limit":  intProp("Maximum rows to return (default 100, capped at 500)."),
			}, nil),
		},
		{
			"name":        "list_recurring_bills",
			"description": "List the owner's known recurring commitments (rent, insurance, subscriptions, utilities) with each one's cadence, expected amount, the next date it falls due, and how many days away that is. Use this for any question about COMMITTED money rather than money already spent: what is due in the next N days, what is spoken for before any discretionary spending, or whether a one-off purchase is actually affordable. A recurring bill is a DECLARED commitment, not a ledger row: it is separate from transactions, so expected_amount is what is expected to leave the account each cycle, while the amount actually charged last cycle (matched to a posted transaction) is reported as last_paid_amount/last_paid_date, and those two differing is the signal a bill changed price. days_until is negative when a cycle is past due with no matching payment found. Pass within_days to keep only bills whose next occurrence falls inside that many days from today, and status (default \"active\") to include paused or ended commitments. committed_total sums the expected amounts of the returned bills, and monthly_equivalent normalises every cadence to a per-month figure so bills on different cadences are comparable: free money over a period is roughly that period's income minus committed_total minus the discretionary spend that spending_summary reports. Expected amounts are the owner's declarations, so treat a bill with amount_variable=true (utilities) as an estimate, not a fact. A bill with auto_matched=false carries no match pattern, so it is reconciled by hand: no payment is ever matched to it automatically, its last_paid fields stay empty, and it is never reported overdue, so absence of a payment there says nothing about whether it was paid.",
			"inputSchema": objectSchema(map[string]any{
				"status":      strProp(`Which commitments to include: "active" (default), "paused", "ended", or "all".`),
				"within_days": intProp("Keep only bills whose next occurrence falls within this many days from today; omit or 0 for all bills."),
				"account_id":  intProp("Restrict to bills paid from one account id; omit or 0 for all. Bills with no account set are excluded when this is given."),
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
			Description:     a.Description,
			DrawdownPolicy:  a.DrawdownPolicy,
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

func toBillResults(bills []finance.BillView) []billResult {
	out := make([]billResult, 0, len(bills))
	for _, b := range bills {
		br := billResult{
			ID:              b.ID,
			Name:            b.Name,
			Payee:           b.Payee,
			Status:          b.Status,
			Cadence:         b.Cadence,
			ExpectedAmount:  b.ExpectedAmount,
			ExpectedMonthly: b.ExpectedMonthly,
			Currency:        b.Currency,
			AmountVariable:  b.AmountVariable,
			NextDue:         b.NextDue.UTC().Format(dateLayout),
			DaysUntil:       b.DaysUntil,
			Overdue:         b.Overdue,
			AutoMatched:     b.AutoMatched,
			LastPaidDate:    fmtDateOnlyPtr(b.LastPaidDate),
			LastPaidAmount:  b.LastPaidAmount,
			AccountName:     b.AccountName,
		}
		if b.AccountID != 0 {
			id := b.AccountID
			br.AccountID = &id
		}
		out = append(out, br)
	}
	return out
}

// balanceSnapshotPointBudget / balanceLedgerPointBudget bound how many buckets one
// balance_history call hands a model. A year of DAILY points for six accounts is a couple
// of thousand objects that mostly answer the same question a month-end close answers, so
// the budget is what makes balance history affordable to expose to an LLM at all.
//
// The ledger budget is much lower because a ledger point carries roughly eight numbers
// against a snapshot point's two. Going over is NOT silent: the oldest buckets are dropped
// (keeping the newest, like externalScanCap), truncated:true comes back with the escape
// hatch, and a slog.Warn fires. They are vars, not consts, so a test can lower them without
// seeding hundreds of buckets.
var (
	balanceSnapshotPointBudget = 400
	balanceLedgerPointBudget   = 150
)

// allotPoints shares the point budget across the series, water-filling from the shallowest
// so a series is never trimmed below what it would have fitted on its own.
//
// An even budget/n split looks fair and is not: two series of 149 and 5 points against a
// budget of 150 would drop 74 points to save 4, and report a truncation for a request only 3%
// over. Filling the small ones first hands the whole remainder to the deep one, which drops
// the 4 points actually over budget and nothing else.
func allotPoints(series []finance.BalanceSeriesView, budget int) []int {
	allot := make([]int, len(series))
	total := 0
	for i, s := range series {
		allot[i] = len(s.Points)
		total += len(s.Points)
	}
	if total <= budget || len(series) == 0 {
		return allot
	}

	// Ascending by size, so each pass gives a series either all it wants or an equal share of
	// what is left.
	idx := make([]int, len(series))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return len(series[idx[a]].Points) < len(series[idx[b]].Points)
	})

	remaining := budget
	left := len(idx)
	for _, i := range idx {
		share := remaining / left
		if share < 1 {
			share = 1 // never starve a series out of the answer entirely
		}
		if n := len(series[i].Points); n < share {
			share = n
		}
		allot[i] = share
		remaining -= share
		if remaining < 0 {
			remaining = 0
		}
		left--
	}
	return allot
}

// toBalanceSeriesResults maps the read-service series to the tool payload, applying the
// point budget (see allotPoints). Each series keeps its NEWEST buckets, mirroring how
// externalScanCap truncates. The bool return is truncated: it is set only when a series was
// actually trimmed, so its absence means a complete answer.
func toBalanceSeriesResults(series []finance.BalanceSeriesView, basis finance.BalanceBasis) ([]balanceSeriesResult, bool) {
	budget := balanceSnapshotPointBudget
	if basis == finance.BasisLedger {
		budget = balanceLedgerPointBudget
	}
	allot := allotPoints(series, budget)
	computed := 0
	for _, s := range series {
		computed += len(s.Points)
	}

	truncated := false
	out := make([]balanceSeriesResult, 0, len(series))
	for i, s := range series {
		pts := s.Points
		if len(pts) > allot[i] {
			pts = pts[len(pts)-allot[i]:]
			truncated = true
		}
		r := balanceSeriesResult{
			AccountID:   s.AccountID,
			AccountName: s.AccountName,
			Class:       s.Class,
			Currency:    s.Currency,
			First:       s.First,
			Last:        s.Last,
			Change:      s.Change,
			ChangePct:   s.ChangePct,
			Points:      make([]balancePointResult, 0, len(pts)),
		}
		if basis == finance.BasisLedger {
			b := string(s.Basis)
			r.Basis = &b
			r.LedgerFrom = fmtDateOnlyPtr(s.LedgerFrom)
			if s.StartUnverified {
				t := true
				r.StartUnverified = &t
			}
			d := s.DriftMax
			r.DriftMax = &d
		}
		if s.Note != "" {
			n := s.Note
			r.Note = &n
		}
		for _, p := range pts {
			r.Points = append(r.Points, toBalancePointResult(p))
		}
		out = append(out, r)
	}
	if truncated {
		slog.Warn("finance: balance_history hit the point budget; the oldest buckets were dropped from at least one series",
			"budget", budget, "basis", string(basis), "series", len(series), "points", computed)
	}
	return out, truncated
}

// toBalancePointResult maps one bucket to the tool payload. Every point the tool emits is a
// bucket START (the tool has no raw mode), so as_of is rendered in the bucket zone: the UTC
// form of a local midnight names the period BEFORE the bucket it labels, which would make a
// model attribute every close and every in/out total to the wrong month or day.
func toBalancePointResult(p finance.BalancePoint) balancePointResult {
	r := balancePointResult{
		AsOf:        p.AsOf.In(finance.BucketZone()).Format(time.RFC3339),
		Balance:     p.Balance,
		Open:        p.Open,
		Close:       p.Close,
		In:          p.In,
		Out:         p.Out,
		Net:         p.Net,
		ExternalIn:  p.ExternalIn,
		ExternalOut: p.ExternalOut,
		Txns:        p.Txns,
		Drift:       p.Drift,
	}
	if p.Carried {
		t := true
		r.Carried = &t
	}
	if p.Source != "" {
		s := p.Source
		r.Source = &s
	}
	if p.FlowMismatch {
		t := true
		r.FlowMismatch = &t
	}
	return r
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

// parseToolStep resolves the balance_history step argument. Unlike the HTTP endpoint the
// tool has no raw mode (a raw per-reading list is a token sink and answers nothing extra),
// so an empty step means the month default, and an unrecognised value is a tool error
// rather than a silent fall back.
func parseToolStep(s string) (finance.BalanceStep, error) {
	switch step := finance.BalanceStep(strings.TrimSpace(s)); step {
	case "":
		return finance.StepMonth, nil
	case finance.StepDay, finance.StepWeek, finance.StepMonth:
		return step, nil
	default:
		return "", fmt.Errorf("unknown step %q (want day, week or month)", s)
	}
}

// balanceWindow resolves the balance_history window from optional parsed bounds: `to`
// defaults to today in Australia/Melbourne (not UTC, which is the previous local day for
// the first ten hours of every Melbourne day), and `from` to 12 months before the effective
// `to`, so a bare call means "the last 12 months". Both bounds are inclusive dates read at
// bucket granularity.
func balanceWindow(from, to *time.Time) (time.Time, time.Time) {
	toT := finance.LocalToday()
	if to != nil {
		toT = *to
	}
	fromT := toT.AddDate(0, -12, 0)
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
