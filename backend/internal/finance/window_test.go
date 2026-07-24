package finance

import (
	"testing"
	"time"
)

// day is a UTC-midnight date helper for the window tests.
func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// TestComputeWindow_NilWatermarkBackfills: a never-synced account (nil watermark)
// yields a full backfill from earliest to today.
func TestComputeWindow_NilWatermarkBackfills(t *testing.T) {
	earliest := day(2020, 1, 1)
	today := day(2026, 7, 24)
	w := ComputeWindow(nil, earliest, today, 7)
	if !w.Backfill {
		t.Errorf("Backfill = false, want true for nil watermark")
	}
	if !w.From.Equal(earliest) {
		t.Errorf("From = %v, want %v (earliest)", w.From, earliest)
	}
	if !w.To.Equal(today) {
		t.Errorf("To = %v, want %v (today)", w.To, today)
	}
}

// TestComputeWindow_SetWatermarkOverlaps: a set watermark yields an incremental run
// from overlapDays before the watermark to today.
func TestComputeWindow_SetWatermarkOverlaps(t *testing.T) {
	earliest := day(2020, 1, 1)
	today := day(2026, 7, 24)
	wm := day(2026, 7, 20)
	w := ComputeWindow(&wm, earliest, today, 7)
	if w.Backfill {
		t.Errorf("Backfill = true, want false for a set watermark")
	}
	if want := day(2026, 7, 13); !w.From.Equal(want) {
		t.Errorf("From = %v, want %v (watermark - 7d)", w.From, want)
	}
	if !w.To.Equal(today) {
		t.Errorf("To = %v, want %v (today)", w.To, today)
	}
}

// TestComputeWindow_ZeroOverlap: an overlap of 0 starts the incremental run exactly
// at the watermark (no re-scan margin).
func TestComputeWindow_ZeroOverlap(t *testing.T) {
	today := day(2026, 7, 24)
	wm := day(2026, 7, 20)
	w := ComputeWindow(&wm, day(2020, 1, 1), today, 0)
	if !w.From.Equal(wm) {
		t.Errorf("From = %v, want %v (watermark, zero overlap)", w.From, wm)
	}
	if w.Backfill {
		t.Errorf("Backfill = true, want false")
	}
}

// TestComputeWindow_OverlapCanPrecedeEarliest: ComputeWindow is pure and does not
// clamp From to earliest; a large overlap simply subtracts, which is harmless (the
// scrape just re-covers more, and re-scanned rows dedup). Monotonic/clamp-to-today
// of the STORED watermark is the ingest's job, exercised in the ingest tests.
func TestComputeWindow_OverlapCanPrecedeEarliest(t *testing.T) {
	today := day(2026, 7, 24)
	wm := day(2026, 7, 20)
	w := ComputeWindow(&wm, day(2020, 1, 1), today, 30)
	if want := day(2026, 6, 20); !w.From.Equal(want) {
		t.Errorf("From = %v, want %v (watermark - 30d)", w.From, want)
	}
}
