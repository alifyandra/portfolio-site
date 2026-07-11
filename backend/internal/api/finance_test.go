package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/danielgtaylor/huma/v2/humatest"
	_ "modernc.org/sqlite"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// newFinanceTestAPI mirrors newWorkTestAPI but wires the finance ingest operation,
// so a request can carry a real finance.sync bearer token through the full
// resolve -> gate -> persist path against an in-memory SQLite DB.
func newFinanceTestAPI(t *testing.T) (humatest.TestAPI, *auth.Service, *ent.Client) {
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
	svc := auth.New(client, auth.Config{})
	_, api := humatest.New(t)
	api.UseMiddleware(svc.Middleware)
	h := New(Deps{Auth: svc, Ent: client})
	h.registerFinance(api)
	return api, svc, client
}

type ingestSummary struct {
	AccountsUpserted         int `json:"accounts_upserted"`
	BalanceSnapshotsInserted int `json:"balance_snapshots_inserted"`
	PostedInserted           int `json:"posted_inserted"`
	PostedDuplicatesSkipped  int `json:"posted_duplicates_skipped"`
	PendingAccountsReplaced  int `json:"pending_accounts_replaced"`
	PendingRowsInserted      int `json:"pending_rows_inserted"`
}

// financePayload is the contract example: one account with a balance, two posted
// rows (the second with a null balance_after AND null merchant), one pending row.
// Explicit JSON nulls on product_code / credit_limit / merchant / balance_after
// prove the null-accepting wire types validate.
func financePayload() map[string]any {
	return map[string]any{
		"source":     "commbank",
		"scraped_at": "2026-07-11T09:00:00.000Z",
		"window":     map[string]any{"from": "2026-04-12", "to": "2026-07-11", "account": "Smart Access"},
		"accounts": []map[string]any{
			{
				"masked_number": "xxxx xxxx 1234",
				"name":          "Smart Access",
				"type":          "everyday",
				"class":         "asset",
				"currency":      "AUD",
				"product_code":  nil,
				"guessed_type":  false,
				"balance": map[string]any{
					"balance":      1957.90,
					"available":    1900.00,
					"credit_limit": nil,
					"as_of":        "2026-07-11T09:00:00.000Z",
				},
			},
		},
		"transactions": map[string]any{
			"posted": []map[string]any{
				{
					"account":       "Smart Access",
					"posted_date":   "2026-07-10",
					"amount":        -42.10,
					"amount_raw":    "$42.10",
					"description":   "EFTPOS PURCHASE SUPERMARKET",
					"merchant":      "Supermarket",
					"balance_after": 1957.90,
				},
				{
					"account":       "Smart Access",
					"posted_date":   "2026-07-09",
					"amount":        -5.00,
					"amount_raw":    "$5.00",
					"description":   "COFFEE",
					"merchant":      nil,
					"balance_after": nil,
				},
			},
			"pending": []map[string]any{
				{
					"account":     "Smart Access",
					"date":        "2026-07-11",
					"amount":      -5.50,
					"amount_raw":  "$5.50",
					"description": "EFTPOS PURCHASE COFFEE CO",
					"merchant":    "Coffee Co",
				},
			},
		},
	}
}

func decodeSummary(t *testing.T, body []byte) ingestSummary {
	t.Helper()
	var s ingestSummary
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("decode summary: %v; body=%s", err, string(body))
	}
	return s
}

// TestIngest_RequiresBearer: no Authorization header is 401 (the gate wins even
// though the body is well-formed).
func TestIngest_RequiresBearer(t *testing.T) {
	api, _, _ := newFinanceTestAPI(t)
	if resp := api.Post("/api/finance/ingest", financePayload()); resp.Code != http.StatusUnauthorized {
		t.Errorf("anonymous ingest = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
}

// TestIngest_ScopeMismatchDenies: a token without finance.sync in scope is 403 even
// though it authenticates.
func TestIngest_ScopeMismatchDenies(t *testing.T) {
	api, svc, client := newFinanceTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"other.job"})
	if resp := api.Post("/api/finance/ingest", financePayload(), bearer(raw)); resp.Code != http.StatusForbidden {
		t.Errorf("wrong-scope ingest = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
}

// TestIngest_HappyPathThenIdempotent lands the contract payload, checks the
// summary counts and rows, then re-POSTs the identical payload to prove the ingest
// is idempotent: no new posted rows (both skipped), the pending set is replaced not
// doubled, and a second balance snapshot is appended (append semantics).
func TestIngest_HappyPathThenIdempotent(t *testing.T) {
	api, svc, client := newFinanceTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"finance.sync"})

	// First ingest.
	resp := api.Post("/api/finance/ingest", financePayload(), bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("first ingest = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	first := decodeSummary(t, resp.Body.Bytes())
	if first != (ingestSummary{
		AccountsUpserted:         1,
		BalanceSnapshotsInserted: 1,
		PostedInserted:           2,
		PostedDuplicatesSkipped:  0,
		PendingAccountsReplaced:  1,
		PendingRowsInserted:      1,
	}) {
		t.Fatalf("first summary = %+v, want 1/1/2/0/1/1", first)
	}

	// Rows after the first ingest.
	if n := client.Account.Query().CountX(ctx); n != 1 {
		t.Errorf("accounts after first = %d, want 1", n)
	}
	if n := client.Transaction.Query().CountX(ctx); n != 2 {
		t.Errorf("posted after first = %d, want 2", n)
	}
	if n := client.PendingTransaction.Query().CountX(ctx); n != 1 {
		t.Errorf("pending after first = %d, want 1", n)
	}
	if n := client.BalanceSnapshot.Query().CountX(ctx); n != 1 {
		t.Errorf("snapshots after first = %d, want 1", n)
	}

	// Second, identical ingest.
	resp = api.Post("/api/finance/ingest", financePayload(), bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("second ingest = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	second := decodeSummary(t, resp.Body.Bytes())
	if second.PostedInserted != 0 || second.PostedDuplicatesSkipped != 2 {
		t.Errorf("second posted = inserted:%d skipped:%d, want 0 inserted / 2 skipped (idempotent ledger)",
			second.PostedInserted, second.PostedDuplicatesSkipped)
	}
	if second.PendingRowsInserted != 1 || second.PendingAccountsReplaced != 1 {
		t.Errorf("second pending = rows:%d accounts:%d, want 1 row / 1 account replaced",
			second.PendingRowsInserted, second.PendingAccountsReplaced)
	}
	if second.BalanceSnapshotsInserted != 0 {
		t.Errorf("second snapshots inserted = %d, want 0 (same as_of deduped, idempotent)", second.BalanceSnapshotsInserted)
	}

	// Rows after the second ingest: everything unchanged, including snapshots (the
	// identical run's snapshot was deduped on (account, as_of), not appended).
	if n := client.Account.Query().CountX(ctx); n != 1 {
		t.Errorf("accounts after second = %d, want 1 (upsert, not duplicate)", n)
	}
	if n := client.Transaction.Query().CountX(ctx); n != 2 {
		t.Errorf("posted after second = %d, want 2 (idempotent upsert)", n)
	}
	if n := client.PendingTransaction.Query().CountX(ctx); n != 1 {
		t.Errorf("pending after second = %d, want 1 (replaced, not doubled)", n)
	}
	if n := client.BalanceSnapshot.Query().CountX(ctx); n != 1 {
		t.Errorf("snapshots after second = %d, want 1 (identical as_of deduped, not appended)", n)
	}

	// Third ingest: a genuinely later reading (bumped as_of, same account) DOES
	// append a new snapshot, while posted stays deduped and pending stays replaced.
	later := financePayload()
	later["accounts"].([]map[string]any)[0]["balance"].(map[string]any)["as_of"] = "2026-07-11T15:30:00.000Z"
	resp = api.Post("/api/finance/ingest", later, bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("third ingest = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	third := decodeSummary(t, resp.Body.Bytes())
	if third.BalanceSnapshotsInserted != 1 {
		t.Errorf("third snapshots inserted = %d, want 1 (later as_of appends)", third.BalanceSnapshotsInserted)
	}
	if third.PostedInserted != 0 || third.PostedDuplicatesSkipped != 2 {
		t.Errorf("third posted = inserted:%d skipped:%d, want 0 inserted / 2 skipped", third.PostedInserted, third.PostedDuplicatesSkipped)
	}
	if third.PendingRowsInserted != 1 || third.PendingAccountsReplaced != 1 {
		t.Errorf("third pending = rows:%d accounts:%d, want 1 row / 1 account replaced", third.PendingRowsInserted, third.PendingAccountsReplaced)
	}
	if n := client.BalanceSnapshot.Query().CountX(ctx); n != 2 {
		t.Errorf("snapshots after third = %d, want 2 (later reading appended)", n)
	}
	if n := client.Transaction.Query().CountX(ctx); n != 2 {
		t.Errorf("posted after third = %d, want 2 (still idempotent)", n)
	}
	if n := client.PendingTransaction.Query().CountX(ctx); n != 1 {
		t.Errorf("pending after third = %d, want 1 (still replaced, not doubled)", n)
	}
}
