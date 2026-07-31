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

// --- views ---

// BillFilter narrows a recurring-bill query. Status is "active", "paused", "ended", or
// "" / "all" for every status. WithinDays > 0 keeps only bills whose reported due date
// falls that many days from today (overdue bills, whose days_until is negative, always
// pass). AccountID > 0 keeps only bills paid from that account, which EXCLUDES bills with
// no account set.
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
	// or after today, but the last UNSETTLED cycle when the bill is overdue (so NextDue
	// and DaysUntil always describe the same date).
	NextDue time.Time
	// DaysUntil is whole days from today to NextDue, so it is negative exactly when the
	// bill is overdue.
	DaysUntil int
	// LastPaidDate / LastPaidAmount come from the newest BillPayment's transaction: when
	// it was actually paid and the magnitude actually charged. Both nil until a cycle is
	// reconciled. LastPaidAmount differing from ExpectedAmount is the repricing signal,
	// not an error.
	LastPaidDate   *time.Time
	LastPaidAmount *float64
	// Overdue: an active bill whose previous cycle is past its match window with no
	// BillPayment linked. An absence, which a row list cannot show.
	Overdue bool
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

// ReconcileSummary reports what one matching pass did, so a "reconcile now" caller can
// see whether anything actually linked.
type ReconcileSummary struct {
	BillsScanned   int
	CyclesChecked  int
	PaymentsLinked int
}

// ErrCycleAlreadyLinked is returned when a hand-made link targets a cycle that already
// carries a MANUAL payment: the matcher may be overruled, an earlier human decision may
// not be silently replaced. The API maps it to a 409.
var ErrCycleAlreadyLinked = errors.New("finance: that cycle already carries a manual payment link")

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

// occurrencesBetween lists every occurrence in the inclusive [from, to] window, oldest
// first. It is faithful to the generator's definition, so a window that starts before the
// anchor yields pre-anchor occurrences; callers that only want real cycles clip at the
// anchor themselves (see reconcileWindow).
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

// reconcileWindow returns the cycles of one bill the matching pass should try to settle:
// every occurrence from the anchor up to match_window_days past today, so a direct debit
// that lands a day or two EARLY still settles its cycle. ended_on caps the window: a
// commitment that stopped expects no occurrence after it.
func reconcileWindow(b *ent.RecurringBill, today time.Time) []time.Time {
	to := normalizeDate(today).AddDate(0, 0, b.MatchWindowDays)
	if b.EndedOn != nil {
		if e := normalizeDate(*b.EndedOn); e.Before(to) {
			to = e
		}
	}
	return occurrencesBetween(b.AnchorDate, b.Cadence, b.AnchorDate, to)
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

	today := normalizeDate(time.Now())
	views := make([]BillView, 0, len(bills))
	for _, b := range bills {
		v, err := billView(ctx, client, b, today)
		if err != nil {
			return nil, BillTotals{}, err
		}
		if f.WithinDays > 0 && v.DaysUntil > f.WithinDays {
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
// eager-loaded.
func billView(ctx context.Context, client *ent.Client, b *ent.RecurringBill, today time.Time) (BillView, error) {
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

	due := nextDue(b.AnchorDate, b.Cadence, today)
	prev := prevDue(b.AnchorDate, b.Cadence, today)
	overdue, err := isOverdue(ctx, client, b, prev, today)
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

// isOverdue reports a missed cycle: an ACTIVE bill whose previous occurrence is at or
// after its anchor, is not past ended_on, is more than match_window_days behind us, and
// has no BillPayment. It is a read-time derivation with no new state.
func isOverdue(ctx context.Context, client *ent.Client, b *ent.RecurringBill, prev, today time.Time) (bool, error) {
	if b.Status != recurringbill.StatusActive {
		return false, nil
	}
	if prev.Before(normalizeDate(b.AnchorDate)) {
		return false, nil
	}
	if b.EndedOn != nil && prev.After(normalizeDate(*b.EndedOn)) {
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

// billMatches is the auto-match rule: a per-bill AND of four clauses, all of them
// owner-declared rather than inferred.
//
//  1. match_pattern is a case-insensitive SUBSTRING of the transaction's description OR
//     merchant (the same two fields search_merchant covers). An empty pattern never
//     matches, so that bill is reconciled by hand only.
//  2. account scope: when the bill carries an account edge, only that account's rows are
//     candidates. scopeAccountID is 0 for a bill with no account, which matches any row.
//  3. amount: |txn.amount| within amount_tolerance_pct of expected_amount, since
//     expected_amount is an unsigned magnitude and Transaction.amount is signed. Skipped
//     ENTIRELY when amount_variable is set (utilities).
//  4. date window: posted_date within match_window_days either side of the occurrence,
//     because a direct debit lands a day or two off nominal.
//
// t must carry its account edge eager-loaded whenever scopeAccountID is non-zero.
func billMatches(b *ent.RecurringBill, scopeAccountID int, t *ent.Transaction, occ time.Time) bool {
	pattern := strings.TrimSpace(b.MatchPattern)
	if pattern == "" {
		return false
	}
	needle := strings.ToLower(pattern)
	if !strings.Contains(strings.ToLower(t.Description), needle) &&
		!strings.Contains(strings.ToLower(t.Merchant), needle) {
		return false
	}
	if scopeAccountID != 0 {
		a := t.Edges.Account
		if a == nil || a.ID != scopeAccountID {
			return false
		}
	}
	if !b.AmountVariable {
		tolerance := b.ExpectedAmount * b.AmountTolerancePct / 100
		if math.Abs(math.Abs(t.Amount)-b.ExpectedAmount) > tolerance {
			return false
		}
	}
	return absInt(daysBetween(occ, t.PostedDate)) <= b.MatchWindowDays
}

// bestMatch picks the posted row that settles one cycle: the closest posted date to the
// occurrence among the rows satisfying billMatches, ties going to the earlier row (the
// candidates arrive posted_date/id ascending), so a re-run is deterministic. A row
// already linked to this bill is skipped; the (bill, transaction) unique index is the
// backstop.
func bestMatch(b *ent.RecurringBill, scopeAccountID int, rows []*ent.Transaction, occ time.Time, used map[int]bool) *ent.Transaction {
	var best *ent.Transaction
	bestGap := 0
	for _, t := range rows {
		if used[t.ID] || !billMatches(b, scopeAccountID, t, occ) {
			continue
		}
		gap := absInt(daysBetween(occ, t.PostedDate))
		if best == nil || gap < bestGap {
			best, bestGap = t, gap
		}
	}
	return best
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
// re-ingest.
//
// Paused bills are skipped (a suspended subscription is not billing). Ended bills are
// still reconciled up to ended_on, so historical cycles settle after the fact.
func ReconcileBills(ctx context.Context, client *ent.Client) (ReconcileSummary, error) {
	sum := ReconcileSummary{}
	candidates, err := client.RecurringBill.Query().
		Where(recurringbill.StatusIn(recurringbill.StatusActive, recurringbill.StatusEnded)).
		WithAccount().
		Order(ent.Asc(recurringbill.FieldID)).
		All(ctx)
	if err != nil {
		return sum, err
	}

	today := normalizeDate(time.Now())
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
		occs := reconcileWindow(b, today)
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

	for _, b := range bills {
		sum.BillsScanned++
		scopeAccountID := 0
		if a := b.Edges.Account; a != nil {
			scopeAccountID = a.ID
		}
		settled, used, err := existingPayments(ctx, client, b.ID)
		if err != nil {
			return sum, err
		}
		for _, occ := range cycles[b.ID] {
			sum.CyclesChecked++
			if settled[occ.Unix()] {
				continue // already reconciled, auto or manual; never overwritten
			}
			t := bestMatch(b, scopeAccountID, rows, occ, used)
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

// LinkBillPayment records a hand-made link between one cycle of a bill and a posted
// transaction, with method=manual so the matching pass never touches it. occurrence is
// optional: when nil the cycle closest to the transaction's posted date is used. If the
// cycle already carries an AUTO link, the manual one replaces it (the human wins over the
// matcher); an existing MANUAL link is left alone and reported as a conflict.
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
	}

	existing, err := client.BillPayment.Query().
		Where(
			billpayment.HasBillWith(recurringbill.IDEQ(billID)),
			billpayment.OccurrenceDateEQ(occ),
		).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}
	if existing != nil {
		if existing.Method == billpayment.MethodManual {
			return nil, ErrCycleAlreadyLinked
		}
		if err := client.BillPayment.DeleteOne(existing).Exec(ctx); err != nil {
			return nil, err
		}
	}
	return client.BillPayment.Create().
		SetBillID(billID).
		SetTransactionID(t.ID).
		SetOccurrenceDate(occ).
		SetMethod(billpayment.MethodManual).
		Save(ctx)
}
