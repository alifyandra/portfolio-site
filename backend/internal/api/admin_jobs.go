package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/apitoken"
	"github.com/alifyandra/portfolio-site/backend/ent/jobrun"
	"github.com/alifyandra/portfolio-site/backend/ent/scheduledjob"
	"github.com/alifyandra/portfolio-site/backend/internal/jobs"
	"github.com/alifyandra/portfolio-site/backend/internal/queue"
)

// The Jobs section of the Admin Console (ADR 0014, phase P6): admin-only control
// over the ScheduledJob registry the in-process scheduler drives. Every operation
// is cookie-auth + requireAdmin as the first line; the frontend gate is UX only.
// Force-start reuses the same enqueue envelope the scheduler emits, and the token
// endpoints reuse P5's auth.MintApiToken/RevokeApiToken (scope-only bearer creds,
// never admin) rather than reimplementing them.

// formatTimePtr renders a nillable time as an HTTP date, or "" when null, matching
// the string-time convention of the other admin DTOs.
func formatTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(http.TimeFormat)
}

// JobDTO is the frontend-facing shape of a ScheduledJob row.
type JobDTO struct {
	ID         int    `json:"id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Stage      string `json:"stage"`
	Enabled    bool   `json:"enabled"`
	Schedule   string `json:"schedule"`
	Timezone   string `json:"timezone"`
	Runner     string `json:"runner"`
	LastRunAt  string `json:"last_run_at,omitempty"`
	NextRunAt  string `json:"next_run_at,omitempty"`
	LastStatus string `json:"last_status,omitempty" doc:"status of the most recent run, empty when the job has never run"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

func toJobDTO(j *ent.ScheduledJob, lastStatus string) JobDTO {
	return JobDTO{
		ID:         j.ID,
		Key:        j.Key,
		Name:       j.Name,
		Stage:      string(j.Stage),
		Enabled:    j.Enabled,
		Schedule:   j.Schedule,
		Timezone:   j.Timezone,
		Runner:     string(j.Runner),
		LastRunAt:  formatTimePtr(j.LastRunAt),
		NextRunAt:  formatTimePtr(j.NextRunAt),
		LastStatus: lastStatus,
		CreatedAt:  j.CreatedAt.UTC().Format(http.TimeFormat),
		UpdatedAt:  j.UpdatedAt.UTC().Format(http.TimeFormat),
	}
}

// JobRunDTO is the frontend-facing shape of one JobRun row (run history).
type JobRunDTO struct {
	ID              int    `json:"id"`
	Status          string `json:"status"`
	Trigger         string `json:"trigger"`
	Runner          string `json:"runner,omitempty"`
	ScheduledFor    string `json:"scheduled_for"`
	StartedAt       string `json:"started_at,omitempty"`
	FinishedAt      string `json:"finished_at,omitempty"`
	DurationSeconds int    `json:"duration_seconds,omitempty" doc:"finished_at - started_at, when both are set"`
	Error           string `json:"error,omitempty"`
	CreatedAt       string `json:"created_at"`
}

func toJobRunDTO(r *ent.JobRun) JobRunDTO {
	dto := JobRunDTO{
		ID:           r.ID,
		Status:       string(r.Status),
		Trigger:      string(r.Trigger),
		Runner:       r.Runner,
		ScheduledFor: r.ScheduledFor.UTC().Format(http.TimeFormat),
		StartedAt:    formatTimePtr(r.StartedAt),
		FinishedAt:   formatTimePtr(r.FinishedAt),
		Error:        r.Error,
		CreatedAt:    r.CreatedAt.UTC().Format(http.TimeFormat),
	}
	if r.StartedAt != nil && r.FinishedAt != nil {
		if d := int(r.FinishedAt.Sub(*r.StartedAt).Seconds()); d > 0 {
			dto.DurationSeconds = d
		}
	}
	return dto
}

// ApiTokenDTO is the frontend-facing shape of an ApiToken. It never carries the
// raw token or its hash (the hash field is Sensitive, so it is stripped anyway);
// the raw token is returned exactly once by the mint endpoint.
type ApiTokenDTO struct {
	ID         int      `json:"id"`
	Name       string   `json:"name"`
	Runner     string   `json:"runner"`
	Scope      []string `json:"scope"`
	LastUsedAt string   `json:"last_used_at,omitempty"`
	ExpiresAt  string   `json:"expires_at,omitempty"`
	CreatedAt  string   `json:"created_at"`
}

func toApiTokenDTO(t *ent.ApiToken) ApiTokenDTO {
	scope := t.Scope
	if scope == nil {
		scope = []string{}
	}
	return ApiTokenDTO{
		ID:         t.ID,
		Name:       t.Name,
		Runner:     t.Runner,
		Scope:      scope,
		LastUsedAt: formatTimePtr(t.LastUsedAt),
		ExpiresAt:  formatTimePtr(t.ExpiresAt),
		CreatedAt:  t.CreatedAt.UTC().Format(http.TimeFormat),
	}
}

// latestRunStatus returns the status of a job's most recent run, or "" when it has
// never run. Called once per job in the list (a handful of rows), so an N+1 here is
// cheaper than a join and keeps the list handler simple.
func (h *Handler) latestRunStatus(ctx context.Context, jobID int) string {
	run, err := h.deps.Ent.JobRun.Query().
		Where(jobrun.HasJobWith(scheduledjob.IDEQ(jobID))).
		Order(ent.Desc(jobrun.FieldCreatedAt)).
		First(ctx)
	if err != nil {
		return ""
	}
	return string(run.Status)
}

type listJobsOutput struct {
	Body struct {
		Jobs []JobDTO `json:"jobs"`
	}
}

// JobKindDTO is the frontend-facing shape of one registrable job kind (jobs.Kind). The
// console renders these as the "Add job" dropdown so key and stage are picked from this
// list, never typed.
type JobKindDTO struct {
	Key             string `json:"key"`
	Name            string `json:"name"`
	Stage           string `json:"stage"`
	DefaultSchedule string `json:"default_schedule"`
	DefaultTimezone string `json:"default_timezone"`
	Description     string `json:"description"`
}

func toJobKindDTO(k jobs.Kind) JobKindDTO {
	return JobKindDTO{
		Key:             k.Key,
		Name:            k.Name,
		Stage:           k.Stage,
		DefaultSchedule: k.DefaultSchedule,
		DefaultTimezone: k.DefaultTimezone,
		Description:     k.Description,
	}
}

type listJobKindsOutput struct {
	Body struct {
		Kinds []JobKindDTO `json:"kinds"`
	}
}

type jobOutput struct {
	Body JobDTO
}

type jobIDInput struct {
	ID int `path:"id" doc:"ScheduledJob DB id"`
}

type createJobInput struct {
	Body struct {
		Key      string                 `json:"key" minLength:"1" doc:"Stable machine key from the job-kinds registry, e.g. \"digest.scrape\". Must be one the worker can dispatch; the stage is derived from it"`
		Name     string                 `json:"name" minLength:"1" doc:"Human label shown in the /admin Jobs list"`
		Schedule string                 `json:"schedule" minLength:"1" doc:"Standard cron expression (robfig/cron form) evaluated in the timezone"`
		Timezone string                 `json:"timezone,omitempty" doc:"IANA timezone name the cron is evaluated in (e.g. UTC, Australia/Melbourne); defaults to UTC"`
		Runner   string                 `json:"runner,omitempty" enum:"server,local,any" doc:"Where the job may run: server (on-box worker), local (external runner), or any; defaults to server"`
		Config   map[string]interface{} `json:"config,omitempty" doc:"Free-form per-job settings read at run time (e.g. source filters, model id); the shape is job-specific"`
		Enabled  bool                   `json:"enabled,omitempty" doc:"Whether the ticker fires this job; defaults to false so a newly registered job stays dormant until an admin turns it on"`
	}
}

type updateJobInput struct {
	ID   int `path:"id" doc:"ScheduledJob DB id"`
	Body struct {
		Enabled  *bool   `json:"enabled,omitempty" doc:"Enable or disable the job; the ticker only fires enabled jobs"`
		Schedule *string `json:"schedule,omitempty" doc:"Standard cron expression (robfig/cron form) evaluated in the timezone"`
		Timezone *string `json:"timezone,omitempty" doc:"IANA timezone name the cron is evaluated in (e.g. UTC, Australia/Melbourne)"`
		Runner   *string `json:"runner,omitempty" enum:"server,local,any" doc:"Where the job may run: server (on-box worker), local (external runner), or any"`
	}
}

type listJobRunsInput struct {
	ID     int `path:"id" doc:"ScheduledJob DB id"`
	Limit  int `query:"limit" default:"20" minimum:"1" maximum:"100" doc:"Max runs to return"`
	Offset int `query:"offset" default:"0" minimum:"0" doc:"Runs to skip for paging"`
}

type listJobRunsOutput struct {
	Body struct {
		Runs  []JobRunDTO `json:"runs"`
		Total int         `json:"total" doc:"total runs for this job, for paging"`
	}
}

type startJobRunOutput struct {
	Body struct {
		Started bool       `json:"started" doc:"true when a run was enqueued; false when no worker is running to pick it up"`
		Message string     `json:"message,omitempty" doc:"why a run was not started (e.g. no worker configured)"`
		Run     *JobRunDTO `json:"run,omitempty" doc:"the queued run when started is true"`
	}
}

func (h *Handler) registerAdminJobs(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-jobs",
		Method:      http.MethodGet,
		Path:        "/api/admin/jobs",
		Summary:     "List the scheduled jobs",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listJobsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		rows, err := h.deps.Ent.ScheduledJob.Query().Order(ent.Asc(scheduledjob.FieldKey)).All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list jobs", err)
		}
		out := &listJobsOutput{}
		out.Body.Jobs = make([]JobDTO, 0, len(rows))
		for _, j := range rows {
			out.Body.Jobs = append(out.Body.Jobs, toJobDTO(j, h.latestRunStatus(ctx, j.ID)))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-job-kinds",
		Method:      http.MethodGet,
		Path:        "/api/admin/job-kinds",
		Summary:     "List the registrable job kinds the worker can dispatch",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listJobKindsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		kinds := jobs.Kinds()
		out := &listJobKindsOutput{}
		out.Body.Kinds = make([]JobKindDTO, 0, len(kinds))
		for _, k := range kinds {
			out.Body.Kinds = append(out.Body.Kinds, toJobKindDTO(k))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-job",
		Method:        http.MethodPost,
		Path:          "/api/admin/jobs",
		Summary:       "Register a new scheduled job",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createJobInput) (*jobOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}

		// The key must name a job the worker can dispatch; otherwise the row would sit
		// enqueued forever (errNoHandler). The registry is the source of truth, and the
		// stage is derived from it rather than trusted from the client.
		kind, ok := jobs.LookupKind(strings.TrimSpace(in.Body.Key))
		if !ok {
			return nil, huma.Error422UnprocessableEntity("unknown job key: must be one of the registered job kinds (see GET /api/admin/job-kinds)")
		}
		if err := jobs.ValidateCron(in.Body.Schedule); err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid cron schedule: " + err.Error())
		}
		tz := strings.TrimSpace(in.Body.Timezone)
		if tz == "" {
			tz = "UTC"
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid timezone: " + err.Error())
		}
		runner := strings.TrimSpace(in.Body.Runner)
		if runner == "" {
			runner = string(scheduledjob.RunnerServer)
		}
		if runner != string(scheduledjob.RunnerServer) && runner != string(scheduledjob.RunnerLocal) && runner != string(scheduledjob.RunnerAny) {
			return nil, huma.Error422UnprocessableEntity("runner must be server, local or any")
		}

		create := h.deps.Ent.ScheduledJob.Create().
			SetKey(kind.Key).
			SetName(strings.TrimSpace(in.Body.Name)).
			SetStage(scheduledjob.Stage(kind.Stage)).
			SetSchedule(in.Body.Schedule).
			SetTimezone(tz).
			SetRunner(scheduledjob.Runner(runner)).
			SetEnabled(in.Body.Enabled)
		if len(in.Body.Config) > 0 {
			create.SetConfig(in.Body.Config)
		}
		// Populate next_run_at eagerly when the job starts enabled, so the console shows
		// a real "Next run" the instant it is created rather than "—" until the first
		// tick. The value is a future instant from the same parser the ticker uses (see
		// jobs.NextRun), so it never fires an immediate backfill. A disabled job is left
		// null (no next run). Cron and timezone are already validated above.
		if in.Body.Enabled {
			if next, err := jobs.NextRun(in.Body.Schedule, tz, time.Now()); err == nil {
				create.SetNextRunAt(next)
			}
		}
		saved, err := create.Save(ctx)
		if ent.IsConstraintError(err) {
			// The key column is unique; a duplicate is a client error, not a 500.
			return nil, huma.Error409Conflict("a job with that key already exists")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to create job", err)
		}
		return &jobOutput{Body: toJobDTO(saved, h.latestRunStatus(ctx, saved.ID))}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "update-job",
		Method:      http.MethodPatch,
		Path:        "/api/admin/jobs/{id}",
		Summary:     "Toggle a job or edit its schedule, timezone and runner",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *updateJobInput) (*jobOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		job, err := h.deps.Ent.ScheduledJob.Get(ctx, in.ID)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("job not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load job", err)
		}

		upd := job.Update()

		// Track the effective schedule/timezone/enabled after this patch (a provided
		// value, else the existing one) so next_run_at can be recomputed from the final
		// state below.
		finalSchedule := job.Schedule
		finalTimezone := job.Timezone
		finalEnabled := job.Enabled

		scheduleChanged := false

		if in.Body.Schedule != nil {
			if err := jobs.ValidateCron(*in.Body.Schedule); err != nil {
				return nil, huma.Error422UnprocessableEntity("invalid cron schedule: " + err.Error())
			}
			upd.SetSchedule(*in.Body.Schedule)
			finalSchedule = *in.Body.Schedule
			scheduleChanged = true
		}
		if in.Body.Timezone != nil {
			tz := strings.TrimSpace(*in.Body.Timezone)
			if tz == "" {
				tz = "UTC"
			}
			if _, err := time.LoadLocation(tz); err != nil {
				return nil, huma.Error422UnprocessableEntity("invalid timezone: " + err.Error())
			}
			upd.SetTimezone(tz)
			finalTimezone = tz
			scheduleChanged = true
		}
		if in.Body.Runner != nil {
			r := *in.Body.Runner
			if r != string(scheduledjob.RunnerServer) && r != string(scheduledjob.RunnerLocal) && r != string(scheduledjob.RunnerAny) {
				return nil, huma.Error422UnprocessableEntity("runner must be server, local or any")
			}
			upd.SetRunner(scheduledjob.Runner(r))
		}
		if in.Body.Enabled != nil {
			upd.SetEnabled(*in.Body.Enabled)
			finalEnabled = *in.Body.Enabled
		}

		// Keep next_run_at consistent with the final state:
		//   - disabled  -> no next run, so clear it;
		//   - enabled   -> ensure a fresh future pointer, but only recompute when it
		//                  would actually change: the schedule/timezone changed, the job
		//                  is transitioning to enabled, or it has no pointer yet.
		// This is what fixes the console's "Next run shows —" after a disable->enable
		// toggle (the value is written now, not a tick later). jobs.NextRun reuses the
		// ticker's parser, so the instant matches what evaluate() would set and never
		// double-fires. A patch that touches only the runner leaves an enabled job's
		// existing pointer alone, so a run already due (but not yet ticked) is not skipped.
		recompute := scheduleChanged || (in.Body.Enabled != nil && *in.Body.Enabled) || job.NextRunAt == nil
		if !finalEnabled {
			upd.ClearNextRunAt()
		} else if recompute {
			if next, err := jobs.NextRun(finalSchedule, finalTimezone, time.Now()); err == nil {
				upd.SetNextRunAt(next)
			} else {
				// Enabling a row whose stored cron is somehow invalid: fall back to
				// clearing so the ticker re-initializes (and logs) rather than 500ing.
				upd.ClearNextRunAt()
			}
		}

		saved, err := upd.Save(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to update job", err)
		}
		return &jobOutput{Body: toJobDTO(saved, h.latestRunStatus(ctx, saved.ID))}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-job-runs",
		Method:      http.MethodGet,
		Path:        "/api/admin/jobs/{id}/runs",
		Summary:     "List a job's run history, newest first",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *listJobRunsInput) (*listJobRunsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		exists, err := h.deps.Ent.ScheduledJob.Query().Where(scheduledjob.IDEQ(in.ID)).Exist(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load job", err)
		}
		if !exists {
			return nil, huma.Error404NotFound("job not found")
		}
		base := h.deps.Ent.JobRun.Query().Where(jobrun.HasJobWith(scheduledjob.IDEQ(in.ID)))
		total, err := base.Clone().Count(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to count runs", err)
		}
		rows, err := base.Clone().
			Order(ent.Desc(jobrun.FieldCreatedAt)).
			Offset(in.Offset).
			Limit(in.Limit).
			All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list runs", err)
		}
		out := &listJobRunsOutput{}
		out.Body.Total = total
		out.Body.Runs = make([]JobRunDTO, 0, len(rows))
		for _, r := range rows {
			out.Body.Runs = append(out.Body.Runs, toJobRunDTO(r))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "start-job-run",
		Method:      http.MethodPost,
		Path:        "/api/admin/jobs/{id}/runs",
		Summary:     "Force-start a manual run for a job",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *jobIDInput) (*startJobRunOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		job, err := h.deps.Ent.ScheduledJob.Get(ctx, in.ID)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("job not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load job", err)
		}

		// Refuse a second run while one is in flight: one queued or running run per
		// job at a time, so a double-click or a race with the scheduler cannot pile up
		// duplicate work.
		active, err := h.deps.Ent.JobRun.Query().
			Where(
				jobrun.HasJobWith(scheduledjob.IDEQ(job.ID)),
				jobrun.StatusIn(jobrun.StatusQueued, jobrun.StatusRunning),
			).
			Exist(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to check for an active run", err)
		}
		if active {
			return nil, huma.Error409Conflict("a run for this job is already queued or running")
		}

		out := &startJobRunOutput{}

		// Degrade gracefully with no queue (local `make up` has no worker): report it
		// as a clear "no worker" response instead of a 500, and do not create a run
		// nothing would ever pick up.
		if h.deps.Queue == nil || !h.deps.Queue.Configured() {
			out.Body.Started = false
			out.Body.Message = "no worker is running locally; a run cannot be started here"
			return out, nil
		}

		// Manual runs carry scheduled_for = now so the JobRun.(scheduled_for, job)
		// unique index still holds; a manual now() never collides with a cron tick.
		now := time.Now()
		run, err := h.deps.Ent.JobRun.Create().
			SetStatus(jobrun.StatusQueued).
			SetTrigger(jobrun.TriggerManual).
			SetScheduledFor(now).
			SetJobID(job.ID).
			Save(ctx)
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("a run for this tick already exists")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to create run", err)
		}

		// Same envelope the scheduler emits: the job key routes it, the config is the
		// payload, and JobRunID drives the worker's queued->running->terminal lifecycle.
		body := []byte("{}")
		if len(job.Config) > 0 {
			b, err := json.Marshal(job.Config)
			if err != nil {
				return nil, huma.Error500InternalServerError("failed to marshal job config", err)
			}
			body = b
		}
		if err := h.deps.Queue.Enqueue(ctx, queue.Job{
			Type:     job.Key,
			Payload:  json.RawMessage(body),
			JobRunID: run.ID,
		}); err != nil {
			// The run row exists but nothing will process it: mark it failed so history
			// reflects reality rather than leaving a run stuck queued forever.
			_ = h.deps.Ent.JobRun.UpdateOneID(run.ID).
				SetStatus(jobrun.StatusFailed).
				SetError("enqueue failed: " + err.Error()).
				SetFinishedAt(time.Now()).
				Exec(ctx)
			return nil, huma.Error500InternalServerError("failed to enqueue run", err)
		}

		dto := toJobRunDTO(run)
		out.Body.Started = true
		out.Body.Run = &dto
		return out, nil
	})

	h.registerAdminTokens(api)
}

type listTokensOutput struct {
	Body struct {
		Tokens []ApiTokenDTO `json:"tokens"`
	}
}

type mintTokenInput struct {
	Body struct {
		Name      string   `json:"name" minLength:"1" doc:"Human label for the token, e.g. \"laptop Claude Code\""`
		Runner    string   `json:"runner" minLength:"1" doc:"Runner identity this token authenticates as, e.g. \"laptop\", \"home-finance\""`
		Scope     []string `json:"scope,omitempty" doc:"Job keys this token may claim/complete work for; empty authorizes nothing"`
		ExpiresAt string   `json:"expires_at,omitempty" doc:"Optional RFC3339 expiry; empty means the token never expires"`
	}
}

type mintTokenOutput struct {
	Body struct {
		Token    string      `json:"token" doc:"The raw bearer token, shown ONCE. Store it now; it cannot be recovered."`
		ApiToken ApiTokenDTO `json:"api_token"`
	}
}

type tokenIDInput struct {
	ID int `path:"id" doc:"ApiToken DB id"`
}

// registerAdminTokens wires the scope-only bearer-token management under the admin
// gate. Minting/revoking is a cookie-auth admin action; the tokens themselves never
// grant admin, only the work API's scope (ADR 0014).
func (h *Handler) registerAdminTokens(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-api-tokens",
		Method:      http.MethodGet,
		Path:        "/api/admin/tokens",
		Summary:     "List the external-runner API tokens",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listTokensOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		rows, err := h.deps.Ent.ApiToken.Query().Order(ent.Desc(apitoken.FieldCreatedAt)).All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list tokens", err)
		}
		out := &listTokensOutput{}
		out.Body.Tokens = make([]ApiTokenDTO, 0, len(rows))
		for _, t := range rows {
			out.Body.Tokens = append(out.Body.Tokens, toApiTokenDTO(t))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "mint-api-token",
		Method:        http.MethodPost,
		Path:          "/api/admin/tokens",
		Summary:       "Mint an external-runner API token (raw value returned once)",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *mintTokenInput) (*mintTokenOutput, error) {
		u, err := requireAdmin(ctx)
		if err != nil {
			return nil, err
		}
		if h.deps.Auth == nil {
			return nil, huma.Error503ServiceUnavailable("auth is not configured; cannot mint tokens")
		}
		var expires *time.Time
		if s := strings.TrimSpace(in.Body.ExpiresAt); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				return nil, huma.Error422UnprocessableEntity("expires_at must be an RFC3339 timestamp")
			}
			expires = &t
		}
		raw, tok, err := h.deps.Auth.MintApiToken(
			ctx,
			u.ID,
			strings.TrimSpace(in.Body.Name),
			strings.TrimSpace(in.Body.Runner),
			in.Body.Scope,
			expires,
		)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to mint token", err)
		}
		out := &mintTokenOutput{}
		out.Body.Token = raw
		out.Body.ApiToken = toApiTokenDTO(tok)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "revoke-api-token",
		Method:        http.MethodDelete,
		Path:          "/api/admin/tokens/{id}",
		Summary:       "Revoke an external-runner API token by id",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *tokenIDInput) (*struct{}, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		if h.deps.Auth == nil {
			return nil, huma.Error503ServiceUnavailable("auth is not configured; cannot revoke tokens")
		}
		if err := h.deps.Auth.RevokeApiToken(ctx, in.ID); err != nil {
			return nil, huma.Error500InternalServerError("failed to revoke token", err)
		}
		return &struct{}{}, nil
	})
}
