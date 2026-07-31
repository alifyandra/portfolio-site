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
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// Recurring-bill endpoint tests (portfolio-site#125). The read lives under /api/finance/*
// and the writes under /api/admin/finance/*, so both register funcs are wired here and the
// auth matrix is asserted across the whole surface: every route is admin-only, enforced in
// the handler rather than by middleware. All labels and amounts are invented.

// newFinanceBillsTestAPI wires the bill read + write endpoints with the real auth
// middleware over an in-memory SQLite ledger.
func newFinanceBillsTestAPI(t *testing.T) (humatest.TestAPI, *ent.Client) {
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
	h.registerAdminFinanceBills(api)
	return api, client
}

// todayUTC is the UTC-midnight "today" the derived due dates are measured from.
func todayUTC() time.Time {
	n := time.Now().UTC()
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// TestFinanceBills_RequiresAdmin: anonymous is 401 and a member is 403 on every bill
// route, read and write alike.
func TestFinanceBills_RequiresAdmin(t *testing.T) {
	api, client := newFinanceBillsTestAPI(t)
	ctx := context.Background()
	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	gets := []string{"/api/finance/bills", "/api/admin/finance/bills"}
	for _, path := range gets {
		if resp := api.Get(path); resp.Code != http.StatusUnauthorized {
			t.Errorf("anon GET %s = %d, want 401", path, resp.Code)
		}
		if resp := api.Get(path, member); resp.Code != http.StatusForbidden {
			t.Errorf("member GET %s = %d, want 403", path, resp.Code)
		}
		if resp := api.Get(path, admin); resp.Code != http.StatusOK {
			t.Errorf("admin GET %s = %d, want 200; body=%s", path, resp.Code, resp.Body.String())
		}
	}

	body := map[string]any{"name": "X", "expected_amount": 10, "anchor_date": "2026-07-01"}
	if resp := api.Post("/api/admin/finance/bills", body); resp.Code != http.StatusUnauthorized {
		t.Errorf("anon POST = %d, want 401", resp.Code)
	}
	if resp := api.Post("/api/admin/finance/bills", member, body); resp.Code != http.StatusForbidden {
		t.Errorf("member POST = %d, want 403", resp.Code)
	}
	if resp := api.Post("/api/admin/finance/bills/reconcile", member); resp.Code != http.StatusForbidden {
		t.Errorf("member reconcile = %d, want 403", resp.Code)
	}
	if resp := api.Patch("/api/admin/finance/bills/1", member, map[string]any{"notes": "x"}); resp.Code != http.StatusForbidden {
		t.Errorf("member PATCH = %d, want 403", resp.Code)
	}
	if resp := api.Delete("/api/admin/finance/bills/1", member); resp.Code != http.StatusForbidden {
		t.Errorf("member DELETE = %d, want 403", resp.Code)
	}
	if resp := api.Post("/api/admin/finance/bills/1/payments", member, map[string]any{"transaction_id": 1}); resp.Code != http.StatusForbidden {
		t.Errorf("member link = %d, want 403", resp.Code)
	}
}

// TestFinanceBills_CRUDAndDerivedFields drives the create -> read -> patch -> delete path
// and checks the wire DTO carries the derived figures: an anchor today gives days_until 0,
// the schema defaults land (AUD / monthly / 10% / 5 days / active), and the committed-money
// line on the read endpoint is non-zero.
func TestFinanceBills_CRUDAndDerivedFields(t *testing.T) {
	api, client := newFinanceBillsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	today := todayUTC()

	resp := api.Post("/api/admin/finance/bills", admin, map[string]any{
		"name":            "Housing",
		"expected_amount": 600,
		"cadence":         "fortnightly",
		"anchor_date":     today.Format(dateLayout),
		"match_pattern":   "housing debit",
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	var created FinanceBillDTO
	if err := json.Unmarshal(resp.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v; body=%s", err, resp.Body.String())
	}
	if created.Currency != "AUD" || created.Status != "active" || created.MatchWindowDays != 5 || created.AmountTolerancePct != 10 {
		t.Errorf("defaults = %+v, want AUD / active / 5 / 10", created)
	}
	if !created.AutoMatched {
		t.Error("auto_matched = false for a bill created with a pattern, want true")
	}
	if created.NextDue != today.Format(dateLayout) || created.DaysUntil != 0 {
		t.Errorf("derived = %s / %d, want today / 0", created.NextDue, created.DaysUntil)
	}
	if created.AccountID != nil {
		t.Errorf("account_id = %v, want null (the edge is optional)", created.AccountID)
	}
	if want := 600.0 * 26 / 12; created.ExpectedMonthly != want {
		t.Errorf("expected_monthly = %v, want %v", created.ExpectedMonthly, want)
	}

	// A duplicate name is a 409, not a second row.
	dup := api.Post("/api/admin/finance/bills", admin, map[string]any{
		"name": "Housing", "expected_amount": 10, "anchor_date": today.Format(dateLayout),
	})
	if dup.Code != http.StatusConflict {
		t.Errorf("duplicate name = %d, want 409; body=%s", dup.Code, dup.Body.String())
	}

	// The read endpoint reports the committed-money roll-up beside the row.
	resp = api.Get("/api/finance/bills?status=active&within_days=30", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("read = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var list struct {
		Bills             []FinanceBillDTO `json:"bills"`
		CommittedTotal    float64          `json:"committed_total"`
		MonthlyEquivalent float64          `json:"monthly_equivalent"`
		Count             int              `json:"count"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode read: %v; body=%s", err, resp.Body.String())
	}
	if list.Count != 1 || list.CommittedTotal != 600 || list.MonthlyEquivalent == 0 {
		t.Errorf("roll-up = %+v, want 1 bill / 600 committed / non-zero monthly", list)
	}

	// PATCH is partial: pausing must not disturb the untouched columns, and a paused bill
	// drops out of the committed total.
	resp = api.Patch("/api/admin/finance/bills/"+itoa(created.ID), admin, map[string]any{"status": "paused"})
	if resp.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var patched FinanceBillDTO
	if err := json.Unmarshal(resp.Body.Bytes(), &patched); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patched.Status != "paused" || patched.ExpectedAmount != 600 || patched.MatchPattern != "housing debit" {
		t.Errorf("patched = %+v, want only status changed", patched)
	}
	resp = api.Get("/api/finance/bills?status=all", admin)
	if err := json.Unmarshal(resp.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode read after pause: %v", err)
	}
	if list.Count != 1 || list.CommittedTotal != 0 {
		t.Errorf("after pause = %+v, want the bill listed with 0 committed", list)
	}

	if resp := api.Patch("/api/admin/finance/bills/9999", admin, map[string]any{"notes": "x"}); resp.Code != http.StatusNotFound {
		t.Errorf("patch missing = %d, want 404", resp.Code)
	}
	if resp := api.Delete("/api/admin/finance/bills/"+itoa(created.ID), admin); resp.Code != http.StatusNoContent {
		t.Errorf("delete = %d, want 204; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Delete("/api/admin/finance/bills/"+itoa(created.ID), admin); resp.Code != http.StatusNotFound {
		t.Errorf("delete again = %d, want 404", resp.Code)
	}
}

// TestFinanceBills_BadInputIs422: a malformed anchor date and an out-of-range status are
// rejected loudly rather than landing as a zero date or an unfiltered list.
func TestFinanceBills_BadInputIs422(t *testing.T) {
	api, client := newFinanceBillsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	if resp := api.Post("/api/admin/finance/bills", admin, map[string]any{
		"name": "Bad", "expected_amount": 10, "anchor_date": "not-a-date",
	}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad anchor = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Post("/api/admin/finance/bills", admin, map[string]any{
		"name": "Bad", "expected_amount": -5, "anchor_date": "2026-07-01",
	}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("negative amount = %d, want 422", resp.Code)
	}
	if resp := api.Get("/api/finance/bills?status=nonsense", admin); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad status = %d, want 422", resp.Code)
	}
}

// TestFinanceBills_HandReconciledBill: a bill created with no match_pattern reports
// auto_matched=false and is never dragged into an overdue state or a narrow "due soon"
// answer, however many unmatched cycles sit behind it. Nothing can ever match it, so its
// absent payments say nothing.
func TestFinanceBills_HandReconciledBill(t *testing.T) {
	api, client := newFinanceBillsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	today := todayUTC()

	acc := client.Account.Create().SetSource("commbank").SetName("Everyday").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	client.Transaction.Create().SetDedupHash("bills-coverage").SetPostedDate(today.AddDate(0, -8, 0)).
		SetAmount(-1).SetDescription("COVERAGE ROW").SetAccountID(acc.ID).SaveX(ctx)

	resp := api.Post("/api/admin/finance/bills", admin, map[string]any{
		"name": "Hand reconciled", "expected_amount": 500,
		"anchor_date": today.AddDate(0, -6, 0).Format(dateLayout),
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d; body=%s", resp.Code, resp.Body.String())
	}
	var bill FinanceBillDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &bill)
	if bill.AutoMatched {
		t.Error("auto_matched = true for a bill with no match pattern, want false")
	}
	if bill.Overdue {
		t.Error("overdue = true for a bill nothing can ever match, want false")
	}
	if bill.DaysUntil < 0 {
		t.Errorf("days_until = %d, want the forward occurrence rather than a backdated cycle", bill.DaysUntil)
	}
}

// TestFinanceBills_ReconcileAndManualLink: reconcile links a cycle from a stored row and a
// re-run links nothing; a hand link sets method=manual, and repeating it is a 409 rather
// than a silent replacement.
func TestFinanceBills_ReconcileAndManualLink(t *testing.T) {
	api, client := newFinanceBillsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	today := todayUTC()

	acc := client.Account.Create().SetSource("commbank").SetName("Everyday").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	client.Transaction.Create().SetDedupHash("bills-1").SetPostedDate(today).
		SetAmount(-120).SetDescription("PLACEHOLDER SERVICE DEBIT").SetAccountID(acc.ID).SaveX(ctx)
	txn := client.Transaction.Query().OnlyX(ctx)

	resp := api.Post("/api/admin/finance/bills", admin, map[string]any{
		"name": "Service", "expected_amount": 120, "anchor_date": today.Format(dateLayout),
		"match_pattern": "placeholder service", "account_id": acc.ID,
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d; body=%s", resp.Code, resp.Body.String())
	}
	var bill FinanceBillDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &bill)

	resp = api.Post("/api/admin/finance/bills/reconcile", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("reconcile = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var rec struct {
		BillsScanned       int `json:"bills_scanned"`
		CandidatesCompared int `json:"candidates_compared"`
		PaymentsLinked     int `json:"payments_linked"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &rec)
	if rec.BillsScanned != 1 || rec.PaymentsLinked != 1 {
		t.Fatalf("reconcile = %+v, want 1 scanned / 1 linked", rec)
	}
	resp = api.Post("/api/admin/finance/bills/reconcile", admin)
	_ = json.Unmarshal(resp.Body.Bytes(), &rec)
	if rec.PaymentsLinked != 0 {
		t.Errorf("re-run linked %d, want 0", rec.PaymentsLinked)
	}
	if rec.CandidatesCompared != 0 {
		t.Errorf("re-run compared %d rows, want 0: a settled cycle costs nothing", rec.CandidatesCompared)
	}

	// The linked cycle surfaces on the read as last_paid_*.
	resp = api.Get("/api/finance/bills", admin)
	var list struct {
		Bills []FinanceBillDTO `json:"bills"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if len(list.Bills) != 1 || list.Bills[0].LastPaidAmount == nil || *list.Bills[0].LastPaidAmount != 120 {
		t.Errorf("last_paid_amount = %v, want 120", list.Bills)
	}
	if list.Bills[0].LastPaidDate == nil || *list.Bills[0].LastPaidDate != today.Format(dateLayout) {
		t.Errorf("last_paid_date = %v, want today", list.Bills[0].LastPaidDate)
	}

	// A hand link over the auto one wins; a second identical one is a conflict.
	resp = api.Post("/api/admin/finance/bills/"+itoa(bill.ID)+"/payments", admin, map[string]any{
		"transaction_id": txn.ID, "occurrence_date": today.Format(dateLayout),
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("manual link = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	var pay BillPaymentDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &pay)
	if pay.Method != "manual" || pay.OccurrenceDate != today.Format(dateLayout) {
		t.Errorf("payment = %+v, want manual on today's cycle", pay)
	}
	resp = api.Post("/api/admin/finance/bills/"+itoa(bill.ID)+"/payments", admin, map[string]any{
		"transaction_id": txn.ID, "occurrence_date": today.Format(dateLayout),
	})
	if resp.Code != http.StatusConflict {
		t.Errorf("repeat manual link = %d, want 409; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Post("/api/admin/finance/bills/9999/payments", admin, map[string]any{"transaction_id": txn.ID}); resp.Code != http.StatusNotFound {
		t.Errorf("link on a missing bill = %d, want 404", resp.Code)
	}
	// A date off the bill's cadence grid is a 422: it would settle a cycle that does not
	// exist and leave the real one open to the matcher.
	if resp := api.Post("/api/admin/finance/bills/"+itoa(bill.ID)+"/payments", admin, map[string]any{
		"transaction_id": txn.ID, "occurrence_date": today.AddDate(0, 0, 3).Format(dateLayout),
	}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("off-grid occurrence_date = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}

	// Deleting the bill takes its links with it, leaving the ledger row alone.
	if resp := api.Delete("/api/admin/finance/bills/"+itoa(bill.ID), admin); resp.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204; body=%s", resp.Code, resp.Body.String())
	}
	if n := client.BillPayment.Query().CountX(ctx); n != 0 {
		t.Errorf("payments after delete = %d, want 0", n)
	}
	if n := client.Transaction.Query().CountX(ctx); n != 1 {
		t.Errorf("transactions after delete = %d, want the ledger row untouched", n)
	}
}

// itoa keeps the path building readable at the call sites.
func itoa(n int) string { return strconv.Itoa(n) }
