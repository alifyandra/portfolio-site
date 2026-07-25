package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/danielgtaylor/huma/v2/humatest"
	_ "modernc.org/sqlite"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// newFinanceReadTestAPI wires the finance read endpoints with the real auth
// middleware and an in-memory SQLite ledger, so a request drives the full cookie ->
// requireAdmin -> read-service path.
func newFinanceReadTestAPI(t *testing.T) (humatest.TestAPI, *ent.Client) {
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
	h.registerFinanceRead(api)
	return api, client
}

// TestFinanceRead_RequiresAdmin: anonymous is 401, a member is 403, an admin is 200
// on every finance read endpoint.
func TestFinanceRead_RequiresAdmin(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	for _, path := range []string{
		"/api/finance/summary",
		"/api/finance/accounts",
		"/api/finance/transactions",
		"/api/finance/pending",
	} {
		if resp := api.Get(path); resp.Code != http.StatusUnauthorized {
			t.Errorf("anon %s = %d, want 401", path, resp.Code)
		}
		if resp := api.Get(path, member); resp.Code != http.StatusForbidden {
			t.Errorf("member %s = %d, want 403", path, resp.Code)
		}
		if resp := api.Get(path, admin); resp.Code != http.StatusOK {
			t.Errorf("admin %s = %d, want 200; body=%s", path, resp.Code, resp.Body.String())
		}
	}
}

// TestFinanceSummary_DTO: the summary endpoint returns the net-worth math and an
// RFC3339 as_of through the wire DTO.
func TestFinanceSummary_DTO(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	asset := client.Account.Create().SetSource("commbank").SetName("Smart Access").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	client.BalanceSnapshot.Create().SetBalance(1500).SetAsOf(time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)).
		SetAccountID(asset.ID).SaveX(ctx)
	liab := client.Account.Create().SetSource("commbank").SetName("Low Rate CC").
		SetType(account.TypeCreditCard).SetClass(account.ClassLiability).SetCurrency("AUD").SaveX(ctx)
	client.BalanceSnapshot.Create().SetBalance(-400).SetAsOf(time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)).
		SetAccountID(liab.ID).SaveX(ctx)

	resp := api.Get("/api/finance/summary", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("summary = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var got FinanceSummaryDTO
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.Body.String())
	}
	if got.NetWorth != 1100 || got.Assets != 1500 || got.Liabilities != 400 {
		t.Errorf("summary = %+v, want 1100/1500/400", got)
	}
	if got.AccountCount != 2 || got.Currency != "AUD" {
		t.Errorf("summary meta = %+v, want 2 accounts / AUD", got)
	}
	if got.AsOf == nil || *got.AsOf != "2026-07-10T09:00:00Z" {
		t.Errorf("as_of = %v, want 2026-07-10T09:00:00Z", got.AsOf)
	}
}

// TestFinanceTransactions_BadDateIs422: a malformed from/to query is a 422, not a
// silently-ignored filter.
func TestFinanceTransactions_BadDateIs422(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	if resp := api.Get("/api/finance/transactions?from=not-a-date", admin); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad from = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
}
