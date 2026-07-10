package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
)

// sessionCookieFor creates a User at the given role and a live Session for it,
// returning the "Cookie: session=<raw>" header so a request drives the real auth
// middleware -> requireAdmin path (the same resolve the session cookie does in
// production). The token is stored as hex(sha256(raw)), matching auth.hashToken.
func sessionCookieFor(t *testing.T, ctx context.Context, client *ent.Client, role user.Role) string {
	t.Helper()
	nano := time.Now().UnixNano()
	u := client.User.Create().
		SetEmail(fmt.Sprintf("%s-%d@x.com", role, nano)).
		SetRole(role).
		SaveX(ctx)
	raw := fmt.Sprintf("sess-%s-%d", role, nano)
	sum := sha256.Sum256([]byte(raw))
	client.Session.Create().
		SetTokenHash(hex.EncodeToString(sum[:])).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetOwner(u).
		SaveX(ctx)
	return "Cookie: session=" + raw
}

// TestCreateJob_Success: an admin POST with a valid body returns 201 and persists a
// ScheduledJob row with the given fields and no next_run_at (the scheduler sets that
// on its first tick).
func TestCreateJob_Success(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Post("/api/admin/jobs", map[string]any{
		"key":      "digest.scrape",
		"name":     "Digest scrape",
		"stage":    "scrape",
		"schedule": "0 18 * * *",
		"timezone": "Australia/Melbourne",
		"runner":   "server",
		"enabled":  true,
	}, cookie)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	var got struct {
		Key     string `json:"key"`
		Stage   string `json:"stage"`
		Runner  string `json:"runner"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Key != "digest.scrape" || got.Stage != "scrape" || got.Runner != "server" || !got.Enabled {
		t.Errorf("dto = %+v, want the created job", got)
	}

	j := client.ScheduledJob.Query().Where(scheduledjob.KeyEQ("digest.scrape")).OnlyX(ctx)
	if j.Stage != scheduledjob.StageScrape || j.Timezone != "Australia/Melbourne" || !j.Enabled {
		t.Errorf("row = stage:%s tz:%s enabled:%v, want scrape/Australia-Melbourne/true", j.Stage, j.Timezone, j.Enabled)
	}
	if j.NextRunAt != nil {
		t.Errorf("next_run_at = %v, want nil (scheduler initializes it on first tick)", j.NextRunAt)
	}
}

// TestCreateJob_DuplicateKeyConflict: a key that already exists is a 409, not a 500
// (the unique index is a client error, not a server fault).
func TestCreateJob_DuplicateKeyConflict(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	client.ScheduledJob.Create().
		SetKey("digest.scrape").
		SetName("existing").
		SetStage(scheduledjob.StageScrape).
		SetSchedule("0 0 * * *").
		SetTimezone("UTC").
		SetRunner(scheduledjob.RunnerServer).
		SaveX(ctx)

	resp := api.Post("/api/admin/jobs", map[string]any{
		"key":      "digest.scrape",
		"name":     "dup",
		"stage":    "llm",
		"schedule": "0 1 * * *",
	}, cookie)
	if resp.Code != http.StatusConflict {
		t.Fatalf("duplicate key = %d, want 409; body=%s", resp.Code, resp.Body.String())
	}
}

// TestCreateJob_InvalidCronRejected: a malformed cron is a 422 and nothing persists.
func TestCreateJob_InvalidCronRejected(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Post("/api/admin/jobs", map[string]any{
		"key":      "digest.scrape",
		"name":     "n",
		"stage":    "scrape",
		"schedule": "not a cron",
	}, cookie)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad cron = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if n := client.ScheduledJob.Query().CountX(ctx); n != 0 {
		t.Errorf("rows = %d, want 0 (an invalid create must not persist)", n)
	}
}

// TestCreateJob_InvalidTimezoneRejected: an unknown IANA timezone is a 422.
func TestCreateJob_InvalidTimezoneRejected(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Post("/api/admin/jobs", map[string]any{
		"key":      "digest.scrape",
		"name":     "n",
		"stage":    "scrape",
		"schedule": "0 0 * * *",
		"timezone": "Mars/Phobos",
	}, cookie)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad tz = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
}

// TestCreateJob_RequiresAdmin is the server-side gate: anonymous is 401, a non-admin
// session is 403, and neither rejected write persists a row.
func TestCreateJob_RequiresAdmin(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	body := map[string]any{
		"key":      "digest.scrape",
		"name":     "n",
		"stage":    "scrape",
		"schedule": "0 0 * * *",
	}

	if resp := api.Post("/api/admin/jobs", body); resp.Code != http.StatusUnauthorized {
		t.Errorf("anonymous create = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	if resp := api.Post("/api/admin/jobs", body, member); resp.Code != http.StatusForbidden {
		t.Errorf("member create = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
	if n := client.ScheduledJob.Query().CountX(ctx); n != 0 {
		t.Errorf("rows = %d, want 0 (the gate must block the write)", n)
	}
}
