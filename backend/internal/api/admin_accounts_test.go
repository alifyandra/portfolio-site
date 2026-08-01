package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/danielgtaylor/huma/v2/humatest"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// newAdminAccountsTestAPI wires the account PATCH with the real auth middleware over an
// in-memory SQLite ledger, so a request drives the full cookie -> requireAdmin -> Ent
// path, and seeds one account to edit.
func newAdminAccountsTestAPI(t *testing.T) (humatest.TestAPI, *ent.Client, *ent.Account) {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	acc := client.Account.Create().
		SetSource("commbank").
		SetName("Smart Access").
		SetMaskedNumber("xxxx xxxx 1234").
		SetType(account.TypeSavings).
		SetClass(account.ClassAsset).
		SetCurrency("AUD").
		SaveX(ctx)

	svc := auth.New(client, auth.Config{})
	_, api := humatest.New(t)
	api.UseMiddleware(svc.Middleware)
	h := New(Deps{Auth: svc, Ent: client})
	h.registerAdminAccounts(api)
	return api, client, acc
}

func decodeAccountDTO(t *testing.T, body []byte) FinanceAccountDTO {
	t.Helper()
	var got FinanceAccountDTO
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, string(body))
	}
	return got
}

// TestAdminAccounts_RequiresAdmin: anonymous is 401 and an authenticated non-admin is
// 403, so the frontend gate is never the real one and a friend cannot label the ledger.
func TestAdminAccounts_RequiresAdmin(t *testing.T) {
	api, client, acc := newAdminAccountsTestAPI(t)
	ctx := context.Background()
	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	path := "/api/admin/accounts/" + strconv.Itoa(acc.ID)
	body := map[string]any{"description": "should never land"}

	if resp := api.Patch(path, body); resp.Code != http.StatusUnauthorized {
		t.Errorf("anon patch = %d, want 401", resp.Code)
	}
	if resp := api.Patch(path, body, member); resp.Code != http.StatusForbidden {
		t.Errorf("member patch = %d, want 403", resp.Code)
	}
	if got := client.Account.GetX(ctx, acc.ID); got.Description != "" {
		t.Errorf("description = %q, want it untouched by the rejected writes", got.Description)
	}
}

// TestAdminAccounts_UpdateHappyPath: an admin sets both owner-authored fields, gets the
// full account DTO back, and the row carries the new values.
func TestAdminAccounts_UpdateHappyPath(t *testing.T) {
	api, client, acc := newAdminAccountsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	const note = "holiday sinking fund, topped up every pay"
	resp := api.Patch("/api/admin/accounts/"+strconv.Itoa(acc.ID), map[string]any{
		"description":     note,
		"drawdown_policy": "no_drawdown",
	}, admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("patch = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	got := decodeAccountDTO(t, resp.Body.Bytes())
	if got.Description != note || got.DrawdownPolicy != "no_drawdown" {
		t.Errorf("body = %q/%q, want %q/no_drawdown", got.Description, got.DrawdownPolicy, note)
	}
	// The response is the full read DTO, so the ingest-owned fields still come back.
	if got.ID != acc.ID || got.Name != "Smart Access" || got.Type != "savings" || got.Class != "asset" {
		t.Errorf("ingest-owned fields = %+v, want them unchanged and present", got)
	}
	row := client.Account.GetX(ctx, acc.ID)
	if row.Description != note || row.DrawdownPolicy != account.DrawdownPolicyNoDrawdown {
		t.Errorf("row = %q/%q, want %q/no_drawdown", row.Description, row.DrawdownPolicy, note)
	}
}

// TestAdminAccounts_PartialPatchLeavesOmittedFieldAlone: the pointer body is the whole
// point. Sending only one key must not reset the other to its zero value.
func TestAdminAccounts_PartialPatchLeavesOmittedFieldAlone(t *testing.T) {
	api, client, acc := newAdminAccountsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	path := "/api/admin/accounts/" + strconv.Itoa(acc.ID)

	const note = "emergency buffer, three months of costs"
	client.Account.UpdateOne(acc).
		SetDescription(note).
		SetDrawdownPolicy(account.DrawdownPolicyEmergencyOnly).
		SaveX(ctx)

	// Policy only: the description survives.
	if resp := api.Patch(path, map[string]any{"drawdown_policy": "flexible"}, admin); resp.Code != http.StatusOK {
		t.Fatalf("policy-only patch = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	row := client.Account.GetX(ctx, acc.ID)
	if row.Description != note {
		t.Errorf("description = %q, want %q (omitted key must not clear it)", row.Description, note)
	}
	if row.DrawdownPolicy != account.DrawdownPolicyFlexible {
		t.Errorf("drawdown_policy = %q, want flexible", row.DrawdownPolicy)
	}

	// Description only: the policy survives.
	if resp := api.Patch(path, map[string]any{"description": "reworded"}, admin); resp.Code != http.StatusOK {
		t.Fatalf("description-only patch = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	row = client.Account.GetX(ctx, acc.ID)
	if row.DrawdownPolicy != account.DrawdownPolicyFlexible {
		t.Errorf("drawdown_policy = %q, want flexible (omitted key must not reset it to unset)", row.DrawdownPolicy)
	}
	if row.Description != "reworded" {
		t.Errorf("description = %q, want reworded", row.Description)
	}

	// An explicit empty string is a clear, not an omission.
	if resp := api.Patch(path, map[string]any{"description": ""}, admin); resp.Code != http.StatusOK {
		t.Fatalf("clear patch = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	if row = client.Account.GetX(ctx, acc.ID); row.Description != "" {
		t.Errorf("description = %q, want empty (explicit clear)", row.Description)
	}
}

// TestAdminAccounts_IngestOwnedKeysAreRejected: name, type, class and source belong to
// the ingest, and the input struct simply has no such keys. Huma's schema is closed, so
// a body carrying them is refused with a 422 naming each one rather than silently
// dropped, and the row is untouched (the writable key in the same body does not land
// either, which is the behaviour we want: a caller confused about ownership gets told).
func TestAdminAccounts_IngestOwnedKeysAreRejected(t *testing.T) {
	api, client, acc := newAdminAccountsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Patch("/api/admin/accounts/"+strconv.Itoa(acc.ID), map[string]any{
		"description": "labelled",
		"name":        "Renamed",
		"type":        "credit_card",
		"class":       "liability",
		"source":      "elsewhere",
	}, admin)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("patch with ingest-owned keys = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	row := client.Account.GetX(ctx, acc.ID)
	if row.Name != "Smart Access" || row.Type != account.TypeSavings || row.Class != account.ClassAsset || row.Source != "commbank" {
		t.Errorf("row = %q/%q/%q/%q, want the ingest-owned fields unchanged", row.Name, row.Type, row.Class, row.Source)
	}
	if row.Description != "" {
		t.Errorf("description = %q, want empty (the whole request was rejected)", row.Description)
	}
}

// TestAdminAccounts_UnknownIDIs404.
func TestAdminAccounts_UnknownIDIs404(t *testing.T) {
	api, client, _ := newAdminAccountsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Patch("/api/admin/accounts/999999", map[string]any{"description": "x"}, admin)
	if resp.Code != http.StatusNotFound {
		t.Errorf("unknown id = %d, want 404; body=%s", resp.Code, resp.Body.String())
	}
}

// TestAdminAccounts_InvalidPolicyIs422: an unknown enum value is rejected rather than
// coerced. SQLite would happily store it in a test and Postgres would then reject it
// against the check constraint in prod, so it has to fail at the edge.
func TestAdminAccounts_InvalidPolicyIs422(t *testing.T) {
	api, client, acc := newAdminAccountsTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Patch("/api/admin/accounts/"+strconv.Itoa(acc.ID), map[string]any{
		"drawdown_policy": "sort_of_flexible",
	}, admin)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid policy = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if row := client.Account.GetX(ctx, acc.ID); row.DrawdownPolicy != account.DrawdownPolicyUnset {
		t.Errorf("drawdown_policy = %q, want unset (the bad value must not land)", row.DrawdownPolicy)
	}
}
