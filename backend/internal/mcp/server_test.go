package mcp

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/wishlistitem"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/finance"
)

// newMCPTestServer builds the MCP handler over an in-memory SQLite ledger with a real
// auth service (so a real finance.read bearer token drives the full resolve -> scope
// gate -> tool path), and seeds one asset account with a 1000 snapshot so
// get_net_worth returns a known value.
func newMCPTestServer(t *testing.T) (http.Handler, *auth.Service, *ent.Client) {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	acc := client.Account.Create().
		SetSource("commbank").
		SetName("Smart Access").
		SetType(account.TypeEveryday).
		SetClass(account.ClassAsset).
		SetCurrency("AUD").
		SaveX(ctx)
	client.BalanceSnapshot.Create().
		SetBalance(1000.00).
		SetAsOf(time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)).
		SetAccountID(acc.ID).
		SaveX(ctx)

	svc := auth.New(client, auth.Config{})
	return Handler(Deps{Ent: client, Auth: svc}), svc, client
}

// mintFinanceReadToken creates an owning user and a bearer token with the given scope.
func mintToken(t *testing.T, ctx context.Context, svc *auth.Service, client *ent.Client, scope []string) string {
	t.Helper()
	u := client.User.Create().SetEmail("mcp@x.com").SaveX(ctx)
	raw, _, err := svc.MintApiToken(ctx, u.ID, "mcp token", "mcp", scope, nil)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return raw
}

// rpc POSTs a JSON-RPC body to the handler with an optional bearer token and returns
// the recorder plus the decoded JSON body (nil for an empty body).
func rpc(t *testing.T, h http.Handler, token string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	var out map[string]any
	if rr.Body.Len() > 0 {
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
	}
	return rr, out
}

// TestMCP_NoTokenIs401: an unauthenticated POST is 401 with a Bearer challenge, and
// never reaches the JSON-RPC layer.
func TestMCP_NoTokenIs401(t *testing.T) {
	h, _, _ := newMCPTestServer(t)
	rr, _ := rpc(t, h, "", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("no-token = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want a Bearer challenge", got)
	}
}

// TestMCP_WrongScopeIs401: a valid token WITHOUT finance.read is rejected (401), so a
// leaked ingest (finance.sync) token cannot read the ledger.
func TestMCP_WrongScopeIs401(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.sync"}) // write scope, not read
	rr, _ := rpc(t, h, raw, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("wrong-scope = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

// TestMCP_GetIs405: GET (the SSE/session verb) is refused since we offer no stream.
func TestMCP_GetIs405(t *testing.T) {
	h, _, _ := newMCPTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d, want 405", rr.Code)
	}
}

// TestMCP_FullTranscript is the JSON-RPC smoke test: a finance.read token drives
// initialize -> tools/list -> tools/call(get_net_worth) and the last call returns the
// seeded net worth. Also checks notifications/initialized is a 202 with no body.
func TestMCP_FullTranscript(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	// 1) initialize: echoes the requested protocol version and identifies the server.
	rr, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "test", "version": "1"},
		},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("initialize = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("initialize result missing; body=%s", rr.Body.String())
	}
	if res["protocolVersion"] != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want 2025-06-18 (echoed)", res["protocolVersion"])
	}
	if si, _ := res["serverInfo"].(map[string]any); si == nil || si["name"] != "aliflabs-finance" {
		t.Errorf("serverInfo = %v, want name aliflabs-finance", res["serverInfo"])
	}
	if caps, _ := res["capabilities"].(map[string]any); caps == nil {
		t.Errorf("capabilities missing tools object")
	} else if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities = %v, want a tools object", caps)
	}

	// 2) notifications/initialized: a notification -> 202 with no response body.
	nrr, _ := rpc(t, h, raw, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	if nrr.Code != http.StatusAccepted {
		t.Fatalf("notifications/initialized = %d, want 202", nrr.Code)
	}
	if nrr.Body.Len() != 0 {
		t.Errorf("notification body = %q, want empty", nrr.Body.String())
	}

	// 3) tools/list: all ten read tools present, each with a schema.
	rr, out = rpc(t, h, raw, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if rr.Code != http.StatusOK {
		t.Fatalf("tools/list = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	tools, _ := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 10 {
		t.Fatalf("tools = %d, want 10", len(tools))
	}
	names := map[string]bool{}
	for _, tv := range tools {
		tm := tv.(map[string]any)
		names[tm["name"].(string)] = true
		if _, ok := tm["inputSchema"]; !ok {
			t.Errorf("tool %v missing inputSchema", tm["name"])
		}
	}
	for _, want := range []string{"get_net_worth", "list_accounts", "list_transactions", "search_merchant", "monthly_summary", "spending_summary", "list_pending", "balance_history", "list_wishlist", "list_recurring_bills"} {
		if !names[want] {
			t.Errorf("tools/list missing %q", want)
		}
	}

	// 4) tools/call get_net_worth: the seeded 1000 asset, no liabilities.
	rr, out = rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 3, "method": "tools/call",
		"params": map[string]any{"name": "get_net_worth", "arguments": map[string]any{}},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("tools/call = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	callRes, _ := out["result"].(map[string]any)
	if callRes == nil {
		t.Fatalf("tools/call result missing; body=%s", rr.Body.String())
	}
	if isErr, _ := callRes["isError"].(bool); isErr {
		t.Fatalf("tools/call returned isError; body=%s", rr.Body.String())
	}
	content, _ := callRes["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(content))
	}
	block := content[0].(map[string]any)
	if block["type"] != "text" {
		t.Errorf("content type = %v, want text", block["type"])
	}
	var nw struct {
		NetWorth     float64 `json:"net_worth"`
		Assets       float64 `json:"assets"`
		Liabilities  float64 `json:"liabilities"`
		Currency     string  `json:"currency"`
		AccountCount int     `json:"account_count"`
	}
	if err := json.Unmarshal([]byte(block["text"].(string)), &nw); err != nil {
		t.Fatalf("decode net worth text: %v; text=%s", err, block["text"])
	}
	if nw.NetWorth != 1000 || nw.Assets != 1000 || nw.Liabilities != 0 {
		t.Errorf("net worth = %+v, want 1000/1000/0", nw)
	}
	if nw.Currency != "AUD" || nw.AccountCount != 1 {
		t.Errorf("summary meta = %+v, want AUD/1 account", nw)
	}
}

// TestMCP_ListRecurringBills drives the committed-money tool over JSON-RPC: an active bill
// comes back with its derived due date and the roll-up figures, within_days filters on the
// derived date, and an out-of-range status is a tool error (the enum is policed in Go, not
// in the JSON schema).
func TestMCP_ListRecurringBills(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	client.RecurringBill.Create().SetName("Housing").SetExpectedAmount(600).
		SetCadence("fortnightly").SetAnchorDate(today.AddDate(0, 0, 4)).SaveX(ctx)
	client.RecurringBill.Create().SetName("Annual thing").SetExpectedAmount(240).
		SetCadence("annual").SetAnchorDate(today.AddDate(0, 0, 100)).SaveX(ctx)

	var res struct {
		Bills []struct {
			Name           string   `json:"name"`
			Cadence        string   `json:"cadence"`
			ExpectedAmount float64  `json:"expected_amount"`
			NextDue        string   `json:"next_due"`
			DaysUntil      int      `json:"days_until"`
			AmountVariable bool     `json:"amount_variable"`
			LastPaidAmount *float64 `json:"last_paid_amount"`
		} `json:"bills"`
		CommittedTotal    float64 `json:"committed_total"`
		MonthlyEquivalent float64 `json:"monthly_equivalent"`
		Count             int     `json:"count"`
	}
	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 30, "method": "tools/call",
		"params": map[string]any{"name": "list_recurring_bills", "arguments": map[string]any{}},
	})
	decodeToolText(t, out, &res)
	if res.Count != 2 || res.CommittedTotal != 840 {
		t.Fatalf("default call = %+v, want 2 bills / 840 committed", res)
	}
	if res.Bills[0].Name != "Housing" || res.Bills[0].DaysUntil != 4 {
		t.Errorf("first bill = %+v, want Housing 4 days out (most urgent first)", res.Bills[0])
	}
	if res.Bills[0].NextDue != today.AddDate(0, 0, 4).Format(dateLayout) {
		t.Errorf("next_due = %q, want %q", res.Bills[0].NextDue, today.AddDate(0, 0, 4).Format(dateLayout))
	}
	if res.Bills[0].LastPaidAmount != nil {
		t.Errorf("last_paid_amount = %v, want null before anything reconciles", res.Bills[0].LastPaidAmount)
	}

	// within_days filters on the derived date, which SQL cannot do.
	_, out = rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 31, "method": "tools/call",
		"params": map[string]any{"name": "list_recurring_bills", "arguments": map[string]any{"within_days": 14}},
	})
	decodeToolText(t, out, &res)
	if res.Count != 1 || res.Bills[0].Name != "Housing" || res.CommittedTotal != 600 {
		t.Errorf("within_days=14 = %+v, want only Housing / 600 committed", res)
	}

	// A bad status is a tool error the model can read, not a protocol error.
	_, out = rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 32, "method": "tools/call",
		"params": map[string]any{"name": "list_recurring_bills", "arguments": map[string]any{"status": "nope"}},
	})
	result, _ := out["result"].(map[string]any)
	if result == nil {
		t.Fatalf("expected a tool result, got %v", out)
	}
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Errorf("isError = false, want true for an out-of-range status")
	}
}

// TestMCP_ListAccountsCarriesOwnerMetadata: the owner-authored description and
// drawdown_policy reach the model (portfolio-site#122). An unlabelled account still
// reports its policy, because "unset" means not yet declared rather than flexible, but
// omits description entirely so an empty string does not cost tokens.
func TestMCP_ListAccountsCarriesOwnerMetadata(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	const note = "sinking fund for one specific thing, not spending money"
	labelled := client.Account.Create().
		SetSource("commbank").
		SetName("Aaa Labelled").
		SetType(account.TypeSavings).
		SetClass(account.ClassAsset).
		SetDescription(note).
		SetDrawdownPolicy(account.DrawdownPolicyNoDrawdown).
		SaveX(ctx)

	var res struct {
		Accounts []struct {
			ID             int     `json:"id"`
			Name           string  `json:"name"`
			Description    *string `json:"description"`
			DrawdownPolicy string  `json:"drawdown_policy"`
		} `json:"accounts"`
	}
	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 40, "method": "tools/call",
		"params": map[string]any{"name": "list_accounts", "arguments": map[string]any{}},
	})
	decodeToolText(t, out, &res)
	if len(res.Accounts) != 2 {
		t.Fatalf("accounts = %d, want 2 (the seeded one plus the labelled one)", len(res.Accounts))
	}
	for _, a := range res.Accounts {
		if a.ID == labelled.ID {
			if a.Description == nil || *a.Description != note {
				t.Errorf("labelled description = %v, want %q", a.Description, note)
			}
			if a.DrawdownPolicy != "no_drawdown" {
				t.Errorf("labelled drawdown_policy = %q, want no_drawdown", a.DrawdownPolicy)
			}
			continue
		}
		if a.Description != nil {
			t.Errorf("unlabelled description = %q, want the key omitted entirely", *a.Description)
		}
		if a.DrawdownPolicy != "unset" {
			t.Errorf("unlabelled drawdown_policy = %q, want unset (always present)", a.DrawdownPolicy)
		}
	}
}

// TestMCP_UnknownMethod: an unknown JSON-RPC method returns -32601.
func TestMCP_UnknownMethod(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})
	_, out := rpc(t, h, raw, map[string]any{"jsonrpc": "2.0", "id": 9, "method": "does/not/exist"})
	e, _ := out["error"].(map[string]any)
	if e == nil || int(e["code"].(float64)) != codeMethodNotFound {
		t.Fatalf("error = %v, want method-not-found (-32601)", out["error"])
	}
}

// TestMCP_UnknownTool: tools/call for a tool that does not exist is -32602.
func TestMCP_UnknownTool(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})
	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 10, "method": "tools/call",
		"params": map[string]any{"name": "delete_everything", "arguments": map[string]any{}},
	})
	e, _ := out["error"].(map[string]any)
	if e == nil || int(e["code"].(float64)) != codeInvalidParams {
		t.Fatalf("error = %v, want invalid-params (-32602)", out["error"])
	}
}

// TestMCP_SearchMissingQueryIsToolError: a required-arg failure is a tool-error result
// (isError=true), not a protocol error, so the model sees the message.
func TestMCP_SearchMissingQueryIsToolError(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})
	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 11, "method": "tools/call",
		"params": map[string]any{"name": "search_merchant", "arguments": map[string]any{"query": "  "}},
	})
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("expected a tool result, got %v", out)
	}
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("isError = false, want true for a missing query")
	}
}

// decodeToolText pulls the single JSON text block out of a successful tools/call result and
// unmarshals it into v, failing the test if the call reported isError or the shape is off.
func decodeToolText(t *testing.T, out map[string]any, v any) {
	t.Helper()
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("no tool result: %v", out)
	}
	if isErr, _ := res["isError"].(bool); isErr {
		t.Fatalf("tool returned isError: %v", res)
	}
	content, _ := res["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(content))
	}
	block := content[0].(map[string]any)
	if err := json.Unmarshal([]byte(block["text"].(string)), v); err != nil {
		t.Fatalf("decode tool text: %v; text=%s", err, block["text"])
	}
}

// TestMCP_SpendingSummaryAndExternalOnly drives the two new capabilities over the JSON-RPC
// surface: list_transactions{external_only:true} drops an internal StepPay repayment, and
// spending_summary (defaulting to the last 30 days) reports external income/spend with the
// internal move excluded and surfaced as internal_transfers_excluded.
func TestMCP_SpendingSummaryAndExternalOnly(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	acc := client.Account.Query().OnlyX(ctx) // the seeded "Smart Access"
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	mk := func(tag string, amt float64, desc string) {
		client.Transaction.Create().SetDedupHash("mcp-" + tag).SetPostedDate(today).
			SetAmount(amt).SetDescription(desc).SetAccountID(acc.ID).SaveX(ctx)
	}
	mk("salary", 3000, "SALARY")             // external in
	mk("rent", -2000, "RENT DIRECT DEBIT")   // external out
	mk("steppay", -500, "StepPay Repayment") // internal, excluded regardless of own accounts

	// list_transactions{external_only:true}: the StepPay repayment is dropped; total is 2.
	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 20, "method": "tools/call",
		"params": map[string]any{"name": "list_transactions", "arguments": map[string]any{"external_only": true}},
	})
	var lt struct {
		Transactions []struct {
			Description string `json:"description"`
		} `json:"transactions"`
		Total int `json:"total"`
	}
	decodeToolText(t, out, &lt)
	if lt.Total != 2 || len(lt.Transactions) != 2 {
		t.Fatalf("external_only = %d rows / total %d, want 2/2", len(lt.Transactions), lt.Total)
	}
	for _, r := range lt.Transactions {
		if strings.Contains(strings.ToLower(r.Description), "steppay") {
			t.Errorf("external_only leaked the StepPay repayment: %q", r.Description)
		}
	}

	// spending_summary with no args (default window includes today): external figures with
	// the internal move excluded.
	_, out = rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 21, "method": "tools/call",
		"params": map[string]any{"name": "spending_summary", "arguments": map[string]any{}},
	})
	var ss struct {
		ExternalSpend float64 `json:"external_spend"`
		Income        float64 `json:"income"`
		Net           float64 `json:"net"`
		Transfers     float64 `json:"internal_transfers_excluded"`
		TxnCount      int     `json:"txn_count"`
	}
	decodeToolText(t, out, &ss)
	if ss.Income != 3000 || ss.ExternalSpend != 2000 || ss.Net != 1000 {
		t.Errorf("spending_summary figures = %+v, want income 3000 / external_spend 2000 / net 1000", ss)
	}
	if ss.Transfers != 500 || ss.TxnCount != 3 {
		t.Errorf("spending_summary excl = %+v, want transfers 500 / txn_count 3", ss)
	}
}

// TestMCP_ListWishlist: the tool defaults to the still-wanted rows, status=all spans
// every state, an item with no amount is counted as unknown rather than summed as zero,
// and an unrecognised status is a tool error the model can read (not a protocol error).
func TestMCP_ListWishlist(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	client.WishlistItem.Create().SetName("new glasses").SetAmount(400).
		SetPriority(wishlistitem.PriorityHigh).SaveX(ctx)
	client.WishlistItem.Create().SetName("camera bag").SaveX(ctx) // price unknown
	client.WishlistItem.Create().SetName("desk lamp").SetAmount(100).
		SetStatus(wishlistitem.StatusBought).SaveX(ctx)

	type wishlistPayload struct {
		Items []struct {
			Name   string   `json:"name"`
			Amount *float64 `json:"amount"`
			Status string   `json:"status"`
		} `json:"items"`
		ItemCount             int     `json:"item_count"`
		KnownCostTotal        float64 `json:"known_cost_total"`
		UnknownCostCount      int     `json:"unknown_cost_count"`
		CurrencyMismatchCount int     `json:"currency_mismatch_count"`
		Truncated             bool    `json:"truncated"`
	}

	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 30, "method": "tools/call",
		"params": map[string]any{"name": "list_wishlist", "arguments": map[string]any{}},
	})
	var def wishlistPayload
	decodeToolText(t, out, &def)
	if def.ItemCount != 2 || len(def.Items) != 2 {
		t.Fatalf("default = %d items (count %d), want the 2 wanted rows", len(def.Items), def.ItemCount)
	}
	if def.KnownCostTotal != 400 || def.UnknownCostCount != 1 {
		t.Errorf("default totals = %+v, want known 400 / unknown 1", def)
	}
	if def.Items[0].Name != "new glasses" {
		t.Errorf("first item = %q, want the high-priority row first", def.Items[0].Name)
	}
	if def.Items[1].Amount != nil {
		t.Errorf("unknown price = %v, want null so it cannot be read as free", *def.Items[1].Amount)
	}
	if def.Truncated {
		t.Errorf("truncated = true on a 2-row list, want absent (a complete answer)")
	}
	if def.CurrencyMismatchCount != 0 {
		t.Errorf("currency_mismatch_count = %d, want 0 (every row is AUD)", def.CurrencyMismatchCount)
	}

	// A limit below the row count is a real truncation, so the flag must fire and the
	// roll-up must describe only the rows returned.
	_, out = rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 33, "method": "tools/call",
		"params": map[string]any{"name": "list_wishlist", "arguments": map[string]any{"limit": 1}},
	})
	var capped wishlistPayload
	decodeToolText(t, out, &capped)
	if !capped.Truncated || capped.ItemCount != 1 {
		t.Errorf("limit=1 = %+v, want 1 item with truncated true", capped)
	}

	_, out = rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 31, "method": "tools/call",
		"params": map[string]any{"name": "list_wishlist", "arguments": map[string]any{"status": "all"}},
	})
	var all wishlistPayload
	decodeToolText(t, out, &all)
	if all.ItemCount != 3 || all.KnownCostTotal != 500 || all.UnknownCostCount != 1 {
		t.Errorf("status=all = %+v, want 3 items / known 500 / unknown 1", all)
	}

	rr, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 32, "method": "tools/call",
		"params": map[string]any{"name": "list_wishlist", "arguments": map[string]any{"status": "nonsense"}},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("bad status = %d, want 200 with a tool error", rr.Code)
	}
	res, _ := out["result"].(map[string]any)
	if res == nil {
		t.Fatalf("bad status: result missing; body=%s", rr.Body.String())
	}
	if isErr, _ := res["isError"].(bool); !isErr {
		t.Errorf("bad status isError = %v, want true (a domain error, not a protocol error)", res["isError"])
	}
	if _, isRPCErr := out["error"]; isRPCErr {
		t.Errorf("bad status returned a JSON-RPC error, want a tool-error result")
	}

	// A foreign-currency amount stays out of the AUD total and is counted instead, so the
	// figure the model weighs a purchase against is never a mixed-currency sum.
	client.WishlistItem.Create().SetName("imported item").SetAmount(1000).
		SetCurrency("USD").SaveX(ctx)
	_, out = rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 34, "method": "tools/call",
		"params": map[string]any{"name": "list_wishlist", "arguments": map[string]any{}},
	})
	var mixed wishlistPayload
	decodeToolText(t, out, &mixed)
	if mixed.ItemCount != 3 {
		t.Fatalf("item_count = %d, want 3 (the foreign row is still listed)", mixed.ItemCount)
	}
	if mixed.KnownCostTotal != 400 || mixed.CurrencyMismatchCount != 1 {
		t.Errorf("mixed currency = %+v, want known 400 / mismatch 1", mixed)
	}
}

// balanceHistoryPayload is the decoded balance_history tool result.
type balanceHistoryPayload struct {
	Step   string `json:"step"`
	Basis  string `json:"basis"`
	Series []struct {
		AccountID   int      `json:"account_id"`
		AccountName string   `json:"account_name"`
		Class       string   `json:"class"`
		Currency    string   `json:"currency"`
		First       *float64 `json:"first"`
		Last        *float64 `json:"last"`
		Change      *float64 `json:"change"`
		ChangePct   *float64 `json:"change_pct"`
		Basis       *string  `json:"basis"`
		LedgerFrom  *string  `json:"ledger_from"`
		DriftMax    *float64 `json:"drift_max"`
		Points      []struct {
			AsOf    string   `json:"as_of"`
			Balance float64  `json:"balance"`
			Carried *bool    `json:"carried"`
			Open    *float64 `json:"open"`
			Close   *float64 `json:"close"`
			Out     *float64 `json:"out"`
			Net     *float64 `json:"net"`
			Txns    *int     `json:"txns"`
			Source  *string  `json:"source"`
		} `json:"points"`
	} `json:"series"`
	Truncated *bool   `json:"truncated"`
	Advice    *string `json:"advice"`
}

// TestMCP_BalanceHistory_DefaultsAndFanOut: a bare call defaults to monthly buckets over the
// last 12 months, and omitting account_id returns every account as its own series with the
// first/last/change roll-up.
func TestMCP_BalanceHistory_DefaultsAndFanOut(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	// A second account so the fan-out has something to fan out to.
	other := client.Account.Create().SetSource("commbank").SetName("Zed Saver").
		SetType(account.TypeSavings).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	client.BalanceSnapshot.Create().SetBalance(2000.00).
		SetAsOf(time.Now().UTC().AddDate(0, 0, -1)).SetAccountID(other.ID).SaveX(ctx)

	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 30, "method": "tools/call",
		"params": map[string]any{"name": "balance_history", "arguments": map[string]any{}},
	})
	var bh balanceHistoryPayload
	decodeToolText(t, out, &bh)

	if bh.Step != "month" {
		t.Errorf("step = %q, want the month default", bh.Step)
	}
	if bh.Basis != "snapshot" {
		t.Errorf("basis = %q, want the snapshot default", bh.Basis)
	}
	if len(bh.Series) != 2 {
		t.Fatalf("series = %d, want 2 (one per account)", len(bh.Series))
	}
	if bh.Truncated != nil {
		t.Errorf("truncated = %v, want absent on a small answer", bh.Truncated)
	}
	for _, s := range bh.Series {
		if s.Currency != "AUD" || s.Class != "asset" {
			t.Errorf("series %d meta = %s/%s, want AUD/asset", s.AccountID, s.Currency, s.Class)
		}
		if s.First == nil || s.Last == nil || s.Change == nil {
			t.Errorf("series %d missing the first/last/change roll-up", s.AccountID)
		}
		if len(s.Points) == 0 {
			t.Errorf("series %d has no points", s.AccountID)
		}
		// The snapshot basis must not leak the ledger series metadata.
		if s.Basis != nil || s.LedgerFrom != nil || s.DriftMax != nil {
			t.Errorf("series %d leaked ledger metadata under basis=snapshot", s.AccountID)
		}
		for i, p := range s.Points {
			if p.Open != nil || p.Close != nil || p.Net != nil || p.Source != nil {
				t.Errorf("series %d point %d leaked ledger fields under basis=snapshot", s.AccountID, i)
			}
		}
	}
}

// TestMCP_BalanceHistory_LedgerBasis: basis=ledger derives closes and per-bucket flows, and
// carries source plus the series-level ledger metadata.
func TestMCP_BalanceHistory_LedgerBasis(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})
	acc := client.Account.Query().OnlyX(ctx) // the seeded account

	now := time.Now().UTC()
	day := func(back int) time.Time {
		d := now.AddDate(0, 0, -back)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	}
	client.Transaction.Create().SetDedupHash("bh-1").SetPostedDate(day(2)).
		SetAmount(-100).SetDescription("MERCHANT").SetBalanceAfter(900).SetAccountID(acc.ID).SaveX(ctx)
	client.Transaction.Create().SetDedupHash("bh-2").SetPostedDate(day(1)).
		SetAmount(-200).SetDescription("MERCHANT").SetBalanceAfter(700).SetAccountID(acc.ID).SaveX(ctx)

	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 31, "method": "tools/call",
		"params": map[string]any{"name": "balance_history", "arguments": map[string]any{
			"step": "day", "basis": "ledger", "account_id": acc.ID,
		}},
	})
	var bh balanceHistoryPayload
	decodeToolText(t, out, &bh)

	if bh.Basis != "ledger" || bh.Step != "day" {
		t.Fatalf("echoed step/basis = %q/%q, want day/ledger", bh.Step, bh.Basis)
	}
	if len(bh.Series) != 1 {
		t.Fatalf("series = %d, want 1", len(bh.Series))
	}
	s := bh.Series[0]
	if s.Basis == nil || *s.Basis != "ledger" || s.LedgerFrom == nil || s.DriftMax == nil {
		t.Errorf("series ledger metadata = %v/%v/%v, want all present", s.Basis, s.LedgerFrom, s.DriftMax)
	}
	// The tool's window runs to today, so the two row days plus a carried today.
	if len(s.Points) != 3 {
		t.Fatalf("points = %d, want 3 (two row days plus a carried today)", len(s.Points))
	}
	p := s.Points[0]
	if p.Open == nil || *p.Open != 1000 {
		t.Errorf("open = %v, want 1000", deref(p.Open))
	}
	if p.Close == nil || *p.Close != 900 || p.Balance != 900 {
		t.Errorf("close/balance = %v/%v, want 900/900", deref(p.Close), p.Balance)
	}
	if p.Out == nil || *p.Out != 100 {
		t.Errorf("out = %v, want 100 as a positive magnitude", deref(p.Out))
	}
	if p.Source == nil || *p.Source != "balance_after" {
		t.Errorf("source = %v, want balance_after", p.Source)
	}
	if p.Txns == nil || *p.Txns != 1 {
		t.Errorf("txns = %v, want 1", p.Txns)
	}
	// Today has no rows, so it repeats the previous close with zero flows and says carried.
	last := s.Points[2]
	if last.Carried == nil || !*last.Carried {
		t.Errorf("today carried = %v, want true", last.Carried)
	}
	if last.Source == nil || *last.Source != "carried" {
		t.Errorf("today source = %v, want carried", last.Source)
	}
	if last.Close == nil || *last.Close != 700 {
		t.Errorf("today close = %v, want the previous close 700", deref(last.Close))
	}
}

// deref renders an optional float for a failure message (%v on a pointer prints an address).
func deref(f *float64) any {
	if f == nil {
		return "nil"
	}
	return *f
}

// TestMCP_BalanceHistory_BucketStartsRenderInLocalZone: every point the tool emits is a
// bucket start, which is a local midnight, so a UTC rendering would name the period BEFORE
// the bucket it labels and a model would attribute the close and the flows to the wrong month.
func TestMCP_BalanceHistory_BucketStartsRenderInLocalZone(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})
	acc := client.Account.Query().OnlyX(ctx)

	mel := finance.BucketZone()
	// 9am Melbourne on the local 1st, which is 23:00 UTC the previous month.
	local := time.Date(2026, 8, 1, 9, 0, 0, 0, mel)
	client.BalanceSnapshot.Create().SetBalance(500).SetAsOf(local.UTC()).SetAccountID(acc.ID).SaveX(ctx)

	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 60, "method": "tools/call",
		"params": map[string]any{"name": "balance_history", "arguments": map[string]any{
			"step": "month", "account_id": acc.ID, "from": "2026-08-01", "to": "2026-08-31",
		}},
	})
	var bh balanceHistoryPayload
	decodeToolText(t, out, &bh)
	if len(bh.Series) != 1 || len(bh.Series[0].Points) != 1 {
		t.Fatalf("want one series with one August bucket, got %d series", len(bh.Series))
	}
	got := bh.Series[0].Points[0].AsOf
	if got != "2026-08-01T00:00:00+10:00" {
		t.Errorf("as_of = %q, want 2026-08-01T00:00:00+10:00 (a UTC rendering would read as July)", got)
	}
	ts, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatalf("as_of %q is not RFC3339: %v", got, err)
	}
	if lt := ts.In(mel); lt.Month() != time.August || lt.Day() != 1 || lt.Hour() != 0 {
		t.Errorf("as_of %q resolves to %s, want the local-midnight 1st of August", got, lt)
	}
}

// TestAllotPoints_WaterFillsFromTheShallowest: the budget must never trim a series below what
// it would have fitted on its own. An even budget/n split looked fair but dropped 74 points
// to save 4 on the reviewer's case, and reported a truncation for a request 3% over.
func TestAllotPoints_WaterFillsFromTheShallowest(t *testing.T) {
	mk := func(counts ...int) []finance.BalanceSeriesView {
		out := make([]finance.BalanceSeriesView, 0, len(counts))
		for _, n := range counts {
			out = append(out, finance.BalanceSeriesView{Points: make([]finance.BalancePoint, n)})
		}
		return out
	}
	for _, tc := range []struct {
		name   string
		counts []int
		budget int
		want   []int
	}{
		{"under budget leaves everything", []int{10, 5}, 150, []int{10, 5}},
		{"exactly at budget leaves everything", []int{100, 50}, 150, []int{100, 50}},
		{"deep plus shallow trims only the deep one", []int{149, 5}, 150, []int{145, 5}},
		{"shallow first in the input order", []int{5, 149}, 150, []int{5, 145}},
		{"one series takes the whole budget", []int{11}, 5, []int{5}},
		{"equal series split evenly", []int{100, 100}, 150, []int{75, 75}},
		{"more series than budget still keeps one point each", []int{3, 3, 3}, 2, []int{1, 1, 1}},
	} {
		got := allotPoints(mk(tc.counts...), tc.budget)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: allot len = %d, want %d", tc.name, len(got), len(tc.want))
		}
		for i := range tc.want {
			if got[i] != tc.want[i] {
				t.Errorf("%s: allot = %v, want %v", tc.name, got, tc.want)
				break
			}
		}
	}
}

// TestMCP_BalanceHistory_ShallowSeriesNotStarvedByDeepOne is the end-to-end half of the
// water-fill fix: the shallow series keeps every point it had, and only the deep one is cut.
func TestMCP_BalanceHistory_ShallowSeriesNotStarvedByDeepOne(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	origSnap := balanceSnapshotPointBudget
	balanceSnapshotPointBudget = 10
	t.Cleanup(func() { balanceSnapshotPointBudget = origSnap })

	// A fixed window so neither series' length depends on the clock, and a deep account plus
	// a shallow one whose 3 points exceed an even 10/2 = 5 share only in aggregate.
	deep := client.Account.Create().SetSource("commbank").SetName("Y deep").
		SetType(account.TypeSavings).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	shallow := client.Account.Create().SetSource("commbank").SetName("Z shallow").
		SetType(account.TypeSavings).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	mel := finance.BucketZone()
	for d := 1; d <= 12; d++ {
		client.BalanceSnapshot.Create().SetBalance(float64(100 * d)).
			SetAsOf(time.Date(2026, 6, d, 9, 0, 0, 0, mel).UTC()).SetAccountID(deep.ID).SaveX(ctx)
	}
	for d := 10; d <= 12; d++ {
		client.BalanceSnapshot.Create().SetBalance(float64(10 * d)).
			SetAsOf(time.Date(2026, 6, d, 9, 0, 0, 0, mel).UTC()).SetAccountID(shallow.ID).SaveX(ctx)
	}

	_, out := rpc(t, h, raw, map[string]any{
		"jsonrpc": "2.0", "id": 61, "method": "tools/call",
		"params": map[string]any{"name": "balance_history", "arguments": map[string]any{
			"step": "day", "from": "2026-06-01", "to": "2026-06-12",
		}},
	})
	var bh balanceHistoryPayload
	decodeToolText(t, out, &bh)
	if bh.Truncated == nil || !*bh.Truncated {
		t.Fatalf("truncated = %v, want true", bh.Truncated)
	}
	byID := map[int]int{}
	for _, s := range bh.Series {
		byID[s.AccountID] = len(s.Points)
	}
	if got := byID[shallow.ID]; got != 3 {
		t.Errorf("shallow series kept %d points, want all 3: a deep series must not starve it", got)
	}
	if got := byID[deep.ID]; got >= 12 || got <= 3 {
		t.Errorf("deep series kept %d points, want it trimmed but given the remaining budget", got)
	}
}

// TestMCP_BalanceHistory_UnknownStepAndBasisAreToolErrors: a bad enum is a tool-error result
// the model can read, not a protocol error and not a silent fall back.
func TestMCP_BalanceHistory_UnknownStepAndBasisAreToolErrors(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	for i, args := range []map[string]any{{"step": "monthly"}, {"basis": "derived"}, {"step": ""}} {
		_, out := rpc(t, h, raw, map[string]any{
			"jsonrpc": "2.0", "id": 40 + i, "method": "tools/call",
			"params": map[string]any{"name": "balance_history", "arguments": args},
		})
		res, _ := out["result"].(map[string]any)
		if res == nil {
			t.Fatalf("args %v: expected a tool result, got %v", args, out)
		}
		isErr, _ := res["isError"].(bool)
		// An empty step is the month default, so only the first two must fail.
		wantErr := i < 2
		if isErr != wantErr {
			t.Errorf("args %v: isError = %v, want %v", args, isErr, wantErr)
		}
	}
}

// TestMCP_BalanceHistory_TruncatedOnlyWhenPointsDropped: over budget the response says so
// and keeps the NEWEST buckets; under budget the flag is absent. A trimmed answer is never
// silent.
func TestMCP_BalanceHistory_TruncatedOnlyWhenPointsDropped(t *testing.T) {
	h, svc, client := newMCPTestServer(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, []string{"finance.read"})

	// Lower the budget instead of seeding hundreds of buckets.
	origSnap := balanceSnapshotPointBudget
	balanceSnapshotPointBudget = 5
	t.Cleanup(func() { balanceSnapshotPointBudget = origSnap })

	// A dedicated account, so the harness's own pre-seeded reading does not extend the
	// series and move the roll-up.
	acc := client.Account.Create().SetSource("commbank").SetName("Zed Budget").
		SetType(account.TypeSavings).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)

	// Ten consecutive daily readings rising 100 to 1000, so a daily series over the window
	// has those ten buckets plus a carried today.
	now := time.Now().UTC()
	for i := 10; i >= 1; i-- {
		d := now.AddDate(0, 0, -i)
		client.BalanceSnapshot.Create().SetBalance(float64(100 * (11 - i))).
			SetAsOf(time.Date(d.Year(), d.Month(), d.Day(), 9, 0, 0, 0, time.UTC)).
			SetAccountID(acc.ID).SaveX(ctx)
	}

	call := func(id int, args map[string]any) balanceHistoryPayload {
		_, out := rpc(t, h, raw, map[string]any{
			"jsonrpc": "2.0", "id": id, "method": "tools/call",
			"params": map[string]any{"name": "balance_history", "arguments": args},
		})
		var bh balanceHistoryPayload
		decodeToolText(t, out, &bh)
		return bh
	}

	over := call(50, map[string]any{"step": "day", "account_id": acc.ID})
	if over.Truncated == nil || !*over.Truncated {
		t.Fatalf("truncated = %v, want true once the budget dropped buckets", over.Truncated)
	}
	if over.Advice == nil || !strings.Contains(*over.Advice, "step") {
		t.Errorf("advice = %v, want the coarsen-step escape hatch", over.Advice)
	}
	if len(over.Series) != 1 {
		t.Fatalf("series = %d, want 1", len(over.Series))
	}
	if got := len(over.Series[0].Points); got > 5 {
		t.Errorf("points = %d, want at most the budget of 5", got)
	}
	// The roll-up still describes the whole requested window, and the kept buckets are the
	// newest, so the last point carries the most recent level.
	if over.Series[0].Last == nil || *over.Series[0].Last != 1000 {
		t.Errorf("last = %v, want 1000 (the newest level kept)", deref(over.Series[0].Last))
	}
	if over.Series[0].First == nil || *over.Series[0].First != 100 {
		t.Errorf("first = %v, want 100 (the roll-up covers the full window, not the trimmed points)", deref(over.Series[0].First))
	}
	if pts := over.Series[0].Points; len(pts) > 0 && pts[0].Balance == 100 {
		t.Error("the oldest bucket survived the trim, so the newest buckets were dropped instead")
	}

	// A monthly step over the same data is well inside the budget, so nothing is dropped and
	// the flag is absent.
	under := call(51, map[string]any{"step": "month", "account_id": acc.ID})
	if under.Truncated != nil {
		t.Errorf("truncated = %v, want absent when nothing was dropped", under.Truncated)
	}
}
