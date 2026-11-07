package finance

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/incomereceipt"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
)

// The income matching pass (ReconcileIncome), the credit-side sibling of ReconcileBills:
// one owner-typed substring per income, no LLM. It links an unsettled pay cycle to the
// posted credit that paid it, as an IncomeReceipt with method auto, and like the bill
// pass it runs after the ingest transaction commits, over rows already in the ledger.
//
// The rule is a per-income AND of five clauses:
//
//  1. match_pattern is a case-insensitive substring of the description OR merchant; an
//     empty pattern never matches, so that income is linked by hand only;
//  2. account scope, when the income names its account;
//  3. a credit (positive amount), since income is money in;
//  4. the amount within incomeAmountTolerancePct of expected_amount;
//  5. posted no more than earlyPayDays before the pay date (pay due on a weekend lands
//     the Friday before, the same bound a hand link has) and no more than
//     incomeMatchLateDays after it.
//
// Unlike a bill the tolerance and window are constants, not columns: pay is regular
// enough for one rule, and a hand link covers the exception.

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
	if !strings.Contains(c.descLower, m.needle) && !strings.Contains(c.merchLower, m.needle) {
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

// bestMatch picks the row that settles one pay date: the closest posted date, ties going
// to the earlier day and then the lower id, skipping any credit that already settles a
// receipt of ANY income (one credit is one payment). The second return is the work done.
func (m incomeMatcher) bestMatch(byDay map[int64][]candidate, occ time.Time, used map[int]bool) (*ent.Transaction, int) {
	var best *ent.Transaction
	bestGap, compared := 0, 0
	for d := -earlyPayDays; d <= incomeMatchLateDays; d++ {
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
	byDay := make(map[int64][]candidate, len(rows))
	for _, t := range rows {
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

	for _, m := range matchers {
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
			t, compared := m.bestMatch(byDay, occ, used)
			sum.CandidatesCompared += compared
			if t == nil {
				continue
			}
			err := client.IncomeReceipt.Create().
				SetIncomeID(m.income.ID).
				SetTransactionID(t.ID).
				SetOccurrenceDate(occ).
				SetMethod(incomereceipt.MethodAuto).
				Exec(ctx)
			if ent.IsConstraintError(err) {
				// A concurrent pass or a hand link got there first; the indexes did their job.
				settled[occ.Unix()] = true
				used[t.ID] = true
				continue
			}
			if err != nil {
				return sum, err
			}
			settled[occ.Unix()] = true
			used[t.ID] = true
			sum.ReceiptsLinked++
		}
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
