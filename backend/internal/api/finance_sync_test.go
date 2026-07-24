package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
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
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
)

const testAckToken = "test-ack-secret"

// newFinanceSyncTestAPI wires the finance sync seam (claim/ack/complete) onto a
// humatest API with the real auth middleware and an in-memory SQLite DB, with the
// finance-sync deps populated (a non-empty ack token, backfill years, overlap days).
func newFinanceSyncTestAPI(t *testing.T) (humatest.TestAPI, *auth.Service, *ent.Client) {
	return newFinanceSyncTestAPIWithToken(t, testAckToken)
}

// newFinanceSyncTestAPIWithToken is newFinanceSyncTestAPI with an explicit configured
// ack token, so a test can exercise the fail-closed (empty token) path.
func newFinanceSyncTestAPIWithToken(t *testing.T, ackToken string) (humatest.TestAPI, *auth.Service, *ent.Client) {
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
	h := New(Deps{
		Auth:                   svc,
		Ent:                    client,
		FinanceSyncAckToken:    ackToken,
		FinanceBackfillYears:   8,
		FinanceSyncOverlapDays: 7,
	})
	h.registerFinanceSync(api)
	return api, svc, client
}

// seedFinanceRun inserts the finance.sync ScheduledJob (once) and a JobRun in the
// given status, optionally with claimable_at set.
func seedFinanceRun(t *testing.T, ctx context.Context, client *ent.Client, status jobrun.Status, claimableAt *time.Time) *ent.JobRun {
	t.Helper()
	job, err := client.ScheduledJob.Query().Where(scheduledjob.KeyEQ("finance.sync")).Only(ctx)
	if ent.IsNotFound(err) {
		job = client.ScheduledJob.Create().
			SetKey("finance.sync").
			SetName("Finance sync").
			SetStage(scheduledjob.StageScrape).
			SetRunner(scheduledjob.RunnerLocal).
			SetSchedule("0 20 * * *").
			SetTimezone("UTC").
			SaveX(ctx)
	} else if err != nil {
		t.Fatalf("load finance job: %v", err)
	}
	// Give each seeded run a distinct scheduled_for (offset by the current run count)
	// so seeding several runs in one test does not collide on the unique
	// (job, scheduled_for) index.
	n := client.JobRun.Query().CountX(ctx)
	c := client.JobRun.Create().
		SetTrigger(jobrun.TriggerSchedule).
		SetScheduledFor(time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -n)).
		SetJobID(job.ID).
		SetStatus(status)
	if claimableAt != nil {
		c.SetClaimableAt(*claimableAt)
	}
	return c.SaveX(ctx)
}

func seedAccount(t *testing.T, ctx context.Context, client *ent.Client, name string, watermark *time.Time) {
	t.Helper()
	c := client.Account.Create().
		SetSource("commbank").
		SetName(name).
		SetType(account.TypeEveryday).
		SetClass(account.ClassAsset)
	if watermark != nil {
		c.SetPostedWatermark(*watermark)
	}
	c.SaveX(ctx)
}

// TestFinanceSyncClaim_RequiresScope: no bearer is 401; a token without finance.sync
// is 403.
func TestFinanceSyncClaim_RequiresScope(t *testing.T) {
	api, svc, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()

	if resp := api.Get("/api/finance/sync/claim"); resp.Code != http.StatusUnauthorized {
		t.Errorf("anonymous claim = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"other.job"})
	if resp := api.Get("/api/finance/sync/claim", bearer(raw)); resp.Code != http.StatusForbidden {
		t.Errorf("wrong-scope claim = %d, want 403; body=%s", resp.Code, resp.Body.String())
	}
}

// TestFinanceSyncClaim_NoRunReturnsNotClaimed: with no claimable run, claim returns
// claimed:false (200).
func TestFinanceSyncClaim_NoRunReturnsNotClaimed(t *testing.T) {
	api, svc, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"finance.sync"})

	resp := api.Get("/api/finance/sync/claim", bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("claim = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var out struct {
		Claimed bool `json:"claimed"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Claimed {
		t.Errorf("claimed = true, want false (no claimable run)")
	}
}

// TestFinanceSyncAck_WrongTokenDenies: a wrong or empty token is 401; the correct
// token flips an awaiting_ack run to claimable (claimable_at set), leaving it
// awaiting_ack (not a status change).
func TestFinanceSyncAck_WrongTokenDenies(t *testing.T) {
	api, _, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()
	run := seedFinanceRun(t, ctx, client, jobrun.StatusAwaitingAck, nil)

	ackURL := fmt.Sprintf("/api/finance/sync/ack?run_id=%d&token=wrong", run.ID)
	if resp := api.Post(ackURL, map[string]any{}); resp.Code != http.StatusUnauthorized {
		t.Errorf("wrong-token ack = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
	// Empty token is also denied (the configured token is non-empty in this test).
	emptyURL := fmt.Sprintf("/api/finance/sync/ack?run_id=%d&token=", run.ID)
	if resp := api.Post(emptyURL, map[string]any{}); resp.Code != http.StatusUnauthorized {
		t.Errorf("empty-token ack = %d, want 401; body=%s", resp.Code, resp.Body.String())
	}
	// The run is untouched: still awaiting_ack, no claimable_at.
	after := client.JobRun.GetX(ctx, run.ID)
	if after.Status != jobrun.StatusAwaitingAck || after.ClaimableAt != nil {
		t.Errorf("after denied ack: status=%q claimable_at=%v, want awaiting_ack/nil", after.Status, after.ClaimableAt)
	}
}

// TestFinanceSyncAck_CorrectTokenFlips: the correct token sets claimable_at and is
// idempotent (a second ack succeeds).
func TestFinanceSyncAck_CorrectTokenFlips(t *testing.T) {
	api, _, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()
	run := seedFinanceRun(t, ctx, client, jobrun.StatusAwaitingAck, nil)

	ackURL := fmt.Sprintf("/api/finance/sync/ack?run_id=%d&token=%s", run.ID, testAckToken)
	if resp := api.Post(ackURL, map[string]any{}); resp.Code != http.StatusOK {
		t.Fatalf("ack = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	after := client.JobRun.GetX(ctx, run.ID)
	if after.ClaimableAt == nil {
		t.Errorf("claimable_at nil after ack, want it set")
	}
	if after.Status != jobrun.StatusAwaitingAck {
		t.Errorf("status = %q after ack, want awaiting_ack (ack is not a status change)", after.Status)
	}
	// Idempotent: a second ack still succeeds.
	if resp := api.Post(ackURL, map[string]any{}); resp.Code != http.StatusOK {
		t.Errorf("second ack = %d, want 200 (idempotent)", resp.Code)
	}
}

// TestFinanceSyncAck_UnsetTokenFailsClosed: when NO ack token is configured the
// endpoint is closed — even an empty presented token is 401 (never let ""=="" match),
// so an unconfigured prod cannot be acked into a claimable state.
func TestFinanceSyncAck_UnsetTokenFailsClosed(t *testing.T) {
	api, _, client := newFinanceSyncTestAPIWithToken(t, "") // no configured token
	ctx := context.Background()
	run := seedFinanceRun(t, ctx, client, jobrun.StatusAwaitingAck, nil)

	// Empty presented token.
	emptyURL := fmt.Sprintf("/api/finance/sync/ack?run_id=%d&token=", run.ID)
	if resp := api.Post(emptyURL, map[string]any{}); resp.Code != http.StatusUnauthorized {
		t.Errorf("empty-token ack with unset secret = %d, want 401 (fail closed); body=%s", resp.Code, resp.Body.String())
	}
	// Any non-empty token is likewise rejected.
	anyURL := fmt.Sprintf("/api/finance/sync/ack?run_id=%d&token=whatever", run.ID)
	if resp := api.Post(anyURL, map[string]any{}); resp.Code != http.StatusUnauthorized {
		t.Errorf("any-token ack with unset secret = %d, want 401 (fail closed)", resp.Code)
	}
	// The run is untouched.
	after := client.JobRun.GetX(ctx, run.ID)
	if after.Status != jobrun.StatusAwaitingAck || after.ClaimableAt != nil {
		t.Errorf("after fail-closed ack: status=%q claimable_at=%v, want awaiting_ack/nil", after.Status, after.ClaimableAt)
	}
}

// TestFinanceSyncAck_UnknownRun: acking a run id that does not exist is 404.
func TestFinanceSyncAck_UnknownRun(t *testing.T) {
	api, _, _ := newFinanceSyncTestAPI(t)
	ackURL := fmt.Sprintf("/api/finance/sync/ack?run_id=99999&token=%s", testAckToken)
	if resp := api.Post(ackURL, map[string]any{}); resp.Code != http.StatusNotFound {
		t.Errorf("ack unknown run = %d, want 404; body=%s", resp.Code, resp.Body.String())
	}
}

// TestFinanceSyncClaim_LeasesAndComputesWindows: a claimable run is leased (marked
// running) and claim returns one window per known account of the source — a
// nil-watermark account is a backfill window, a set-watermark account is an
// incremental (from = watermark - overlap) window.
func TestFinanceSyncClaim_LeasesAndComputesWindows(t *testing.T) {
	api, svc, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"finance.sync"})

	now := time.Now()
	run := seedFinanceRun(t, ctx, client, jobrun.StatusAwaitingAck, &now) // already claimable

	// Two accounts: one never synced (backfill), one with a watermark (incremental).
	seedAccount(t, ctx, client, "Smart Access", nil)
	wm := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -30)
	seedAccount(t, ctx, client, "NetBank Saver", &wm)

	resp := api.Get("/api/finance/sync/claim", bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("claim = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var out struct {
		Claimed bool `json:"claimed"`
		RunID   int  `json:"run_id"`
		Windows []struct {
			Account  string `json:"account"`
			From     string `json:"from"`
			To       string `json:"to"`
			Backfill bool   `json:"backfill"`
		} `json:"windows"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.Body.String())
	}
	if !out.Claimed || out.RunID != run.ID {
		t.Fatalf("claim out = %+v, want claimed with run %d", out, run.ID)
	}
	if len(out.Windows) != 2 {
		t.Fatalf("windows = %d, want 2 (one per account)", len(out.Windows))
	}
	byAcct := map[string]struct {
		From, To string
		Backfill bool
	}{}
	for _, w := range out.Windows {
		byAcct[w.Account] = struct {
			From, To string
			Backfill bool
		}{w.From, w.To, w.Backfill}
	}
	if w, ok := byAcct["Smart Access"]; !ok || !w.Backfill {
		t.Errorf("Smart Access window = %+v, want backfill=true", w)
	}
	// NetBank Saver: incremental, from = watermark - 7d overlap.
	wantFrom := wm.AddDate(0, 0, -7).Format("2006-01-02")
	if w, ok := byAcct["NetBank Saver"]; !ok || w.Backfill || w.From != wantFrom {
		t.Errorf("NetBank Saver window = %+v, want incremental from %s", w, wantFrom)
	}

	// The run was leased: awaiting_ack -> running.
	after := client.JobRun.GetX(ctx, run.ID)
	if after.Status != jobrun.StatusRunning {
		t.Errorf("run status = %q after claim, want running (leased)", after.Status)
	}

	// A second claim finds nothing (the only run is now running, not claimable).
	resp2 := api.Get("/api/finance/sync/claim", bearer(raw))
	var out2 struct {
		Claimed bool `json:"claimed"`
	}
	_ = json.Unmarshal(resp2.Body.Bytes(), &out2)
	if out2.Claimed {
		t.Errorf("second claim = claimed true, want false (run already leased)")
	}
}

// TestFinanceSyncClaim_NoAccountsEmptyWindows: a claimable run with zero known
// accounts of the source is still claimed, with an empty windows array (the source
// discovers and backfills on its first run).
func TestFinanceSyncClaim_NoAccountsEmptyWindows(t *testing.T) {
	api, svc, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"finance.sync"})
	now := time.Now()
	seedFinanceRun(t, ctx, client, jobrun.StatusAwaitingAck, &now)

	resp := api.Get("/api/finance/sync/claim", bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("claim = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	var out struct {
		Claimed bool               `json:"claimed"`
		Windows []financeWindowDTO `json:"windows"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Claimed || len(out.Windows) != 0 {
		t.Errorf("claim out = claimed:%v windows:%d, want claimed with 0 windows", out.Claimed, len(out.Windows))
	}
}

// TestFinanceSyncComplete: a claimed (running) run is closed succeeded; the seam
// enforces scope, run_id presence, valid status, and is idempotent.
func TestFinanceSyncComplete(t *testing.T) {
	api, svc, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"finance.sync"})
	run := seedFinanceRun(t, ctx, client, jobrun.StatusRunning, nil)

	// No scope -> 401.
	if resp := api.Post("/api/finance/sync/complete", map[string]any{"run_id": run.ID, "status": "succeeded"}); resp.Code != http.StatusUnauthorized {
		t.Errorf("anonymous complete = %d, want 401", resp.Code)
	}
	// Missing run_id -> 422.
	if resp := api.Post("/api/finance/sync/complete", map[string]any{"status": "succeeded"}, bearer(raw)); resp.Code != http.StatusUnprocessableEntity {
		t.Errorf("complete without run_id = %d, want 422; body=%s", resp.Code, resp.Body.String())
	}
	// Happy path -> succeeded + finished_at.
	resp := api.Post("/api/finance/sync/complete", map[string]any{"run_id": run.ID, "status": "succeeded"}, bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("complete = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	after := client.JobRun.GetX(ctx, run.ID)
	if after.Status != jobrun.StatusSucceeded || after.FinishedAt == nil {
		t.Errorf("after complete: status=%q finished_at=%v, want succeeded/set", after.Status, after.FinishedAt)
	}
	// Idempotent re-complete stays 200.
	if resp := api.Post("/api/finance/sync/complete", map[string]any{"run_id": run.ID, "status": "succeeded"}, bearer(raw)); resp.Code != http.StatusOK {
		t.Errorf("re-complete = %d, want 200 (idempotent)", resp.Code)
	}
}

// TestFinanceSyncComplete_RejectsUnclaimed: completing a run that is not `running`
// (here still awaiting_ack, i.e. never claimed) is a 409 and mutates nothing, so an
// unclaimed or reaper-failed run cannot be silently flipped to succeeded.
func TestFinanceSyncComplete_RejectsUnclaimed(t *testing.T) {
	api, svc, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"finance.sync"})
	run := seedFinanceRun(t, ctx, client, jobrun.StatusAwaitingAck, nil)

	resp := api.Post("/api/finance/sync/complete", map[string]any{"run_id": run.ID, "status": "succeeded"}, bearer(raw))
	if resp.Code != http.StatusConflict {
		t.Fatalf("complete unclaimed run = %d, want 409; body=%s", resp.Code, resp.Body.String())
	}
	if after := client.JobRun.GetX(ctx, run.ID); after.Status != jobrun.StatusAwaitingAck || after.FinishedAt != nil {
		t.Errorf("after rejected complete: status=%q finished_at=%v, want awaiting_ack/nil (untouched)", after.Status, after.FinishedAt)
	}

	// A run failed by the reaper cannot be flipped to succeeded either (other terminal).
	failed := seedFinanceRun(t, ctx, client, jobrun.StatusFailed, nil)
	if resp := api.Post("/api/finance/sync/complete", map[string]any{"run_id": failed.ID, "status": "succeeded"}, bearer(raw)); resp.Code != http.StatusConflict {
		t.Errorf("flip failed->succeeded = %d, want 409", resp.Code)
	}
}

// TestFinanceSyncComplete_Failed marks a run failed and records it.
func TestFinanceSyncComplete_Failed(t *testing.T) {
	api, svc, client := newFinanceSyncTestAPI(t)
	ctx := context.Background()
	raw := mintToken(t, ctx, svc, client, "home-finance", []string{"finance.sync"})
	run := seedFinanceRun(t, ctx, client, jobrun.StatusRunning, nil)

	resp := api.Post("/api/finance/sync/complete", map[string]any{"run_id": run.ID, "status": "failed"}, bearer(raw))
	if resp.Code != http.StatusOK {
		t.Fatalf("complete failed = %d, want 200; body=%s", resp.Code, resp.Body.String())
	}
	if after := client.JobRun.GetX(ctx, run.ID); after.Status != jobrun.StatusFailed {
		t.Errorf("status = %q, want failed", after.Status)
	}
}
