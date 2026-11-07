package finance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/incomereceipt"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
)

// Recurring Income (ADR 0019). A RecurringIncome is a DECLARED repeating receipt
// (salary, rent received, a regular transfer in): a RecurringBill's shape in the opposite
// direction, kept as its own entity because a bill is a commitment and income is not.
// It is what the Projection will walk forward alongside the bills, so this file answers
// what is expected, when the next pay day falls, and when money last actually arrived.
//
// Every receipt is linked by hand for now (LinkIncomeReceipt). There is no matching pass
// and so no "missed income" flag: an absence only means something where a pass could
// have found the credit, which is the same reason a bill with no match_pattern is never
// reported overdue. The Recurrence Suggestion pass is where a counterparty to match on
// first appears.
//
// The occurrence arithmetic is the bill generator's (bills.go), reached through
// billCadence. Nothing here rounds money; the caller formats.

// ErrIncomeCycleReceived is returned when a hand link targets a cycle that already carries
// a receipt. The API maps it to a 409.
var ErrIncomeCycleReceived = errors.New("finance: that pay cycle already has a receipt linked")

// ErrNotAPayDate is returned when a hand link names a date that is not one of the
// income's derived pay dates, since such a link would settle a cycle that does not
// exist. The API maps it to a 422.
var ErrNotAPayDate = errors.New("finance: that date is not one of the income's pay dates")

// ErrNotACredit is returned when a hand link names a debit. Income is money in, so a row
// with a non-positive amount can never be what paid a cycle. The API maps it to a 422.
var ErrNotACredit = errors.New("finance: only a credit (a positive amount) can be an income receipt")

// IncomeFilter narrows a recurring-income query. Status is "active", "paused", "ended",
// or "" / "all" for every status. WithinDays > 0 keeps only incomes whose next pay day
// falls inside that many days from today. AccountID > 0 keeps only incomes paid into
// that account, which EXCLUDES incomes with no account set.
type IncomeFilter struct {
	Status     string
	WithinDays int
	AccountID  int
}

// IncomeView is one recurring income's stored columns plus the part derived on read.
// AccountID is 0 (and AccountName empty) when the income carries no account edge.
type IncomeView struct {
	ID             int
	Name           string
	ExpectedAmount float64
	Currency       string
	Cadence        string
	AnchorDate     time.Time
	Status         string
	EndedOn        *time.Time
	Notes          string
	AccountID      int
	AccountName    string
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// NextExpected is the earliest pay date on or after today, or, once an income has
	// ended, the last pay date on or before ended_on (nothing after it is expected).
	NextExpected time.Time
	// DaysUntil is whole days from today to NextExpected; negative only for an ended
	// income's final cycle.
	DaysUntil int
	// LastReceivedDate / LastReceivedAmount come from the newest IncomeReceipt's
	// transaction: when the money actually posted and how much. Both nil until a cycle is
	// linked. The amount differing from ExpectedAmount is information, not an error.
	LastReceivedDate   *time.Time
	LastReceivedAmount *float64
	// ExpectedMonthly normalises ExpectedAmount to a per-month figure so incomes on
	// different cadences can be summed.
	ExpectedMonthly float64
}

// IncomeTotals is the expected-money roll-up over a returned income set. Count is every
// income returned; the two money figures count ACTIVE incomes only, so a paused or ended
// income never inflates what is expected.
type IncomeTotals struct {
	ExpectedTotal     float64
	MonthlyEquivalent float64
	Count             int
}

// billCadence maps an income cadence onto the bill enum so the one occurrence generator
// serves both. The two enums carry the same value set by construction, and
// TestIncomeCadence_SameValuesAsBillCadence fails if they ever drift.
func billCadence(c recurringincome.Cadence) recurringbill.Cadence {
	return recurringbill.Cadence(c)
}

// ListRecurringIncome returns the declared incomes matching the filter with their derived
// pay dates and last-received figures, plus the expected-money roll-up over the returned
// set. Results are ordered soonest first (days_until ascending, then name). Like the bill
// list, the derived date is computed in Go, so within_days filtering and ordering happen
// after the load; the set is a handful of rows.
func ListRecurringIncome(ctx context.Context, client *ent.Client, f IncomeFilter) ([]IncomeView, IncomeTotals, error) {
	q := client.RecurringIncome.Query().WithAccount()
	switch status := strings.ToLower(strings.TrimSpace(f.Status)); status {
	case "", "all":
		// every status
	case string(recurringincome.StatusActive), string(recurringincome.StatusPaused), string(recurringincome.StatusEnded):
		q = q.Where(recurringincome.StatusEQ(recurringincome.Status(status)))
	default:
		return nil, IncomeTotals{}, fmt.Errorf("finance: invalid income status %q (want active, paused, ended or all)", f.Status)
	}
	if f.AccountID != 0 {
		q = q.Where(recurringincome.HasAccountWith(account.IDEQ(f.AccountID)))
	}
	incomes, err := q.Order(ent.Asc(recurringincome.FieldName)).All(ctx)
	if err != nil {
		return nil, IncomeTotals{}, err
	}

	today := localToday()
	views := make([]IncomeView, 0, len(incomes))
	for _, inc := range incomes {
		v, err := incomeView(ctx, client, inc, today)
		if err != nil {
			return nil, IncomeTotals{}, err
		}
		// within_days is a window AHEAD: an ended income's final cycle in the past is
		// history, not a pay day coming up.
		if f.WithinDays > 0 && (v.DaysUntil < 0 || v.DaysUntil > f.WithinDays) {
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
	return views, incomeTotals(views), nil
}

// incomeView maps one stored income to its view. It expects the account edge to be
// eager-loaded.
func incomeView(ctx context.Context, client *ent.Client, inc *ent.RecurringIncome, today time.Time) (IncomeView, error) {
	c := billCadence(inc.Cadence)
	v := IncomeView{
		ID:              inc.ID,
		Name:            inc.Name,
		ExpectedAmount:  inc.ExpectedAmount,
		Currency:        inc.Currency,
		Cadence:         string(inc.Cadence),
		AnchorDate:      normalizeDate(inc.AnchorDate),
		Status:          string(inc.Status),
		EndedOn:         inc.EndedOn,
		Notes:           inc.Notes,
		CreatedAt:       inc.CreatedAt,
		UpdatedAt:       inc.UpdatedAt,
		ExpectedMonthly: monthlyEquivalent(inc.ExpectedAmount, c),
	}
	if a := inc.Edges.Account; a != nil {
		v.AccountID = a.ID
		v.AccountName = a.Name
	}

	last, err := client.IncomeReceipt.Query().
		Where(incomereceipt.HasIncomeWith(recurringincome.IDEQ(inc.ID))).
		WithTransaction().
		Order(ent.Desc(incomereceipt.FieldOccurrenceDate), ent.Desc(incomereceipt.FieldID)).
		First(ctx)
	if err != nil && !ent.IsNotFound(err) {
		return IncomeView{}, err
	}
	if last != nil {
		if t := last.Edges.Transaction; t != nil {
			d := normalizeDate(t.PostedDate)
			amt := t.Amount // a receipt is always a credit (LinkIncomeReceipt), so already positive
			v.LastReceivedDate = &d
			v.LastReceivedAmount = &amt
		}
	}

	next := nextDue(inc.AnchorDate, c, today)
	// An income that stopped expects nothing after ended_on, so the generator is clipped
	// here; otherwise a finished job keeps reporting a pay day forever.
	if inc.EndedOn != nil {
		if e := normalizeDate(*inc.EndedOn); next.After(e) {
			next = lastOccurrenceOnOrBefore(inc.AnchorDate, c, e)
		}
	}
	v.NextExpected = next
	v.DaysUntil = daysBetween(today, next)
	return v, nil
}

// incomeTotals rolls a returned income set into the expected-money figures. Only ACTIVE
// incomes count toward the money.
func incomeTotals(views []IncomeView) IncomeTotals {
	t := IncomeTotals{Count: len(views)}
	for _, v := range views {
		if v.Status != string(recurringincome.StatusActive) {
			continue
		}
		t.ExpectedTotal += v.ExpectedAmount
		t.MonthlyEquivalent += v.ExpectedMonthly
	}
	return t
}

// LinkIncomeReceipt records a hand-made link between one cycle of an income and the
// posted credit that paid it.
//
// occurrence is optional: when nil the pay date closest to the transaction's posted date
// is used, and when given it must be one of the income's derived pay dates
// (ErrNotAPayDate otherwise). The transaction must be a credit (ErrNotACredit). A cycle
// that already has a receipt is refused with ErrIncomeCycleReceived rather than replaced,
// since every receipt is a human decision; the (income, transaction) unique index refuses
// one credit settling two cycles, surfacing as an ent constraint error.
func LinkIncomeReceipt(ctx context.Context, client *ent.Client, incomeID, txnID int, occurrence *time.Time) (*ent.IncomeReceipt, error) {
	inc, err := client.RecurringIncome.Get(ctx, incomeID)
	if err != nil {
		return nil, err
	}
	t, err := client.Transaction.Get(ctx, txnID)
	if err != nil {
		return nil, err
	}
	if t.Amount <= 0 {
		return nil, ErrNotACredit
	}
	c := billCadence(inc.Cadence)
	occ := nearestOccurrence(inc.AnchorDate, c, t.PostedDate)
	if occurrence != nil {
		occ = normalizeDate(*occurrence)
		if !isOccurrence(inc.AnchorDate, c, occ) {
			return nil, ErrNotAPayDate
		}
	}

	taken, err := client.IncomeReceipt.Query().
		Where(
			incomereceipt.HasIncomeWith(recurringincome.IDEQ(incomeID)),
			incomereceipt.OccurrenceDateEQ(occ),
		).
		Exist(ctx)
	if err != nil {
		return nil, err
	}
	if taken {
		return nil, ErrIncomeCycleReceived
	}
	r, err := client.IncomeReceipt.Create().
		SetIncomeID(incomeID).
		SetTransactionID(t.ID).
		SetOccurrenceDate(occ).
		Save(ctx)
	if err != nil {
		return nil, err
	}
	return r, nil
}
