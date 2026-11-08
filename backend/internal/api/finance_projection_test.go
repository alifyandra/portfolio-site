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
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// Projection endpoint tests (ADR 0019). The walk's rules are pinned in
// internal/finance/projection_test.go against a fixed clock; these cover the HTTP surface:
// the admin guard, query validation, and the wire shape (every point marked projected,
// bucket starts in the Melbourne zone, money rounded to the cent). All labels and amounts
// are invented.

func newFinanceProjectionTestAPI(t *testing.T) (humatest.TestAPI, *ent.Client) {
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
	h.registerFinanceProjection(api)
	return api, client
}

type projectionResponse struct {
	Today       string                   `json:"today"`
	HorizonEnd  string                   `json:"horizon_end"`
	HorizonDays int                      `json:"horizon_days"`
	Step        string                   `json:"step"`
	Accounts    []ProjectionAccountDTO   `json:"accounts"`
	NetWorth    ProjectionNetWorthDTO    `json:"net_worth"`
	Events      []ProjectionEventDTO     `json:"events"`
	Excluded    []ProjectionExclusionDTO `json:"excluded"`
}

// TestFinanceProjection_RequiresAdmin: anonymous is 401 and a member is 403, the same
// matrix as every other finance read.
func TestFinanceProjection_RequiresAdmin(t *testing.T) {
	api, client := newFinanceProjectionTestAPI(t)
	ctx := context.Background()
	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	const path = "/api/finance/projection"
	if resp := api.Get(path); resp.Code != http.StatusUnauthorized {
		t.Errorf("anon GET = %d, want 401", resp.Code)
	}
	if resp := api.Get(path, member); resp.Code != http.StatusForbidden {
		t.Errorf("member GET = %d, want 403", resp.Code)
	}
	if resp := api.Get(path, admin); resp.Code != http.StatusOK {
		t.Errorf("admin GET = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
}

// TestFinanceProjection_BadQuery: an unknown step is a 400 naming the allowed set, and a
// horizon outside 1..730 is refused by the schema.
func TestFinanceProjection_BadQuery(t *testing.T) {
	api, client := newFinanceProjectionTestAPI(t)
	admin := sessionCookieFor(t, context.Background(), client, user.RoleAdmin)

	resp := api.Get("/api/finance/projection?step=monthly", admin)
	if resp.Code != http.StatusBadRequest || !strings.Contains(resp.Body.String(), "week") {
		t.Errorf("step=monthly = %d %s, want 400 naming the steps", resp.Code, resp.Body.String())
	}
	for _, q := range []string{"horizon_days=0", "horizon_days=731"} {
		if resp := api.Get("/api/finance/projection?"+q, admin); resp.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d, want 422", q, resp.Code)
		}
	}
	resp = api.Get("/api/finance/projection?horizon_days=730&step=month", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("horizon_days=730 = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var body projectionResponse
	_ = json.Unmarshal(resp.Body.Bytes(), &body)
	if body.HorizonDays != 730 || body.Step != "month" {
		t.Errorf("echo = %d / %q, want 730 / month", body.HorizonDays, body.Step)
	}
}

// TestFinanceProjection_WireShape drives one account with a weekly bill whose amount has a
// third decimal, plus a bill with no account. Every point is projected and starts on a
// Melbourne local midnight, the money is rounded to the cent at the wire, the unassigned
// bill moves net worth only, and "today" is the Melbourne day.
func TestFinanceProjection_WireShape(t *testing.T) {
	api, client := newFinanceProjectionTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	today := melbourneToday(t)

	acc := client.Account.Create().SetSource("commbank").SetName("Everyday").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SaveX(ctx)
	client.BalanceSnapshot.Create().SetAccountID(acc.ID).SetBalance(1000).
		SetAsOf(boundaryNow).SaveX(ctx)
	client.RecurringBill.Create().SetName("Phone").SetExpectedAmount(33.333).
		SetCadence(recurringbill.CadenceWeekly).SetAnchorDate(today.AddDate(0, 0, 1)).
		SetAccountID(acc.ID).SaveX(ctx)
	client.RecurringBill.Create().SetName("Rates").SetExpectedAmount(100).
		SetCadence(recurringbill.CadenceMonthly).SetAnchorDate(today.AddDate(0, 0, 2)).SaveX(ctx)

	resp := api.Get("/api/finance/projection?horizon_days=14", admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("GET = %d; body=%s", resp.Code, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), `"projected":false`) {
		t.Error("a point is not marked projected")
	}
	var body projectionResponse
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Today != today.Format(dateLayout) || body.HorizonEnd != today.AddDate(0, 0, 14).Format(dateLayout) {
		t.Errorf("today/end = %s / %s, want the Melbourne day and 14 days on", body.Today, body.HorizonEnd)
	}
	if len(body.Accounts) != 1 {
		t.Fatalf("accounts = %d, want 1", len(body.Accounts))
	}
	line := body.Accounts[0]
	if len(line.Points) != 15 {
		t.Fatalf("points = %d, want 15 (today through today+14)", len(line.Points))
	}
	for _, p := range line.Points {
		if !p.Projected {
			t.Fatalf("point %s not marked projected", p.AsOf)
		}
		if !strings.HasSuffix(p.AsOf, "T00:00:00+11:00") && !strings.HasSuffix(p.AsOf, "T00:00:00+10:00") {
			t.Fatalf("as_of %s is not a Melbourne local midnight", p.AsOf)
		}
	}
	// 1000 - 2 x 33.333 = 933.334, rounded at the wire from the exact sum.
	if last := line.Points[len(line.Points)-1]; last.Balance != 933.33 {
		t.Errorf("account closes at %v, want 933.33", last.Balance)
	}
	if line.StartBalance == nil || *line.StartBalance != 1000 || line.StartAsOf == nil {
		t.Errorf("start = %v at %v, want 1000 with its reading time", line.StartBalance, line.StartAsOf)
	}
	nw := body.NetWorth
	if last := nw.Points[len(nw.Points)-1]; last.Balance != 833.33 {
		t.Errorf("net worth closes at %v, want 833.33 (the unassigned bill too)", last.Balance)
	}

	if len(body.Events) != 3 {
		t.Fatalf("events = %+v, want two phone cycles and one rates cycle", body.Events)
	}
	for _, e := range body.Events {
		switch e.Name {
		case "Phone":
			if e.Amount != -33.33 || e.AccountID == nil || *e.AccountID != acc.ID || e.Kind != "bill" {
				t.Errorf("phone event = %+v, want -33.33 on the account", e)
			}
		case "Rates":
			if e.Amount != -100 || e.AccountID != nil {
				t.Errorf("rates event = %+v, want -100 with no account", e)
			}
		}
	}
}
