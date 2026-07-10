package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/danielgtaylor/huma/v2/humatest"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/artifact"
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

// newWorkTestAPI wires the work + admin + whatsapp operations onto a humatest API
// with the real auth middleware and an in-memory SQLite DB, so a request can carry
// a real bearer token and exercise the full resolve -> gate -> handler path.
func newWorkTestAPI(t *testing.T) (humatest.TestAPI, *auth.Service, *ent.Client) {
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
	h.registerWork(api)
	h.registerAdmin(api)
	h.registerWhatsApp(api)
	return api, svc, client
}

// mintToken creates an owning User and a scope-only bearer token, returning the raw
// token to put in an Authorization header.
func mintToken(t *testing.T, ctx context.Context, svc *auth.Service, client *ent.Client, runner string, scope []string) string {
	t.Helper()
	email := fmt.Sprintf("%s-%d@x.com", runner, time.Now().UnixNano())
	u := client.User.Create().SetEmail(email).SaveX(ctx)
	raw, _, err := svc.MintApiToken(ctx, u.ID, runner+" token", runner, scope, nil)
	if err != nil {
		t.Fatalf("mint token: %v", err)
	}
	return raw
}

// seedClaimable inserts a claimable ScheduledJob (runner local) with one JobRun and
// n pending inline Artifacts produced by that run, and returns the run id + artifact
// ids. The claim API keys artifacts off their owning job, so the artifacts' job edge
// is this job.
func seedClaimable(t *testing.T, ctx context.Context, client *ent.Client, jobKey string, runner scheduledjob.Runner, n int) (runID int, artIDs []int) {
	t.Helper()
	job := client.ScheduledJob.Create().
		SetKey(jobKey).
		SetName(jobKey).
		SetStage(scheduledjob.StageLlm).
		SetSchedule("0 0 * * *").
		SetTimezone("UTC").
		SetRunner(runner).
		SaveX(ctx)
	run := client.JobRun.Create().
		SetTrigger(jobrun.TriggerSchedule).
		SetScheduledFor(time.Now().UTC().Truncate(24 * time.Hour)).
		SetJobID(job.ID).
		SaveX(ctx)
	for i := 0; i < n; i++ {
		body := fmt.Sprintf("content-%d", i)
		a := client.Artifact.Create().
			SetLabel(fmt.Sprintf("source:%d", i)).
			SetStorage(artifact.StorageInline).
			SetContent(body).
			SetContentType("text/markdown").
			SetSizeBytes(len(body)).
			SetStatus(artifact.StatusPending).
			SetExpiresAt(time.Now().Add(7 * 24 * time.Hour)).
			SetJobID(job.ID).
			SetProducedByID(run.ID).
			SaveX(ctx)
		artIDs = append(artIDs, a.ID)
	}
	return run.ID, artIDs
}

type claimResp struct {
	Claimed   bool   `json:"claimed"`
	Job       string `json:"job"`
	JobRunID  int    `json:"job_run_id"`
	Date      string `json:"date"`
	Artifacts []struct {
		ID      int    `json:"id"`
		Content string `json:"content"`
	} `json:"artifacts"`
}

func bearer(raw string) string { return "Authorization: Bearer " + raw }

// --- gate / auth invariants ---

// TestWork_RequiresBearer: no Authorization header is 401 on both work operations.
func TestWork_RequiresBearer(t *testing.T) {
	api, _, _ := newWorkTestAPI(t)
	if resp := api.Get("/api/work/claim?job=test.job"); resp.Code != http.StatusUnauthorized {
		t.Errorf("anonymous claim = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Post("/api/work/complete", map[string]any{"job": "test.job"}); resp.Code != http.StatusUnauthorized {
		t.Errorf("anonymous complete = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
}

// TestWork_EmptyScopeDenies is the deny-by-default invariant end-to-end: a token
// with an empty scope is 403 on both operations even though it authenticates.
func TestWork_EmptyScopeDenies(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "laptop", []string{})

	if resp := api.Get("/api/work/claim?job=test.job", bearer(raw)); resp.Code != http.StatusForbidden {
		t.Errorf("empty-scope claim = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Post("/api/work/complete", map[string]any{"job": "test.job"}, bearer(raw)); resp.Code != http.StatusForbidden {
		t.Errorf("empty-scope complete = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
}

// TestWork_ScopeMismatchDenies: a token scoped to one job cannot claim another.
func TestWork_ScopeMismatchDenies(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "laptop", []string{"other.job"})
	if resp := api.Get("/api/work/claim?job=test.job", bearer(raw)); resp.Code != http.StatusForbidden {
		t.Errorf("scope-mismatch claim = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
}

// TestWork_RevokedTokenIs401: a revoked token no longer authenticates.
func TestWork_RevokedTokenIs401(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	email := fmt.Sprintf("laptop-%d@x.com", time.Now().UnixNano())
	u := client.User.Create().SetEmail(email).SaveX(ctx)
	raw, tok, err := svc.MintApiToken(ctx, u.ID, "laptop", "laptop", []string{"test.job"}, nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if err := svc.RevokeApiToken(ctx, tok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if resp := api.Get("/api/work/claim?job=test.job", bearer(raw)); resp.Code != http.StatusUnauthorized {
		t.Errorf("revoked claim = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
}

// TestWork_ExpiredTokenIs401: a token past its expiry no longer authenticates.
func TestWork_ExpiredTokenIs401(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	email := fmt.Sprintf("laptop-%d@x.com", time.Now().UnixNano())
	u := client.User.Create().SetEmail(email).SaveX(ctx)
	past := time.Now().Add(-time.Minute)
	raw, _, err := svc.MintApiToken(ctx, u.ID, "laptop", "laptop", []string{"test.job"}, &past)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if resp := api.Get("/api/work/claim?job=test.job", bearer(raw)); resp.Code != http.StatusUnauthorized {
		t.Errorf("expired claim = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
}

// TestBearer_CannotReachAdmin is the isolation invariant: a bearer token, however
// scoped, is invisible to requireAdmin (which reads the session-user slot), so the
// cookie-only admin console rejects it as anonymous (401).
func TestBearer_CannotReachAdmin(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	// A deliberately broad scope, to prove scope never buys admin access.
	raw := mintToken(t, ctx, svc, client, "laptop", []string{"admin", "*", "test.job"})
	if resp := api.Get("/api/admin/playlists", bearer(raw)); resp.Code != http.StatusUnauthorized {
		t.Errorf("bearer -> admin = %d, want 401 (bearer invisible to requireAdmin); body=%s", resp.Code, resp.Body.String())
	}
}

// TestBearer_CannotReachFriendTool: a bearer token cannot reach the friend-gated
// WhatsApp tool either (requireFriend also reads the session-user slot).
func TestBearer_CannotReachFriendTool(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "laptop", []string{"test.job"})
	if resp := api.Get("/api/wa/templates", bearer(raw)); resp.Code != http.StatusUnauthorized {
		t.Errorf("bearer -> friend tool = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
}

// --- claim / complete behavior ---

// TestClaim_NoWorkReturnsClaimedFalse: a valid scoped token with nothing to do gets
// a 200 with claimed=false (poller-friendly), not an error.
func TestClaim_NoWorkReturnsClaimedFalse(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "laptop", []string{"test.job"})
	resp := api.Get("/api/work/claim?job=test.job", bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("claim = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var got claimResp
	if err := json.Unmarshal(resp.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Claimed {
		t.Errorf("claimed=true with no work seeded")
	}
}

// TestClaim_ServerJobNotClaimable: a job whose runner is server is never handed to
// a runner, even one scoped to it.
func TestClaim_ServerJobNotClaimable(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	seedClaimable(t, ctx, client, "server.job", scheduledjob.RunnerServer, 2)
	raw := mintToken(t, ctx, svc, client, "laptop", []string{"server.job"})
	resp := api.Get("/api/work/claim?job=server.job", bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("claim = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var got claimResp
	_ = json.Unmarshal(resp.Body.Bytes(), &got)
	if got.Claimed {
		t.Errorf("server job was claimed; server-only work must never be handed out")
	}
}

// TestClaimThenComplete_HappyPath: claim hands back the pending artifacts and run
// context; complete writes the dated Digest, marks the artifacts done + consumed by
// the run, and closes the run succeeded.
func TestClaimThenComplete_HappyPath(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	runID, artIDs := seedClaimable(t, ctx, client, "test.job", scheduledjob.RunnerLocal, 2)
	raw := mintToken(t, ctx, svc, client, "laptop", []string{"test.job"})

	resp := api.Get("/api/work/claim?job=test.job", bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("claim = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var claim claimResp
	if err := json.Unmarshal(resp.Body.Bytes(), &claim); err != nil {
		t.Fatalf("decode claim: %v", err)
	}
	if !claim.Claimed || claim.JobRunID != runID || len(claim.Artifacts) != 2 {
		t.Fatalf("claim = %+v, want claimed run=%d with 2 artifacts", claim, runID)
	}
	if claim.Artifacts[0].Content == "" {
		t.Error("inline artifact content was not returned")
	}

	// The claim flipped the artifacts to claimed by this runner.
	for _, a := range client.Artifact.Query().Where(artifact.IDIn(artIDs...)).AllX(ctx) {
		if a.Status != artifact.StatusClaimed || a.ClaimedBy != "laptop" {
			t.Errorf("artifact %d status=%s claimed_by=%q, want claimed/laptop", a.ID, a.Status, a.ClaimedBy)
		}
	}

	date := time.Now().UTC().Format("2006-01-02")
	resp = api.Post("/api/work/complete", map[string]any{
		"job":          "test.job",
		"job_run_id":   runID,
		"artifact_ids": artIDs,
		"date":         date,
		"content":      "# Daily briefing\n\nbody",
	}, bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("complete = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}

	// Digest written via the idempotent upsert.
	d := client.Digest.Query().OnlyX(ctx)
	if d.Content != "# Daily briefing\n\nbody" {
		t.Errorf("digest content = %q, want the completed briefing", d.Content)
	}
	// Artifacts done + consumed by the run.
	for _, a := range client.Artifact.Query().Where(artifact.IDIn(artIDs...)).AllX(ctx) {
		if a.Status != artifact.StatusDone {
			t.Errorf("artifact %d status=%s, want done", a.ID, a.Status)
		}
	}
	// Run closed succeeded with the runner recorded.
	run := client.JobRun.GetX(ctx, runID)
	if run.Status != jobrun.StatusSucceeded || run.Runner != "laptop" || run.FinishedAt == nil {
		t.Errorf("run = status:%s runner:%q finished:%v, want succeeded/laptop/set", run.Status, run.Runner, run.FinishedAt)
	}
}

// TestComplete_OwnershipGuard: a runner cannot finalize artifacts that another
// runner currently holds under a live lease (409).
func TestComplete_OwnershipGuard(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	runID, artIDs := seedClaimable(t, ctx, client, "test.job", scheduledjob.RunnerLocal, 1)
	rawA := mintToken(t, ctx, svc, client, "laptop", []string{"test.job"})
	rawB := mintToken(t, ctx, svc, client, "home-finance", []string{"test.job"})

	// A claims (artifacts now held by "laptop" with a live lease).
	if resp := api.Get("/api/work/claim?job=test.job", bearer(rawA)); resp.Code != http.StatusOK {
		t.Fatalf("A claim = %d; body=%s", resp.Code, resp.Body.String())
	}
	// B tries to complete A's live claim -> 409.
	resp := api.Post("/api/work/complete", map[string]any{
		"job":          "test.job",
		"job_run_id":   runID,
		"artifact_ids": artIDs,
		"date":         time.Now().UTC().Format("2006-01-02"),
		"content":      "stolen",
	}, bearer(rawB))
	if resp.Code != http.StatusConflict {
		t.Fatalf("B stealing A's live claim = %d, want 409; body=%s", resp.Code, resp.Body.String())
	}
}

// TestComplete_StaleDoubleCompleteIsIdempotent: completing the same claim twice is
// harmless (the Digest upsert and done-marking are idempotent), so the second call
// still succeeds and leaves exactly one completed digest.
func TestComplete_StaleDoubleCompleteIsIdempotent(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	runID, artIDs := seedClaimable(t, ctx, client, "test.job", scheduledjob.RunnerLocal, 1)
	raw := mintToken(t, ctx, svc, client, "laptop", []string{"test.job"})

	if resp := api.Get("/api/work/claim?job=test.job", bearer(raw)); resp.Code != http.StatusOK {
		t.Fatalf("claim = %d; body=%s", resp.Code, resp.Body.String())
	}
	body := map[string]any{
		"job":          "test.job",
		"job_run_id":   runID,
		"artifact_ids": artIDs,
		"date":         time.Now().UTC().Format("2006-01-02"),
		"content":      "briefing",
	}
	if resp := api.Post("/api/work/complete", body, bearer(raw)); resp.Code != http.StatusOK {
		t.Fatalf("first complete = %d; body=%s", resp.Code, resp.Body.String())
	}
	if resp := api.Post("/api/work/complete", body, bearer(raw)); resp.Code != http.StatusOK {
		t.Fatalf("second (stale) complete = %d, want 200 (idempotent); body=%s", resp.Code, resp.Body.String())
	}
	if n := client.Digest.Query().CountX(ctx); n != 1 {
		t.Errorf("digest rows = %d, want exactly 1 after a double complete", n)
	}
}

// TestClaim_ConcurrentSingleWinner is the atomicity invariant: with several
// claimers racing for one runnable run, exactly one gets the artifacts and the rest
// get claimed=false. The conditional UPDATE is the mutual-exclusion point (a
// concurrent claimer sees zero pending rows once the winner commits), so an artifact
// is never handed to two runners.
func TestClaim_ConcurrentSingleWinner(t *testing.T) {
	api, svc, client := newWorkTestAPI(t)
	ctx := context.Background()
	_, artIDs := seedClaimable(t, ctx, client, "test.job", scheduledjob.RunnerLocal, 3)
	raw := mintToken(t, ctx, svc, client, "laptop", []string{"test.job"})

	const claimers = 5
	var wg sync.WaitGroup
	results := make([]claimResp, claimers)
	for i := 0; i < claimers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := api.Get("/api/work/claim?job=test.job", bearer(raw))
			if resp.Code != http.StatusOK {
				t.Errorf("claimer %d = %d; body=%s", i, resp.Code, resp.Body.String())
				return
			}
			_ = json.Unmarshal(resp.Body.Bytes(), &results[i])
		}(i)
	}
	wg.Wait()

	winners, totalHandedOut := 0, 0
	for _, r := range results {
		if r.Claimed {
			winners++
			totalHandedOut += len(r.Artifacts)
		}
	}
	if winners != 1 {
		t.Errorf("winners = %d, want exactly 1 (no double hand-out)", winners)
	}
	if totalHandedOut != len(artIDs) {
		t.Errorf("artifacts handed out = %d, want %d", totalHandedOut, len(artIDs))
	}
	// Every artifact ends up claimed exactly once, by the single winning runner.
	for _, a := range client.Artifact.Query().Where(artifact.IDIn(artIDs...)).AllX(ctx) {
		if a.Status != artifact.StatusClaimed || a.ClaimedBy != "laptop" {
			t.Errorf("artifact %d status=%s claimed_by=%q, want claimed/laptop", a.ID, a.Status, a.ClaimedBy)
		}
	}
}
