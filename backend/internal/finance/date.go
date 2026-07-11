package finance

import (
	"fmt"
	"strings"
	"time"
)

// normalizeDate collapses t to UTC midnight, the canonical form every stored
// finance date takes (posted_date, pending date, snapshot as_of). Mirrors the same
// approach used for Digest.date so a date-only value is stored consistently.
func normalizeDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// parseDate reads a date-only payload field (posted_date, pending date) in bare
// YYYY-MM-DD or RFC3339 form and returns it normalized to UTC midnight. Storage
// keys off the parsed day; DedupHash uses the raw string, not this.
func parseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return normalizeDate(t), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return normalizeDate(t), nil
	}
	return time.Time{}, fmt.Errorf("finance: invalid date %q (want RFC3339 or YYYY-MM-DD)", s)
}

// parseDateTime reads a full RFC3339 timestamp (a balance as_of, e.g.
// "2026-07-11T09:00:00.000Z") and returns the instant in UTC, WITHOUT date-only
// normalization: the intra-day reading time is load-bearing (it distinguishes two
// same-day snapshots and is the snapshot's idempotency key with the account).
func parseDateTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("finance: invalid timestamp %q (want RFC3339): %w", s, err)
	}
	return t.UTC(), nil
}
