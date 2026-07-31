package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"sort"
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
	"github.com/alifyandra/portfolio-site/backend/internal/finance"
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

// TestFinanceBalanceHistory_UnknownStepAndBasisAre400: a mistyped step or basis is
// rejected, never quietly served as the raw/snapshot default, so a caller cannot read a
// differently-shaped series as the one it asked for.
func TestFinanceBalanceHistory_UnknownStepAndBasisAre400(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	acc := client.Account.Create().SetSource("commbank").SetName("A").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)

	base := "/api/finance/accounts/" + strconv.Itoa(acc.ID) + "/balances"
	for _, q := range []string{"?step=monthly", "?step=hour", "?basis=derived", "?basis=ledgers"} {
		if resp := api.Get(base+q, admin); resp.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400; body=%s", q, resp.Code, resp.Body.String())
		}
	}
	// The known values are accepted.
	for _, q := range []string{"", "?step=day", "?step=week", "?step=month", "?basis=snapshot", "?basis=ledger", "?step=month&basis=ledger"} {
		if resp := api.Get(base+q, admin); resp.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200; body=%s", q, resp.Code, resp.Body.String())
		}
	}
}

// TestFinanceBalanceHistory_SnapshotResponseUnchanged: omitting step and basis returns the
// raw reading list with exactly as_of and balance per point and no other keys, so nothing
// consuming that path changes. Bucketing on the snapshot basis adds only `carried`.
func TestFinanceBalanceHistory_SnapshotResponseUnchanged(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	acc := client.Account.Create().SetSource("commbank").SetName("A").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	// Two readings a day apart, both inside the default 90-day window.
	now := time.Now().UTC()
	client.BalanceSnapshot.Create().SetBalance(100).SetAsOf(now.AddDate(0, 0, -3)).SetAccountID(acc.ID).SaveX(ctx)
	client.BalanceSnapshot.Create().SetBalance(300).SetAsOf(now.AddDate(0, 0, -1)).SetAccountID(acc.ID).SaveX(ctx)

	base := "/api/finance/accounts/" + strconv.Itoa(acc.ID) + "/balances"

	// Raw: exactly the two keys, on every point, and no series-level keys.
	resp := api.Get(base, admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("raw = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw) != 1 || raw["points"] == nil {
		t.Errorf("raw body keys = %v, want only points", keysOf(raw))
	}
	pts, _ := raw["points"].([]any)
	if len(pts) != 2 {
		t.Fatalf("raw points = %d, want 2", len(pts))
	}
	for i, p := range pts {
		pm := p.(map[string]any)
		if len(pm) != 2 || pm["as_of"] == nil || pm["balance"] == nil {
			t.Errorf("raw point %d keys = %v, want only as_of and balance", i, keysOf(pm))
		}
	}

	// basis=snapshot with a step: still only as_of/balance plus carried where it applies,
	// and no ledger fields anywhere.
	resp = api.Get(base+"?step=day", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("step=day = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var bucketed map[string]any
	if err := json.Unmarshal(resp.Body.Bytes(), &bucketed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(bucketed) != 1 {
		t.Errorf("snapshot body keys = %v, want only points (ledger metadata must be absent)", keysOf(bucketed))
	}
	bpts, _ := bucketed["points"].([]any)
	if len(bpts) != 3 {
		t.Fatalf("day points = %d, want 3 (a reading, a carried gap, a reading)", len(bpts))
	}
	carried := 0
	for i, p := range bpts {
		pm := p.(map[string]any)
		for _, banned := range []string{"open", "close", "in", "out", "net", "external_in", "external_out", "txns", "source", "drift", "flow_mismatch"} {
			if _, ok := pm[banned]; ok {
				t.Errorf("snapshot point %d leaked the ledger field %q", i, banned)
			}
		}
		if c, ok := pm["carried"]; ok {
			if c != true {
				t.Errorf("point %d carried = %v, want true whenever the key is present", i, c)
			}
			carried++
		}
	}
	if carried != 1 {
		t.Errorf("carried points = %d, want 1 (the gap day)", carried)
	}
}

// TestFinanceBalanceHistory_LedgerBasisDTO: basis=ledger carries the per-bucket flow fields
// and the series-level metadata through the wire DTO.
func TestFinanceBalanceHistory_LedgerBasisDTO(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	acc := client.Account.Create().SetSource("commbank").SetName("A").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)

	now := time.Now().UTC()
	day := func(back int) time.Time {
		d := now.AddDate(0, 0, -back)
		return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	}
	client.Transaction.Create().SetDedupHash("api-l1").SetPostedDate(day(3)).
		SetAmount(-100).SetDescription("MERCHANT").SetBalanceAfter(900).SetAccountID(acc.ID).SaveX(ctx)
	client.Transaction.Create().SetDedupHash("api-l2").SetPostedDate(day(2)).
		SetAmount(-200).SetDescription("MERCHANT").SetBalanceAfter(700).SetAccountID(acc.ID).SaveX(ctx)

	resp := api.Get("/api/finance/accounts/"+strconv.Itoa(acc.ID)+"/balances?step=day&basis=ledger", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("ledger = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Points          []BalancePointDTO `json:"points"`
		Basis           *string           `json:"basis"`
		LedgerFrom      *string           `json:"ledger_from"`
		StartUnverified *bool             `json:"start_unverified"`
		DriftMax        *float64          `json:"drift_max"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.Body.String())
	}
	if body.Basis == nil || *body.Basis != "ledger" {
		t.Errorf("basis = %v, want ledger", body.Basis)
	}
	if body.LedgerFrom == nil || *body.LedgerFrom != day(3).Format(dateLayout) {
		t.Errorf("ledger_from = %v, want %s", body.LedgerFrom, day(3).Format(dateLayout))
	}
	if body.StartUnverified == nil || !*body.StartUnverified {
		t.Errorf("start_unverified = %v, want true", body.StartUnverified)
	}
	if body.DriftMax == nil {
		t.Error("drift_max missing under basis=ledger")
	}
	if len(body.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(body.Points))
	}
	p := body.Points[0]
	if p.Open == nil || *p.Open != 1000 {
		t.Errorf("open = %v, want 1000", p.Open)
	}
	if p.Close == nil || *p.Close != 900 || p.Balance != 900 {
		t.Errorf("close/balance = %v/%v, want 900/900", p.Close, p.Balance)
	}
	if p.Out == nil || *p.Out != 100 {
		t.Errorf("out = %v, want 100 as a positive magnitude", p.Out)
	}
	if p.Net == nil || *p.Net != -100 {
		t.Errorf("net = %v, want -100", p.Net)
	}
	if p.Txns == nil || *p.Txns != 1 {
		t.Errorf("txns = %v, want 1", p.Txns)
	}
	if p.Source == nil || *p.Source != "balance_after" {
		t.Errorf("source = %v, want balance_after", p.Source)
	}
	if p.FlowMismatch != nil {
		t.Errorf("flow_mismatch = %v, want absent on a consistent bucket", p.FlowMismatch)
	}
}

// TestFinanceBalanceHistory_BucketStartsRenderInLocalZone: a bucket start is a local
// midnight, so rendering it in UTC names the calendar period BEFORE the bucket it labels
// (the August month bucket becomes 2026-07-31T14:00:00Z, which reads as July). Bucketed
// points must therefore carry the local offset. Raw points are reading instants, not bucket
// labels, so they keep UTC.
func TestFinanceBalanceHistory_BucketStartsRenderInLocalZone(t *testing.T) {
	api, client := newFinanceReadTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	acc := client.Account.Create().SetSource("commbank").SetName("A").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)

	mel := finance.BucketZone()
	// 9am Melbourne on the local 1st of August is 23:00 UTC on 31 July, the exact case a
	// UTC-rendered bucket start misattributes. The December reading covers daylight saving.
	aug := time.Date(2026, 8, 1, 9, 0, 0, 0, mel).UTC()
	dec := time.Date(2026, 12, 1, 9, 0, 0, 0, mel).UTC()
	client.BalanceSnapshot.Create().SetBalance(100).SetAsOf(aug).SetAccountID(acc.ID).SaveX(ctx)
	client.BalanceSnapshot.Create().SetBalance(200).SetAsOf(dec).SetAccountID(acc.ID).SaveX(ctx)

	base := "/api/finance/accounts/" + strconv.Itoa(acc.ID) + "/balances"

	// days=0 is the full history, so the window does not depend on the clock.
	resp := api.Get(base+"?step=month&days=0", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("step=month = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var body struct {
		Points []BalancePointDTO `json:"points"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Points) != 5 {
		t.Fatalf("points = %d, want 5 (Aug..Dec)", len(body.Points))
	}
	if got, want := body.Points[0].AsOf, "2026-08-01T00:00:00+10:00"; got != want {
		t.Errorf("first bucket as_of = %q, want %q (a UTC rendering would name July)", got, want)
	}
	if got, want := body.Points[4].AsOf, "2026-12-01T00:00:00+11:00"; got != want {
		t.Errorf("last bucket as_of = %q, want %q (+11 under daylight saving)", got, want)
	}
	// Every bucketed point must parse back to a local midnight on the local 1st.
	for i, p := range body.Points {
		ts, err := time.Parse(time.RFC3339, p.AsOf)
		if err != nil {
			t.Fatalf("point %d as_of %q is not RFC3339: %v", i, p.AsOf, err)
		}
		lt := ts.In(mel)
		if lt.Day() != 1 || lt.Hour() != 0 {
			t.Errorf("point %d as_of = %q, want a local-midnight 1st", i, p.AsOf)
		}
	}

	// The raw series is unchanged: reading instants in UTC.
	resp = api.Get(base+"?days=0", admin)
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if len(body.Points) != 2 {
		t.Fatalf("raw points = %d, want 2", len(body.Points))
	}
	if got, want := body.Points[0].AsOf, "2026-07-31T23:00:00Z"; got != want {
		t.Errorf("raw as_of = %q, want %q (a reading instant, still UTC)", got, want)
	}
}

// keysOf lists a decoded object's keys, for asserting that a response carries no more than
// the fields it is supposed to.
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
