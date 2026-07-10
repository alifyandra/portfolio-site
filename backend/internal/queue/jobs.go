package queue

// Job type identifiers. Add new ones here as async work is introduced.
const (
	// TypeContactNotify emails Alif about a new contact-form submission.
	TypeContactNotify = "contact.notify"
	// TypeDigestBuild builds the dated Digest from the active Sources. It is
	// schedule-triggered (EventBridge Scheduler -> SQS), never produced in-process,
	// so there is no Go producer for it. See ADR 0013.
	TypeDigestBuild = "digest.build"
	// TypeDigestCollect polls in-flight Anthropic Message Batches submitted by
	// digest.build (prod batch mode) and persists the completed Digest for any that
	// have ended. Like digest.build it is schedule-triggered (a recurring EventBridge
	// Scheduler cron -> SQS) and carries no payload — it drains every pending Digest.
	// See ADR 0013 (Batch API amendment).
	TypeDigestCollect = "digest.collect"
	// TypeDigestScrape is the scrape stage of the split digest (ADR 0014): it fetches
	// the active Sources on-box and persists one Artifact per source, never calling
	// Anthropic. It is driven by the in-process scheduler (carries a JobRunID), not
	// EventBridge. Dormant until a ScheduledJob row for it is enabled.
	TypeDigestScrape = "digest.scrape"
	// TypeDigestLlm is the LLM stage of the split digest (ADR 0014): it assembles the
	// pending scrape Artifacts and summarizes them (inline in local mode, or via the
	// Fargate task submitting a batch in fargate mode), producing the dated Digest.
	// Scheduler-driven (carries a JobRunID). Dormant until its ScheduledJob is enabled.
	TypeDigestLlm = "digest.llm"
)

// ContactNotifyPayload is the body of a TypeContactNotify job.
type ContactNotifyPayload struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Body  string `json:"body"`
}

// DigestBuildPayload is the body of a TypeDigestBuild job. Date is optional: an
// RFC3339 or YYYY-MM-DD target day, empty meaning today in UTC (the worker
// resolves it). It exists so a backfill can target a specific day; the daily
// schedule enqueues an empty payload.
type DigestBuildPayload struct {
	Date string `json:"date,omitempty"`
}
