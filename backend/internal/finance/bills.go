package finance

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/billpayment"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
)

// Recurring bills (portfolio-site#125). A RecurringBill is a DECLARED repeating
// commitment (rent, insurance, a subscription, a utility), not a ledger row. The ledger
// only knows what already happened; this file answers the questions it cannot: what is
// already committed, what falls due next, whether a cycle was paid, and whether the
// charged amount has drifted from the declaration.
//
// Two halves live here, both pure functions over the Ent client like read.go, so the HTTP
// endpoints and the MCP tool run identical queries (ADR 0017):
//
//   - The occurrence generator (nextDue / prevDue / occurrencesBetween). Every occurrence
//     is derived from (cadence, anchor_date); next_due is NEVER stored, so there is no
//     bumping job and no cache to go stale.
//   - The matching pass (ReconcileBills), a rules-based sibling of isInternalTransfer
//     (read.go): one owner-typed substring per bill, no LLM, no categorisation. It runs
//     over rows that are ALREADY in the ledger, after the ingest transaction commits, so
//     a matcher bug can never reject a good ingest window and a fixed matcher can be
//     re-run without a re-ingest.
//
// Nothing here rounds money; the caller formats.

// maxOccurrences bounds occurrence generation so a malformed window can never spin
// forever. At real cadences and windows (a decade of weekly bills is ~520) it is never
// reached.
const maxOccurrences = 10000

// reportTZ is the timezone the owner reads these dates in. A due date is a calendar fact,
// so "today" has to be the Melbourne calendar day: for the first ten hours of an
// Australian day the UTC day is still yesterday, which would report a bill due today as
// one day out and cut every overdue window a day short. Loaded once at package level; the
// binaries are built with -tags timetzdata so the IANA database is embedded (see the
// Dockerfile), and a missing database degrades to UTC rather than panicking.
var reportTZ = loadReportTZ()

func loadReportTZ() *time.Location {
	loc, err := time.LoadLocation("Australia/Melbourne")
	if err != nil {
		return time.UTC
	}
	return loc
}

// localToday is the current Melbourne calendar day rendered as a UTC-midnight date, the
// canonical form every stored finance date takes (see normalizeDate).
func localToday() time.Time {
	n := time.Now().In(reportTZ)
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

// --- views ---

// BillFilter narrows a recurring-bill query. Status is "active", "paused", "ended", or
// "" / "all" for every status. WithinDays > 0 keeps only bills falling due inside that
// many days, plus anything genuinely overdue (actionable now); a past due date that is
// NOT overdue is history and stays out. AccountID > 0 keeps only bills paid from that
// account, which EXCLUDES bills with no account set.
type BillFilter struct {
	Status     string
	WithinDays int
	AccountID  int
}

// BillView is one recurring bill's stored columns plus the part that is derived on read.
// AccountID is 0 (and AccountName empty) when the bill carries no account edge.
type BillView struct {
	ID                 int
	Name               string
	Payee              string
	ExpectedAmount     float64
	Currency           string
	Cadence            string
	AnchorDate         time.Time
	AmountVariable     bool
	AmountTolerancePct float64
	MatchPattern       string
	MatchWindowDays    int
	Status             string
	EndedOn            *time.Time
	Notes              string
	AccountID          int
	AccountName        string
	CreatedAt          time.Time
	UpdatedAt          time.Time

	// NextDue is the due date that needs attention: normally the earliest occurrence on
	// or after today; the last UNSETTLED cycle when the bill is overdue (so NextDue and
	// DaysUntil always describe the same date); and the last EXPECTED cycle once a bill
	// has ended, since no occurrence past ended_on is expected.
	NextDue time.Time
	// DaysUntil is whole days from today to NextDue, so it is negative whenever NextDue
	// is in the past (an overdue cycle, or an ended bill's final cycle).
	DaysUntil int
	// LastPaidDate / LastPaidAmount come from the newest BillPayment's transaction: when
	// it was actually paid and the magnitude actually charged. Both nil until a cycle is
	// reconciled. LastPaidAmount differing from ExpectedAmount is the repricing signal,
	// not an error.
	LastPaidDate   *time.Time
	LastPaidAmount *float64
	// Overdue: an active, auto-matched bill whose previous cycle sits inside the ledger's
	// coverage, is past its match window, and carries no BillPayment. An absence, which a
	// row list cannot show.
	Overdue bool
	// AutoMatched is false when the bill carries no match_pattern, the schema's
	// hand-reconciled mode. Nothing will ever link such a bill's cycles, so no absence can
	// be inferred from it: it is never reported overdue, and a caller must be able to tell
	// "not tracked automatically" from "paid" rather than inferring it from a due date.
	AutoMatched bool
	// ExpectedMonthly normalises ExpectedAmount to a per-month figure so bills on
	// different cadences can be summed.
	ExpectedMonthly float64
}

// BillTotals is the committed-money roll-up over a returned bill set. Count is every bill
// returned; the two money figures count ACTIVE bills only, so a paused subscription or an
// ended commitment never inflates what is spoken for.
type BillTotals struct {
	CommittedTotal    float64
	MonthlyEquivalent float64
	Count             int
}

// ReconcileOptions bounds one matching pass to a posted-date range. Both bounds are
// inclusive; nil leaves that side open. The post-ingest hook passes the window it just
// ingested, so the daily sync costs the same in year five as on day one, while the admin
// reconcile button leaves both open for a full backfill.
type ReconcileOptions struct {
	From *time.Time
	To   *time.Time
}

// ReconcileSummary reports what one matching pass did, so a "reconcile now" caller can see
// whether anything actually linked. CandidatesCompared is the work performed (how many
// posted rows the match rule was evaluated against); it is reported because this pass runs
// inline on the synchronous ingest request, so its cost has to stay observable and bounded
// rather than growing with the ledger.
type ReconcileSummary struct {
	BillsScanned       int
	CyclesChecked      int
	CandidatesCompared int
	PaymentsLinked     int
}

// ErrCycleAlreadyLinked is returned when a hand-made link targets a cycle that already
// carries a MANUAL payment: the matcher may be overruled, an earlier human decision may
// not be silently replaced. The API maps it to a 409.
var ErrCycleAlreadyLinked = errors.New("finance: that cycle already carries a manual payment link")

// ErrNotAnOccurrence is returned when a hand-made link names a date that is not one of the
// bill's derived cycles. Such a link settles a cycle that does not exist, leaving the real
// one open to the matcher while the bill reports a payment and an overdue absence at the
// same time. The API maps it to a 422.
var ErrNotAnOccurrence = errors.New("finance: that date is not one of the bill's due dates")

// --- occurrence generation ---

// cadenceStep reports how one occurrence advances to the next: either a whole number of
// days (weekly, fortnightly) or a whole number of calendar months (monthly, quarterly,
// annual). Exactly one of the two is non-zero.
func cadenceStep(c recurringbill.Cadence) (days int, months int) {
	switch c {
	case recurringbill.CadenceWeekly:
		return 7, 0
	case recurringbill.CadenceFortnightly:
		return 14, 0
	case recurringbill.CadenceQuarterly:
		return 0, 3
	case recurringbill.CadenceAnnual:
		return 0, 12
	default: // monthly, and the safe fallback for an unknown value
		return 0, 1
	}
}

// daysInMonth returns how many days the given month has. Day 0 of the following month is
// the last day of this one, and Go normalises month 13 into the next January.
func daysInMonth(year int, m time.Month) int {
	return time.Date(year, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

// addMonthsClamped advances t by n calendar months keeping the anchor's day-of-month,
// CLAMPED to the target month's last day: a 31st anchor lands on 30 April and 28 February
// (29 in a leap year). time.AddDate would instead roll a too-large day forward into the
// next month (31 Jan + 1 month = 3 March), which would silently shift a bill's cycle.
func addMonthsClamped(t time.Time, n int) time.Time {
	u := normalizeDate(t)
	day := u.Day()
	// Step from the 1st so the month arithmetic itself cannot overflow.
	first := time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, n, 0)
	if last := daysInMonth(first.Year(), first.Month()); day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

// occurrenceAt returns the n-th occurrence of a cadence from its anchor. n is signed, so
// the sequence runs forever in both directions (n=0 is the anchor itself).
func occurrenceAt(anchor time.Time, c recurringbill.Cadence, n int) time.Time {
	days, months := cadenceStep(c)
	if days > 0 {
		return normalizeDate(anchor).AddDate(0, 0, n*days)
	}
	return addMonthsClamped(anchor, n*months)
}

// ceilDiv divides a by a positive b rounding toward +infinity. Go's integer division
// truncates toward zero, which is the wrong direction for a negative numerator here.
func ceilDiv(a, b int) int {
	q := a / b
	if a%b > 0 {
		q++
	}
	return q
}

// dueOrdinal returns the smallest n whose occurrence is on or after today: the ordinal of
// next_due, with prev_due sitting at n-1. Day-based cadences are exact arithmetic; month
// stepping clamps to month ends, so it estimates from the calendar-month difference and
// then walks the (strictly increasing) sequence the last step or two.
func dueOrdinal(anchor time.Time, c recurringbill.Cadence, today time.Time) int {
	anchor, today = normalizeDate(anchor), normalizeDate(today)
	days, months := cadenceStep(c)
	if days > 0 {
		return ceilDiv(int(today.Sub(anchor)/(24*time.Hour)), days)
	}
	monthsDiff := (today.Year()-anchor.Year())*12 + int(today.Month()) - int(anchor.Month())
	n := monthsDiff / months
	for !occurrenceAt(anchor, c, n).Before(today) {
		n--
	}
	for occurrenceAt(anchor, c, n).Before(today) {
		n++
	}
	return n
}

// nextDue is the earliest occurrence on or after today.
func nextDue(anchor time.Time, c recurringbill.Cadence, today time.Time) time.Time {
	return occurrenceAt(anchor, c, dueOrdinal(anchor, c, today))
}

// prevDue is the latest occurrence strictly before today. Missed-bill detection keys off
// it, from the same generator.
func prevDue(anchor time.Time, c recurringbill.Cadence, today time.Time) time.Time {
	return occurrenceAt(anchor, c, dueOrdinal(anchor, c, today)-1)
}

// lastOccurrenceOnOrBefore is the latest occurrence that is not after d. It is how
// ended_on caps a bill: the last cycle it was ever expected to bill.
func lastOccurrenceOnOrBefore(anchor time.Time, c recurringbill.Cadence, d time.Time) time.Time {
	d = normalizeDate(d)
	n := dueOrdinal(anchor, c, d)
	if occ := occurrenceAt(anchor, c, n); !occ.After(d) {
		return occ
	}
	return occurrenceAt(anchor, c, n-1)
}

// isOccurrence reports whether d falls on the bill's cadence grid.
func isOccurrence(anchor time.Time, c recurringbill.Cadence, d time.Time) bool {
	d = normalizeDate(d)
	return occurrenceAt(anchor, c, dueOrdinal(anchor, c, d)).Equal(d)
}

// occurrencesBetween lists every occurrence in the inclusive [from, to] window, oldest
// first. It is faithful to the generator's definition, so a window that starts before the
// anchor yields pre-anchor occurrences; callers that only want real cycles clip at the
// anchor themselves (see cycleWindow).
func occurrencesBetween(anchor time.Time, c recurringbill.Cadence, from, to time.Time) []time.Time {
	from, to = normalizeDate(from), normalizeDate(to)
	if to.Before(from) {
		return nil
	}
	start := dueOrdinal(anchor, c, from)
	out := make([]time.Time, 0, 8)
	for i := 0; i < maxOccurrences; i++ {
		occ := occurrenceAt(anchor, c, start+i)
		if occ.After(to) {
			break
		}
		out = append(out, occ)
	}
	return out
}

// nearestOccurrence returns the cycle whose derived due date is closest to d, ties going
// to the earlier one. It is how a hand-linked transaction is assigned to a cycle when the
// caller does not name one.
func nearestOccurrence(anchor time.Time, c recurringbill.Cadence, d time.Time) time.Time {
	d = normalizeDate(d)
	next := nextDue(anchor, c, d)
	prev := prevDue(anchor, c, d)
	if daysBetween(prev, d) <= daysBetween(d, next) {
		return prev
	}
	return next
}

// cycleWindow returns the cycles of one bill the matching pass should try to settle,
// bounded on BOTH sides so a pass costs what it was asked about rather than however long
// ago the bill happens to be anchored. The lower bound is the latest of:
//
//   - the anchor, since there is no cycle before it;
//   - the ledger's earliest posted row minus the match window, since a cycle the ledger
//     does not cover can never be reconciled, so rescanning it on every pass is waste;
//   - the requested range's start minus the match window, since only rows in that range
//     can settle anything (the post-ingest hook passes the window it just ingested).
//
// The upper bound is the earliest of today plus the match window (so a debit landing early
// still settles its cycle), the requested range's end plus the match window, and ended_on
// (no occurrence after it is expected).
func cycleWindow(b *ent.RecurringBill, coverageFrom *time.Time, opts ReconcileOptions, today time.Time) []time.Time {
	w := b.MatchWindowDays
	from := normalizeDate(b.AnchorDate)
	if coverageFrom != nil {
		if lo := normalizeDate(*coverageFrom).AddDate(0, 0, -w); lo.After(from) {
			from = lo
		}
	}
	if opts.From != nil {
		if lo := normalizeDate(*opts.From).AddDate(0, 0, -w); lo.After(from) {
			from = lo
		}
	}
	to := normalizeDate(today).AddDate(0, 0, w)
	if opts.To != nil {
		if hi := normalizeDate(*opts.To).AddDate(0, 0, w); hi.Before(to) {
			to = hi
		}
	}
	if b.EndedOn != nil {
		if e := normalizeDate(*b.EndedOn); e.Before(to) {
			to = e
		}
	}
	return occurrencesBetween(b.AnchorDate, b.Cadence, from, to)
}

// daysBetween counts whole days from a to b (negative when b is earlier). Both are
// collapsed to UTC midnight first, so the difference is an exact multiple of 24h.
func daysBetween(a, b time.Time) int {
	return int(normalizeDate(b).Sub(normalizeDate(a)) / (24 * time.Hour))
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// monthlyEquivalent normalises one cycle's expected amount to a per-month figure (52 and
// 26 cycles a year for weekly and fortnightly), so bills on different cadences can be
// summed into one committed-money number.
func monthlyEquivalent(amount float64, c recurringbill.Cadence) float64 {
	switch c {
	case recurringbill.CadenceWeekly:
		return amount * 52 / 12
	case recurringbill.CadenceFortnightly:
		return amount * 26 / 12
	case recurringbill.CadenceQuarterly:
		return amount / 3
	case recurringbill.CadenceAnnual:
		return amount / 12
	default: // monthly
		return amount
	}
}

// earliestPostedDate is the ledger's coverage start: the oldest posted row's date, or nil
// when the ledger is empty. A cycle before it cannot be reconciled at all, so an absence
// there means "no data", not "not paid", and it must never be reported as a missed bill.
// Bounding a derivation at MIN(posted_date) is the same rule the balance series applies.
func earliestPostedDate(ctx context.Context, client *ent.Client) (*time.Time, error) {
	t, err := client.Transaction.Query().
		Order(ent.Asc(transaction.FieldPostedDate), ent.Asc(transaction.FieldID)).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d := normalizeDate(t.PostedDate)
	return &d, nil
}

// --- read ---

// ListRecurringBills returns the declared commitments matching the filter with their
// derived due dates and last-paid figures, plus the committed-money roll-up over the
// returned set. Results are ordered most urgent first (days_until ascending, then name),
// which the SQL cannot do: next_due is derived, not stored, so within_days filtering and
// ordering both happen in Go. That is the same deliberate load-then-filter shape the read
// layer already uses for external_only (read.go), and the set is a handful of rows.
func ListRecurringBills(ctx context.Context, client *ent.Client, f BillFilter) ([]BillView, BillTotals, error) {
	q := client.RecurringBill.Query().WithAccount()
	switch status := strings.ToLower(strings.TrimSpace(f.Status)); status {
	case "", "all":
		// every status
	case string(recurringbill.StatusActive), string(recurringbill.StatusPaused), string(recurringbill.StatusEnded):
		q = q.Where(recurringbill.StatusEQ(recurringbill.Status(status)))
	default:
		return nil, BillTotals{}, fmt.Errorf("finance: invalid bill status %q (want active, paused, ended or all)", f.Status)
	}
	if f.AccountID != 0 {
		q = q.Where(recurringbill.HasAccountWith(account.IDEQ(f.AccountID)))
	}
	bills, err := q.Order(ent.Asc(recurringbill.FieldName)).All(ctx)
	if err != nil {
		return nil, BillTotals{}, err
	}

	// One coverage query for the whole list, not one per bill.
	coverageFrom, err := earliestPostedDate(ctx, client)
	if err != nil {
		return nil, BillTotals{}, err
	}

	today := localToday()
	views := make([]BillView, 0, len(bills))
	for _, b := range bills {
		v, err := billView(ctx, client, b, coverageFrom, today)
		if err != nil {
			return nil, BillTotals{}, err
		}
		// within_days keeps the window ahead plus anything actionably overdue. A due date
		// in the past that is NOT overdue (an ended bill's final cycle, a hand-reconciled
		// bill) is history rather than something falling due, so it stays out.
		if f.WithinDays > 0 && !v.Overdue && (v.DaysUntil < 0 || v.DaysUntil > f.WithinDays) {
			continue
		}
		views = append(views, v)
	}
	sort.SliceStable(views, func(i, j int) bool {
		if views[i].DaysUntil != views[j].DaysUntil {
			return views[i].DaysUntil < views[j].DaysUntil
		}
		return views[i].Name < views[j].Name
	})
	return views, billTotals(views), nil
}

// billView maps one stored bill to its view, deriving the due date, the days until it,
// the last reconciled payment and the overdue flag. It expects the account edge to be
// eager-loaded. coverageFrom is the ledger's earliest posted date (nil when empty).
func billView(ctx context.Context, client *ent.Client, b *ent.RecurringBill, coverageFrom *time.Time, today time.Time) (BillView, error) {
	v := BillView{
		ID:                 b.ID,
		Name:               b.Name,
		Payee:              b.Payee,
		ExpectedAmount:     b.ExpectedAmount,
		Currency:           b.Currency,
		Cadence:            string(b.Cadence),
		AnchorDate:         normalizeDate(b.AnchorDate),
		AmountVariable:     b.AmountVariable,
		AmountTolerancePct: b.AmountTolerancePct,
		MatchPattern:       b.MatchPattern,
		MatchWindowDays:    b.MatchWindowDays,
		Status:             string(b.Status),
		EndedOn:            b.EndedOn,
		Notes:              b.Notes,
		CreatedAt:          b.CreatedAt,
		UpdatedAt:          b.UpdatedAt,
		AutoMatched:        strings.TrimSpace(b.MatchPattern) != "",
		ExpectedMonthly:    monthlyEquivalent(b.ExpectedAmount, b.Cadence),
	}
	if a := b.Edges.Account; a != nil {
		v.AccountID = a.ID
		v.AccountName = a.Name
	}

	last, err := client.BillPayment.Query().
		Where(billpayment.HasBillWith(recurringbill.IDEQ(b.ID))).
		WithTransaction().
		Order(ent.Desc(billpayment.FieldOccurrenceDate), ent.Desc(billpayment.FieldID)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return BillView{}, err
	}
	if last != nil {
		if t := last.Edges.Transaction; t != nil {
			d := normalizeDate(t.PostedDate)
			v.LastPaidDate = &d
			// The magnitude, so it compares directly with the unsigned expected_amount
			// (Transaction.amount is signed, money out negative).
			amt := math.Abs(t.Amount)
			v.LastPaidAmount = &amt
		}
	}

	prev := prevDue(b.AnchorDate, b.Cadence, today)
	due := nextDue(b.AnchorDate, b.Cadence, today)
	// A commitment that stopped expects no occurrence after ended_on, so the generator is
	// clipped HERE and not only on the overdue path: otherwise a cancelled bill keeps
	// reporting a fresh due date forever and renders as "due today".
	if b.EndedOn != nil {
		if e := normalizeDate(*b.EndedOn); due.After(e) {
			due = lastOccurrenceOnOrBefore(b.AnchorDate, b.Cadence, e)
		}
	}
	overdue, err := isOverdue(ctx, client, b, coverageFrom, prev, today)
	if err != nil {
		return BillView{}, err
	}
	if overdue {
		// Report the unsettled cycle rather than the next one, so days_until is negative
		// and the two fields describe the same date.
		v.Overdue = true
		due = prev
	}
	v.NextDue = due
	v.DaysUntil = daysBetween(today, due)
	return v, nil
}

// isOverdue reports a missed cycle: an ACTIVE bill, carrying a match pattern, whose
// previous occurrence is at or after its anchor, is not past ended_on, sits inside the
// ledger's coverage, is more than match_window_days behind us, and has no BillPayment. It
// is a read-time derivation with no new state.
//
// Two of those exclusions are load-bearing, because "overdue" is an inference from an
// ABSENCE and an absence only means something where a payment could have been found:
//
//   - No match_pattern means the bill is reconciled by hand (the schema's own
//     hand-reconciled mode). Nothing will ever link a cycle automatically, so every cycle
//     would read as missed forever and permanently backdate the reported due date.
//     AutoMatched reports that state honestly instead.
//   - A cycle before the ledger's earliest posted row cannot be reconciled at all, so it
//     is "no data" rather than "not paid".
func isOverdue(ctx context.Context, client *ent.Client, b *ent.RecurringBill, coverageFrom *time.Time, prev, today time.Time) (bool, error) {
	if b.Status != recurringbill.StatusActive {
		return false, nil
	}
	if strings.TrimSpace(b.MatchPattern) == "" {
		return false, nil
	}
	if prev.Before(normalizeDate(b.AnchorDate)) {
		return false, nil
	}
	if b.EndedOn != nil && prev.After(normalizeDate(*b.EndedOn)) {
		return false, nil
	}
	// The cycle has to be reconcilable: a payment can land up to match_window_days before
	// its due date, so a cycle is covered once prev+window reaches the ledger's start.
	if coverageFrom == nil {
		return false, nil
	}
	if prev.AddDate(0, 0, b.MatchWindowDays).Before(normalizeDate(*coverageFrom)) {
		return false, nil
	}
	if !today.After(prev.AddDate(0, 0, b.MatchWindowDays)) {
		return false, nil
	}
	n, err := client.BillPayment.Query().
		Where(
			billpayment.HasBillWith(recurringbill.IDEQ(b.ID)),
			billpayment.OccurrenceDateEQ(prev),
		).
		Count(ctx)
	if err != nil {
		return false, err
	}
	return n == 0, nil
}

// billTotals rolls a returned bill set into the committed-money figures. Only ACTIVE
// bills count toward the money: a paused subscription is not billing and an ended
// commitment is finished, so neither is spoken for.
func billTotals(views []BillView) BillTotals {
	t := BillTotals{Count: len(views)}
	for _, v := range views {
		if v.Status != string(recurringbill.StatusActive) {
			continue
		}
		t.CommittedTotal += v.ExpectedAmount
		t.MonthlyEquivalent += v.ExpectedMonthly
	}
	return t
}

// --- matching ---

// candidate is one posted row prepared for matching. The lowered description and merchant
// are computed ONCE per pass instead of once per (bill, cycle, row) comparison, which is
// what made the naive loop allocate two strings for every comparison it made.
type candidate struct {
	txn        *ent.Transaction
	descLower  string
	merchLower string
	accountID  int
	day        time.Time
}

// newCandidate prepares a posted row for matching. The account edge must be eager-loaded
// for a scoped bill to match it.
func newCandidate(t *ent.Transaction) candidate {
	c := candidate{
		txn:        t,
		descLower:  strings.ToLower(t.Description),
		merchLower: strings.ToLower(t.Merchant),
		day:        normalizeDate(t.PostedDate),
	}
	if a := t.Edges.Account; a != nil {
		c.accountID = a.ID
	}
	return c
}

// billMatcher is one bill's compiled match rule: the lowered needle and the account scope
// resolved once, so the per-cycle loop allocates nothing.
type billMatcher struct {
	bill    *ent.RecurringBill
	needle  string
	scopeID int
}

// newBillMatcher compiles a bill's rule. An empty match_pattern yields an empty needle and
// therefore never matches, which is the schema's "reconciled by hand" mode.
func newBillMatcher(b *ent.RecurringBill) billMatcher {
	m := billMatcher{bill: b, needle: strings.ToLower(strings.TrimSpace(b.MatchPattern))}
	if a := b.Edges.Account; a != nil {
		m.scopeID = a.ID
	}
	return m
}

// matches is the auto-match rule: a per-bill AND of four clauses, all of them
// owner-declared rather than inferred.
//
//  1. match_pattern is a case-insensitive SUBSTRING of the transaction's description OR
//     merchant (the same two fields search_merchant covers). An empty pattern never
//     matches, so that bill is reconciled by hand only.
//  2. account scope: when the bill carries an account edge, only that account's rows are
//     candidates. A zero scope (a bill with no account) matches any row.
//  3. amount: |txn.amount| within amount_tolerance_pct of expected_amount, since
//     expected_amount is an unsigned magnitude and Transaction.amount is signed. Skipped
//     ENTIRELY when amount_variable is set (utilities).
//  4. date window: posted_date within match_window_days either side of the occurrence,
//     because a direct debit lands a day or two off nominal.
func (m billMatcher) matches(c candidate, occ time.Time) bool {
	if m.needle == "" {
		return false
	}
	if !strings.Contains(c.descLower, m.needle) && !strings.Contains(c.merchLower, m.needle) {
		return false
	}
	if m.scopeID != 0 && c.accountID != m.scopeID {
		return false
	}
	if !m.bill.AmountVariable {
		tolerance := m.bill.ExpectedAmount * m.bill.AmountTolerancePct / 100
		if math.Abs(math.Abs(c.txn.Amount)-m.bill.ExpectedAmount) > tolerance {
			return false
		}
	}
	return absInt(daysBetween(occ, c.day)) <= m.bill.MatchWindowDays
}

// bestMatch picks the posted row that settles one cycle: the closest posted date to the
// occurrence among the rows satisfying the rule, ties going to the earlier day and then to
// the lower row id (the day buckets arrive posted_date/id ascending), so a re-run is
// deterministic. Candidates are indexed BY DAY, so only the 2*window+1 days a cycle can
// possibly draw from are examined rather than every row in the pass, which is what turned
// the old loop into cycles x rows. A row already linked to this bill is skipped; the
// (bill, transaction) unique index is the backstop. The second return is how many rows the
// rule was evaluated against, so the caller can report the work done.
func (m billMatcher) bestMatch(byDay map[int64][]candidate, occ time.Time, used map[int]bool) (*ent.Transaction, int) {
	var best *ent.Transaction
	bestGap, compared := 0, 0
	w := m.bill.MatchWindowDays
	for d := -w; d <= w; d++ {
		for _, c := range byDay[occ.AddDate(0, 0, d).Unix()] {
			if used[c.txn.ID] {
				continue
			}
			compared++
			if !m.matches(c, occ) {
				continue
			}
			if gap := absInt(d); best == nil || gap < bestGap {
				best, bestGap = c.txn, gap
			}
		}
	}
	return best, compared
}

// ReconcileBills runs the matching pass over transactions that are ALREADY in the ledger
// and links each unsettled cycle to the posted row that paid it. It is idempotent and
// re-runnable: a settled cycle is skipped, a `manual` link is never touched, and the two
// unique indexes on BillPayment make a concurrent double-run a no-op rather than a
// duplicate.
//
// It must run AFTER the ingest transaction commits, never inside it (see Ingest): the
// ingest is one all-or-nothing window with a reconciliation gate, so a matcher bug in
// there could reject a good window, and a fixed matcher could not be re-run without a
// re-ingest. Because it runs inline on that synchronous request, its cost is bounded by
// opts (the window just ingested) and by the ledger's coverage rather than by how long ago
// a bill was anchored: see cycleWindow, and CandidatesCompared for the work it reports.
//
// Paused bills are skipped (a suspended subscription is not billing). Ended bills are
// still reconciled up to ended_on, so historical cycles settle after the fact.
func ReconcileBills(ctx context.Context, client *ent.Client, opts ReconcileOptions) (ReconcileSummary, error) {
	sum := ReconcileSummary{}
	candidates, err := client.RecurringBill.Query().
		Where(recurringbill.StatusIn(recurringbill.StatusActive, recurringbill.StatusEnded)).
		WithAccount().
		Order(ent.Asc(recurringbill.FieldID)).
		All(ctx)
	if err != nil {
		return sum, err
	}

	coverageFrom, err := earliestPostedDate(ctx, client)
	if err != nil {
		return sum, err
	}
	if coverageFrom == nil {
		return sum, nil // an empty ledger can settle nothing
	}

	today := localToday()
	// Cycles per bill, plus the union of every bill's candidate date window, so the whole
	// pass loads posted rows once instead of once per bill.
	cycles := make(map[int][]time.Time, len(candidates))
	var from, to time.Time
	bills := make([]*ent.RecurringBill, 0, len(candidates))
	for _, b := range candidates {
		// An empty pattern means "never auto-matched", so it is not a candidate at all.
		if strings.TrimSpace(b.MatchPattern) == "" {
			continue
		}
		occs := cycleWindow(b, coverageFrom, opts, today)
		if len(occs) == 0 {
			continue
		}
		bills = append(bills, b)
		cycles[b.ID] = occs
		lo := occs[0].AddDate(0, 0, -b.MatchWindowDays)
		hi := occs[len(occs)-1].AddDate(0, 0, b.MatchWindowDays)
		if from.IsZero() || lo.Before(from) {
			from = lo
		}
		if to.IsZero() || hi.After(to) {
			to = hi
		}
	}
	if len(bills) == 0 {
		return sum, nil
	}

	rows, err := client.Transaction.Query().
		Where(transaction.PostedDateGTE(from), transaction.PostedDateLTE(to)).
		WithAccount().
		Order(ent.Asc(transaction.FieldPostedDate), ent.Asc(transaction.FieldID)).
		All(ctx)
	if err != nil {
		return sum, err
	}
	// Index the window by posted day, lowering each row's text exactly once for the whole
	// pass, so a cycle only looks at the days it can actually draw from.
	byDay := make(map[int64][]candidate, len(rows))
	for _, t := range rows {
		c := newCandidate(t)
		byDay[c.day.Unix()] = append(byDay[c.day.Unix()], c)
	}

	for _, b := range bills {
		sum.BillsScanned++
		matcher := newBillMatcher(b)
		settled, used, err := existingPayments(ctx, client, b.ID)
		if err != nil {
			return sum, err
		}
		for _, occ := range cycles[b.ID] {
			if settled[occ.Unix()] {
				continue // already reconciled, auto or manual; never overwritten
			}
			sum.CyclesChecked++
			t, compared := matcher.bestMatch(byDay, occ, used)
			sum.CandidatesCompared += compared
			if t == nil {
				continue
			}
			err := client.BillPayment.Create().
				SetBillID(b.ID).
				SetTransactionID(t.ID).
				SetOccurrenceDate(occ).
				SetMethod(billpayment.MethodAuto).
				Exec(ctx)
			if ent.IsConstraintError(err) {
				// A concurrent pass linked this cycle or row first; the unique indexes did
				// their job, so treat it as already settled.
				settled[occ.Unix()] = true
				used[t.ID] = true
				continue
			}
			if err != nil {
				return sum, err
			}
			settled[occ.Unix()] = true
			used[t.ID] = true
			sum.PaymentsLinked++
		}
	}
	return sum, nil
}

// existingPayments loads one bill's reconciliation state: which cycles are already
// settled (keyed by the occurrence date) and which transactions are already linked to it
// (so one row cannot settle two cycles of the same bill).
func existingPayments(ctx context.Context, client *ent.Client, billID int) (settled map[int64]bool, used map[int]bool, err error) {
	rows, err := client.BillPayment.Query().
		Where(billpayment.HasBillWith(recurringbill.IDEQ(billID))).
		WithTransaction().
		All(ctx)
	if err != nil {
		return nil, nil, err
	}
	settled = make(map[int64]bool, len(rows))
	used = make(map[int]bool, len(rows))
	for _, p := range rows {
		settled[normalizeDate(p.OccurrenceDate).Unix()] = true
		if t := p.Edges.Transaction; t != nil {
			used[t.ID] = true
		}
	}
	return settled, used, nil
}

// PostedDateRange is the inclusive posted-date span of an ingest payload's posted rows, or
// ok=false when it carried none. The ingest handler passes it to ReconcileBills so the
// post-commit matching pass only considers the cycles the new rows could possibly settle,
// instead of every cycle since each bill's anchor.
func PostedDateRange(p *Payload) (from, to time.Time, ok bool) {
	for i := range p.Transactions.Posted {
		d, err := parseDate(p.Transactions.Posted[i].PostedDate)
		if err != nil {
			continue // Ingest already validated these; a bad one just does not widen the window
		}
		if !ok || d.Before(from) {
			from = d
		}
		if !ok || d.After(to) {
			to = d
		}
		ok = true
	}
	return from, to, ok
}

// LinkBillPayment records a hand-made link between one cycle of a bill and a posted
// transaction, with method=manual so the matching pass never touches it.
//
// occurrence is optional: when nil the cycle closest to the transaction's posted date is
// used, and when given it must be a real occurrence of the bill (ErrNotAnOccurrence
// otherwise), since a date off the cadence grid would settle a cycle that does not exist
// and leave the real one open to the matcher.
//
// If the cycle already carries an AUTO link the manual one replaces it (the human wins over
// the matcher), inside ONE transaction so a constraint failure on the insert rolls the
// delete back rather than destroying the link it was replacing. An existing MANUAL link is
// left alone and reported as ErrCycleAlreadyLinked.
func LinkBillPayment(ctx context.Context, client *ent.Client, billID, txnID int, occurrence *time.Time) (*ent.BillPayment, error) {
	b, err := client.RecurringBill.Get(ctx, billID)
	if err != nil {
		return nil, err
	}
	t, err := client.Transaction.Get(ctx, txnID)
	if err != nil {
		return nil, err
	}
	occ := nearestOccurrence(b.AnchorDate, b.Cadence, t.PostedDate)
	if occurrence != nil {
		occ = normalizeDate(*occurrence)
		if !isOccurrence(b.AnchorDate, b.Cadence, occ) {
			return nil, ErrNotAnOccurrence
		}
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := tx.BillPayment.Query().
		Where(
			billpayment.HasBillWith(recurringbill.IDEQ(billID)),
			billpayment.OccurrenceDateEQ(occ),
		).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, rollback(tx, err)
	}
	if existing != nil {
		if existing.Method == billpayment.MethodManual {
			return nil, rollback(tx, ErrCycleAlreadyLinked)
		}
		if err := tx.BillPayment.DeleteOne(existing).Exec(ctx); err != nil {
			return nil, rollback(tx, err)
		}
	}
	p, err := tx.BillPayment.Create().
		SetBillID(billID).
		SetTransactionID(t.ID).
		SetOccurrenceDate(occ).
		SetMethod(billpayment.MethodManual).
		Save(ctx)
	if err != nil {
		return nil, rollback(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return p, nil
}
