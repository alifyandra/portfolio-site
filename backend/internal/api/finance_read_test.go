package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
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
	"github.com/alifyandra/portfolio-site/backend/ent/wishlistitem"
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
		"/api/finance/wishlist",
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

// wishlistResponse is the wire shape of GET /api/finance/wishlist.
type wishlistResponse struct {
	Items     []FinanceWishlistItemDTO `json:"items"`
	Totals    FinanceWishlistTotalsDTO `json:"totals"`
	Truncated bool                     `json:"truncated"`
}

// getWishlist calls the read endpoint and decodes it, failing the test on a non-200.
func getWishlist(t *testing.T, api humatest.TestAPI, cookie, query string) wishlistResponse {
	t.Helper()
	resp := api.Get("/api/finance/wishlist"+query, cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("wishlist%s = %d, want 200; body=%s", query, resp.Code, resp.Body.String())
	}
	var got wishlistResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.Body.String())
	}
	return got
}

// TestFinanceWishlist_StatusFilterAndTotals: the default read is wanted-only, status=all
// spans every state, a single status filters to it, and an item with no amount lands in
// unknown_cost_count instead of being summed as zero.
func TestFinanceWishlist_StatusFilterAndTotals(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	client.WishlistItem.Create().SetName("new glasses").SetAmount(400).SaveX(ctx)
	client.WishlistItem.Create().SetName("camera bag").SaveX(ctx) // price unknown
	client.WishlistItem.Create().SetName("desk lamp").SetAmount(100).
		SetStatus(wishlistitem.StatusBought).SaveX(ctx)

	def := getWishlist(t, api, admin, "")
	if len(def.Items) != 2 || def.Totals.ItemCount != 2 {
		t.Fatalf("default items = %d (count %d), want 2 wanted rows", len(def.Items), def.Totals.ItemCount)
	}
	if def.Totals.KnownCostTotal != 400 || def.Totals.UnknownCostCount != 1 {
		t.Errorf("default totals = %+v, want known 400 / unknown 1", def.Totals)
	}
	if def.Totals.Currency != "AUD" {
		t.Errorf("currency = %q, want AUD", def.Totals.Currency)
	}
	for _, it := range def.Items {
		if it.Status != "wanted" {
			t.Errorf("default read returned status %q, want wanted only", it.Status)
		}
	}

	all := getWishlist(t, api, admin, "?status=all")
	if len(all.Items) != 3 || all.Totals.KnownCostTotal != 500 || all.Totals.UnknownCostCount != 1 {
		t.Errorf("status=all = %d items, totals %+v, want 3 items / known 500 / unknown 1", len(all.Items), all.Totals)
	}

	bought := getWishlist(t, api, admin, "?status=bought")
	if len(bought.Items) != 1 || bought.Items[0].Name != "desk lamp" {
		t.Errorf("status=bought = %+v, want just the bought row", bought.Items)
	}
	if bought.Items[0].Amount == nil || *bought.Items[0].Amount != 100 {
		t.Errorf("bought amount = %v, want 100", bought.Items[0].Amount)
	}

	// The nil amount must survive to the wire as null, never as 0.
	unknown := getWishlist(t, api, admin, "?status=wanted")
	for _, it := range unknown.Items {
		if it.Name == "camera bag" && it.Amount != nil {
			t.Errorf("unknown-price amount = %v, want null", *it.Amount)
		}
	}
}

// TestFinanceWishlist_ForeignCurrencyExcludedFromTotal: a priced row in another currency
// is kept out of the single-currency cost total and counted instead, so the figure the
// model weighs a purchase against is never a mixed-currency sum.
func TestFinanceWishlist_ForeignCurrencyExcludedFromTotal(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	client.WishlistItem.Create().SetName("local item").SetAmount(400).SaveX(ctx)
	client.WishlistItem.Create().SetName("imported item").SetAmount(1000).
		SetCurrency("USD").SaveX(ctx)
	client.WishlistItem.Create().SetName("no price").SetCurrency("USD").SaveX(ctx)

	got := getWishlist(t, api, admin, "")
	if got.Totals.ItemCount != 3 {
		t.Fatalf("item_count = %d, want 3 (every row is still listed)", got.Totals.ItemCount)
	}
	if got.Totals.KnownCostTotal != 400 {
		t.Errorf("known_cost_total = %v, want 400 (the foreign amount excluded)", got.Totals.KnownCostTotal)
	}
	if got.Totals.CurrencyMismatchCount != 1 {
		t.Errorf("currency_mismatch_count = %d, want 1", got.Totals.CurrencyMismatchCount)
	}
	// A foreign row with no amount has no figure to convert, so it is only unknown.
	if got.Totals.UnknownCostCount != 1 {
		t.Errorf("unknown_cost_count = %d, want 1", got.Totals.UnknownCostCount)
	}
}

// TestFinanceWishlist_TruncatedOnlyWhenRowsDropped: the read reports truncated only when
// the row limit actually dropped items, so its absence means the roll-up is complete.
func TestFinanceWishlist_TruncatedOnlyWhenRowsDropped(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	// The endpoint takes no limit, so the read-service default (100) is the cap here.
	for i := 0; i < 100; i++ {
		client.WishlistItem.Create().SetName("item " + strconv.Itoa(i)).SetAmount(10).SaveX(ctx)
	}
	full := getWishlist(t, api, admin, "")
	if full.Truncated {
		t.Errorf("truncated = true at exactly the cap, want false (no rows dropped)")
	}
	if full.Totals.ItemCount != 100 || full.Totals.KnownCostTotal != 1000 {
		t.Errorf("totals = %+v, want 100 items / 1000", full.Totals)
	}

	client.WishlistItem.Create().SetName("one over the cap").SetAmount(10).SaveX(ctx)
	over := getWishlist(t, api, admin, "")
	if !over.Truncated {
		t.Errorf("truncated = false past the cap, want true (rows were dropped)")
	}
	if over.Totals.ItemCount != 100 {
		t.Errorf("item_count = %d, want the capped 100", over.Totals.ItemCount)
	}
}

// TestFinanceWishlist_Ordering: priority high to low first, then the nearest deadline,
// with a null deadline sorting last inside its priority band.
func TestFinanceWishlist_Ordering(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	far := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	near := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	client.WishlistItem.Create().SetName("high no deadline").
		SetPriority(wishlistitem.PriorityHigh).SaveX(ctx)
	client.WishlistItem.Create().SetName("low near deadline").
		SetPriority(wishlistitem.PriorityLow).SetDeadline(near).SaveX(ctx)
	client.WishlistItem.Create().SetName("high far deadline").
		SetPriority(wishlistitem.PriorityHigh).SetDeadline(far).SaveX(ctx)
	client.WishlistItem.Create().SetName("medium near deadline").
		SetPriority(wishlistitem.PriorityMedium).SetDeadline(near).SaveX(ctx)

	got := getWishlist(t, api, admin, "")
	names := make([]string, 0, len(got.Items))
	for _, it := range got.Items {
		names = append(names, it.Name)
	}
	want := []string{"high far deadline", "high no deadline", "medium near deadline", "low near deadline"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Errorf("order = %v, want %v", names, want)
	}
	if got.Items[0].Deadline == nil || *got.Items[0].Deadline != "2026-12-01" {
		t.Errorf("deadline = %v, want the date-only 2026-12-01", got.Items[0].Deadline)
	}
	if got.Items[1].Deadline != nil {
		t.Errorf("no-deadline row rendered %v, want null", *got.Items[1].Deadline)
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
