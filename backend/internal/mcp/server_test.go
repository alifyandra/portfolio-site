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
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
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

	// 3) tools/list: all seven read tools present, each with a schema.
	rr, out = rpc(t, h, raw, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"})
	if rr.Code != http.StatusOK {
		t.Fatalf("tools/list = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	tools, _ := out["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 7 {
		t.Fatalf("tools = %d, want 7", len(tools))
	}
	names := map[string]bool{}
	for _, tv := range tools {
		tm := tv.(map[string]any)
		names[tm["name"].(string)] = true
		if _, ok := tm["inputSchema"]; !ok {
			t.Errorf("tool %v missing inputSchema", tm["name"])
		}
	}
	for _, want := range []string{"get_net_worth", "list_accounts", "list_transactions", "search_merchant", "monthly_summary", "spending_summary", "list_pending"} {
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
