package jobs

// The catalog of scheduler-driven job kinds an admin may register (ADR 0014). It is
// the single source of truth for what a ScheduledJob key is allowed to be: the worker
// only knows how to dispatch these keys (cmd/worker/main.go's handle() switch), so a
// row with any other key would sit enqueued forever, rejected as errNoHandler. The
// create-job admin endpoint validates against this list, and the /admin Jobs console
// renders it as the "Add job" dropdown so key and stage are picked, never typed.
//
// Keep this in lockstep with the worker's handle() switch: a scheduler-driven job kind
// belongs here iff the worker has a case for it. digest.collect and digest.build stay
// EventBridge-driven and are deliberately absent.

// Kind describes one registrable job: its immutable key, the stage half it belongs to,
// and sensible defaults the console pre-fills so a fresh job is one click from correct.
type Kind struct {
	Key             string `json:"key"`
	Name            string `json:"name"`
	Stage           string `json:"stage"`
	DefaultSchedule string `json:"default_schedule"`
	DefaultTimezone string `json:"default_timezone"`
	Description     string `json:"description"`
	// AckGated marks a kind whose fired run does no server-side work and must pause
	// for an explicit human approval before it is runnable (ADR 0016). The scheduler
	// creates such a run directly in awaiting_ack, does NOT enqueue it to the worker,
	// and sends a refresh-handshake notification instead. Default false: every other
	// kind keeps the ordinary queued+enqueue fire path.
	AckGated bool `json:"ack_gated,omitempty"`
}

// kinds is the registry. Order is the display order in the console dropdown (scrape
// before llm, matching the pipeline order). The default schedules match the digest
// cutover values (scrape 17:30 UTC, llm 18:00 UTC) so a re-register reproduces prod.
var kinds = []Kind{
	{
		Key:             "digest.scrape",
		Name:            "Digest — scrape sources",
		Stage:           "scrape",
		DefaultSchedule: "30 17 * * *",
		DefaultTimezone: "UTC",
		Description:     "Fetch the active Sources on-box and persist one Artifact per source (never calls Anthropic).",
	},
	{
		Key:             "digest.llm",
		Name:            "Digest — summarise",
		Stage:           "llm",
		DefaultSchedule: "0 18 * * *",
		DefaultTimezone: "UTC",
		Description:     "Assemble the pending scrape Artifacts and summarise them into the dated Digest.",
	},
	{
		Key:   "finance.sync",
		Name:  "Finance sync",
		Stage: "scrape",
		// A daily coordinated refresh. The stage is a formality (finance does not use
		// the two-stage artifact pipeline); scrape is the closest fit since the run is
		// a "fetch fresh data" instruction handed to the external finance source.
		DefaultSchedule: "0 20 * * *",
		DefaultTimezone: "UTC",
		Description:     "Plan a daily finance refresh window and hand it to the external finance source. Ack-gated: the run pauses for a one-off human approval before it becomes claimable (see ADR 0016).",
		AckGated:        true,
	},
}

// Kinds returns a copy of the registry so a caller cannot mutate the shared slice.
func Kinds() []Kind {
	return append([]Kind(nil), kinds...)
}

// LookupKind returns the Kind for a key, or ok=false when the key is not registrable.
func LookupKind(key string) (Kind, bool) {
	for _, k := range kinds {
		if k.Key == key {
			return k, true
		}
	}
	return Kind{}, false
}

// ackGatedKeys returns the keys of every ack-gated kind. The reaper uses it to
// exempt ack-gated runs (ADR 0016), whose lifecycle is externally driven
// (claim -> ingest -> complete) and so is not bounded by the in-process running
// lease.
func ackGatedKeys() []string {
	var keys []string
	for _, k := range kinds {
		if k.AckGated {
			keys = append(keys, k.Key)
		}
	}
	return keys
}
