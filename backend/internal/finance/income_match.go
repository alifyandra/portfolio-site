package finance

import (
	"context"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/incomereceipt"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
)

// The income matching pass (ReconcileIncome), the credit-side sibling of ReconcileBills:
// one owner-typed pattern per income, no LLM. It links an unsettled pay cycle to the
// posted credit that paid it, as an IncomeReceipt with method auto, and like the bill
// pass it runs after the ingest transaction commits, over rows already in the ledger.
//
// The rule is a per-income AND of five clauses, over credits that are eligible at all:
// never a move between the owner's own accounts (isInternalTransfer, the spending
// roll-ups' rule) and never a credit that reads like money coming back (refundRe: a
// refund, reversal or chargeback), since neither is anyone paying the owner. A hand link
// can still settle a cycle with either.
//
//
//  1. match_pattern occurs case-insensitively in the description OR merchant on word
//     boundaries (containsToken: "ATO" is not found in "POTATO"); an empty pattern never
//     matches, so that income is linked by hand only;
//  2. account scope, when the income names its account;
//  3. a credit (positive amount), since income is money in;
//  4. the amount within incomeAmountTolerancePct of expected_amount;
//  5. posted no more than earlyPayDays before the pay date (pay due on a weekend lands
//     the Friday before, the same bound a hand link has) and no more than
//     incomeMatchLateDays after it.
//
// Unlike a bill the tolerance and window are constants, not columns: pay is regular
// enough for one rule, and a hand link covers the exception.

// refundRe finds the words that mark a credit as money coming back rather than money
// earned. Such a credit can match an income's pattern and amount (a refund from the
// employer's salary-packaging provider) and is never that income's pay.
var refundRe = regexp.MustCompile(`(?i)\b(?:refund(?:s|ed)?|reversal|reversed|charge ?backs?)\b`)

// autoMatchable reports whether a credit may be considered by the matching pass at all.
func autoMatchable(t *ent.Transaction, own4 map[string]bool) bool {
	return !isInternalTransfer(t.Description, own4) && !refundRe.MatchString(t.Description) && !refundRe.MatchString(t.Merchant)
}

const (
	// incomeMatchLateDays is how long after its pay date a credit may post and still
	// settle that cycle: the bills' default match_window_days.
	incomeMatchLateDays = 5
	// incomeAmountTolerancePct is the bills' default amount_tolerance_pct.
	incomeAmountTolerancePct = 10
)

// IncomeReconcileSummary reports what one income matching pass did, with the work it
// performed, for the same reason ReconcileSummary does: it runs inline on the ingest.
type IncomeReconcileSummary struct {
	IncomesScanned     int
	CyclesChecked      int
	CandidatesCompared int
	ReceiptsLinked     int
	// Ambiguous counts credits two incomes fit equally well (same cents off, same distance
	// from their pay dates). They are left for a hand link rather than guessed.
	Ambiguous int
}

// incomeMatcher is one income's compiled rule.
type incomeMatcher struct {
	income  *ent.RecurringIncome
	needle  string
	scopeID int
}

func newIncomeMatcher(inc *ent.RecurringIncome) incomeMatcher {
	m := incomeMatcher{income: inc, needle: strings.ToLower(strings.TrimSpace(inc.MatchPattern))}
	if a := inc.Edges.Account; a != nil {
		m.scopeID = a.ID
	}
	return m
}

// matches applies the five clauses to one candidate row for one pay date.
func (m incomeMatcher) matches(c candidate, occ time.Time) bool {
	if m.needle == "" || c.txn.Amount <= 0 {
		return false
	}
	if !containsToken(c.descLower, m.needle) && !containsToken(c.merchLower, m.needle) {
		return false
	}
	if m.scopeID != 0 && c.accountID != m.scopeID {
		return false
	}
	expected := m.income.ExpectedAmount
	if math.Abs(c.txn.Amount-expected) > expected*incomeAmountTolerancePct/100 {
		return false
	}
	gap := daysBetween(occ, c.day)
	return gap >= -earlyPayDays && gap <= incomeMatchLateDays
}

// incomeFit is one way a credit could settle a cycle: which income (an index into the
// pass's matchers), which pay date, how far off the expected amount in whole cents, and
// the signed day offset from the pay date (negative when paid early).
type incomeFit struct {
	m      int
	occ    time.Time
	c      candidate
	cents  int64
	offset int
}

// closer orders two fits of ONE credit to two incomes: fewer cents off the expected amount
// first, then the nearer pay date. Equal on both is a tie, which no rule here can break.
func (f incomeFit) closer(o incomeFit) int {
	if f.cents != o.cents {
		return cmpInt64(f.cents, o.cents)
	}
	return cmpInt64(int64(absInt(f.offset)), int64(absInt(o.offset)))
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// centsOff is how far a credit is from an expected amount, in whole cents, so two fits
// compare exactly rather than on float noise.
func centsOff(amount, expected float64) int64 {
	return int64(math.Round(math.Abs(amount-expected) * 100))
}

// assignIncomeCredits decides which credit settles which cycle, across every income in
// the pass at once, so incomes sharing a payer's pattern cannot steal each other's pay.
// fits holds every (income, unsettled cycle, unused credit) the rule accepts, in pass
// order (income, then pay date, then posted day and id).
//
//  1. Each credit belongs to the income it fits best (incomeFit.closer over that income's
//     best cycle). When two incomes tie for it, nothing says which one it pays: it is
//     left for a hand link and counted in the second return.
//  2. Within its income a cycle takes the owned credit nearest its pay date, ties going to
//     the earlier day and then the lower id (the single-income rule), and one credit
//     settles one cycle.
//
// The result is in pass order, so a re-run is deterministic.
func assignIncomeCredits(fits []incomeFit) ([]incomeFit, int) {
	type key struct{ txn, m int }
	bestPer := map[key]incomeFit{}
	var txnOrder []int
	incomesOf := map[int][]int{}
	for _, f := range fits {
		k := key{f.c.txn.ID, f.m}
		b, seen := bestPer[k]
		if !seen {
			if len(incomesOf[k.txn]) == 0 {
				txnOrder = append(txnOrder, k.txn)
			}
			incomesOf[k.txn] = append(incomesOf[k.txn], f.m)
		}
		if !seen || f.closer(b) < 0 {
			bestPer[k] = f
		}
	}
	owner := map[int]int{}
	ambiguous := 0
	for _, txn := range txnOrder {
		ms := incomesOf[txn]
		win, tied := bestPer[key{txn, ms[0]}], false
		for _, m := range ms[1:] {
			switch f := bestPer[key{txn, m}]; f.closer(win) {
			case -1:
				win, tied = f, false
			case 0:
				tied = true
			}
		}
		if tied {
			ambiguous++
			continue
		}
		owner[txn] = win.m
	}

	type cycle struct {
		m   int
		occ int64
	}
	var order []cycle
	byCycle := map[cycle][]incomeFit{}
	for _, f := range fits {
		if m, ok := owner[f.c.txn.ID]; !ok || m != f.m {
			continue
		}
		cy := cycle{f.m, f.occ.Unix()}
		if _, seen := byCycle[cy]; !seen {
			order = append(order, cy)
		}
		byCycle[cy] = append(byCycle[cy], f)
	}
	used := map[int]bool{}
	out := make([]incomeFit, 0, len(order))
	for _, cy := range order {
		var best *incomeFit
		for i := range byCycle[cy] {
			f := &byCycle[cy][i]
			if used[f.c.txn.ID] {
				continue // an earlier cycle of the same income took it
			}
			if best == nil || absInt(f.offset) < absInt(best.offset) {
				best = f
			}
		}
		if best != nil {
			used[best.c.txn.ID] = true
			out = append(out, *best)
		}
	}
	return out, ambiguous
}

// incomeCycleWindow is the set of an income's pay dates one pass should try to settle,
// bounded on both sides like cycleWindow. A pay date can draw on credits posted from
// earlyPayDays before it to incomeMatchLateDays after it, so the lower bound is the
// latest of the anchor, the ledger's start less the late window, and the requested
// range's start less the late window; the upper bound is the earliest of a week past
// today (a credit posted today may pay a cycle up to earlyPayDays ahead), the requested
// range's end plus earlyPayDays, and ended_on.
func incomeCycleWindow(inc *ent.RecurringIncome, coverageFrom *time.Time, opts ReconcileOptions, today time.Time) []time.Time {
	from := normalizeDate(inc.AnchorDate)
	if coverageFrom != nil {
		if lo := normalizeDate(*coverageFrom).AddDate(0, 0, -incomeMatchLateDays); lo.After(from) {
			from = lo
		}
	}
	if opts.From != nil {
		if lo := normalizeDate(*opts.From).AddDate(0, 0, -incomeMatchLateDays); lo.After(from) {
			from = lo
		}
	}
	to := normalizeDate(today).AddDate(0, 0, earlyPayDays)
	if opts.To != nil {
		if hi := normalizeDate(*opts.To).AddDate(0, 0, earlyPayDays); hi.Before(to) {
			to = hi
		}
	}
	if inc.EndedOn != nil {
		if e := normalizeDate(*inc.EndedOn); e.Before(to) {
			to = e
		}
	}
	return occurrencesBetween(inc.AnchorDate, billCadence(inc.Cadence), from, to)
}

// ReconcileIncome runs the income matching pass. It is idempotent and re-runnable: a
// settled cycle is skipped whoever settled it, so a hand-made receipt is never touched,
// and the unique indexes on IncomeReceipt make a concurrent double run a no-op. Paused
// incomes are skipped; ended ones are matched up to ended_on, so late history settles.
// Which credit settles which cycle is decided across all incomes at once
// (assignIncomeCredits), and a credit two incomes fit equally well is not linked.
func ReconcileIncome(ctx context.Context, client *ent.Client, opts ReconcileOptions) (IncomeReconcileSummary, error) {
	sum := IncomeReconcileSummary{}
	incomes, err := client.RecurringIncome.Query().
		Where(
			recurringincome.StatusIn(recurringincome.StatusActive, recurringincome.StatusEnded),
			recurringincome.MatchPatternNEQ(""),
		).
		WithAccount().
		Order(ent.Asc(recurringincome.FieldID)).
		All(ctx)
	if err != nil {
		return sum, err
	}
	coverageFrom, err := earliestPostedDate(ctx, client)
	if err != nil {
		return sum, err
	}
	if coverageFrom == nil {
		return sum, nil
	}

	today := localToday()
	cycles := make(map[int][]time.Time, len(incomes))
	var from, to time.Time
	matchers := make([]incomeMatcher, 0, len(incomes))
	for _, inc := range incomes {
		m := newIncomeMatcher(inc)
		if m.needle == "" {
			continue // a whitespace-only pattern
		}
		occs := incomeCycleWindow(inc, coverageFrom, opts, today)
		if len(occs) == 0 {
			continue
		}
		matchers = append(matchers, m)
		cycles[inc.ID] = occs
		lo := occs[0].AddDate(0, 0, -earlyPayDays)
		hi := occs[len(occs)-1].AddDate(0, 0, incomeMatchLateDays)
		if from.IsZero() || lo.Before(from) {
			from = lo
		}
		if to.IsZero() || hi.After(to) {
			to = hi
		}
	}
	if len(matchers) == 0 {
		return sum, nil
	}

	rows, err := client.Transaction.Query().
		Where(
			transaction.PostedDateGTE(from),
			transaction.PostedDateLTE(to),
			transaction.AmountGT(0),
		).
		WithAccount().
		Order(ent.Asc(transaction.FieldPostedDate), ent.Asc(transaction.FieldID)).
		All(ctx)
	if err != nil {
		return sum, err
	}
	own4, err := loadOwnLast4(ctx, client)
	if err != nil {
		return sum, err
	}
	byDay := make(map[int64][]candidate, len(rows))
	for _, t := range rows {
		if !autoMatchable(t, own4) {
			continue
		}
		c := newCandidate(t)
		byDay[c.day.Unix()] = append(byDay[c.day.Unix()], c)
	}
	// Credits already settling a receipt of any income in the window: one credit, one
	// payment, across every income.
	usedIDs, err := client.IncomeReceipt.Query().
		Where(incomereceipt.HasTransactionWith(transaction.PostedDateGTE(from), transaction.PostedDateLTE(to))).
		QueryTransaction().
		IDs(ctx)
	if err != nil {
		return sum, err
	}
	used := make(map[int]bool, len(usedIDs))
	for _, id := range usedIDs {
		used[id] = true
	}

	var fits []incomeFit
	for mi, m := range matchers {
		sum.IncomesScanned++
		settled, err := settledReceipts(ctx, client, m.income.ID)
		if err != nil {
			return sum, err
		}
		for _, occ := range cycles[m.income.ID] {
			if settled[occ.Unix()] {
				continue // manual or auto, never overwritten
			}
			sum.CyclesChecked++
			for d := -earlyPayDays; d <= incomeMatchLateDays; d++ {
				for _, c := range byDay[occ.AddDate(0, 0, d).Unix()] {
					if used[c.txn.ID] {
						continue // one credit is one payment, across every income
					}
					sum.CandidatesCompared++
					if m.matches(c, occ) {
						fits = append(fits, incomeFit{m: mi, occ: occ, c: c, cents: centsOff(c.txn.Amount, m.income.ExpectedAmount), offset: d})
					}
				}
			}
		}
	}
	assigned, ambiguous := assignIncomeCredits(fits)
	sum.Ambiguous = ambiguous
	for _, f := range assigned {
		err := client.IncomeReceipt.Create().
			SetIncomeID(matchers[f.m].income.ID).
			SetTransactionID(f.c.txn.ID).
			SetOccurrenceDate(f.occ).
			SetMethod(incomereceipt.MethodAuto).
			Exec(ctx)
		if ent.IsConstraintError(err) {
			continue // a concurrent pass or a hand link got there first; the indexes did their job
		}
		if err != nil {
			return sum, err
		}
		sum.ReceiptsLinked++
	}
	return sum, nil
}

// settledReceipts is the set of one income's pay dates that already carry a receipt.
func settledReceipts(ctx context.Context, client *ent.Client, incomeID int) (map[int64]bool, error) {
	rows, err := client.IncomeReceipt.Query().
		Where(incomereceipt.HasIncomeWith(recurringincome.IDEQ(incomeID))).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(rows))
	for _, r := range rows {
		out[normalizeDate(r.OccurrenceDate).Unix()] = true
	}
	return out, nil
}

// containsToken reports whether needle occurs in hay (both lowercased already) on word
// boundaries: an occurrence counts only when the character before it is not the same kind
// (letter or digit) as the needle's first character, and the character after it not the
// same kind as its last. So "ato" finds "ato payg" and "ato-0123" but not "potato", and
// "acme" still finds "acme0123", where a reference number is glued to the name. A needle
// edge that is neither (punctuation, a space) needs no boundary. Empty never matches.
func containsToken(hay, needle string) bool {
	if needle == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(needle)
	last, _ := utf8.DecodeLastRuneInString(needle)
	for off := 0; off <= len(hay)-len(needle); {
		i := strings.Index(hay[off:], needle)
		if i < 0 {
			return false
		}
		start, end := off+i, off+i+len(needle)
		before, _ := utf8.DecodeLastRuneInString(hay[:start])
		after, _ := utf8.DecodeRuneInString(hay[end:])
		if (start == 0 || !sameWordClass(before, first)) && (end == len(hay) || !sameWordClass(after, last)) {
			return true
		}
		_, size := utf8.DecodeRuneInString(hay[start:])
		off = start + size
	}
	return false
}

// sameWordClass reports whether r continues the word that edge ends: both letters or
// both digits. An edge that is neither is never continued.
func sameWordClass(r, edge rune) bool {
	switch {
	case unicode.IsLetter(edge):
		return unicode.IsLetter(r)
	case unicode.IsDigit(edge):
		return unicode.IsDigit(r)
	}
	return false
}
