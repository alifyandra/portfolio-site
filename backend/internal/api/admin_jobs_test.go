package api

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/danielgtaylor/huma/v2/humatest"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/ent/user"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
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

// --- start-job-run (force "Run now") ---

// stubEnqueuer records enqueues and reports a fixed configured state, so the force-start
// handler's enqueue side effect is observable without a live SQS.
type stubEnqueuer struct {
	configured bool
	enqueued   []queue.Job
}

func (s *stubEnqueuer) Configured() bool { return s.configured }

func (s *stubEnqueuer) Enqueue(_ context.Context, j queue.Job) error {
	s.enqueued = append(s.enqueued, j)
	return nil
}

// stubNotifier records the notifications the finance sync path sends, so the handshake
// and the start/finish reports are observable (and can be told to fail, to prove
// non-fatality).
type stubNotifier struct {
	fail     bool
	calls    []int              // run ids notified via NotifyRefresh
	started  []int              // run ids notified via NotifyRunStarted
	finished []stubFinishedCall // terminal reports, in order
}

// stubFinishedCall is one NotifyRunFinished call, kept whole so a test can assert the
// status and the reason reached the notification and not just that something fired.
type stubFinishedCall struct {
	runID  int
	status string
	detail string
}

func (n *stubNotifier) NotifyRefresh(_ context.Context, runID int, _ string) error {
	n.calls = append(n.calls, runID)
	if n.fail {
		return fmt.Errorf("notify boom")
	}
	return nil
}

func (n *stubNotifier) NotifyRunStarted(_ context.Context, runID int, _, _ string) error {
	n.started = append(n.started, runID)
	if n.fail {
		return fmt.Errorf("notify boom")
	}
	return nil
}

func (n *stubNotifier) NotifyRunFinished(_ context.Context, runID int, _, status, detail string) error {
	n.finished = append(n.finished, stubFinishedCall{runID: runID, status: status, detail: detail})
	if n.fail {
		return fmt.Errorf("notify boom")
	}
	return nil
}

// newAdminJobsTestAPI wires the admin operations onto a humatest API with the real auth
// middleware and an in-memory SQLite DB, injecting a stub queue + notifier so the
// force-start ("Run now") paths are exercisable end to end.
func newAdminJobsTestAPI(t *testing.T, q enqueuer, n notifier) (humatest.TestAPI, *ent.Client) {
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
	h := New(Deps{Auth: svc, Ent: client, Queue: q, Notifier: n})
	h.registerAdmin(api)
	return api, client
}

// seedSyncJob inserts the ack-gated finance.sync ScheduledJob for the force-start tests.
func seedSyncJob(t *testing.T, ctx context.Context, client *ent.Client) *ent.ScheduledJob {
	t.Helper()
	return client.ScheduledJob.Create().
		SetKey("finance.sync").
		SetName("Finance sync").
		SetStage(scheduledjob.StageScrape).
		SetSchedule("0 20 * * *").
		SetTimezone("UTC").
		SetRunner(scheduledjob.RunnerLocal).
		SetEnabled(true).
		SaveX(ctx)
}

// TestStartJobRun_AckGatedAwaitsApproval: force-starting an ack-gated job (finance.sync)
// creates the run directly in awaiting_ack with claimable_at NULL (a human still taps
// Approve), fires the refresh notification exactly once, and does NOT enqueue to the
// worker — so "Run now" exercises the same handshake the scheduler cron does.
func TestStartJobRun_AckGatedAwaitsApproval(t *testing.T) {
	q := &stubEnqueuer{configured: true}
	n := &stubNotifier{}
	api, client := newAdminJobsTestAPI(t, q, n)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	job := seedSyncJob(t, ctx, client)

	resp := api.Post(fmt.Sprintf("/api/admin/jobs/%d/runs", job.ID), map[string]any{}, cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("ack-gated start = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}

	run := client.JobRun.Query().Where(jobrun.HasJobWith(scheduledjob.IDEQ(job.ID))).OnlyX(ctx)
	if run.Status != jobrun.StatusAwaitingAck {
		t.Errorf("run status = %q, want awaiting_ack", run.Status)
	}
	if run.Trigger != jobrun.TriggerManual {
		t.Errorf("run trigger = %q, want manual", run.Trigger)
	}
	if run.ClaimableAt != nil {
		t.Errorf("claimable_at = %v, want nil (a human still approves)", run.ClaimableAt)
	}
	if len(n.calls) != 1 || n.calls[0] != run.ID {
		t.Errorf("notifier calls = %v, want exactly [%d]", n.calls, run.ID)
	}
	if len(q.enqueued) != 0 {
		t.Errorf("enqueued = %d, want 0 (an ack-gated run is never enqueued)", len(q.enqueued))
	}
}

// TestStartJobRun_AckGatedSecondReturns409: a second "Run now" while a run is already
// awaiting_ack is a 409 (idempotency), leaving exactly one run and one notification.
func TestStartJobRun_AckGatedSecondReturns409(t *testing.T) {
	q := &stubEnqueuer{configured: true}
	n := &stubNotifier{}
	api, client := newAdminJobsTestAPI(t, q, n)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	job := seedSyncJob(t, ctx, client)

	path := fmt.Sprintf("/api/admin/jobs/%d/runs", job.ID)
	if resp := api.Post(path, map[string]any{}, cookie); resp.Code != http.StatusOK {
		t.Fatalf("first start = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Post(path, map[string]any{}, cookie); resp.Code != http.StatusConflict {
		t.Fatalf("second start = %d, want 409 (already awaiting approval); body=%s", resp.Code, resp.Body.String())
	}
	if got := client.JobRun.Query().CountX(ctx); got != 1 {
		t.Errorf("runs = %d, want 1 (the second click must not create another)", got)
	}
	if len(n.calls) != 1 {
		t.Errorf("notifier calls = %d, want 1 (the 409 must not re-notify)", len(n.calls))
	}
}

// TestStartJobRun_NonAckGatedEnqueues: a non-ack-gated job (digest.scrape) keeps the
// unchanged queued+enqueue path — the run is created queued, enqueued once under the
// job key, and the notifier is never touched.
func TestStartJobRun_NonAckGatedEnqueues(t *testing.T) {
	q := &stubEnqueuer{configured: true}
	n := &stubNotifier{}
	api, client := newAdminJobsTestAPI(t, q, n)
	ctx := context.Background()
	cookie := sessionCookieFor(t, ctx, client, user.RoleAdmin)
	job := client.ScheduledJob.Create().
		SetKey("digest.scrape").
		SetName("Digest scrape").
		SetStage(scheduledjob.StageScrape).
		SetSchedule("30 17 * * *").
		SetTimezone("UTC").
		SetRunner(scheduledjob.RunnerServer).
		SetEnabled(true).
		SaveX(ctx)

	resp := api.Post(fmt.Sprintf("/api/admin/jobs/%d/runs", job.ID), map[string]any{}, cookie)
	if resp.Code != http.StatusOK {
		t.Fatalf("non-ack start = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var out struct {
		Started bool `json:"started"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Started {
		t.Errorf("started = false, want true (a run was enqueued)")
	}

	run := client.JobRun.Query().Where(jobrun.HasJobWith(scheduledjob.IDEQ(job.ID))).OnlyX(ctx)
	if run.Status != jobrun.StatusQueued {
		t.Errorf("run status = %q, want queued", run.Status)
	}
	if run.Trigger != jobrun.TriggerManual {
		t.Errorf("run trigger = %q, want manual", run.Trigger)
	}
	if len(q.enqueued) != 1 || q.enqueued[0].Type != "digest.scrape" || q.enqueued[0].JobRunID != run.ID {
		t.Errorf("enqueued = %+v, want one digest.scrape job carrying run %d", q.enqueued, run.ID)
	}
	if len(n.calls) != 0 {
		t.Errorf("notifier calls = %d, want 0 (a non-ack-gated job never notifies)", len(n.calls))
	}
}
