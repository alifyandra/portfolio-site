package finance

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
)

// Every figure in this file is invented and round. Real balances, merchant names, account
// names and date spans stay out of this repo; the tests exercise arithmetic, not data.

// mel builds a local-zone instant in the bucket zone, for asserting bucket starts.
func mel(y int, m time.Month, d, h int) time.Time {
	return time.Date(y, m, d, h, 0, 0, 0, bucketLoc)
}

// utcDay builds a UTC-midnight date, the form posted_date and every date-only bound take.
func utcDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// seedTxnAfter appends a posted transaction carrying the bank's running balance.
func seedTxnAfter(t *testing.T, ctx context.Context, client *ent.Client, acc *ent.Account, tag string, date time.Time, amount, after float64, desc string) {
	t.Helper()
	client.Transaction.Create().
		SetDedupHash("bs-" + tag).
		SetPostedDate(date).
		SetAmount(amount).
		SetDescription(desc).
		SetBalanceAfter(after).
		SetAccountID(acc.ID).
		SaveX(ctx)
}

// seedTxnNoAfter appends a posted transaction with NO running balance, which is the shape
// the walk strategy has to cope with.
func seedTxnNoAfter(t *testing.T, ctx context.Context, client *ent.Client, acc *ent.Account, tag string, date time.Time, amount float64, desc string) {
	t.Helper()
	client.Transaction.Create().
		SetDedupHash("bs-" + tag).
		SetPostedDate(date).
		SetAmount(amount).
		SetDescription(desc).
		SetAccountID(acc.ID).
		SaveX(ctx)
}

func onlySeries(t *testing.T, series []BalanceSeriesView) BalanceSeriesView {
	t.Helper()
	if len(series) != 1 {
		t.Fatalf("series = %d, want exactly 1", len(series))
	}
	return series[0]
}

func f64(t *testing.T, p *float64, field string) float64 {
	t.Helper()
	if p == nil {
		t.Fatalf("%s is nil, want a value", field)
	}
	return *p
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

// TestBucketZoneIsMelbourne guards the one piece of environment this feature depends on: if
// the zone database is missing, bucketLoc silently degrades to UTC and every boundary test
// below would still pass while the real edges moved 10 hours.
func TestBucketZoneIsMelbourne(t *testing.T) {
	if bucketLoc == time.UTC {
		t.Fatal("bucketLoc fell back to UTC: the Australia/Melbourne zone database is unavailable")
	}
	// Winter in Melbourne is +10.
	if _, off := mel(2026, 7, 1, 0).Zone(); off != 10*3600 {
		t.Errorf("July offset = %ds, want 36000 (+10)", off)
	}
	// Daylight saving is +11.
	if _, off := mel(2026, 12, 1, 0).Zone(); off != 11*3600 {
		t.Errorf("December offset = %ds, want 39600 (+11)", off)
	}
}

// TestParseBalanceStepAndBasis: the known values parse, an unknown one errors rather than
// falling back.
func TestParseBalanceStepAndBasis(t *testing.T) {
	for _, s := range []string{"", "day", "week", "month"} {
		if _, err := ParseBalanceStep(s); err != nil {
			t.Errorf("ParseBalanceStep(%q) = %v, want ok", s, err)
		}
	}
	if _, err := ParseBalanceStep("monthly"); err == nil {
		t.Error("ParseBalanceStep(monthly) = nil error, want a rejection rather than a fall back to raw")
	}
	for _, s := range []string{"", "snapshot", "ledger"} {
		if _, err := ParseBalanceBasis(s); err != nil {
			t.Errorf("ParseBalanceBasis(%q) = %v, want ok", s, err)
		}
	}
	if _, err := ParseBalanceBasis("derived"); err == nil {
		t.Error("ParseBalanceBasis(derived) = nil error, want a rejection")
	}
}

// TestBucketStart_WeekStartsLocalMonday: a week bucket starts the local Monday, from any day
// inside it, including across a month edge.
func TestBucketStart_WeekStartsLocalMonday(t *testing.T) {
	// 2026-07-15 is a Wednesday; its week starts Monday 2026-07-13.
	wantMon := mel(2026, 7, 13, 0)
	for _, d := range []int{13, 14, 15, 16, 17, 18, 19} {
		got := bucketStart(mel(2026, 7, d, 12), StepWeek)
		if !got.Equal(wantMon) {
			t.Errorf("week bucket of Jul %d = %s, want %s", d, got, wantMon)
		}
		if got.Weekday() != time.Monday {
			t.Errorf("week bucket of Jul %d starts %s, want Monday", d, got.Weekday())
		}
	}
	// The Monday after that is a new bucket, and never the same one.
	if got := bucketStart(mel(2026, 7, 20, 0), StepWeek); got.Equal(wantMon) {
		t.Error("Jul 20 (the next Monday) fell in the previous week bucket")
	}
	// Across a month edge: 2026-08-01 is a Saturday, so its week starts Monday 2026-07-27.
	if got, want := bucketStart(mel(2026, 8, 1, 9), StepWeek), mel(2026, 7, 27, 0); !got.Equal(want) {
		t.Errorf("week bucket of Aug 1 = %s, want %s (Monday in the previous month)", got, want)
	}
}

// TestBucketStart_MelbourneMonthEdge is the reason bucketing is not on UTC: a reading at 9am
// Melbourne on the local 1st is 23:00 UTC on the previous month's last day, so a UTC bucket
// would file it under the wrong month.
func TestBucketStart_MelbourneMonthEdge(t *testing.T) {
	reading := mel(2026, 8, 1, 9) // 2026-07-31T23:00Z
	if reading.UTC().Month() != time.July {
		t.Fatalf("fixture is wrong: %s is not 23:00 UTC in July", reading.UTC())
	}
	got := bucketStart(reading, StepMonth)
	if want := mel(2026, 8, 1, 0); !got.Equal(want) {
		t.Errorf("month bucket = %s, want %s (August, not the UTC month)", got, want)
	}
	if got.Day() != 1 {
		t.Errorf("month bucket starts on day %d, want the local 1st", got.Day())
	}
}

// TestBucketStart_UTCMidnightDateKeysToSameLocalDay: posted_date is UTC-midnight-normalised,
// and UTC midnight is 10 or 11am the SAME Melbourne day, so a date-only row keys into the
// day a human would call it. This is what lets rows and readings share one bucket grid.
func TestBucketStart_UTCMidnightDateKeysToSameLocalDay(t *testing.T) {
	for _, tc := range []struct{ y, d int }{{2026, 15}, {2026, 1}} {
		row := utcDay(tc.y, 7, tc.d)
		if got, want := bucketStart(row, StepDay), mel(tc.y, 7, tc.d, 0); !got.Equal(want) {
			t.Errorf("day bucket of %s = %s, want %s", row.Format(time.RFC3339), got, want)
		}
	}
}

// TestBucketEnumeration_DSTNeitherDropsNorDuplicates walks day buckets across both Melbourne
// transitions. The clocks-forward day is 23 hours and the clocks-back day is 25, so anything
// adding a fixed 24h would skip or repeat a bucket.
func TestBucketEnumeration_DSTNeitherDropsNorDuplicates(t *testing.T) {
	// Daylight saving starts the first Sunday in October (2026-10-04, 2am -> 3am), and ends
	// the first Sunday in April (2026-04-05, 3am -> 2am).
	for _, w := range []struct {
		name       string
		from, to   time.Time
		wantDays   int
		transition int
	}{
		{"clocks forward", mel(2026, 10, 1, 0), mel(2026, 10, 8, 0), 8, 4},
		{"clocks back", mel(2026, 4, 1, 0), mel(2026, 4, 8, 0), 8, 5},
	} {
		var starts []time.Time
		seen := map[int64]bool{}
		for b := bucketStart(w.from, StepDay); !b.After(bucketStart(w.to, StepDay)); b = nextBucketStart(b, StepDay) {
			if seen[bucketKey(b)] {
				t.Fatalf("%s: bucket %s emitted twice", w.name, b)
			}
			seen[bucketKey(b)] = true
			starts = append(starts, b)
		}
		if len(starts) != w.wantDays {
			t.Errorf("%s: %d day buckets, want %d", w.name, len(starts), w.wantDays)
		}
		// Every bucket must be local midnight, on consecutive calendar days, transition
		// included.
		for i, b := range starts {
			if b.Hour() != 0 || b.Minute() != 0 {
				t.Errorf("%s: bucket %d = %s, want local midnight", w.name, i, b)
			}
			if b.Day() != i+1 {
				t.Errorf("%s: bucket %d falls on day %d, want %d (a day was dropped or repeated)", w.name, i, b.Day(), i+1)
			}
		}
		// The transition day itself is one bucket whose span is not 24 hours.
		tr := bucketStart(mel(2026, starts[0].Month(), w.transition, 12), StepDay)
		span := nextBucketStart(tr, StepDay).Sub(tr)
		if span == 24*time.Hour {
			t.Errorf("%s: transition day %s spans 24h, so the fixture is not on a real transition", w.name, tr)
		}
		if span != 23*time.Hour && span != 25*time.Hour {
			t.Errorf("%s: transition day %s spans %v, want 23h or 25h", w.name, tr, span)
		}
	}
	// A month bucket containing a transition is still exactly one month long in calendar
	// terms.
	oct := bucketStart(mel(2026, 10, 20, 0), StepMonth)
	if got, want := nextBucketStart(oct, StepMonth), mel(2026, 11, 1, 0); !got.Equal(want) {
		t.Errorf("month after %s = %s, want %s", oct, got, want)
	}
}

// TestSnapshotSeries_LastReadingInBucketWins: two readings on the same local day collapse to
// the LATER one (close-of-period), never an average and never a sum.
func TestSnapshotSeries_LastReadingInBucketWins(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	// Same local day, two intra-day readings.
	seedSnapshot(t, ctx, client, a, 1000, mel(2026, 7, 15, 9).UTC())
	seedSnapshot(t, ctx, client, a, 1400, mel(2026, 7, 15, 18).UTC())

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 1 {
		t.Fatalf("points = %d, want 1 day bucket", len(s.Points))
	}
	if s.Points[0].Balance != 1400 {
		t.Errorf("bucket close = %.2f, want 1400 (the later reading, not an average of 1200 or a sum of 2400)", s.Points[0].Balance)
	}
	if !s.Points[0].AsOf.Equal(mel(2026, 7, 15, 0)) {
		t.Errorf("as_of = %s, want the bucket start %s", s.Points[0].AsOf, mel(2026, 7, 15, 0))
	}
	if s.Points[0].Carried {
		t.Error("a real reading was flagged carried")
	}
}

// TestSnapshotSeries_CarriesQuietBucketForward: a bucket with no reading repeats the previous
// close and is flagged, and nothing is back-filled before the first reading.
func TestSnapshotSeries_CarriesQuietBucketForward(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedSnapshot(t, ctx, client, a, 500, mel(2026, 7, 10, 9).UTC())
	// Jul 11 and Jul 12 have no reading.
	seedSnapshot(t, ctx, client, a, 800, mel(2026, 7, 13, 9).UTC())

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 4 {
		t.Fatalf("points = %d, want 4 (Jul 10..13, no back-fill before Jul 10)", len(s.Points))
	}
	want := []struct {
		bal     float64
		carried bool
		day     int
	}{{500, false, 10}, {500, true, 11}, {500, true, 12}, {800, false, 13}}
	for i, w := range want {
		p := s.Points[i]
		if p.Balance != w.bal || p.Carried != w.carried || p.AsOf.Day() != w.day {
			t.Errorf("point %d = {%.2f carried=%v day=%d}, want {%.2f carried=%v day=%d}",
				i, p.Balance, p.Carried, p.AsOf.Day(), w.bal, w.carried, w.day)
		}
	}
	// Roll-up.
	if f64(t, s.First, "first") != 500 || f64(t, s.Last, "last") != 800 || f64(t, s.Change, "change") != 300 {
		t.Errorf("roll-up = %v/%v/%v, want 500/800/300", s.First, s.Last, s.Change)
	}
	if got := f64(t, s.ChangePct, "change_pct"); !near(got, 60) {
		t.Errorf("change_pct = %.4f, want 60", got)
	}
}

// TestSnapshotSeries_NoBackFillBeforeFirstReading: a From well before the account's first
// reading does not manufacture pre-history.
func TestSnapshotSeries_NoBackFillBeforeFirstReading(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedSnapshot(t, ctx, client, a, 700, mel(2026, 7, 20, 9).UTC())

	from := utcDay(2026, 7, 10)
	to := utcDay(2026, 7, 22)
	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, From: &from, To: &to})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	// Jul 20 (the reading) plus carried Jul 21 and Jul 22. Nothing for Jul 10..19.
	if len(s.Points) != 3 {
		t.Fatalf("points = %d, want 3 (Jul 20..22); a bucket before the first reading was back-filled", len(s.Points))
	}
	if s.Points[0].AsOf.Day() != 20 {
		t.Errorf("first point is Jul %d, want Jul 20", s.Points[0].AsOf.Day())
	}
}

// TestSnapshotSeries_MonthBucketUsesLocalFirst: a 9am-Melbourne reading on the local 1st
// belongs to that month, and a month with no reading carries.
func TestSnapshotSeries_MonthBucketUsesLocalFirst(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedSnapshot(t, ctx, client, a, 200, mel(2026, 6, 15, 9).UTC())
	seedSnapshot(t, ctx, client, a, 900, mel(2026, 8, 1, 9).UTC()) // 2026-07-31T23:00Z

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepMonth})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 3 {
		t.Fatalf("points = %d, want 3 (Jun, carried Jul, Aug)", len(s.Points))
	}
	want := []struct {
		month   time.Month
		bal     float64
		carried bool
	}{
		{time.June, 200, false},
		{time.July, 200, true}, // no reading in July: carries June's close
		{time.August, 900, false},
	}
	for i, w := range want {
		p := s.Points[i]
		if p.AsOf.Month() != w.month || p.AsOf.Day() != 1 {
			t.Errorf("point %d as_of = %s, want the local 1st of %s", i, p.AsOf, w.month)
		}
		if p.Balance != w.bal || p.Carried != w.carried {
			t.Errorf("point %d = {%.2f carried=%v}, want {%.2f carried=%v}", i, p.Balance, p.Carried, w.bal, w.carried)
		}
	}
	// The August reading is 23:00 UTC on 31 July, so a UTC-bucketed series would have put it
	// in July and returned two points.
	if s.Points[2].AsOf.Month() != time.August {
		t.Error("the 9am-local reading on the local 1st was filed under the UTC month")
	}
}

// TestLedgerSeries_PathA_CloseIsLastRowBalanceAfter: with the bank's running balance present,
// the bucket close IS that figure at the bucket's last row, and open is the previous close.
func TestLedgerSeries_PathA_CloseIsLastRowBalanceAfter(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)

	// Day 1: two rows. Opening level is 1000 (1200 - 200 on the first row).
	seedTxnAfter(t, ctx, client, a, "d1a", utcDay(2026, 7, 10), 200, 1200, "SALARY")
	seedTxnAfter(t, ctx, client, a, "d1b", utcDay(2026, 7, 10), -300, 900, "GROCERY STORE")
	// Day 2: one row.
	seedTxnAfter(t, ctx, client, a, "d2", utcDay(2026, 7, 11), -100, 800, "COFFEE SHOP")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(s.Points))
	}

	p0 := s.Points[0]
	if got := f64(t, p0.Close, "close"); got != 900 {
		t.Errorf("day1 close = %.2f, want 900 (balance_after of the LAST row, not the first)", got)
	}
	if p0.Balance != 900 {
		t.Errorf("day1 balance = %.2f, want it to equal close 900", p0.Balance)
	}
	if got := f64(t, p0.Open, "open"); got != 1000 {
		t.Errorf("day1 open = %.2f, want 1000 (first row's balance_after minus its amount)", got)
	}
	if p0.Source != SourceBalanceAfter {
		t.Errorf("day1 source = %q, want %q", p0.Source, SourceBalanceAfter)
	}
	if !s.StartUnverified {
		t.Error("start_unverified = false, want true (the earliest bucket's opening was synthesized)")
	}

	p1 := s.Points[1]
	if got := f64(t, p1.Open, "open"); got != 900 {
		t.Errorf("day2 open = %.2f, want 900 == day1 close", got)
	}
	if got := f64(t, p1.Close, "close"); got != 800 {
		t.Errorf("day2 close = %.2f, want 800", got)
	}
	if s.LedgerFrom == nil || !s.LedgerFrom.Equal(utcDay(2026, 7, 10)) {
		t.Errorf("ledger_from = %v, want 2026-07-10", s.LedgerFrom)
	}
}

// TestLedgerSeries_OpenEqualsPreviousClose over several buckets, and close - open == net
// holds on every one.
func TestLedgerSeries_OpenEqualsPreviousClose(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)

	// A consistent ledger: each balance_after is the previous one plus the amount.
	running := 5000.0
	for i, amt := range []float64{-100, 250, -50, -400, 1000} {
		running += amt
		seedTxnAfter(t, ctx, client, a, "seq"+string(rune('a'+i)), utcDay(2026, 7, 10+i), amt, running, "MERCHANT")
	}

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 5 {
		t.Fatalf("points = %d, want 5", len(s.Points))
	}
	for i, p := range s.Points {
		open := f64(t, p.Open, "open")
		close := f64(t, p.Close, "close")
		net := f64(t, p.Net, "net")
		if !near(close-open, net) {
			t.Errorf("point %d: close - open = %.2f, want net %.2f", i, close-open, net)
		}
		if p.FlowMismatch {
			t.Errorf("point %d: flow_mismatch set on a consistent ledger", i)
		}
		if i > 0 {
			if prev := f64(t, s.Points[i-1].Close, "prev close"); !near(open, prev) {
				t.Errorf("point %d: open = %.2f, want previous close %.2f", i, open, prev)
			}
		}
	}
	if got := f64(t, s.Points[4].Close, "close"); got != 5700 {
		t.Errorf("final close = %.2f, want 5700", got)
	}
}

// TestLedgerSeries_FlowMismatchFiresWhenARowIsMissing: pulling a row out of a ledger whose
// running balances still reflect it makes close - open disagree with net, which is exactly
// the silent-data-loss signal the check exists for.
func TestLedgerSeries_FlowMismatchFiresWhenARowIsMissing(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)

	seedTxnAfter(t, ctx, client, a, "m1", utcDay(2026, 7, 10), -100, 900, "MERCHANT")
	// Jul 11's running balance drops to 500, but the -300 row that would explain 600 of that
	// is absent: only a -100 row is here.
	seedTxnAfter(t, ctx, client, a, "m2", utcDay(2026, 7, 11), -100, 500, "MERCHANT")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(s.Points))
	}
	if s.Points[0].FlowMismatch {
		t.Error("the consistent first bucket was flagged flow_mismatch")
	}
	if !s.Points[1].FlowMismatch {
		t.Error("flow_mismatch = false on the bucket whose close - open does not equal net; a missing row went unreported")
	}
}

// TestLedgerSeries_DriftWhenSnapshotDisagrees: where a reading falls in a bucket, drift is
// the derived close minus that reading, and the series reports the largest magnitude.
func TestLedgerSeries_DriftWhenSnapshotDisagrees(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)

	seedTxnAfter(t, ctx, client, a, "dr1", utcDay(2026, 7, 10), -100, 900, "MERCHANT")
	seedTxnAfter(t, ctx, client, a, "dr2", utcDay(2026, 7, 11), -100, 800, "MERCHANT")
	// A reading on Jul 11 that disagrees with the derived 800 by 50.
	seedSnapshot(t, ctx, client, a, 750, mel(2026, 7, 11, 18).UTC())

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if s.Points[0].Drift != nil {
		t.Errorf("day1 drift = %v, want nil (no reading falls in that bucket)", s.Points[0].Drift)
	}
	if got := f64(t, s.Points[1].Drift, "drift"); !near(got, 50) {
		t.Errorf("day2 drift = %.2f, want 50 (derived 800 minus the reading 750)", got)
	}
	if !near(s.DriftMax, 50) {
		t.Errorf("drift_max = %.2f, want 50", s.DriftMax)
	}
}

// TestLedgerSeries_PathB_AnchorBucketIsTheReadingVerbatim and the reverse recurrence over
// several buckets matches a hand-computed series.
func TestLedgerSeries_PathB_AnchorBucketIsTheReadingVerbatim(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	// A liability with no running balance in the ledger, which is the shape that forces the
	// walk. class never flips a sign: the debt simply stays negative.
	a := seedAccount(t, ctx, client, "C", account.ClassLiability, account.TypeCreditCard)

	// Purchases (negative) and one repayment (positive), one per day.
	seedTxnNoAfter(t, ctx, client, a, "w1", utcDay(2026, 7, 10), -100, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, a, "w2", utcDay(2026, 7, 11), -200, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, a, "w3", utcDay(2026, 7, 12), 300, "PAYMENT RECEIVED")
	seedTxnNoAfter(t, ctx, client, a, "w4", utcDay(2026, 7, 13), -400, "MERCHANT")
	// The anchor: the only trustworthy level, on the newest bucket.
	seedSnapshot(t, ctx, client, a, -1000, mel(2026, 7, 13, 18).UTC())

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 4 {
		t.Fatalf("points = %d, want 4", len(s.Points))
	}

	// Hand-computed by close(n-1) = close(n) - net(n), newest first:
	//   Jul 13 close = -1000 (the reading, verbatim)
	//   Jul 12 close = -1000 - (-400) = -600
	//   Jul 11 close = -600  - (+300) = -900
	//   Jul 10 close = -900  - (-200) = -700
	// and open(Jul 10) = -700 - (-100) = -600.
	wantCloses := []float64{-700, -900, -600, -1000}
	for i, w := range wantCloses {
		if got := f64(t, s.Points[i].Close, "close"); !near(got, w) {
			t.Errorf("point %d close = %.2f, want %.2f", i, got, w)
		}
		if s.Points[i].Source != SourceAccumulated {
			t.Errorf("point %d source = %q, want %q", i, s.Points[i].Source, SourceAccumulated)
		}
	}
	if got := s.Points[3].Balance; !near(got, -1000) {
		t.Errorf("newest bucket balance = %.2f, want the anchor reading -1000 exactly", got)
	}
	if got := f64(t, s.Points[0].Open, "open"); !near(got, -600) {
		t.Errorf("earliest open = %.2f, want -600 (the recurrence extended one step)", got)
	}
	// open(n) == close(n-1) all the way along, and the flow invariant holds.
	for i := range s.Points {
		open := f64(t, s.Points[i].Open, "open")
		close := f64(t, s.Points[i].Close, "close")
		net := f64(t, s.Points[i].Net, "net")
		if !near(close-open, net) {
			t.Errorf("point %d: close - open = %.2f, want net %.2f", i, close-open, net)
		}
		if i > 0 && !near(open, f64(t, s.Points[i-1].Close, "prev close")) {
			t.Errorf("point %d: open != previous close", i)
		}
	}
	// The liability's debt is reported in its own convention, never made positive here.
	if f64(t, s.Last, "last") >= 0 {
		t.Errorf("last = %.2f, want a negative debt (the +asset/-liability rule applies at a SUM, not at a point)", f64(t, s.Last, "last"))
	}
}

// TestLedgerSeries_PathB_ForwardPastTheAnchor covers the case neither issue specifies: rows
// posted after the anchor reading are walked FORWARD with the mirrored recurrence.
func TestLedgerSeries_PathB_ForwardPastTheAnchor(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "C", account.ClassLiability, account.TypeCreditCard)

	seedTxnNoAfter(t, ctx, client, a, "f1", utcDay(2026, 7, 10), -100, "MERCHANT")
	seedSnapshot(t, ctx, client, a, -500, mel(2026, 7, 10, 18).UTC()) // anchor on the oldest bucket
	seedTxnNoAfter(t, ctx, client, a, "f2", utcDay(2026, 7, 11), -200, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, a, "f3", utcDay(2026, 7, 12), 50, "PAYMENT RECEIVED")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(s.Points))
	}
	// close(n+1) = close(n) + net(n+1): -500, then -700, then -650.
	for i, w := range []float64{-500, -700, -650} {
		if got := f64(t, s.Points[i].Close, "close"); !near(got, w) {
			t.Errorf("point %d close = %.2f, want %.2f", i, got, w)
		}
	}
}

// TestLedgerSeries_PathB_MissingAnchorEmitsNoSeries: with no reading to anchor on, an
// arbitrary row is never used as a level. The series comes back empty WITH a reason.
func TestLedgerSeries_PathB_MissingAnchorEmitsNoSeries(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "C", account.ClassLiability, account.TypeCreditCard)
	seedTxnNoAfter(t, ctx, client, a, "na1", utcDay(2026, 7, 10), -100, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, a, "na2", utcDay(2026, 7, 11), -200, "MERCHANT")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 0 {
		t.Errorf("points = %d, want 0 for an account with no anchor", len(s.Points))
	}
	if s.Note == "" {
		t.Error("note is empty: an empty series must say why rather than looking like no data")
	}
}

// TestLedgerSeries_InvestmentExcluded: an investment account is skipped from ledger
// derivation, with the reason, because its balance moves with the market and not with rows.
func TestLedgerSeries_InvestmentExcluded(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "I", account.ClassAsset, account.TypeInvestment)
	seedTxnAfter(t, ctx, client, a, "inv1", utcDay(2026, 7, 10), 1000, 1000, "CONTRIBUTION")
	seedSnapshot(t, ctx, client, a, 1500, mel(2026, 7, 11, 9).UTC())

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 0 {
		t.Errorf("points = %d, want 0: an investment account must be excluded from ledger derivation", len(s.Points))
	}
	if s.Note == "" {
		t.Error("note is empty: the exclusion must state why")
	}
	// The snapshot basis still serves it.
	snap, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisSnapshot})
	if err != nil {
		t.Fatalf("BalanceSeries(snapshot): %v", err)
	}
	if len(onlySeries(t, snap).Points) == 0 {
		t.Error("basis=snapshot returned nothing for an investment account, but only the LEDGER basis excludes it")
	}
}

// TestLedgerSeries_CarriedBucketHasZeroFlows: a quiet bucket repeats the previous close,
// is flagged carried with source "carried", and reports no flows.
func TestLedgerSeries_CarriedBucketHasZeroFlows(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedTxnAfter(t, ctx, client, a, "c1", utcDay(2026, 7, 10), -100, 900, "MERCHANT")
	// Jul 11 is quiet.
	seedTxnAfter(t, ctx, client, a, "c2", utcDay(2026, 7, 12), -200, 700, "MERCHANT")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 3 {
		t.Fatalf("points = %d, want 3", len(s.Points))
	}
	q := s.Points[1]
	if !q.Carried {
		t.Error("the quiet bucket was not flagged carried")
	}
	if q.Source != SourceCarried {
		t.Errorf("quiet bucket source = %q, want %q", q.Source, SourceCarried)
	}
	if got := f64(t, q.Close, "close"); got != 900 {
		t.Errorf("quiet bucket close = %.2f, want the previous close 900", got)
	}
	if f64(t, q.In, "in") != 0 || f64(t, q.Out, "out") != 0 || f64(t, q.Net, "net") != 0 {
		t.Errorf("quiet bucket flows = in %v / out %v / net %v, want zeros", q.In, q.Out, q.Net)
	}
	if q.Txns == nil || *q.Txns != 0 {
		t.Errorf("quiet bucket txns = %v, want 0", q.Txns)
	}
	if q.FlowMismatch {
		t.Error("a carried bucket must not be flagged flow_mismatch: no change and no flow agree")
	}
}

// TestLedgerSeries_FlowsMatchSpendingSummary is the tie-out that matters: per-bucket
// external figures must equal what spending_summary reports over the same window, or nobody
// can trust either. Internal transfers stay out of external_* but remain in gross in/out
// and in txns.
func TestLedgerSeries_FlowsMatchSpendingSummary(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	// Two invented masked numbers, so the internal-transfer classifier has an own-account
	// counterparty to recognise without reusing any value seeded elsewhere.
	checking := client.Account.Create().SetSource("commbank").SetName("A spending").
		SetMaskedNumber("xxxx 4242").SetType(account.TypeEveryday).SetClass(account.ClassAsset).
		SetCurrency("AUD").SaveX(ctx)
	client.Account.Create().SetSource("commbank").SetName("B saving").
		SetMaskedNumber("xxxx 4343").SetType(account.TypeSavings).SetClass(account.ClassAsset).
		SetCurrency("AUD").SaveX(ctx)

	day := utcDay(2026, 7, 15)
	// External in, external out, and an internal transfer to the owner's own second account.
	seedTxnAfter(t, ctx, client, checking, "ts1", day, 3000, 4000, "SALARY")
	seedTxnAfter(t, ctx, client, checking, "ts2", day, -500, 3500, "RENT DIRECT DEBIT")
	seedTxnAfter(t, ctx, client, checking, "ts3", day, -1000, 2500, "Transfer to xx4343")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: checking.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(s.Points))
	}
	p := s.Points[0]

	bucket, err := SpendingSummary(ctx, client, checking.ID, day, day)
	if err != nil {
		t.Fatalf("SpendingSummary: %v", err)
	}
	if got := f64(t, p.ExternalIn, "external_in"); !near(got, bucket.Income) {
		t.Errorf("external_in = %.2f, want spending_summary income %.2f", got, bucket.Income)
	}
	if got := f64(t, p.ExternalOut, "external_out"); !near(got, bucket.Spend) {
		t.Errorf("external_out = %.2f, want spending_summary spend %.2f", got, bucket.Spend)
	}
	if got := f64(t, p.ExternalOut, "external_out"); got < 0 {
		t.Errorf("external_out = %.2f, want a positive magnitude", got)
	}
	// Gross figures still include the internal leg, and txns counts it.
	if got := f64(t, p.In, "in"); !near(got, 3000) {
		t.Errorf("gross in = %.2f, want 3000", got)
	}
	if got := f64(t, p.Out, "out"); !near(got, 1500) {
		t.Errorf("gross out = %.2f, want 1500 (500 rent + the 1000 internal leg), as a positive magnitude", got)
	}
	if got := f64(t, p.Net, "net"); !near(got, 1500) {
		t.Errorf("net = %.2f, want 1500 (in - out)", got)
	}
	if p.Txns == nil || *p.Txns != 3 {
		t.Errorf("txns = %v, want 3 (internal legs included)", p.Txns)
	}
	if !near(f64(t, p.ExternalIn, "external_in"), 3000) || !near(f64(t, p.ExternalOut, "external_out"), 500) {
		t.Errorf("external figures = %v / %v, want 3000 / 500 (the internal transfer excluded from both)", p.ExternalIn, p.ExternalOut)
	}
}

// TestLedgerSeries_NoBucketBeforeMinPostedDate: the derived series refuses to reach back
// past the oldest posted row even when asked to, and reports ledger_from so a caller can
// tell a flat line from a ledger that does not go that far.
func TestLedgerSeries_NoBucketBeforeMinPostedDate(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedTxnAfter(t, ctx, client, a, "mp1", utcDay(2026, 7, 20), -100, 900, "MERCHANT")
	seedTxnAfter(t, ctx, client, a, "mp2", utcDay(2026, 7, 21), -100, 800, "MERCHANT")

	from := utcDay(2026, 1, 1)
	to := utcDay(2026, 7, 22)
	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger, From: &from, To: &to})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 3 {
		t.Fatalf("points = %d, want 3 (Jul 20..22); buckets before the oldest posted row were emitted", len(s.Points))
	}
	if s.Points[0].AsOf.Day() != 20 {
		t.Errorf("first point is day %d, want 20 == MIN(posted_date)", s.Points[0].AsOf.Day())
	}
	if s.LedgerFrom == nil || !s.LedgerFrom.Equal(utcDay(2026, 7, 20)) {
		t.Errorf("ledger_from = %v, want 2026-07-20", s.LedgerFrom)
	}
	if !s.StartUnverified {
		t.Error("start_unverified = false, want true on the earliest emitted bucket")
	}
}

// TestLedgerSeries_WeekBucketsStartMonday over the ledger basis, with the closes taken from
// each week's last row.
func TestLedgerSeries_WeekBucketsStartMonday(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	// Week of Mon 2026-07-13: rows on Tue and Fri. Week of Mon 2026-07-20: one row.
	seedTxnAfter(t, ctx, client, a, "wk1", utcDay(2026, 7, 14), -100, 900, "MERCHANT")
	seedTxnAfter(t, ctx, client, a, "wk2", utcDay(2026, 7, 17), -200, 700, "MERCHANT")
	seedTxnAfter(t, ctx, client, a, "wk3", utcDay(2026, 7, 22), -50, 650, "MERCHANT")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepWeek, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 2 {
		t.Fatalf("points = %d, want 2 week buckets", len(s.Points))
	}
	for i, wantDay := range []int{13, 20} {
		if s.Points[i].AsOf.Weekday() != time.Monday {
			t.Errorf("week %d starts %s, want Monday", i, s.Points[i].AsOf.Weekday())
		}
		if s.Points[i].AsOf.Day() != wantDay {
			t.Errorf("week %d starts on day %d, want %d", i, s.Points[i].AsOf.Day(), wantDay)
		}
	}
	if got := f64(t, s.Points[0].Close, "close"); got != 700 {
		t.Errorf("week 1 close = %.2f, want 700 (the week's LAST row)", got)
	}
	if got := f64(t, s.Points[1].Open, "open"); got != 700 {
		t.Errorf("week 2 open = %.2f, want 700 == week 1 close", got)
	}
}

// TestLedgerSeries_CoverageProbeChoosesPerAccount: two accounts in one fan-out get different
// strategies from the same call, decided by measured coverage rather than by class.
func TestLedgerSeries_CoverageProbeChoosesPerAccount(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	// Deliberately a LIABILITY that carries running balances, and an ASSET that does not, so
	// a class-based shortcut would pick the wrong strategy for both.
	liabWithAfter := seedAccount(t, ctx, client, "A liability with running balances", account.ClassLiability, account.TypeCreditCard)
	assetNoAfter := seedAccount(t, ctx, client, "B asset without running balances", account.ClassAsset, account.TypeSavings)

	seedTxnAfter(t, ctx, client, liabWithAfter, "cp1", utcDay(2026, 7, 10), -100, -900, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, assetNoAfter, "cp2", utcDay(2026, 7, 10), -100, "MERCHANT")
	seedSnapshot(t, ctx, client, assetNoAfter, 400, mel(2026, 7, 10, 18).UTC())

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("series = %d, want 2 (one per account)", len(series))
	}
	byID := map[int]BalanceSeriesView{}
	for _, s := range series {
		byID[s.AccountID] = s
	}
	if got := byID[liabWithAfter.ID].Points[0].Source; got != SourceBalanceAfter {
		t.Errorf("liability-with-coverage source = %q, want %q (coverage decides, not class)", got, SourceBalanceAfter)
	}
	if got := byID[assetNoAfter.ID].Points[0].Source; got != SourceAccumulated {
		t.Errorf("asset-without-coverage source = %q, want %q (coverage decides, not class)", got, SourceAccumulated)
	}
	if got := byID[assetNoAfter.ID].Points[0].Balance; !near(got, 400) {
		t.Errorf("walked account close = %.2f, want the anchor reading 400", got)
	}
}

// TestLedgerSeries_MixedCoverageBucketAccumulates: on an account the probe finds covered, a
// bucket whose rows happen to carry no running balance accumulates forward from the previous
// close and says so, rather than inventing a level or silently reporting a stale one.
func TestLedgerSeries_MixedCoverageBucketAccumulates(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedTxnAfter(t, ctx, client, a, "mx1", utcDay(2026, 7, 10), -100, 900, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, a, "mx2", utcDay(2026, 7, 11), -200, "MERCHANT")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(s.Points))
	}
	p := s.Points[1]
	if p.Source != SourceAccumulated {
		t.Errorf("uncovered bucket source = %q, want %q", p.Source, SourceAccumulated)
	}
	if got := f64(t, p.Close, "close"); !near(got, 700) {
		t.Errorf("uncovered bucket close = %.2f, want 700 (900 + net -200)", got)
	}
	if p.Carried {
		t.Error("a bucket with rows was flagged carried")
	}
}

// TestLedgerSeries_RowToRowCheckFlagsTheBucket: consecutive running balances that do not
// differ by the intervening amount flag the bucket holding the later row.
func TestLedgerSeries_RowToRowCheckFlagsTheBucket(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedTxnAfter(t, ctx, client, a, "rr1", utcDay(2026, 7, 10), -100, 900, "MERCHANT")
	// 900 -> 600 is a 300 move, but the row says -100.
	seedTxnAfter(t, ctx, client, a, "rr2", utcDay(2026, 7, 11), -100, 600, "MERCHANT")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if !s.Points[1].FlowMismatch {
		t.Error("flow_mismatch = false on the bucket whose running balance does not tie to its amount")
	}
}

// TestLedgerSeries_WindowedPathANeverFabricatesZero is the regression for the worst output
// this code could produce. The window opens AFTER the newest snapshot and the bucket
// immediately before it holds no running-balance row, which is common at step=day. Reading a
// previous close straight out of the preceding bucket returned 0 there, so every emitted
// point came back as a confident zero balance.
func TestLedgerSeries_WindowedPathANeverFabricatesZero(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)

	seedTxnAfter(t, ctx, client, a, "z1", utcDay(2026, 6, 1), -10, 990, "MERCHANT")
	seedTxnAfter(t, ctx, client, a, "z2", utcDay(2026, 6, 3), -20, 970, "MERCHANT")
	// The newest reading is on Jun 1, well before the window.
	seedSnapshot(t, ctx, client, a, 990, mel(2026, 6, 1, 18).UTC())

	from := utcDay(2026, 6, 5)
	to := utcDay(2026, 6, 6)
	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{
		AccountID: a.ID, Step: StepDay, Basis: BasisLedger, From: &from, To: &to,
	})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 2 {
		t.Fatalf("points = %d, want 2 (Jun 5 and Jun 6)", len(s.Points))
	}
	for i, p := range s.Points {
		if p.Balance == 0 {
			t.Fatalf("point %d balance = 0: a level was fabricated out of an unassigned bucket", i)
		}
		if !near(p.Balance, 970) {
			t.Errorf("point %d balance = %.2f, want the last known close 970", i, p.Balance)
		}
		if got := f64(t, p.Open, "open"); !near(got, 970) {
			t.Errorf("point %d open = %.2f, want 970", i, got)
		}
		if !p.Carried || p.Source != SourceCarried {
			t.Errorf("point %d = carried %v / source %q, want a carried repeat", i, p.Carried, p.Source)
		}
		if p.FlowMismatch {
			t.Errorf("point %d flagged flow_mismatch on a quiet carried bucket", i)
		}
	}
	// The level was established before the window, so its opening is a real previous close.
	if s.StartUnverified {
		t.Error("start_unverified = true, but the level came from a bucket before the window")
	}
}

// TestLedgerSeries_NoFabricatedZeroAcrossAnyWindow sweeps every single-day and multi-day
// window over a sparse fixture on BOTH derivation paths and asserts no emitted point is a
// level nobody ever measured. The structural guarantee is that a point is only emitted once a
// level has been established, either by the anchor reading or by a bucket carrying the bank's
// own running balance, so a zero cannot be reached; this is the empirical half of that.
func TestLedgerSeries_NoFabricatedZeroAcrossAnyWindow(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	covered := seedAccount(t, ctx, client, "A covered", account.ClassAsset, account.TypeEveryday)
	walked := seedAccount(t, ctx, client, "B walked", account.ClassLiability, account.TypeCreditCard)

	// Sparse on purpose: long quiet stretches either side of the rows, so most windows open on
	// a bucket with nothing in it.
	seedTxnAfter(t, ctx, client, covered, "s1", utcDay(2026, 6, 5), -10, 990, "MERCHANT")
	seedTxnAfter(t, ctx, client, covered, "s2", utcDay(2026, 6, 12), -20, 970, "MERCHANT")
	seedSnapshot(t, ctx, client, covered, 990, mel(2026, 6, 5, 18).UTC())

	seedTxnNoAfter(t, ctx, client, walked, "w1", utcDay(2026, 6, 5), -10, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, walked, "w2", utcDay(2026, 6, 12), -20, "MERCHANT")
	seedSnapshot(t, ctx, client, walked, -500, mel(2026, 6, 12, 18).UTC())

	for _, accID := range []int{covered.ID, walked.ID} {
		for _, step := range []BalanceStep{StepDay, StepWeek, StepMonth} {
			for startDay := 1; startDay <= 25; startDay++ {
				for _, length := range []int{0, 3, 10} {
					from := utcDay(2026, 6, startDay)
					to := utcDay(2026, 6, startDay+length)
					series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{
						AccountID: accID, Step: step, Basis: BasisLedger, From: &from, To: &to,
					})
					if err != nil {
						t.Fatalf("BalanceSeries: %v", err)
					}
					s := onlySeries(t, series)
					for i, p := range s.Points {
						if p.Balance == 0 {
							t.Fatalf("acct %d step %s window Jun %d+%d: point %d has a fabricated zero balance",
								accID, step, startDay, length, i)
						}
						// Every point must also carry an open, and a carried point must repeat
						// the level rather than reset it.
						if p.Open == nil || p.Close == nil {
							t.Fatalf("acct %d step %s window Jun %d+%d: point %d missing open/close",
								accID, step, startDay, length, i)
						}
						if p.Carried && !near(*p.Open, *p.Close) {
							t.Fatalf("acct %d step %s window Jun %d+%d: carried point %d moved the level",
								accID, step, startDay, length, i)
						}
					}
				}
			}
		}
	}
}

// TestLedgerSeries_NoLevelBeforeWindowEmitsNothing: with no running-balance row anywhere at
// or before the requested buckets, there is nothing to carry in, so leading buckets are
// omitted rather than filled with a zero.
func TestLedgerSeries_NoLevelBeforeWindowEmitsNothing(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	// The only covered row is AFTER the window, so within the window nothing is known.
	seedTxnAfter(t, ctx, client, a, "nl1", utcDay(2026, 6, 10), -10, 990, "MERCHANT")

	from := utcDay(2026, 6, 10)
	to := utcDay(2026, 6, 11)
	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{
		AccountID: a.ID, Step: StepDay, Basis: BasisLedger, From: &from, To: &to,
	})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	// Jun 10 establishes the level, Jun 11 carries it. Neither is a zero.
	if len(s.Points) != 2 {
		t.Fatalf("points = %d, want 2", len(s.Points))
	}
	if !near(s.Points[0].Balance, 990) || !near(s.Points[1].Balance, 990) {
		t.Errorf("balances = %.2f/%.2f, want 990/990", s.Points[0].Balance, s.Points[1].Balance)
	}
	if !s.StartUnverified {
		t.Error("start_unverified = false, but the first emitted bucket's opening was synthesized")
	}
}

// TestLedgerSeries_MixedBucketClosesOnTheAccumulatedLevel: when a bucket's LAST row carries
// no running balance, the close must be accumulated the remaining distance rather than
// stopping at a stale mid-bucket figure, and the provenance must not claim to be the bank's.
func TestLedgerSeries_MixedBucketClosesOnTheAccumulatedLevel(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)

	seedTxnAfter(t, ctx, client, a, "mb1", utcDay(2026, 6, 1), -10, 990, "MERCHANT")
	seedTxnAfter(t, ctx, client, a, "mb2", utcDay(2026, 6, 2), -20, 970, "MERCHANT")
	// Trailing row with no running balance: the true close is 970 - 5 = 965.
	seedTxnNoAfter(t, ctx, client, a, "mb3", utcDay(2026, 6, 3), -5, "MERCHANT")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepMonth, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 1 {
		t.Fatalf("points = %d, want 1 month bucket", len(s.Points))
	}
	p := s.Points[0]
	if got := f64(t, p.Close, "close"); !near(got, 965) {
		t.Errorf("close = %.2f, want 965 (970 accumulated over the trailing uncovered row)", got)
	}
	if p.Source != SourceAccumulated {
		t.Errorf("source = %q, want %q: the close is not the bank's own figure", p.Source, SourceAccumulated)
	}
	// Opening the month is 1000, and close - open must still equal net.
	if got := f64(t, p.Open, "open"); !near(got, 1000) {
		t.Errorf("open = %.2f, want 1000", got)
	}
	if p.FlowMismatch {
		t.Error("flow_mismatch set on a bucket that now ties out")
	}
}

// TestLedgerSeries_BothPathsAgreeOnQuietBuckets: an empty bucket must report carried with
// zero flows on the walked path exactly as it does on the running-balance path. Before the
// fix the walk assigned every bucket a close, so a walked series never produced a carried
// point at all, which killed the chart's dashed rendering for the accounts that need it most.
func TestLedgerSeries_BothPathsAgreeOnQuietBuckets(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	// Same amounts, same days, on two accounts: one carrying running balances, one not.
	covered := seedAccount(t, ctx, client, "A covered", account.ClassAsset, account.TypeEveryday)
	walked := seedAccount(t, ctx, client, "B walked", account.ClassAsset, account.TypeSavings)

	// Jun 2 is deliberately quiet on both.
	seedTxnAfter(t, ctx, client, covered, "bp1", utcDay(2026, 6, 1), -100, 900, "MERCHANT")
	seedTxnAfter(t, ctx, client, covered, "bp2", utcDay(2026, 6, 3), -200, 700, "MERCHANT")
	seedTxnAfter(t, ctx, client, covered, "bp3", utcDay(2026, 6, 4), -50, 650, "MERCHANT")

	seedTxnNoAfter(t, ctx, client, walked, "bw1", utcDay(2026, 6, 1), -100, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, walked, "bw2", utcDay(2026, 6, 3), -200, "MERCHANT")
	seedTxnNoAfter(t, ctx, client, walked, "bw3", utcDay(2026, 6, 4), -50, "MERCHANT")
	// Anchor the walk on the newest bucket at the same level the covered account closes at.
	seedSnapshot(t, ctx, client, walked, 650, mel(2026, 6, 4, 18).UTC())

	get := func(id int) BalanceSeriesView {
		series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: id, Step: StepDay, Basis: BasisLedger})
		if err != nil {
			t.Fatalf("BalanceSeries: %v", err)
		}
		return onlySeries(t, series)
	}
	c := get(covered.ID)
	w := get(walked.ID)

	if len(c.Points) != 4 || len(w.Points) != 4 {
		t.Fatalf("points = %d covered / %d walked, want 4 each", len(c.Points), len(w.Points))
	}
	// The quiet Jun 2 bucket, index 1, must look the same on both paths.
	for name, s := range map[string]BalanceSeriesView{"covered": c, "walked": w} {
		q := s.Points[1]
		if !q.Carried {
			t.Errorf("%s: quiet bucket carried = false, want true", name)
		}
		if q.Source != SourceCarried {
			t.Errorf("%s: quiet bucket source = %q, want %q", name, q.Source, SourceCarried)
		}
		if f64(t, q.In, "in") != 0 || f64(t, q.Out, "out") != 0 || f64(t, q.Net, "net") != 0 {
			t.Errorf("%s: quiet bucket has nonzero flows", name)
		}
		if q.Txns == nil || *q.Txns != 0 {
			t.Errorf("%s: quiet bucket txns = %v, want 0", name, q.Txns)
		}
		if q.FlowMismatch {
			t.Errorf("%s: quiet bucket flagged flow_mismatch", name)
		}
	}
	// And the two paths must agree on every close and open, since the data is identical.
	for i := range c.Points {
		if !near(c.Points[i].Balance, w.Points[i].Balance) {
			t.Errorf("point %d: covered close %.2f != walked close %.2f", i, c.Points[i].Balance, w.Points[i].Balance)
		}
		if !near(f64(t, c.Points[i].Open, "open"), f64(t, w.Points[i].Open, "open")) {
			t.Errorf("point %d: opens disagree between the two paths", i)
		}
	}
}

// TestLedgerSeries_OffsettingRowErrorsStillFlag: two row-level errors that cancel leave the
// bucket total intact, so the close - open == net check stays silent. The consecutive
// running-balance check is what catches them.
func TestLedgerSeries_OffsettingRowErrorsStillFlag(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)

	// 900 -> 750 is -150 against a stated -100; 750 -> 700 is -50 against a stated -100. The
	// two errors offset, so over the month close - open == net exactly.
	seedTxnAfter(t, ctx, client, a, "of1", utcDay(2026, 6, 1), -100, 900, "MERCHANT")
	seedTxnAfter(t, ctx, client, a, "of2", utcDay(2026, 6, 2), -100, 750, "MERCHANT")
	seedTxnAfter(t, ctx, client, a, "of3", utcDay(2026, 6, 3), -100, 700, "MERCHANT")

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepMonth, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	if len(s.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(s.Points))
	}
	p := s.Points[0]
	open := f64(t, p.Open, "open")
	close := f64(t, p.Close, "close")
	net := f64(t, p.Net, "net")
	if !near(close-open, net) {
		t.Fatalf("fixture is wrong: close - open = %.2f already differs from net %.2f, so the bucket check would catch it", close-open, net)
	}
	if !p.FlowMismatch {
		t.Error("flow_mismatch = false: offsetting row-level errors escaped both checks")
	}
}

// TestSnapshotSeries_MultiCurrencyReportsItsOwn: each series carries its own currency, with
// no mixing and no conversion.
func TestSnapshotSeries_MultiCurrencyReportsItsOwn(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	aud := client.Account.Create().SetSource("commbank").SetName("A local").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("AUD").SaveX(ctx)
	usd := client.Account.Create().SetSource("commbank").SetName("B foreign").
		SetType(account.TypeEveryday).SetClass(account.ClassAsset).SetCurrency("USD").SaveX(ctx)
	seedSnapshot(t, ctx, client, aud, 100, mel(2026, 7, 10, 9).UTC())
	seedSnapshot(t, ctx, client, usd, 200, mel(2026, 7, 10, 9).UTC())

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{Step: StepDay})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("series = %d, want 2", len(series))
	}
	got := map[string]string{}
	for _, s := range series {
		got[s.AccountName] = s.Currency
	}
	if got["A local"] != "AUD" || got["B foreign"] != "USD" {
		t.Errorf("currencies = %v, want each series reporting its own", got)
	}
}

// TestLedgerSeries_PendingIsOutOfScope: PendingTransaction is a separate volatile entity and
// derivation is posted-only, so a pending row must not move any figure.
func TestLedgerSeries_PendingIsOutOfScope(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedTxnAfter(t, ctx, client, a, "pd1", utcDay(2026, 7, 10), -100, 900, "MERCHANT")
	client.PendingTransaction.Create().
		SetDate(utcDay(2026, 7, 10)).SetAmount(-5000).SetDescription("PENDING MERCHANT").
		SetAccountID(a.ID).SaveX(ctx)

	series, err := BalanceSeries(ctx, client, BalanceSeriesFilter{AccountID: a.ID, Step: StepDay, Basis: BasisLedger})
	if err != nil {
		t.Fatalf("BalanceSeries: %v", err)
	}
	s := onlySeries(t, series)
	p := s.Points[0]
	if got := f64(t, p.Close, "close"); got != 900 {
		t.Errorf("close = %.2f, want 900: a pending row leaked into the derivation", got)
	}
	if p.Txns == nil || *p.Txns != 1 {
		t.Errorf("txns = %v, want 1 (posted only)", p.Txns)
	}
}
