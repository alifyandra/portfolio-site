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

// TestCreateJob_Success: an admin POST with a valid body returns 201, derives the
// stage from the registry (the client no longer sends it), and — because the job is
// created enabled — populates next_run_at with a future instant immediately.
func TestCreateJob_Success(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	before := time.Now()
	resp := api.Post("/api/admin/jobs", map[string]any{
		"key":      "digest.scrape",
		"name":     "Digest scrape",
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
	if j.NextRunAt == nil {
		t.Fatalf("next_run_at = nil, want a future instant (created enabled)")
	}
	if !j.NextRunAt.After(before) {
		t.Errorf("next_run_at = %v, want after %v (a future activation, never a stale backfill)", j.NextRunAt, before)
	}
}

// TestCreateJob_DisabledLeavesNextRunNil: a job created disabled has no next run until
// an admin turns it on.
func TestCreateJob_DisabledLeavesNextRunNil(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Post("/api/admin/jobs", map[string]any{
		"key":      "digest.llm",
		"name":     "Digest summarise",
		"schedule": "0 18 * * *",
		"enabled":  false,
	}, cookie)
	if resp.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201; body=%s", resp.Code, resp.Body.String())
	}
	j := client.ScheduledJob.Query().Where(scheduledjob.KeyEQ("digest.llm")).OnlyX(ctx)
	if j.Stage != scheduledjob.StageLlm {
		t.Errorf("stage = %s, want llm (derived from the key)", j.Stage)
	}
	if j.NextRunAt != nil {
		t.Errorf("next_run_at = %v, want nil (a disabled job has no next run)", j.NextRunAt)
	}
}

// TestCreateJob_UnknownKeyRejected: a key not in the registry is a 422 and nothing
// persists (the worker could never dispatch it).
func TestCreateJob_UnknownKeyRejected(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Post("/api/admin/jobs", map[string]any{
		"key":      "totally.madeup",
		"name":     "nope",
		"schedule": "0 0 * * *",
	}, cookie)
	if resp.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown key = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	if n := client.ScheduledJob.Query().CountX(ctx); n != 0 {
		t.Errorf("rows = %d, want 0 (an unregistrable key must not persist)", n)
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

// seedJob inserts a ScheduledJob directly (bypassing the API) for update tests.
func seedJob(t *testing.T, ctx context.Context, client *ent.Client, enabled bool, next *time.Time) *ent.ScheduledJob {
	t.Helper()
	c := client.ScheduledJob.Create().
		SetKey("digest.scrape").
		SetName("Digest scrape").
		SetStage(scheduledjob.StageScrape).
		SetSchedule("0 18 * * *").
		SetTimezone("UTC").
		SetRunner(scheduledjob.RunnerServer).
		SetEnabled(enabled)
	if next != nil {
		c.SetNextRunAt(*next)
	}
	return c.SaveX(ctx)
}

// TestUpdateJob_EnableSetsNextRun is the fix for the console's "Next run shows —"
// bug: re-enabling a disabled job (which has no next_run_at) must populate a future
// next_run_at synchronously, so the UI shows it at once rather than waiting for a tick.
func TestUpdateJob_EnableSetsNextRun(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	job := seedJob(t, ctx, client, false, nil)

	before := time.Now()
	resp := api.Patch(fmt.Sprintf("/api/admin/jobs/%d", job.ID), map[string]any{"enabled": true}, cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("enable = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var got struct {
		Enabled   bool   `json:"enabled"`
		NextRunAt string `json:"next_run_at"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Enabled || got.NextRunAt == "" {
		t.Errorf("dto = %+v, want enabled with a next_run_at", got)
	}

	reloaded := client.ScheduledJob.GetX(ctx, job.ID)
	if reloaded.NextRunAt == nil {
		t.Fatalf("next_run_at = nil after enable, want a future instant")
	}
	if !reloaded.NextRunAt.After(before) {
		t.Errorf("next_run_at = %v, want after %v", reloaded.NextRunAt, before)
	}
}

// TestUpdateJob_DisableClearsNextRun: disabling a job clears its next_run_at (a
// disabled job has no next run and the ticker never fires it).
func TestUpdateJob_DisableClearsNextRun(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	future := time.Now().Add(6 * time.Hour)
	job := seedJob(t, ctx, client, true, &future)

	resp := api.Patch(fmt.Sprintf("/api/admin/jobs/%d", job.ID), map[string]any{"enabled": false}, cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("disable = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	reloaded := client.ScheduledJob.GetX(ctx, job.ID)
	if reloaded.NextRunAt != nil {
		t.Errorf("next_run_at = %v, want nil after disable", reloaded.NextRunAt)
	}
}

// TestUpdateJob_RunnerOnlyPreservesNextRun: patching only the runner of an enabled job
// must leave its existing next_run_at untouched, so a run already due (but not yet
// ticked) is not skipped by an unrelated edit.
func TestUpdateJob_RunnerOnlyPreservesNextRun(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	fixed := time.Now().Add(3 * time.Hour).Truncate(time.Second)
	job := seedJob(t, ctx, client, true, &fixed)

	resp := api.Patch(fmt.Sprintf("/api/admin/jobs/%d", job.ID), map[string]any{"runner": "any"}, cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("patch runner = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	reloaded := client.ScheduledJob.GetX(ctx, job.ID)
	if reloaded.Runner != scheduledjob.RunnerAny {
		t.Errorf("runner = %s, want any", reloaded.Runner)
	}
	if reloaded.NextRunAt == nil || !reloaded.NextRunAt.Equal(fixed) {
		t.Errorf("next_run_at = %v, want unchanged %v (a runner-only edit must not move it)", reloaded.NextRunAt, fixed)
	}
}

// TestListJobKinds_ReturnsRegistry: an admin GET returns the registrable kinds, and a
// non-admin is refused. This is the source of truth the console's "Add job" dropdown
// renders.
func TestListJobKinds_ReturnsRegistry(t *testing.T) {
	api, _, client := newWorkTestAPI(t)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)

	resp := api.Get("/api/admin/job-kinds", cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("list kinds = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var got struct {
		Kinds []struct {
			Key   string `json:"key"`
			Stage string `json:"stage"`
		} `json:"kinds"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	keys := map[string]string{}
	for _, k := range got.Kinds {
		keys[k.Key] = k.Stage
	}
	if keys["digest.scrape"] != "scrape" || keys["digest.llm"] != "llm" {
		t.Errorf("kinds = %+v, want digest.scrape/scrape and digest.llm/llm", got.Kinds)
	}

	member := sessionCookieFor(t, ctx, client, user.RoleMember)
	if resp := api.Get("/api/admin/job-kinds", member); resp.Code != http.StatusForbidden {
		t.Errorf("member list kinds = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
}
