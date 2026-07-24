package finance

import "time"

// SyncWindow is the [From, To] range a finance sync should scrape, plus whether it
// is a full backfill (no prior watermark) or an incremental run. It is derived by
// ComputeWindow from an account's posted watermark; the broker turns it into the
// window.from/window.to it POSTs back on the ingest payload.
type SyncWindow struct {
	From, To time.Time
	Backfill bool
}

// ComputeWindow derives the sync window for one account from its posted watermark
// (issue #88). It is pure and touches no DB, so it is trivially testable and can be
// reused by any caller (the deferred sync-planning endpoint, the broker, a test).
//
//   - nil watermark => the account was never synced: a full backfill from earliest
//     to today.
//   - a set watermark => an incremental run from overlapDays before the watermark
//     (re-scanning that overlap so a late-settling/backdated row is still caught;
//     the overlap is idempotent because re-scanned rows dedup on their hash) to
//     today.
//
// today is passed in (not read from the clock) so callers control it and tests stay
// deterministic. Clamping the watermark to today and keeping it monotonic is the
// ingest's job (it owns the stored value); this only shapes the scrape range.
func ComputeWindow(watermark *time.Time, earliest, today time.Time, overlapDays int) SyncWindow {
	if watermark == nil {
		return SyncWindow{From: earliest, To: today, Backfill: true}
	}
	return SyncWindow{From: watermark.AddDate(0, 0, -overlapDays), To: today, Backfill: false}
}
