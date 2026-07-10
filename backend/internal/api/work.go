package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/artifact"
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/predicate"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/internal/auth"
	"github.com/alifyandra/portfolio-site/backend/internal/digest"
)

// The external-runner work API (ADR 0014). Two operations, both gated on a
// scope-only bearer token and NEVER on the admin/friend role: an external runner
// (Alif's laptop, or the future home finance-scraper) claims a claimable job's
// pending artifacts, does the LLM/compute step wherever it is signed in, and posts
// the result back. The topology is "runner pulls from AWS", so it needs zero
// inbound access. A bearer identity is invisible to requireAdmin/requireFriend, so
// a leaked runner token can reach only the work its scope names.

// workTags groups the work-API operations under one OpenAPI tag.
var workTags = []string{"work"}

const (
	// workLeaseTTL is how long a claim holds artifacts before the lease lapses and
	// they become reclaimable, so a runner that crashes mid-job does not strand work.
	workLeaseTTL = 10 * time.Minute
	// workGetPresignTTL bounds a presigned GET handed to a runner for an S3-backed
	// artifact. It is longer than the write presign: the runner may fetch several
	// inputs and process before finishing.
	workGetPresignTTL = 15 * time.Minute
	// workResultPrefix namespaces large runner-result objects in the assets bucket.
	workResultPrefix = "work-results/"
)

// requireBearer enforces the work-API gate: the request must carry a valid
// Authorization: Bearer token whose scope authorizes jobKey. It returns the
// resolved identity on success. It NEVER consults the admin/friend role and reads
// only the bearer context slot, so a runner token can reach the work API and
// nothing else. An empty scope authorizes nothing. See ADR 0014.
func requireBearer(ctx context.Context, jobKey string) (*auth.BearerIdentity, error) {
	id := auth.BearerFromContext(ctx)
	if id == nil {
		return nil, huma.Error401Unauthorized("a valid bearer token is required")
	}
	if !id.Allows(jobKey) {
		return nil, huma.Error403Forbidden("this token's scope does not authorize that job")
	}
	return id, nil
}

// claimableArtifacts is the predicate for the artifacts a claim may take for a job:
// they belong to a ScheduledJob with the given key whose runner is local or any (a
// server job is never claimable, so a scoped token still cannot pull server-only
// work), and they are either pending or hold a claim lease that has already lapsed
// (so a crashed runner's lease is reclaimable after workLeaseTTL).
func claimableArtifacts(jobKey string, now time.Time) predicate.Artifact {
	return artifact.And(
		artifact.HasJobWith(
			scheduledjob.KeyEQ(jobKey),
			scheduledjob.RunnerIn(scheduledjob.RunnerLocal, scheduledjob.RunnerAny),
		),
		artifact.Or(
			artifact.StatusEQ(artifact.StatusPending),
			artifact.And(
				artifact.StatusEQ(artifact.StatusClaimed),
				artifact.ExpiresAtNotNil(),
				artifact.ExpiresAtLTE(now),
			),
		),
	)
}

// --- claim ---

type claimInput struct {
	Job string `query:"job" required:"true" minLength:"1" doc:"ScheduledJob key to claim work for; must be in the token's scope"`
}

type claimedArtifactDTO struct {
	ID          int    `json:"id"`
	Label       string `json:"label"`
	ContentType string `json:"content_type"`
	SizeBytes   int    `json:"size_bytes"`
	// Content is the inline payload for small artifacts; empty when the payload lives
	// in S3 (fetch it from URL instead).
	Content string `json:"content,omitempty"`
	// URL is a presigned S3 GET for an S3-backed artifact; empty when the payload is
	// inline.
	URL string `json:"url,omitempty"`
}

type claimOutput struct {
	Body struct {
		Claimed         bool                 `json:"claimed" doc:"false when there was no runnable work for this job"`
		Job             string               `json:"job,omitempty"`
		JobRunID        int                  `json:"job_run_id,omitempty" doc:"the run to pass back to /complete"`
		Date            string               `json:"date,omitempty" doc:"the run's scheduled day (YYYY-MM-DD); the digest idempotency key"`
		LeaseTTLSeconds int                  `json:"lease_ttl_seconds,omitempty" doc:"how long the claim lease is held before the artifacts become reclaimable"`
		Artifacts       []claimedArtifactDTO `json:"artifacts,omitempty"`
	}
}

// --- complete ---

type completeInput struct {
	Body struct {
		Job         string `json:"job" minLength:"1" doc:"ScheduledJob key; must be in the token's scope"`
		JobRunID    int    `json:"job_run_id,omitempty" doc:"the run returned by /claim, to close"`
		ArtifactIDs []int  `json:"artifact_ids,omitempty" doc:"the claimed artifacts to mark done"`
		Status      string `json:"status,omitempty" enum:"completed,failed" doc:"terminal outcome; defaults to completed"`
		Error       string `json:"error,omitempty" doc:"failure reason when status is failed"`
		// Digest-style result. A dated result is written via the existing idempotent
		// Digest upsert; generic jobs with no dated output omit these.
		Date       string `json:"date,omitempty" doc:"YYYY-MM-DD idempotency key for the resulting Digest"`
		Content    string `json:"content,omitempty" doc:"inline result content (the digest markdown)"`
		ContentKey string `json:"content_key,omitempty" doc:"S3 key of a large result previously uploaded via the presigned PUT"`
		// UploadContentType, set with no inline content, requests a presigned S3 PUT
		// for a large result instead of finalizing the run.
		UploadContentType string `json:"upload_content_type,omitempty" doc:"request a presigned PUT for a large result of this MIME type"`
	}
}

type uploadTicketDTO struct {
	URL     string            `json:"url"`
	Key     string            `json:"key"`
	Method  string            `json:"method"`
	Headers map[string]string `json:"headers" doc:"headers to send verbatim on the PUT (Content-Type is bound into the signature)"`
}

type completeOutput struct {
	Body struct {
		Done bool `json:"done" doc:"true when the run was closed; false when an upload URL was returned instead"`
		// Upload is returned (Done=false) when the caller asked for a presigned PUT for
		// a large result; the caller uploads then calls complete again with content_key.
		Upload *uploadTicketDTO `json:"upload,omitempty"`
	}
}

func (h *Handler) registerWork(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "claim-work",
		Method:      http.MethodGet,
		Path:        "/api/work/claim",
		Summary:     "Claim a runnable job's pending artifacts (external runner)",
		Tags:        workTags,
		Security:    bearerAuthSecurity,
	}, h.claimWork)

	huma.Register(api, huma.Operation{
		OperationID: "complete-work",
		Method:      http.MethodPost,
		Path:        "/api/work/complete",
		Summary:     "Post back a claimed job's result and close the run (external runner)",
		Tags:        workTags,
		Security:    bearerAuthSecurity,
	}, h.completeWork)
}

// claimWork atomically hands one runnable run's artifacts to the caller. It picks
// the oldest claimable artifact for the job, resolves its producing run, and flips
// that run's claimable artifacts to claimed under a single conditional UPDATE
// (WHERE still pending/expired). Two concurrent claimers therefore never take the
// same artifacts: the losing UPDATE matches zero rows once the winner has committed
// (the rows are now claimed with a live lease), and the loser returns "no work".
func (h *Handler) claimWork(ctx context.Context, in *claimInput) (*claimOutput, error) {
	id, err := requireBearer(ctx, in.Job)
	if err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("work API is not available")
	}

	now := time.Now()
	lease := now.Add(workLeaseTTL)
	pred := claimableArtifacts(in.Job, now)

	out := &claimOutput{}

	tx, err := h.deps.Ent.Tx(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to start claim", err)
	}

	// Oldest claimable artifact -> its producing run is the run this claim targets.
	first, err := tx.Artifact.Query().
		Where(pred).
		WithProducedBy().
		Order(ent.Asc(artifact.FieldID)).
		First(ctx)
	if ent.IsNotFound(err) {
		_ = tx.Rollback()
		return out, nil // no runnable work: Claimed stays false
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, huma.Error500InternalServerError("failed to find claimable work", err)
	}
	run := first.Edges.ProducedBy
	if run == nil {
		_ = tx.Rollback()
		return nil, huma.Error500InternalServerError("claimable artifact has no producing run", nil)
	}

	// Atomic flip of this run's claimable artifacts. The predicate re-checked here is
	// the mutual-exclusion point: a concurrent claimer that committed first leaves
	// zero rows matching (they are claimed with a future lease), so n == 0 -> no work.
	n, err := tx.Artifact.Update().
		Where(pred, artifact.HasProducedByWith(jobrun.IDEQ(run.ID))).
		SetStatus(artifact.StatusClaimed).
		SetClaimedBy(id.Runner).
		SetClaimedAt(now).
		SetExpiresAt(lease).
		Save(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, huma.Error500InternalServerError("failed to claim work", err)
	}
	if n == 0 {
		_ = tx.Rollback()
		return out, nil // another runner won the race: no work
	}

	// Re-select exactly this run's now-claimed artifacts for this runner to build the
	// payload. A run's artifacts are claimed together, so this is precisely the set
	// just flipped.
	claimed, err := tx.Artifact.Query().
		Where(
			artifact.HasProducedByWith(jobrun.IDEQ(run.ID)),
			artifact.StatusEQ(artifact.StatusClaimed),
			artifact.ClaimedByEQ(id.Runner),
		).
		Order(ent.Asc(artifact.FieldID)).
		All(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, huma.Error500InternalServerError("failed to load claimed work", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, huma.Error500InternalServerError("failed to commit claim", err)
	}

	out.Body.Claimed = true
	out.Body.Job = in.Job
	out.Body.JobRunID = run.ID
	out.Body.Date = run.ScheduledFor.UTC().Format("2006-01-02")
	out.Body.LeaseTTLSeconds = int(workLeaseTTL.Seconds())
	for _, a := range claimed {
		dto := claimedArtifactDTO{
			ID:          a.ID,
			Label:       a.Label,
			ContentType: a.ContentType,
			SizeBytes:   a.SizeBytes,
		}
		switch a.Storage {
		case artifact.StorageInline:
			dto.Content = a.Content
		case artifact.StorageS3:
			if h.deps.Storage == nil {
				return nil, huma.Error503ServiceUnavailable("object storage is not available for an S3-backed artifact")
			}
			url, err := h.deps.Storage.PresignGetURL(ctx, a.S3Key, workGetPresignTTL)
			if err != nil {
				return nil, huma.Error500InternalServerError("failed to presign artifact download", err)
			}
			dto.URL = url
		}
		out.Body.Artifacts = append(out.Body.Artifacts, dto)
	}
	return out, nil
}

// completeWork writes a claimed run's result and closes it. Two shapes:
//
//   - Phase 1 (large result): the caller sends upload_content_type and no content.
//     We hand back a presigned S3 PUT (Content-Type bound into the signature exactly
//     as admin_uploads.go) and leave the run open; the caller uploads then calls
//     complete again with content_key.
//   - Finalize: write the dated Digest via the existing idempotent upsert, mark the
//     claimed artifacts done, and close the run with the runner's identity.
//
// A runner may only finalize artifacts it currently holds, UNLESS the lease has
// already expired. A stale double-complete is harmless: the Digest upsert and the
// done-marking are both idempotent, so re-running writes the same terminal state.
func (h *Handler) completeWork(ctx context.Context, in *completeInput) (*completeOutput, error) {
	id, err := requireBearer(ctx, in.Body.Job)
	if err != nil {
		return nil, err
	}
	if h.deps.Ent == nil {
		return nil, huma.Error503ServiceUnavailable("work API is not available")
	}
	now := time.Now()
	out := &completeOutput{}

	// Phase 1: hand back a presigned PUT for a large result if asked (and nothing was
	// sent inline yet). The run stays claimed; the runner uploads then re-calls with
	// content_key.
	if in.Body.UploadContentType != "" && in.Body.Content == "" && in.Body.ContentKey == "" {
		if h.deps.Storage == nil {
			return nil, huma.Error503ServiceUnavailable("object storage is not available")
		}
		ct := strings.ToLower(strings.TrimSpace(in.Body.UploadContentType))
		keyID, err := randomHex(16)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to generate upload key", err)
		}
		key := fmt.Sprintf("%s%d-%s", workResultPrefix, in.Body.JobRunID, keyID)
		url, err := h.deps.Storage.PresignPutURL(ctx, key, ct, uploadPresignTTL)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to presign upload", err)
		}
		out.Body.Done = false
		out.Body.Upload = &uploadTicketDTO{
			URL:     url,
			Key:     key,
			Method:  http.MethodPut,
			Headers: map[string]string{"Content-Type": ct},
		}
		return out, nil
	}

	// Ownership guard: reject an attempt to finalize an artifact that is currently
	// claimed by a DIFFERENT runner with a still-live lease. A lapsed lease, or an
	// artifact the caller itself holds, is fine (stale double-complete is harmless).
	if len(in.Body.ArtifactIDs) > 0 {
		arts, err := h.deps.Ent.Artifact.Query().Where(artifact.IDIn(in.Body.ArtifactIDs...)).All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load artifacts", err)
		}
		for _, a := range arts {
			leaseLive := a.ExpiresAt == nil || a.ExpiresAt.After(now)
			if a.Status == artifact.StatusClaimed && a.ClaimedBy != id.Runner && leaseLive {
				return nil, huma.Error409Conflict("one or more artifacts are claimed by another runner with a live lease")
			}
		}
	}

	// Resolve the result content: inline, or read back a large result uploaded to S3.
	content := in.Body.Content
	if content == "" && in.Body.ContentKey != "" {
		if h.deps.Storage == nil {
			return nil, huma.Error503ServiceUnavailable("object storage is not available")
		}
		data, err := h.deps.Storage.GetObject(ctx, in.Body.ContentKey)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to read uploaded result", err)
		}
		content = string(data)
	}

	failed := in.Body.Status == "failed"

	// Write the dated Digest via the existing idempotent upsert (digest.Persist
	// upserts a completed row and no-demotes a failed one), so a redelivered or
	// duplicated complete never corrupts a good day. Runs outside the tx below; it is
	// idempotent, so a later tx failure just re-persists the same row on retry.
	if in.Body.Date != "" {
		r := &digest.Result{Date: in.Body.Date}
		if failed {
			r.Status = digest.StatusFailed
			r.Error = in.Body.Error
		} else {
			r.Status = digest.StatusCompleted
			r.Content = content
		}
		if err := digest.Persist(ctx, h.deps.Ent, r); err != nil {
			return nil, huma.Error500InternalServerError("failed to persist result", err)
		}
	}

	// Mark the claimed artifacts done and attribute them to the closing run, then
	// close the run, in one transaction. Idempotent: re-running sets the same values.
	runStatus := jobrun.StatusSucceeded
	if failed {
		runStatus = jobrun.StatusFailed
	}
	tx, err := h.deps.Ent.Tx(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to start completion", err)
	}
	if len(in.Body.ArtifactIDs) > 0 {
		upd := tx.Artifact.Update().
			Where(artifact.IDIn(in.Body.ArtifactIDs...)).
			SetStatus(artifact.StatusDone)
		if in.Body.JobRunID != 0 {
			upd = upd.SetConsumedByID(in.Body.JobRunID)
		}
		if _, err := upd.Save(ctx); err != nil {
			_ = tx.Rollback()
			return nil, huma.Error500InternalServerError("failed to mark artifacts done", err)
		}
	}
	if in.Body.JobRunID != 0 {
		u := tx.JobRun.UpdateOneID(in.Body.JobRunID).
			SetStatus(runStatus).
			SetRunner(id.Runner).
			SetFinishedAt(now)
		if failed && in.Body.Error != "" {
			u = u.SetError(in.Body.Error)
		}
		if err := u.Exec(ctx); err != nil {
			_ = tx.Rollback()
			if ent.IsNotFound(err) {
				return nil, huma.Error404NotFound("job run not found")
			}
			return nil, huma.Error500InternalServerError("failed to close run", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, huma.Error500InternalServerError("failed to commit completion", err)
	}

	out.Body.Done = true
	return out, nil
}
