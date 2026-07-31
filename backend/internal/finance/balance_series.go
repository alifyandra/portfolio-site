package finance

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/balancesnapshot"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
)

// Bucketed balance series (portfolio-site#124) and the ledger-derived basis on the same
// grammar (#127). Part of the pure read layer (ADR 0017): the HTTP handler and the MCP
// tool are thin adapters over BalanceSeries, so the dashboard and the LLM cannot report
// different numbers. Nothing here rounds; the caller formats.
//
// Two things drive every decision below.
//
// A balance is a STOCK (a level at an instant), not a FLOW (a quantity accumulated over
// a period). The correct bucket aggregate for a stock is the LAST reading in the bucket,
// which is close-of-period. Summing balances is meaningless and averaging them invents a
// figure the bank never reported, so neither happens anywhere in this file.
//
// A bucket with no reading repeats the previous close and is flagged Carried, because for
// a stock "no reading" means "as far as we know the level did not change", not "unknown".
// Carry only ever runs forward: buckets before a series' start are omitted, never
// back-filled, so no pre-history is invented.

// bucketLoc is the zone bucket boundaries align to. Bucket edges have to land on the
// owner's local day or a monthly series is wrong at the edges: a reading taken at 9am
// Melbourne on the local 1st is 23:00 UTC on the previous month's last day, so a
// UTC-bucketed month files it under the wrong month. Resolved once at package level
// (same shape as internal/jobs.scheduler); the Dockerfile builds with -tags timetzdata
// so LoadLocation works under CGO_ENABLED=0.
//
// Known and deliberately not fixed here: MonthlySummary buckets on UTC month edges, so
// its months and these disagree by 10 or 11 hours. Aligning it would shift figures that
// have already been looked at, so it is a separate change.
var bucketLoc = loadBucketLoc()

func loadBucketLoc() *time.Location {
	loc, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		// The zone database is compiled in, so this cannot happen in a built binary.
		// Degrading to UTC keeps the read side answering rather than killing the process;
		// the bucket edges would shift by 10 or 11 hours, hence the loud log.
		slog.Error("finance: Australia/Melbourne unavailable, bucketing balance series on UTC instead", "err", err)
		return time.UTC
	}
	return loc
}

// flowEpsilon is the tolerance for the reconciliation comparisons. Money is carried as
// float64, so an exact == on a sum of amounts would fire on representation noise; half a
// cent is well below anything a real dropped or duplicated row would produce.
const flowEpsilon = 0.005

// BalanceStep is the bucket width of a balance series. The zero value is the raw
// per-reading list, which is what the balances endpoint returned before #124, so an
// existing caller that sends no step keeps its current output.
type BalanceStep string

const (
	StepRaw   BalanceStep = ""
	StepDay   BalanceStep = "day"
	StepWeek  BalanceStep = "week"
	StepMonth BalanceStep = "month"
)

// ParseBalanceStep validates a wire step value. An unknown value is an error rather than
// a silent fall back to raw: a caller that mistypes "monthly" must be told, not handed a
// differently-shaped series it will misread.
func ParseBalanceStep(s string) (BalanceStep, error) {
	switch BalanceStep(s) {
	case StepRaw, StepDay, StepWeek, StepMonth:
		return BalanceStep(s), nil
	}
	return "", fmt.Errorf("unknown step %q (want day, week or month, or omit it for the raw readings)", s)
}

// BalanceBasis selects where a series' closes come from. BasisSnapshot reads the
// append-only BalanceSnapshot readings (the bank's own figures) and is the default, so
// #124's behaviour is what an existing caller keeps getting. BasisLedger derives the
// closes from posted transactions and adds per-bucket open/in/out.
type BalanceBasis string

const (
	BasisSnapshot BalanceBasis = "snapshot"
	BasisLedger   BalanceBasis = "ledger"
)

// ParseBalanceBasis validates a wire basis value; empty means the snapshot default and an
// unknown value is an error rather than a silent fall back.
func ParseBalanceBasis(s string) (BalanceBasis, error) {
	switch BalanceBasis(s) {
	case "", BasisSnapshot:
		return BasisSnapshot, nil
	case BasisLedger:
		return BasisLedger, nil
	}
	return "", fmt.Errorf("unknown basis %q (want snapshot or ledger)", s)
}

// Per-point provenance under BasisLedger. A ledger series is not uniformly authoritative
// and the caller needs to know which kind of number it is reading.
const (
	// SourceBalanceAfter is the bank's own running balance at the bucket's last row. It
	// cannot drift.
	SourceBalanceAfter = "balance_after"
	// SourceAccumulated is arithmetic from the anchor reading. It is exact only if the
	// ledger in between is complete.
	SourceAccumulated = "accumulated"
	// SourceCarried is a repeat of the previous close for a bucket with no activity.
	SourceCarried = "carried"
)

// BalanceSeriesFilter narrows a balance series query. AccountID 0 spans every account,
// each returned as its own series. From/To are inclusive bounds; a nil bound is open. The
// bucket a bound falls in is included whole, so From/To are read at bucket granularity.
type BalanceSeriesFilter struct {
	AccountID int
	From      *time.Time
	To        *time.Time
	Step      BalanceStep
	Basis     BalanceBasis
}

// BalanceSeriesView is one account's balance series plus the roll-up a caller needs to
// answer "is this going up or down" without walking the points. First/Last/Change always
// describe the whole computed series even if a caller later trims points for a token
// budget, so the direction stays true to what was asked for.
//
// The ledger-only fields are zero under BasisSnapshot. LedgerFrom is the account's oldest
// posted row, which is the backward edge of what can be derived; a caller comparing two
// accounts needs it to tell "flat" from "no ledger that far back". Note carries the reason
// a series came back empty (an investment account, or no anchor to walk from) so a silent
// empty array is never the whole answer.
type BalanceSeriesView struct {
	AccountID   int
	AccountName string
	Class       string
	Currency    string
	Basis       BalanceBasis
	Step        BalanceStep
	Points      []BalancePoint

	First     *float64
	Last      *float64
	Change    *float64
	ChangePct *float64

	LedgerFrom      *time.Time
	StartUnverified bool
	DriftMax        float64
	Note            string
}

// BalanceSeries computes one series per matching account. Bucketing runs in Go over rows
// loaded by date range, not in SQL: the finance tests run on in-memory sqlite which has no
// date_trunc, and the bucket rules (last-in-bucket, carry-forward, no back-fill, local
// boundaries, DST) are exactly the part that needs unit tests. The dataset is one reading
// per account per day, so there is no volume argument for pushing the work down either.
// It follows MonthlySummary's shape: load the window whole, pre-seed the buckets, fold in
// Go.
//
// StepRaw is coerced to StepDay here because a series with open/close/flows has to have a
// period; the raw reading list has no bucket to attach them to. The balances endpoint
// keeps serving raw output by calling BalanceHistory directly instead.
func BalanceSeries(ctx context.Context, client *ent.Client, f BalanceSeriesFilter) ([]BalanceSeriesView, error) {
	if f.Step == StepRaw {
		f.Step = StepDay
	}
	if f.Basis == "" {
		f.Basis = BasisSnapshot
	}

	accs, err := seriesAccounts(ctx, client, f.AccountID)
	if err != nil {
		return nil, err
	}

	// The owner's own account numbers tell an internal transfer from a payment to someone
	// else, so they are loaded across every account regardless of the AccountID filter
	// (a transfer is still internal when the query is scoped to one side of the move).
	// Only the ledger basis classifies flows.
	var own4 map[string]bool
	if f.Basis == BasisLedger {
		own4, err = loadOwnLast4(ctx, client)
		if err != nil {
			return nil, err
		}
	}

	out := make([]BalanceSeriesView, 0, len(accs))
	for _, acc := range accs {
		var v BalanceSeriesView
		if f.Basis == BasisLedger {
			v, err = ledgerSeries(ctx, client, acc, f, own4)
		} else {
			v, err = snapshotSeries(ctx, client, acc, f)
		}
		if err != nil {
			return nil, err
		}
		v.finalize()
		out = append(out, v)
	}
	return out, nil
}

// seriesAccounts resolves the accounts a series query covers, name-ordered like Accounts
// so a fan-out response is stable. AccountID 0 spans all of them.
func seriesAccounts(ctx context.Context, client *ent.Client, accountID int) ([]*ent.Account, error) {
	q := client.Account.Query().Order(ent.Asc(account.FieldName))
	if accountID != 0 {
		q = q.Where(account.IDEQ(accountID))
	}
	return q.All(ctx)
}

// LocalToday is today's date in the bucket zone, normalised to UTC midnight the way every
// stored finance date is, so it keys straight into today's bucket. Callers that need a
// default "to" bound use it rather than time.Now().UTC(), which is the previous local day
// for the first ten hours of every Melbourne day.
func LocalToday() time.Time {
	n := time.Now().In(bucketLoc)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// bucketStart maps an instant to the START of the bucket containing it, as a local-zone
// instant. A day bucket runs local midnight to local midnight, a week starts local Monday,
// a month starts the local 1st.
//
// Every timestamp reaching here is converted to the local zone first, which is what makes
// the two kinds of input agree: posted_date is already UTC-midnight-normalised, and UTC
// midnight is 10 or 11am the SAME Melbourne day, so a date-only row keys into the day a
// human would call it. A snapshot as_of is a real instant, so the conversion is what puts
// a 9am local reading in the right month.
func bucketStart(t time.Time, step BalanceStep) time.Time {
	lt := t.In(bucketLoc)
	switch step {
	case StepWeek:
		// Go's Weekday has Sunday at 0; shift so Monday is 0 and walk back that many days.
		// time.Date normalises a non-positive day-of-month, so month/year edges need no
		// special case.
		back := (int(lt.Weekday()) + 6) % 7
		return time.Date(lt.Year(), lt.Month(), lt.Day()-back, 0, 0, 0, 0, bucketLoc)
	case StepMonth:
		return time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, bucketLoc)
	default:
		return time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, bucketLoc)
	}
}

// nextBucketStart is the exclusive upper edge of the bucket starting at start, so a bucket
// spans [start, nextBucketStart).
//
// It steps the CALENDAR date and re-derives local midnight rather than adding 24h, which
// is what makes a DST transition neither drop nor duplicate a bucket: the day the clocks
// move is 23 or 25 hours long but still exactly one calendar day, and Melbourne's
// transitions happen at 2am/3am local so local midnight always exists.
func nextBucketStart(start time.Time, step BalanceStep) time.Time {
	switch step {
	case StepWeek:
		return time.Date(start.Year(), start.Month(), start.Day()+7, 0, 0, 0, 0, bucketLoc)
	case StepMonth:
		return time.Date(start.Year(), start.Month()+1, 1, 0, 0, 0, 0, bucketLoc)
	default:
		return time.Date(start.Year(), start.Month(), start.Day()+1, 0, 0, 0, 0, bucketLoc)
	}
}

// bucketKey is a map key for a bucket start. A time.Time is a poor map key (a monotonic
// reading and the location pointer both take part in ==), so buckets are keyed by their
// start's Unix second, which is unique per bucket.
func bucketKey(t time.Time) int64 { return t.Unix() }

// --- basis=snapshot ---

// snapshotSeries buckets an account's BalanceSnapshot readings. The bucket close is the
// LAST reading in the bucket by (as_of, id), matching the ordering BalanceHistory already
// uses, so every plotted point is a figure the bank actually sent.
func snapshotSeries(ctx context.Context, client *ent.Client, acc *ent.Account, f BalanceSeriesFilter) (BalanceSeriesView, error) {
	v := newSeriesView(acc, f)

	// The query bound is the bucket's own start instant, not the raw From. From arrives as
	// a UTC-midnight date, and the bucket it names begins 10 or 11 hours earlier in UTC, so
	// filtering on the raw value would drop a 9am-local reading that belongs to the first
	// bucket.
	var qFrom *time.Time
	if f.From != nil {
		s := bucketStart(*f.From, f.Step)
		qFrom = &s
	}
	readings, err := BalanceHistory(ctx, client, acc.ID, qFrom)
	if err != nil {
		return v, err
	}
	if f.To != nil {
		end := nextBucketStart(bucketStart(*f.To, f.Step), f.Step)
		kept := make([]BalancePoint, 0, len(readings))
		for _, r := range readings {
			if r.AsOf.Before(end) {
				kept = append(kept, r)
			}
		}
		readings = kept
	}

	// prime is the newest reading STRICTLY BEFORE the window. A stock's level persists, so
	// a windowed view opens at the last known level instead of a hole; without this a
	// 12-month request against an account last read 13 months ago returns nothing and reads
	// as "no data". This is still forward carry, and it never reaches back before the
	// account's first ever reading because there is nothing there to find.
	var prime *float64
	if qFrom != nil {
		s, err := client.BalanceSnapshot.Query().
			Where(
				balancesnapshot.HasAccountWith(account.IDEQ(acc.ID)),
				balancesnapshot.AsOfLT(*qFrom),
			).
			Order(ent.Desc(balancesnapshot.FieldAsOf), ent.Desc(balancesnapshot.FieldID)).
			First(ctx)
		if err != nil && !ent.IsNotFound(err) {
			return v, err
		}
		if s != nil {
			b := s.Balance
			prime = &b
		}
	}

	if len(readings) == 0 && prime == nil {
		return v, nil
	}

	// Readings arrive ascending by (as_of, id), so overwriting the bucket's entry leaves
	// the LAST reading in it: close-of-period, no averaging, no summing.
	closes := make(map[int64]float64, len(readings))
	for _, r := range readings {
		closes[bucketKey(bucketStart(r.AsOf, f.Step))] = r.Balance
	}

	var lo time.Time
	if f.From != nil {
		lo = bucketStart(*f.From, f.Step)
	}
	if prime == nil && len(readings) > 0 {
		// No level is known before the window, so the series starts at its first reading's
		// bucket. Nothing earlier is back-filled.
		first := bucketStart(readings[0].AsOf, f.Step)
		if lo.IsZero() || first.After(lo) {
			lo = first
		}
	}
	if lo.IsZero() {
		return v, nil
	}

	// With no upper bound the series ends at the last real reading rather than inventing
	// carried buckets up to today; a caller that wants the line brought to today passes To.
	var hi time.Time
	switch {
	case f.To != nil:
		hi = bucketStart(*f.To, f.Step)
	case len(readings) > 0:
		hi = bucketStart(readings[len(readings)-1].AsOf, f.Step)
	default:
		hi = lo
	}
	if hi.Before(lo) {
		return v, nil
	}

	var prev float64
	havePrev := false
	if prime != nil {
		prev, havePrev = *prime, true
	}
	for b := lo; !b.After(hi); b = nextBucketStart(b, f.Step) {
		if c, ok := closes[bucketKey(b)]; ok {
			v.Points = append(v.Points, BalancePoint{AsOf: b, Balance: c})
			prev, havePrev = c, true
			continue
		}
		if !havePrev {
			// Before the first known level: omit rather than back-fill.
			continue
		}
		v.Points = append(v.Points, BalancePoint{AsOf: b, Balance: prev, Carried: true})
	}
	return v, nil
}

// --- basis=ledger ---

// ledgerBucket accumulates one bucket's rows while a series is derived. Gross In/Out/Net
// cover every posted row; the tally applies the internal-transfer exclusion for the
// external-only figures.
type ledgerBucket struct {
	start time.Time
	in    float64
	out   float64
	net   float64
	txns  int
	tally spendTally

	lastAfter *float64 // the bank's running balance at the bucket's last row that carries one

	close    float64
	hasClose bool
	source   string

	rowMismatch bool // a consecutive-balance_after check failed inside this bucket
}

// ledgerSeries derives an account's closes from posted transactions, with per-bucket open,
// gross in/out/net and external-only in/out.
//
// The strategy is chosen per account by MEASURED balance_after coverage at query time, not
// hardcoded by class: if the broker ever starts capturing running balances for the card
// accounts the strategy upgrades itself with no code change.
//
//   - Coverage present: one ascending pass and the bucket close IS the bank's own
//     balance_after at the bucket's last row. Exact, and it cannot drift. This is preferred
//     for CORRECTNESS, not speed. The whole ledger is small enough that walking it is free,
//     so time is not the argument; accumulated arithmetic compounds every dropped or
//     duplicated row into every earlier balance, silently and permanently, and the bank's
//     own figure cannot.
//   - Coverage absent: a reverse incremental walk from the newest BalanceSnapshot. The
//     ledger supplies deltas, not levels, so that reading is the only trustworthy level to
//     anchor on.
func ledgerSeries(ctx context.Context, client *ent.Client, acc *ent.Account, f BalanceSeriesFilter, own4 map[string]bool) (BalanceSeriesView, error) {
	v := newSeriesView(acc, f)

	// An investment account is a portfolio, not a cash ledger: its balance moves with the
	// market with no transaction behind it, so accumulating amounts over holdings would
	// produce nonsense. Skip it and say why rather than emitting a wrong series.
	if acc.Type == account.TypeInvestment {
		v.Note = "ledger derivation is not available for an investment account: its balance moves with the market with no transaction behind it, so per-period cash flows do not describe it. Use basis=snapshot."
		return v, nil
	}

	hasAcc := transaction.HasAccountWith(account.IDEQ(acc.ID))

	// The oldest posted row is the backward edge of what can be derived. There is no
	// backward low-water-mark on Account (posted_watermark is the FORWARD edge), so
	// MIN(posted_date) cannot tell "the account's history starts here" from "the backfill
	// stopped here". Nothing older is emitted, the series reports ledger_from, and the
	// earliest bucket whose opening had to be synthesized is flagged start_unverified.
	firstRow, err := client.Transaction.Query().Where(hasAcc).
		Order(ent.Asc(transaction.FieldPostedDate), ent.Asc(transaction.FieldID)).
		First(ctx)
	if ent.IsNotFound(err) {
		v.Note = "no posted transactions for this account, so there is no ledger to derive a series from. Use basis=snapshot."
		return v, nil
	}
	if err != nil {
		return v, err
	}
	ledgerFrom := firstRow.PostedDate
	v.LedgerFrom = &ledgerFrom

	lastRow, err := client.Transaction.Query().Where(hasAcc).
		Order(ent.Desc(transaction.FieldPostedDate), ent.Desc(transaction.FieldID)).
		First(ctx)
	if err != nil {
		return v, err
	}

	// The anchor is the newest BalanceSnapshot. It is loaded for both strategies: the walk
	// needs it as its only level, and it also sets how far a bounded-above series can run.
	anchor, err := latestSnapshot(ctx, client, acc.ID)
	if err != nil {
		return v, err
	}

	// Emitted range. Never before the ledger's own first bucket.
	lo := bucketStart(ledgerFrom, f.Step)
	if f.From != nil {
		if fb := bucketStart(*f.From, f.Step); fb.After(lo) {
			lo = fb
		}
	}
	var hi time.Time
	if f.To != nil {
		hi = bucketStart(*f.To, f.Step)
	} else {
		hi = bucketStart(lastRow.PostedDate, f.Step)
		if anchor != nil {
			if ab := bucketStart(anchor.AsOf, f.Step); ab.After(hi) {
				hi = ab
			}
		}
	}
	if hi.Before(lo) {
		return v, nil
	}

	// Derivation range. It has to cover the anchor's bucket even when the requested window
	// excludes it, because the walk starts there: a window entirely before the anchor still
	// needs every row between the two to walk back into it.
	derivLo, derivHi := lo, hi
	if anchor != nil {
		ab := bucketStart(anchor.AsOf, f.Step)
		if ab.Before(derivLo) {
			derivLo = ab
		}
		if ab.After(derivHi) {
			derivHi = ab
		}
	}
	if lfb := bucketStart(ledgerFrom, f.Step); derivLo.Before(lfb) {
		derivLo = lfb
	}
	rangeStart := derivLo
	rangeEnd := nextBucketStart(derivHi, f.Step)

	// Coverage probe. It is one Count, because coverage is all-or-nothing per account in
	// practice: a covered account is covered from its very first row, so there is no
	// scraper-era boundary to find inside one.
	covered, err := client.Transaction.Query().
		Where(hasAcc,
			transaction.PostedDateGTE(rangeStart),
			transaction.PostedDateLT(rangeEnd),
			transaction.BalanceAfterNotNil()).
		Count(ctx)
	if err != nil {
		return v, err
	}
	pathBalanceAfter := covered > 0

	if !pathBalanceAfter && anchor == nil {
		// No running balance in the ledger and no reading to anchor on. Anchoring on an
		// arbitrary row would invent a level, so emit nothing and say why.
		v.Note = "this account's ledger carries no running balance and it has no balance snapshot to anchor a walk on, so no ledger series can be derived. Use basis=snapshot once a reading exists."
		return v, nil
	}

	// One unpaged load, bounded by the date range (MonthlySummary's pattern).
	rows, err := client.Transaction.Query().
		Where(hasAcc, transaction.PostedDateGTE(rangeStart), transaction.PostedDateLT(rangeEnd)).
		Order(ent.Asc(transaction.FieldPostedDate), ent.Asc(transaction.FieldID)).
		All(ctx)
	if err != nil {
		return v, err
	}

	// Pre-seed every bucket in the derivation range so a quiet one still exists to carry.
	idx := make(map[int64]*ledgerBucket)
	order := make([]*ledgerBucket, 0)
	for b := derivLo; !b.After(derivHi); b = nextBucketStart(b, f.Step) {
		lb := &ledgerBucket{start: b}
		idx[bucketKey(b)] = lb
		order = append(order, lb)
	}

	for _, t := range rows {
		lb := idx[bucketKey(bucketStart(t.PostedDate, f.Step))]
		if lb == nil {
			continue
		}
		lb.txns++
		lb.net += t.Amount
		if t.Amount >= 0 {
			lb.in += t.Amount
		} else {
			lb.out += -t.Amount
		}
		// The one classifier: spendTally.add applies the internal-transfer exclusion that
		// MonthlySummary and SpendingSummary already use, so these per-bucket figures tie
		// out to those tools over the same window. An internal transfer counts once, on its
		// outbound leg, which is that shared behaviour and not a bug to fix here.
		lb.tally.add(t.Amount, t.Description, own4)
		if t.BalanceAfter != nil {
			lb.lastAfter = t.BalanceAfter
		}
	}

	if pathBalanceAfter {
		for _, lb := range order {
			if lb.lastAfter != nil {
				lb.close = *lb.lastAfter
				lb.hasClose = true
				lb.source = SourceBalanceAfter
			}
		}
		checkRunningBalance(acc.ID, rows, idx, f.Step)
	} else if err := walkFromAnchor(order, idx, anchor, f.Step); err != nil {
		v.Note = err.Error()
		return v, nil
	}

	// Snapshot readings in the emitted range, for the derived-close-vs-reading check. The
	// bucket's own close-of-period reading is the one to compare against, so a bucket with
	// several readings uses its last.
	snaps, err := BalanceHistory(ctx, client, acc.ID, &lo)
	if err != nil {
		return v, err
	}
	emittedEnd := nextBucketStart(hi, f.Step)
	snapCloses := make(map[int64]float64, len(snaps))
	for _, s := range snaps {
		if !s.AsOf.Before(emittedEnd) {
			continue
		}
		snapCloses[bucketKey(bucketStart(s.AsOf, f.Step))] = s.Balance
	}

	startIdx, endIdx := emittedRange(order, lo, hi)
	if startIdx < 0 {
		return v, nil
	}

	// open(n) = close(n-1), taken from the running series and never stored or separately
	// derived. The earliest emitted bucket has no previous, so its opening is synthesized;
	// that is the one number in the series whose meaning depends on the ledger genuinely
	// starting where it appears to, hence start_unverified.
	var prev float64
	if startIdx > 0 {
		prev = order[startIdx-1].close
	} else {
		prev = seedOpening(order[startIdx], rows, pathBalanceAfter)
		v.StartUnverified = true
	}

	for i := startIdx; i <= endIdx; i++ {
		lb := order[i]
		open := prev
		var close float64
		source := lb.source
		carried := false

		switch {
		case lb.hasClose:
			close = lb.close
		case lb.txns == 0:
			// Quiet bucket: repeat the previous close, flag it, zero flows.
			close = open
			carried = true
			source = SourceCarried
		default:
			// Rows but no running balance on any of them, on an account the probe found
			// covered: mixed coverage, which prod does not show. Accumulate forward from
			// the previous close rather than inventing a level, and say so via source.
			close = open + lb.net
			source = SourceAccumulated
		}

		p := BalancePoint{
			AsOf:        lb.start,
			Balance:     close,
			Carried:     carried,
			Source:      source,
			Open:        f64p(open),
			Close:       f64p(close),
			In:          f64p(lb.in),
			Out:         f64p(lb.out),
			Net:         f64p(lb.net),
			ExternalIn:  f64p(lb.tally.Income),
			ExternalOut: f64p(lb.tally.Spend),
			Txns:        intp(lb.txns),
		}

		// Reconciliation 1: a derived close and a reading in the same bucket measure the
		// same quantity, so a difference means a dropped or duplicated transaction.
		if sb, ok := snapCloses[bucketKey(lb.start)]; ok {
			d := close - sb
			p.Drift = f64p(d)
			if math.Abs(d) > v.DriftMax {
				v.DriftMax = math.Abs(d)
			}
		}
		// Reconciliation 2: close - open must equal the bucket's net flow. If it does not, a
		// row is missing from the bucket.
		if math.Abs((close-open)-lb.net) > flowEpsilon {
			p.FlowMismatch = true
		}
		// Reconciliation 3 (running-balance accounts only) localises the same failure to a
		// row pair; it lands on the bucket holding the later row.
		if lb.rowMismatch {
			p.FlowMismatch = true
		}

		v.Points = append(v.Points, p)
		prev = close
	}
	return v, nil
}

// walkFromAnchor fills every bucket's close by incremental arithmetic around the newest
// BalanceSnapshot, for an account whose ledger carries no running balance.
//
// The anchor's own bucket takes the reading VERBATIM: it is the only level the bank gave
// us, so it is never adjusted for rows posted later the same period.
//
// Backwards, which is the direction the ask asked for, the recurrence is
//
//	close(n-1) = close(n) - sum(amount of rows in bucket n)
//
// per row balance_before(t) = balance_after(t) - amount(t). It is the reverse of a forward
// running balance, so it SUBTRACTS on the way back. amount is uniformly signed across
// account types and class never flips a transaction sign, so a liability needs no special
// case: its debt simply stays negative, exactly as the snapshot path reports it.
func walkFromAnchor(order []*ledgerBucket, idx map[int64]*ledgerBucket, anchor *ent.BalanceSnapshot, step BalanceStep) error {
	if anchor == nil {
		return fmt.Errorf("no balance snapshot to anchor a ledger walk on")
	}
	// The anchor's as_of is a real instant, not a date, so it is keyed through bucketStart
	// like everything else and lands in its LOCAL bucket.
	ab := bucketStart(anchor.AsOf, step)
	anchorBucket := idx[bucketKey(ab)]
	if anchorBucket == nil {
		return fmt.Errorf("the newest balance reading falls outside this account's derivable range, so there is no anchor to walk from")
	}
	anchorIdx := -1
	for i, lb := range order {
		if lb == anchorBucket {
			anchorIdx = i
			break
		}
	}
	anchorBucket.close = anchor.Balance
	anchorBucket.hasClose = true
	anchorBucket.source = SourceAccumulated

	for i := anchorIdx - 1; i >= 0; i-- {
		order[i].close = order[i+1].close - order[i+1].net
		order[i].hasClose = true
		order[i].source = SourceAccumulated
	}
	// Rows posted AFTER the anchor reading are not covered by either issue, which only
	// describes the backward walk. Resolved by mirroring the recurrence forward:
	// close(n+1) = close(n) + sum(amount of rows in bucket n+1). Leaving these buckets
	// empty would have shown the newest end of the series as carried and flat, which is
	// worse than arithmetic that is right whenever the ledger is complete.
	for i := anchorIdx + 1; i < len(order); i++ {
		order[i].close = order[i-1].close + order[i].net
		order[i].hasClose = true
		order[i].source = SourceAccumulated
	}
	return nil
}

// seedOpening synthesizes the opening of the earliest emitted bucket, which by definition
// has no previous close to take.
//
// On a running-balance account it is the bank's own figure at the first row minus that
// row's amount, which is the level immediately before it. That is arithmetically right, and
// it means "the opening balance" only if the ledger truly starts there, which is why the
// caller flags the bucket start_unverified.
//
// On a walked account the recurrence already answers it: extending it one step past the
// earliest bucket gives close - net, which IS that bucket's opening.
func seedOpening(first *ledgerBucket, rows []*ent.Transaction, pathBalanceAfter bool) float64 {
	if pathBalanceAfter {
		for _, t := range rows {
			if t.BalanceAfter != nil {
				return *t.BalanceAfter - t.Amount
			}
		}
	}
	return first.close - first.net
}

// checkRunningBalance is reconciliation 3, and it only applies where the bank supplied a
// running balance: the difference between two consecutive rows' balance_after must equal
// the later row's amount. A break means a row is missing or duplicated between them.
//
// It flags the bucket holding the later row so the failure reaches the response, and logs
// the row pair (ids only) so the offending pair is findable, which per-bucket flow figures
// alone would not give.
func checkRunningBalance(accountID int, rows []*ent.Transaction, idx map[int64]*ledgerBucket, step BalanceStep) {
	var prevAfter *float64
	prevID := 0
	for _, t := range rows {
		if t.BalanceAfter == nil {
			prevAfter = nil
			continue
		}
		if prevAfter != nil && math.Abs((*t.BalanceAfter-*prevAfter)-t.Amount) > flowEpsilon {
			if lb := idx[bucketKey(bucketStart(t.PostedDate, step))]; lb != nil {
				lb.rowMismatch = true
			}
			slog.Warn("finance: ledger running balance does not tie between consecutive rows; a posted row may be missing or duplicated",
				"account_id", accountID, "prev_txn_id", prevID, "txn_id", t.ID)
		}
		prevAfter = t.BalanceAfter
		prevID = t.ID
	}
}

// emittedRange locates the [lo, hi] slice of the derivation buckets that is actually
// returned. The derivation range can extend past both ends to reach the anchor.
func emittedRange(order []*ledgerBucket, lo, hi time.Time) (int, int) {
	start, end := -1, -1
	for i, lb := range order {
		if lb.start.Before(lo) || lb.start.After(hi) {
			continue
		}
		if start < 0 {
			start = i
		}
		end = i
	}
	return start, end
}

// --- shared ---

func newSeriesView(acc *ent.Account, f BalanceSeriesFilter) BalanceSeriesView {
	return BalanceSeriesView{
		AccountID:   acc.ID,
		AccountName: acc.Name,
		Class:       string(acc.Class),
		Currency:    acc.Currency,
		Basis:       f.Basis,
		Step:        f.Step,
		Points:      []BalancePoint{},
	}
}

// finalize computes the first/last/change roll-up so "is this going up or down" costs a
// caller no arithmetic over the points. ChangePct divides by the magnitude of the opening
// level so a liability moving from -1000 to -500 reads as a 50% move rather than -50%; it
// stays nil when the opening level is zero, since no percentage is meaningful there.
func (v *BalanceSeriesView) finalize() {
	if len(v.Points) == 0 {
		return
	}
	first := v.Points[0].Balance
	last := v.Points[len(v.Points)-1].Balance
	change := last - first
	v.First, v.Last, v.Change = f64p(first), f64p(last), f64p(change)
	if first != 0 {
		v.ChangePct = f64p(change / math.Abs(first) * 100)
	}
}

func f64p(f float64) *float64 { return &f }
func intp(i int) *int         { return &i }
