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

// Recurrence Suggestion and income matching endpoint tests (ADR 0019). Every
// counterparty, amount and reference is invented.

func newFinanceSuggestionsTestAPI(t *testing.T) (humatest.TestAPI, *ent.Client) {
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
	h := New(Deps{Auth: svc, Ent: client, Now: func() time.Time { return boundaryNow }})
	h.registerFinanceIncome(api)
	h.registerAdminFinanceBills(api)
	h.registerFinanceSuggestions(api)
	return api, client
}

// seedSuggestionLedger posts two synthetic runs into one account: a monthly streaming
// debit and a fortnightly salary credit, each four rows ending a few days ago.
func seedSuggestionLedger(t *testing.T, ctx context.Context, client *ent.Client) *ent.Account {
	t.Helper()
	today := melbourneToday(t)
	acc := client.Account.Create().SetSource("commbank").SetName("Everyday").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	monthly := today.AddDate(0, 0, -3)
	if monthly.Day() > 28 {
		monthly = monthly.AddDate(0, 0, 28-monthly.Day())
	}
	for i := 0; i < 4; i++ {
		client.Transaction.Create().SetDedupHash("sg-zylo-" + itoa(i)).SetPostedDate(monthly.AddDate(0, -i, 0)).
			SetAmount(-15.99).SetDescription("ZYLO STREAMING " + itoa(1000+i)).SetAccountID(acc.ID).SaveX(ctx)
		client.Transaction.Create().SetDedupHash("sg-pay-" + itoa(i)).SetPostedDate(today.AddDate(0, 0, -2-14*i)).
			SetAmount(2450.004).SetDescription("Salary ACME WIDGETS").SetAccountID(acc.ID).SaveX(ctx)
	}
	return acc
}

// TestFinanceSuggestions_RequiresAdmin: anonymous is 401 and a member is 403 on every
// new route.
func TestFinanceSuggestions_RequiresAdmin(t *testing.T) {
	api, client := newFinanceSuggestionsTestAPI(t)
	ctx := context.Background()
	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	for _, r := range []struct {
		method, path string
		body         map[string]any
	}{
		{http.MethodGet, "/api/admin/finance/suggestions", nil},
		{http.MethodGet, "/api/admin/finance/suggestions/1", nil},
		{http.MethodPost, "/api/admin/finance/suggestions/scan", nil},
		{http.MethodPost, "/api/admin/finance/suggestions/1/accept", map[string]any{"match_pattern": ""}},
		{http.MethodPost, "/api/admin/finance/suggestions/1/dismiss", nil},
		{http.MethodPost, "/api/admin/finance/income/reconcile", nil},
	} {
		var body []any
		if r.method == http.MethodPost {
			if r.body == nil {
				r.body = map[string]any{}
			}
			body = []any{r.body}
		}
		if resp := api.Do(r.method, r.path, body...); resp.Code != http.StatusUnauthorized {
			t.Errorf("anon %s %s = %d, want 401", r.method, r.path, resp.Code)
		}
		if resp := api.Do(r.method, r.path, append([]any{member}, body...)...); resp.Code != http.StatusForbidden {
			t.Errorf("member %s %s = %d, want 403", r.method, r.path, resp.Code)
		}
	}
}

type suggestionList struct {
	Suggestions []FinanceSuggestionDTO `json:"suggestions"`
	Count       int                    `json:"count"`
}

// TestFinanceSuggestions_ScanAcceptDismiss drives the review loop: a scan stores both
// runs as pending; accepting the salary creates the income with its pattern and its
// history as auto receipts; dismissing the subscription is remembered, so a rescan
// offers nothing new; a decided suggestion refuses a second decision.
func TestFinanceSuggestions_ScanAcceptDismiss(t *testing.T) {
	api, client := newFinanceSuggestionsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	acc := seedSuggestionLedger(t, ctx, client)

	resp := api.Post("/api/admin/finance/suggestions/scan", admin, map[string]any{})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"created":2`) {
		t.Fatalf("scan = %d %s, want 200 with 2 created", resp.Code, resp.Body.String())
	}
	var list suggestionList
	resp = api.Get("/api/admin/finance/suggestions", admin)
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if resp.Code != http.StatusOK || list.Count != 2 {
		t.Fatalf("list = %d %s, want 2 pending", resp.Code, resp.Body.String())
	}
	var bill, income FinanceSuggestionDTO
	for _, s := range list.Suggestions {
		switch s.Kind {
		case "bill":
			bill = s
		case "income":
			income = s
		}
	}
	if bill.Cadence != "monthly" || bill.ExpectedAmount != 15.99 || bill.MatchPattern != "ZYLO STREAMING" ||
		bill.AccountID == nil || *bill.AccountID != acc.ID || len(bill.Evidence) != 4 || bill.Evidence[0].Amount != -15.99 {
		t.Errorf("bill suggestion = %+v", bill)
	}
	if income.Cadence != "fortnightly" || income.ExpectedAmount != 2450 || income.Status != "pending" || income.DecidedAt != nil {
		t.Errorf("income suggestion = %+v, want fortnightly 2450 pending", income)
	}
	if income.Evidence[0].PostedDate <= income.Evidence[3].PostedDate {
		t.Errorf("evidence not newest first: %+v", income.Evidence)
	}
	if bill.EvidenceCount != 4 || bill.EvidenceSince != bill.Evidence[3].PostedDate {
		t.Errorf("bill evidence count/since = %d/%s, want 4 and the oldest row's date", bill.EvidenceCount, bill.EvidenceSince)
	}
	var detail FinanceSuggestionDTO
	resp = api.Get("/api/admin/finance/suggestions/"+itoa(bill.ID), admin)
	_ = json.Unmarshal(resp.Body.Bytes(), &detail)
	if resp.Code != http.StatusOK || detail.ID != bill.ID || len(detail.Evidence) != 4 || detail.EvidenceCount != 4 {
		t.Errorf("detail = %d %s, want the bill suggestion with all 4 rows", resp.Code, resp.Body.String())
	}
	if resp := api.Get("/api/admin/finance/suggestions/9999", admin); resp.Code != http.StatusNotFound {
		t.Errorf("detail unknown = %d, want 404", resp.Code)
	}

	base := "/api/admin/finance/suggestions/"
	if resp := api.Post(base+itoa(bill.ID)+"/accept", admin, map[string]any{"name": strings.Repeat("é", 61), "match_pattern": ""}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("accept with a 122-byte name = %d, want 422", resp.Code)
	}
	if resp := api.Post(base+itoa(bill.ID)+"/accept", admin, map[string]any{"match_pattern": strings.Repeat("é", 101)}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("accept with a 202-byte pattern = %d, want 422", resp.Code)
	}
	if resp := api.Post(base+"9999/accept", admin, map[string]any{"match_pattern": ""}); resp.Code != http.StatusNotFound {
		t.Errorf("accept unknown = %d, want 404", resp.Code)
	}
	// The match pattern is the owner's: an accept that does not state one is refused, so
	// the suggested text is never copied into the declaration unseen.
	if resp := api.Post(base+itoa(income.ID)+"/accept", admin, map[string]any{"name": "Salary"}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("accept without match_pattern = %d, want 422", resp.Code)
	}
	resp = api.Post(base+itoa(income.ID)+"/accept", admin, map[string]any{"name": "Salary", "match_pattern": "  acme widgets "})
	if resp.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", resp.Code, resp.Body.String())
	}
	var accepted struct {
		Kind     string `json:"kind"`
		IncomeID *int   `json:"income_id"`
		BillID   *int   `json:"bill_id"`
		Linked   int    `json:"linked"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &accepted)
	if accepted.Kind != "income" || accepted.IncomeID == nil || accepted.BillID != nil || accepted.Linked != 4 {
		t.Fatalf("accept = %+v, want an income with 4 receipts", accepted)
	}
	resp = api.Get("/api/finance/income", admin)
	if !strings.Contains(resp.Body.String(), `"match_pattern":"acme widgets"`) || !strings.Contains(resp.Body.String(), `"name":"Salary"`) {
		t.Errorf("income list = %s, want Salary with the owner's edited pattern", resp.Body.String())
	}
	resp = api.Get("/api/admin/finance/income/"+itoa(*accepted.IncomeID)+"/receipts", admin)
	if strings.Count(resp.Body.String(), `"method":"auto"`) != 4 {
		t.Errorf("receipts = %s, want 4 auto", resp.Body.String())
	}
	if resp := api.Post(base+itoa(income.ID)+"/accept", admin, map[string]any{"match_pattern": ""}); resp.Code != http.StatusConflict {
		t.Errorf("accept twice = %d, want 409", resp.Code)
	}

	resp = api.Post(base+itoa(bill.ID)+"/dismiss", admin, map[string]any{})
	var dismissed FinanceSuggestionDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &dismissed)
	if resp.Code != http.StatusOK || dismissed.Status != "dismissed" || dismissed.DecidedAt == nil {
		t.Fatalf("dismiss = %d %s", resp.Code, resp.Body.String())
	}
	if resp := api.Post(base+itoa(bill.ID)+"/dismiss", admin, map[string]any{}); resp.Code != http.StatusConflict {
		t.Errorf("dismiss twice = %d, want 409", resp.Code)
	}
	if resp := api.Post(base+itoa(bill.ID)+"/accept", admin, map[string]any{"match_pattern": ""}); resp.Code != http.StatusConflict {
		t.Errorf("accept a dismissed one = %d, want 409", resp.Code)
	}
	if resp := api.Post(base+"9999/dismiss", admin, map[string]any{}); resp.Code != http.StatusNotFound {
		t.Errorf("dismiss unknown = %d, want 404", resp.Code)
	}

	resp = api.Post("/api/admin/finance/suggestions/scan", admin, map[string]any{})
	if !strings.Contains(resp.Body.String(), `"created":0`) || !strings.Contains(resp.Body.String(), `"dismissed":1`) {
		t.Errorf("rescan = %s, want nothing new and the dismissal remembered", resp.Body.String())
	}
	resp = api.Get("/api/admin/finance/suggestions?status=all", admin)
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if list.Count != 2 {
		t.Errorf("all = %d, want 2", list.Count)
	}
	resp = api.Get("/api/admin/finance/suggestions", admin)
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if list.Count != 0 || !strings.Contains(resp.Body.String(), `"suggestions":[]`) {
		t.Errorf("pending = %s, want an empty list", resp.Body.String())
	}

	// Deleting the accepted income reopens its suggestion for review; the dismissed one
	// stays dismissed.
	if resp := api.Delete("/api/admin/finance/income/"+itoa(*accepted.IncomeID), admin); resp.Code != http.StatusNoContent {
		t.Fatalf("delete accepted income = %d %s", resp.Code, resp.Body.String())
	}
	resp = api.Get("/api/admin/finance/suggestions", admin)
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if list.Count != 1 || list.Suggestions[0].ID != income.ID || list.Suggestions[0].IncomeID != nil || list.Suggestions[0].DecidedAt != nil {
		t.Errorf("pending after the income went = %s, want the income suggestion reopened and unlinked", resp.Body.String())
	}
	resp = api.Get("/api/admin/finance/suggestions?status=dismissed", admin)
	_ = json.Unmarshal(resp.Body.Bytes(), &list)
	if list.Count != 1 || list.Suggestions[0].ID != bill.ID {
		t.Errorf("dismissed = %s, want the bill suggestion still dismissed", resp.Body.String())
	}
}

// TestFinanceSuggestions_DeletingAnAcceptedBillReopens: deleting the bill an accepted
// suggestion created puts the suggestion back to pending, so the run can be accepted
// again; a bill not made from a suggestion deletes as before.
func TestFinanceSuggestions_DeletingAnAcceptedBillReopens(t *testing.T) {
	api, client := newFinanceSuggestionsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	seedSuggestionLedger(t, ctx, client)
	if resp := api.Post("/api/admin/finance/suggestions/scan", admin, map[string]any{}); resp.Code != http.StatusOK {
		t.Fatalf("scan = %d", resp.Code)
	}
	var list suggestionList
	_ = json.Unmarshal(api.Get("/api/admin/finance/suggestions", admin).Body.Bytes(), &list)
	var bill FinanceSuggestionDTO
	for _, s := range list.Suggestions {
		if s.Kind == "bill" {
			bill = s
		}
	}
	resp := api.Post("/api/admin/finance/suggestions/"+itoa(bill.ID)+"/accept", admin, map[string]any{"match_pattern": "zylo streaming"})
	var accepted struct {
		BillID *int `json:"bill_id"`
	}
	_ = json.Unmarshal(resp.Body.Bytes(), &accepted)
	if resp.Code != http.StatusOK || accepted.BillID == nil {
		t.Fatalf("accept = %d %s", resp.Code, resp.Body.String())
	}
	if resp := api.Delete("/api/admin/finance/bills/"+itoa(*accepted.BillID), admin); resp.Code != http.StatusNoContent {
		t.Fatalf("delete bill = %d %s", resp.Code, resp.Body.String())
	}
	var got FinanceSuggestionDTO
	_ = json.Unmarshal(api.Get("/api/admin/finance/suggestions/"+itoa(bill.ID), admin).Body.Bytes(), &got)
	if got.Status != "pending" || got.BillID != nil || got.DecidedAt != nil {
		t.Fatalf("suggestion after its bill went = %+v, want pending and unlinked", got)
	}
	if resp := api.Post("/api/admin/finance/suggestions/"+itoa(bill.ID)+"/accept", admin, map[string]any{"match_pattern": ""}); resp.Code != http.StatusOK {
		t.Errorf("accept again = %d %s, want 200", resp.Code, resp.Body.String())
	}
}

// TestFinanceIncome_MatchPatternAndReconcile: an income takes a match pattern on create
// and patch (byte-capped like the name), and the reconcile route links a matching credit
// as an auto receipt; one paid two days early still settles its cycle.
func TestFinanceIncome_MatchPatternAndReconcile(t *testing.T) {
	api, client := newFinanceSuggestionsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	today := melbourneToday(t)
	acc := client.Account.Create().SetSource("commbank").SetName("Everyday").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	client.Transaction.Create().SetDedupHash("mp-pay").SetPostedDate(today).
		SetAmount(800).SetDescription("WAGES QUOKKA CAFE").SetAccountID(acc.ID).SaveX(ctx)

	resp := api.Post("/api/admin/finance/income", admin, map[string]any{
		"name": "Cafe", "expected_amount": 800, "cadence": "weekly",
		"anchor_date": today.AddDate(0, 0, 2).Format(dateLayout), "match_pattern": "  numbat  ",
	})
	var inc FinanceIncomeDTO
	_ = json.Unmarshal(resp.Body.Bytes(), &inc)
	if resp.Code != http.StatusCreated || inc.MatchPattern != "numbat" {
		t.Fatalf("create = %d %s, want the pattern trimmed", resp.Code, resp.Body.String())
	}
	path := "/api/admin/finance/income/" + itoa(inc.ID)
	if resp := api.Patch(path, admin, map[string]any{"match_pattern": strings.Repeat("é", 101)}); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("202-byte pattern = %d, want 422", resp.Code)
	}
	resp = api.Post("/api/admin/finance/income/reconcile", admin, map[string]any{})
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"receipts_linked":0`) {
		t.Errorf("reconcile with the wrong pattern = %d %s, want nothing linked", resp.Code, resp.Body.String())
	}
	if resp := api.Patch(path, admin, map[string]any{"match_pattern": "quokka cafe"}); resp.Code != http.StatusOK {
		t.Fatalf("patch pattern = %d %s", resp.Code, resp.Body.String())
	}
	resp = api.Post("/api/admin/finance/income/reconcile", admin, map[string]any{})
	if !strings.Contains(resp.Body.String(), `"receipts_linked":1`) {
		t.Fatalf("reconcile = %s, want one receipt", resp.Body.String())
	}
	resp = api.Get(path+"/receipts", admin)
	want := `"occurrence_date":"` + today.AddDate(0, 0, 2).Format(dateLayout) + `","method":"auto"`
	if !strings.Contains(resp.Body.String(), want) {
		t.Errorf("receipts = %s, want the pay date two days out settled by today's credit, auto", resp.Body.String())
	}
	if resp := api.Patch(path, admin, map[string]any{"match_pattern": ""}); resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"match_pattern":""`) {
		t.Errorf("clear pattern = %d %s", resp.Code, resp.Body.String())
	}
}
