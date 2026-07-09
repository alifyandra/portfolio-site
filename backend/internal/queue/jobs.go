package queue

// Job type identifiers. Add new ones here as async work is introduced.
const (
	// TypeContactNotify emails Alif about a new contact-form submission.
	TypeContactNotify = "contact.notify"
	// TypeDigestBuild builds the dated Digest from the active Sources. It is
	// schedule-triggered (EventBridge Scheduler -> SQS), never produced in-process,
	// so there is no Go producer for it. See ADR 0013.
	TypeDigestBuild = "digest.build"
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
