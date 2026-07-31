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
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/ent/wishlistitem"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// newAdminWishlistTestAPI wires only the wishlist write endpoints with the real auth
// middleware and an in-memory SQLite database, so a request drives the full cookie ->
// requireAdmin -> Ent path.
func newAdminWishlistTestAPI(t *testing.T) (humatest.TestAPI, *ent.Client) {
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
	h.registerAdminWishlist(api)
	return api, client
}

// decodeWishlistItem reads an AdminWishlistItemDTO out of a write response.
func decodeWishlistItem(t *testing.T, body []byte) AdminWishlistItemDTO {
	t.Helper()
	var got AdminWishlistItemDTO
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, string(body))
	}
	return got
}

// TestAdminWishlist_RequiresAdmin: anonymous is 401 and any authenticated non-admin is
// 403 on every write, so the UI gate is never the real one.
func TestAdminWishlist_RequiresAdmin(t *testing.T) {
	api, client := newAdminWishlistTestAPI(t)
	ctx := context.Background()
	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	item := client.WishlistItem.Create().SetName("new glasses").SaveX(ctx)

	if resp := api.Post("/api/admin/wishlist", map[string]any{"name": "x"}); resp.Code != http.StatusUnauthorized {
		t.Errorf("anon create = %d, want 401", resp.Code)
	}
	if resp := api.Post("/api/admin/wishlist", map[string]any{"name": "x"}, member); resp.Code != http.StatusForbidden {
		t.Errorf("member create = %d, want 403", resp.Code)
	}
	if resp := api.Patch("/api/admin/wishlist/1", map[string]any{"name": "x"}); resp.Code != http.StatusUnauthorized {
		t.Errorf("anon update = %d, want 401", resp.Code)
	}
	if resp := api.Patch("/api/admin/wishlist/1", map[string]any{"name": "x"}, member); resp.Code != http.StatusForbidden {
		t.Errorf("member update = %d, want 403", resp.Code)
	}
	if resp := api.Delete("/api/admin/wishlist/1"); resp.Code != http.StatusUnauthorized {
		t.Errorf("anon delete = %d, want 401", resp.Code)
	}
	if resp := api.Delete("/api/admin/wishlist/1", member); resp.Code != http.StatusForbidden {
		t.Errorf("member delete = %d, want 403", resp.Code)
	}
	if n := client.WishlistItem.Query().CountX(ctx); n != 1 {
		t.Errorf("rows = %d, want the 1 seeded row untouched", n)
	}
	if _, err := client.WishlistItem.Get(ctx, item.ID); err != nil {
		t.Errorf("seeded row = %v, want it to survive the rejected writes", err)
	}
}

// TestAdminWishlist_CreateDefaults: a create with only a name takes the schema defaults
// (medium / wanted / AUD / estimate), leaves the price unknown rather than zero, and has
// no resolved_at while it is still wanted.
func TestAdminWishlist_CreateDefaults(t *testing.T) {
	api, client := newAdminWishlistTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Post("/api/admin/wishlist", map[string]any{"name": "camera bag"}, admin)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	got := decodeWishlistItem(t, resp.Body.Bytes())
	if got.Priority != "medium" || got.Status != "wanted" || got.Currency != "AUD" || !got.AmountIsEstimate {
		t.Errorf("defaults = %+v, want medium/wanted/AUD/estimate", got)
	}
	if got.Amount != nil {
		t.Errorf("amount = %v, want null (unknown price, not zero)", *got.Amount)
	}
	if got.ResolvedAt != nil || got.Deadline != nil {
		t.Errorf("resolved_at/deadline = %v/%v, want both null", got.ResolvedAt, got.Deadline)
	}
	if n := client.WishlistItem.Query().CountX(ctx); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

// TestAdminWishlist_DeadlineRoundTrip: a date-only deadline is stored at UTC midnight and
// comes back as YYYY-MM-DD; an empty string on PATCH clears it; a malformed date is a 422.
func TestAdminWishlist_DeadlineRoundTrip(t *testing.T) {
	api, client := newAdminWishlistTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Post("/api/admin/wishlist", map[string]any{
		"name":     "car service",
		"amount":   450.0,
		"priority": "high",
		"deadline": "2026-09-01",
	}, admin)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	got := decodeWishlistItem(t, resp.Body.Bytes())
	if got.Deadline == nil || *got.Deadline != "2026-09-01" {
		t.Fatalf("deadline = %v, want 2026-09-01", got.Deadline)
	}
	row := client.WishlistItem.GetX(ctx, got.ID)
	wantMidnight := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if row.Deadline == nil || !row.Deadline.UTC().Equal(wantMidnight) {
		t.Errorf("stored deadline = %v, want UTC midnight %v", row.Deadline, wantMidnight)
	}

	cleared := api.Patch("/api/admin/wishlist/"+strconv.Itoa(got.ID), map[string]any{"deadline": ""}, admin)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear deadline = %d, want 200; body=%s", cleared.Code, cleared.Body.String())
	}
	if d := decodeWishlistItem(t, cleared.Body.Bytes()).Deadline; d != nil {
		t.Errorf("deadline after clear = %v, want null", *d)
	}

	bad := api.Patch("/api/admin/wishlist/"+strconv.Itoa(got.ID), map[string]any{"deadline": "not-a-date"}, admin)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad deadline = %d, want 422; body=%s", bad.Code, bad.Body.String())
	}
}

// TestAdminWishlist_ResolvedAtTransitions: the server stamps resolved_at when an item
// leaves wanted and clears it when it goes back, so the UI never computes the lifecycle.
// A repeated set of the same status does not re-stamp.
func TestAdminWishlist_ResolvedAtTransitions(t *testing.T) {
	api, client := newAdminWishlistTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	item := client.WishlistItem.Create().SetName("new glasses").SetAmount(400).SaveX(ctx)
	path := "/api/admin/wishlist/" + strconv.Itoa(item.ID)

	bought := api.Patch(path, map[string]any{"status": "bought"}, admin)
	if bought.Code != http.StatusOK {
		t.Fatalf("mark bought = %d, want 200; body=%s", bought.Code, bought.Body.String())
	}
	stamped := decodeWishlistItem(t, bought.Body.Bytes())
	if stamped.Status != "bought" || stamped.ResolvedAt == nil {
		t.Fatalf("bought = %+v, want status bought with a resolved_at", stamped)
	}
	firstStamp := *stamped.ResolvedAt

	// Setting the same status again is not a move, so the original decision time stands.
	again := api.Patch(path, map[string]any{"status": "bought", "priority": "low"}, admin)
	if again.Code != http.StatusOK {
		t.Fatalf("repeat bought = %d, want 200; body=%s", again.Code, again.Body.String())
	}
	if r := decodeWishlistItem(t, again.Body.Bytes()).ResolvedAt; r == nil || *r != firstStamp {
		t.Errorf("resolved_at = %v, want the unchanged %s", r, firstStamp)
	}

	back := api.Patch(path, map[string]any{"status": "wanted"}, admin)
	if back.Code != http.StatusOK {
		t.Fatalf("back to wanted = %d, want 200; body=%s", back.Code, back.Body.String())
	}
	reopened := decodeWishlistItem(t, back.Body.Bytes())
	if reopened.Status != "wanted" || reopened.ResolvedAt != nil {
		t.Errorf("reopened = %+v, want status wanted with resolved_at null", reopened)
	}
	if row := client.WishlistItem.GetX(ctx, item.ID); row.ResolvedAt != nil || row.Status != wishlistitem.StatusWanted {
		t.Errorf("row = %s / %v, want wanted with no resolved_at", row.Status, row.ResolvedAt)
	}

	abandoned := api.Patch(path, map[string]any{"status": "abandoned"}, admin)
	if r := decodeWishlistItem(t, abandoned.Body.Bytes()).ResolvedAt; r == nil {
		t.Errorf("abandoned resolved_at = nil, want a stamp")
	}
}

// TestAdminWishlist_AmountUnknownClears: amount_unknown moves a priced item back to an
// unknown price, which a bare JSON null could not express.
func TestAdminWishlist_AmountUnknownClears(t *testing.T) {
	api, client := newAdminWishlistTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	item := client.WishlistItem.Create().SetName("laptop").SetAmount(2000).SaveX(ctx)

	resp := api.Patch("/api/admin/wishlist/"+strconv.Itoa(item.ID), map[string]any{"amount_unknown": true}, admin)
	if resp.Code != http.StatusOK {
		t.Fatalf("clear amount = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	if a := decodeWishlistItem(t, resp.Body.Bytes()).Amount; a != nil {
		t.Errorf("amount = %v, want null", *a)
	}

	set := api.Patch("/api/admin/wishlist/"+strconv.Itoa(item.ID), map[string]any{"amount": 1800.0}, admin)
	if a := decodeWishlistItem(t, set.Body.Bytes()).Amount; a == nil || *a != 1800 {
		t.Errorf("amount = %v, want 1800", a)
	}
}

// TestAdminWishlist_DeleteAndNotFound: delete is a 204 and removes the row; a write
// against an unknown id is a 404, not a 500.
func TestAdminWishlist_DeleteAndNotFound(t *testing.T) {
	api, client := newAdminWishlistTestAPI(t)
	ctx := context.Background()
	admin := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	item := client.WishlistItem.Create().SetName("desk lamp").SaveX(ctx)

	if resp := api.Delete("/api/admin/wishlist/"+strconv.Itoa(item.ID), admin); resp.Code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204; body=%s", resp.Code, resp.Body.String())
	}
	if n := client.WishlistItem.Query().CountX(ctx); n != 0 {
		t.Errorf("rows = %d, want 0", n)
	}
	if resp := api.Delete("/api/admin/wishlist/"+strconv.Itoa(item.ID), admin); resp.Code != http.StatusNotFound {
		t.Errorf("re-delete = %d, want 404", resp.Code)
	}
	if resp := api.Patch("/api/admin/wishlist/"+strconv.Itoa(item.ID), map[string]any{"name": "gone"}, admin); resp.Code != http.StatusNotFound {
		t.Errorf("update missing = %d, want 404", resp.Code)
	}
}
