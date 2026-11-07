package api

import (
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
	"github.com/danielgtaylor/huma/v2/humatest"
	_ "modernc.org/sqlite"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// Recurring Income endpoint tests (ADR 0019). The read lives under /api/finance/* and
// the writes under /api/admin/finance/*, the same split as the bills; every route is
// admin-only, enforced in the handler. All labels and amounts are invented.

// newFinanceIncomeTestAPI wires the income endpoints with the real auth middleware over
// an in-memory SQLite ledger.
func newFinanceIncomeTestAPI(t *testing.T) (humatest.TestAPI, *ent.Client) {
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
	h.registerFinanceIncome(api)
	return api, client
}

// melbourneToday is the owner's calendar day as a UTC-midnight date, the clock the
// derived pay dates are measured against. A UTC "today" would be a day behind for the
// first hours of every Australian morning.
func melbourneToday(t *testing.T) time.Time {
	t.Helper()
	loc, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	n := time.Now().In(loc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// TestFinanceIncome_RequiresAdmin: anonymous is 401 and a member is 403 on every route.
func TestFinanceIncome_RequiresAdmin(t *testing.T) {
	api, client := newFinanceIncomeTestAPI(t)
	ctx := context.Background()
	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	if resp := api.Get("/api/finance/income"); resp.Code != http.StatusUnauthorized {
		t.Errorf("anon GET = %d, want 401", resp.Code)
	}
	if resp := api.Get("/api/finance/income", member); resp.Code != http.StatusForbidden {
		t.Errorf("member GET = %d, want 403", resp.Code)
	}
	if resp := api.Get("/api/finance/income", admin); resp.Code != http.StatusOK {
		t.Errorf("admin GET = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}

	body := map[string]any{"name": "X", "expected_amount": 10, "anchor_date": "2026-07-01"}
	if resp := api.Post("/api/admin/finance/income", body); resp.Code != http.StatusUnauthorized {
		t.Errorf("anon POST = %d, want 401", resp.Code)
	}
	if resp := api.Post("/api/admin/finance/income", member, body); resp.Code != http.StatusForbidden {
		t.Errorf("member POST = %d, want 403", resp.Code)
	}
	if resp := api.Patch("/api/admin/finance/income/1", member, map[string]any{"notes": "x"}); resp.Code != http.StatusForbidden {
		t.Errorf("member PATCH = %d, want 403", resp.Code)
	}
	if resp := api.Delete("/api/admin/finance/income/1", member); resp.Code != http.StatusForbidden {
		t.Errorf("member DELETE = %d, want 403", resp.Code)
	}
	if resp := api.Post("/api/admin/finance/income/1/receipts", member, map[string]any{"transaction_id": 1}); resp.Code != http.StatusForbidden {
		t.Errorf("member link = %d, want 403", resp.Code)
	}
}

// TestFinanceIncome_CRUDAndDerivedFields drives create -> read -> patch -> delete and
// checks the wire DTO: an anchor today gives days_until 0, the schema defaults land, the
// read carries the active-only roll-up, a duplicate name is a 409.
func TestFinanceIncome_CRUDAndDerivedFields(t *testing.T) {
	api, client := newFinanceIncomeTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	today := melbourneToday(t)

	resp := api.Post("/api/admin/finance/income", admin, map[string]any{
		"name":            "Salary",
		"expected_amount": 1000,
		"cadence":         "fortnightly",
		"anchor_date":     today.Format(dateLayout),
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	var created FinanceIncomeDTO
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v; body=%s", err, resp.Body.String())
	}
	if created.Currency != "AUD" || created.Status != "active" || created.Cadence != "fortnightly" {
		t.Errorf("defaults = %+v, want AUD / active / fortnightly", created)
	}
	if created.NextExpected != today.Format(dateLayout) || created.DaysUntil != 0 {
		t.Errorf("derived = %s / %d, want today / 0", created.NextExpected, created.DaysUntil)
	}
	if created.AccountID != nil || created.LastReceivedDate != nil || created.LastReceivedAmount != nil {
		t.Errorf("optional fields = %+v, want null account and nothing received", created)
	}
	if want := 1000.0 * 26 / 12; created.ExpectedMonthly != want {
		t.Errorf("expected_monthly = %v, want %v", created.ExpectedMonthly, want)
	}

	if resp := api.Post("/api/admin/finance/income", admin, map[string]any{
		"name": "Salary", "expected_amount": 5, "anchor_date": today.Format(dateLayout),
	}); resp.Code != http.StatusConflict {
		t.Errorf("duplicate name = %d, want 409", resp.Code)
	}

	// Pause it: it stays in the all-status list but leaves the money.
	resp = api.Patch("/api/admin/finance/income/"+itoa(created.ID), admin, map[string]any{"status": "paused", "notes": "leave"})
	if resp.Code != http.StatusOK {
		t.Fatalf("patch = %d; body=%s", resp.Code, resp.Body.String())
	}
	var patched FinanceIncomeDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &patched)
	if patched.Status != "paused" || patched.Notes != "leave" || patched.ExpectedAmount != 1000 {
		t.Errorf("patched = %+v, want paused / leave / amount untouched", patched)
	}

	resp = api.Get("/api/finance/income?status=all", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("list = %d; body=%s", resp.Code, resp.Body.String())
	}
	var list struct {
		Income            []FinanceIncomeDTO `json:"income"`
		ExpectedTotal     float64            `json:"expected_total"`
		MonthlyEquivalent float64            `json:"monthly_equivalent"`
		Count             int                `json:"count"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if list.Count != 1 || len(list.Income) != 1 || list.ExpectedTotal != 0 || list.MonthlyEquivalent != 0 {
		t.Errorf("list = %+v, want one paused income and zero expected money", list)
	}
	resp = api.Get("/api/finance/income", admin) // default status=active
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if list.Count != 0 {
		t.Errorf("default (active) list count = %d, want 0 while paused", list.Count)
	}

	if resp := api.Delete("/api/admin/finance/income/"+itoa(created.ID), admin); resp.Code != http.StatusNoContent {
		t.Fatalf("delete = %d; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Delete("/api/admin/finance/income/"+itoa(created.ID), admin); resp.Code != http.StatusNotFound {
		t.Errorf("delete again = %d, want 404", resp.Code)
	}
	if resp := api.Patch("/api/admin/finance/income/9999", admin, map[string]any{"notes": "x"}); resp.Code != http.StatusNotFound {
		t.Errorf("patch missing = %d, want 404", resp.Code)
	}
}

// TestFinanceIncome_BadInputIs422: a malformed anchor, a non-positive amount, an unknown
// status filter, and a name that fits Huma's rune limit but not Ent's byte limit are all
// rejected as 422 rather than landing as a zero date, an unfiltered list or a 500.
func TestFinanceIncome_BadInputIs422(t *testing.T) {
	api, client := newFinanceIncomeTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	cases := map[string]map[string]any{
		"bad anchor":      {"name": "Bad", "expected_amount": 10, "anchor_date": "not-a-date"},
		"missing anchor":  {"name": "Bad", "expected_amount": 10},
		"negative amount": {"name": "Bad", "expected_amount": -5, "anchor_date": "2026-07-01"},
		"bad cadence":     {"name": "Bad", "expected_amount": 10, "anchor_date": "2026-07-01", "cadence": "daily"},
		// 120 runes, 240 bytes: inside Huma's maxLength, outside Ent's MaxLen.
		"multibyte name": {"name": strings.Repeat("é", 120), "expected_amount": 10, "anchor_date": "2026-07-01"},
	}
	for name, body := range cases {
		if resp := api.Post("/api/admin/finance/income", admin, body); resp.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422; body=%s", name, resp.Code, resp.Body.String())
		}
	}
	if resp := api.Get("/api/finance/income?status=nonsense", admin); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad status = %d, want 422", resp.Code)
	}

	resp := api.Post("/api/admin/finance/income", admin, map[string]any{"name": "Ok", "expected_amount": 10, "anchor_date": "2026-07-01"})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d; body=%s", resp.Code, resp.Body.String())
	}
	var inc FinanceIncomeDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &inc)
	if resp := api.Patch("/api/admin/finance/income/"+itoa(inc.ID), admin, map[string]any{"name": strings.Repeat("é", 120)}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("multibyte rename = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
}

// TestFinanceIncome_LinkReceipt: a hand link is 201 and shows up as last received; the
// same cycle again is 409, a debit is 422, an off-grid date is 422, and an unknown
// transaction is 404. Deleting the income takes its receipts with it.
func TestFinanceIncome_LinkReceipt(t *testing.T) {
	api, client := newFinanceIncomeTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	acc := client.Account.Create().SetSource("commbank").SetName("Everyday").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	credit := client.Transaction.Create().SetDedupHash("income-credit").SetPostedDate(time.Date(2026, time.June, 14, 0, 0, 0, 0, time.UTC)).
		SetAmount(990).SetDescription("PAY CREDIT").SetAccountID(acc.ID).SaveX(ctx)
	debit := client.Transaction.Create().SetDedupHash("income-debit").SetPostedDate(time.Date(2026, time.June, 15, 0, 0, 0, 0, time.UTC)).
		SetAmount(-990).SetDescription("TRANSFER OUT").SetAccountID(acc.ID).SaveX(ctx)

	resp := api.Post("/api/admin/finance/income", admin, map[string]any{
		"name": "Salary", "expected_amount": 1000, "anchor_date": "2026-03-15", "account_id": acc.ID,
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d; body=%s", resp.Code, resp.Body.String())
	}
	var inc FinanceIncomeDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &inc)
	if inc.AccountID == nil || *inc.AccountID != acc.ID || inc.AccountName != "Everyday" {
		t.Errorf("account = %v/%q, want %d/Everyday", inc.AccountID, inc.AccountName, acc.ID)
	}
	path := "/api/admin/finance/income/" + itoa(inc.ID) + "/receipts"

	resp = api.Post(path, admin, map[string]any{"transaction_id": credit.ID})
	if resp.Code != http.StatusCreated {
		t.Fatalf("link = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	var receipt IncomeReceiptDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &receipt)
	if receipt.OccurrenceDate != "2026-06-15" || receipt.IncomeID != inc.ID || receipt.TransactionID != credit.ID {
		t.Errorf("receipt = %+v, want cycle 2026-06-15 on income %d / txn %d", receipt, inc.ID, credit.ID)
	}

	if resp := api.Post(path, admin, map[string]any{"transaction_id": credit.ID}); resp.Code != http.StatusConflict {
		t.Errorf("same cycle again = %d, want 409", resp.Code)
	}
	if resp := api.Post(path, admin, map[string]any{"transaction_id": debit.ID, "occurrence_date": "2026-07-15"}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("debit = %d, want 422", resp.Code)
	}
	if resp := api.Post(path, admin, map[string]any{"transaction_id": credit.ID, "occurrence_date": "2026-07-14"}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("off-grid = %d, want 422", resp.Code)
	}
	if resp := api.Post(path, admin, map[string]any{"transaction_id": 9999}); resp.Code != http.StatusNotFound {
		t.Errorf("unknown txn = %d, want 404", resp.Code)
	}

	resp = api.Get("/api/finance/income", admin)
	var list struct {
		Income []FinanceIncomeDTO `json:"income"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if len(list.Income) != 1 || list.Income[0].LastReceivedDate == nil || *list.Income[0].LastReceivedDate != "2026-06-14" ||
		list.Income[0].LastReceivedAmount == nil || *list.Income[0].LastReceivedAmount != 990 {
		t.Errorf("list = %+v, want last received 2026-06-14 / 990", list.Income)
	}

	if resp := api.Delete("/api/admin/finance/income/"+itoa(inc.ID), admin); resp.Code != http.StatusNoContent {
		t.Fatalf("delete = %d; body=%s", resp.Code, resp.Body.String())
	}
	if n := client.IncomeReceipt.Query().CountX(ctx); n != 0 {
		t.Errorf("receipts after delete = %d, want 0", n)
	}
	if n := client.Transaction.Query().CountX(ctx); n != 2 {
		t.Errorf("transactions after delete = %d, want 2 (the ledger is untouched)", n)
	}
}

// TestFinanceIncome_LinkReceiptRules: the window, account and one-receipt-per-credit
// rules reach the wire as 422 / 422 / 409, and a link that loses a race on the cycle is
// reported as the cycle taken rather than as the transaction already linked.
func TestFinanceIncome_LinkReceiptRules(t *testing.T) {
	api, client := newFinanceIncomeTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	today := melbourneToday(t)

	everyday := client.Account.Create().SetSource("commbank").SetName("Everyday").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	savings := client.Account.Create().SetSource("commbank").SetName("Savings").
		SetType(account.TypeSavings).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	credit := func(hash string, acc *ent.Account) *ent.Transaction {
		return client.Transaction.Create().SetDedupHash(hash).SetPostedDate(today).
			SetAmount(500).SetDescription("PAY CREDIT").SetAccountID(acc.ID).SaveX(ctx)
	}
	pay, other, wrong := credit("rules-pay", everyday), credit("rules-other", everyday), credit("rules-wrong", savings)

	create := func(name string) FinanceIncomeDTO {
		resp := api.Post("/api/admin/finance/income", admin, map[string]any{
			"name": name, "expected_amount": 500, "cadence": "weekly",
			"anchor_date": today.AddDate(0, 0, -14).Format(dateLayout), "account_id": everyday.ID,
		})
		if resp.Code != http.StatusCreated {
			t.Fatalf("create %s = %d; body=%s", name, resp.Code, resp.Body.String())
		}
		var inc FinanceIncomeDTO
		_ = json.Unmarshal(resp.Body.Bytes(), &inc)
		return inc
	}
	salary, bonus := create("Salary"), create("Bonus")
	link := func(inc FinanceIncomeDTO, body map[string]any) *httptest.ResponseRecorder {
		return api.Post("/api/admin/finance/income/"+itoa(inc.ID)+"/receipts", admin, body)
	}

	if resp := link(salary, map[string]any{"transaction_id": pay.ID, "occurrence_date": today.AddDate(0, 0, 7).Format(dateLayout)}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("future cycle = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if resp := link(salary, map[string]any{"transaction_id": pay.ID, "occurrence_date": today.AddDate(0, 0, -21).Format(dateLayout)}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("pre-anchor cycle = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if resp := link(salary, map[string]any{"transaction_id": wrong.ID}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("wrong account = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if resp := link(salary, map[string]any{"transaction_id": pay.ID}); resp.Code != http.StatusCreated {
		t.Fatalf("link = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	resp := link(bonus, map[string]any{"transaction_id": pay.ID})
	if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), "transaction is already linked") {
		t.Errorf("one credit on two incomes = %d %s, want 409 naming the transaction", resp.Code, resp.Body.String())
	}

	// A concurrent link takes bonus's current cycle between the pre-check and the insert.
	fired := false
	client.IncomeReceipt.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if !fired && m.Op().Is(ent.OpCreate) {
				fired = true
				client.IncomeReceipt.Create().SetIncomeID(bonus.ID).SetTransactionID(wrong.ID).
					SetOccurrenceDate(today).SaveX(ctx)
			}
			return next.Mutate(ctx, m)
		})
	})
	resp = link(bonus, map[string]any{"transaction_id": other.ID, "occurrence_date": today.Format(dateLayout)})
	if resp.Code != http.StatusConflict || !strings.Contains(resp.Body.String(), "pay cycle already has a receipt") {
		t.Errorf("lost race on the cycle = %d %s, want 409 naming the cycle", resp.Code, resp.Body.String())
	}
}
