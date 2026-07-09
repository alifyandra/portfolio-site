package digest

import (
	"fmt"
	"strings"
	"time"
)

// NormalizeDate collapses t to UTC midnight — the canonical Digest.date value and
// idempotency key. Everything keys off this so a redelivery for the same day
// upserts the same row rather than creating a duplicate (see ADR 0013).
func NormalizeDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

// ParseDate reads a target day in RFC3339 or YYYY-MM-DD form, normalized to UTC
// midnight. An empty string means today in UTC. Used for DIGEST_DATE (cmd/digest)
// and the optional DigestBuildPayload.Date (the worker).
func ParseDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return NormalizeDate(time.Now()), nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return NormalizeDate(t), nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return NormalizeDate(t), nil
	}
	return time.Time{}, fmt.Errorf("digest: invalid date %q (want RFC3339 or YYYY-MM-DD)", s)
}
