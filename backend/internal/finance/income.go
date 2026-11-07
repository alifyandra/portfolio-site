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
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
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
// billCadence. Nothing here rounds money: the API rounds to the cent at its DTO boundary.

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

// ErrCycleOutOfRange is returned when a hand link targets a pay date that is on the grid
// but is not a cycle the credit can have paid: before the anchor (no cycle existed), after
// ended_on (none was expected), or more than earlyPayDays after the credit posted. The API
// maps it to a 422.
var ErrCycleOutOfRange = errors.New("finance: that pay date is outside the income's cycles: it must fall on or after the anchor date, at most 7 days after the transaction posted, and not after ended_on")

// earlyPayDays is how far AHEAD of its posted date a credit may settle a cycle. Pay that
// falls on a weekend or a public holiday usually lands the business day before, so a
// Sunday pay date is paid on the Friday and the credit posts before its cycle. A week
// covers a weekend plus a holiday on either side; anything earlier is not that cycle's
// pay. There is no matching bound the other way, since late pay is still pay.
const earlyPayDays = 7

// ErrTransactionReceived is returned when the credit already settles a receipt of ANY
// income. One posted credit is one payment, so it pays at most one cycle. The API maps it
// to a 409.
var ErrTransactionReceived = errors.New("finance: that transaction is already linked as an income receipt")

// ErrWrongAccount is returned when the income names the account it is paid into and the
// credit posted to a different one. The API maps it to a 422.
var ErrWrongAccount = errors.New("finance: that transaction was posted to a different account than the income is paid into")

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

	// NextExpected is the earliest pay date on or after today and, when the income has an
	// ended_on, on or before it (nothing after it is expected). Nil when no such date is
	// left, so a finished income never reports a past date as its next one.
	NextExpected *time.Time
	// DaysUntil is whole days from today to NextExpected, never negative; nil with it.
	DaysUntil *int
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
// income never inflates what is expected, and never one whose ended_on has passed.
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
// set. Results are ordered soonest first (days_until ascending, incomes with no pay date
// left last, then name). Like the bill
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
		// within_days is a window AHEAD: an income with no pay date left has nothing
		// coming up.
		if f.WithinDays > 0 && (v.DaysUntil == nil || *v.DaysUntil > f.WithinDays) {
			continue
		}
		views = append(views, v)
	}
	sort.SliceStable(views, func(i, j int) bool {
		a, b := views[i].DaysUntil, views[j].DaysUntil
		switch {
		case a == nil && b == nil:
		case a == nil:
			return false
		case b == nil:
			return true
		case *a != *b:
			return *a < *b
		}
		return views[i].Name < views[j].Name
	})
	return views, incomeTotals(views, today), nil
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
	// An income that stopped expects nothing after ended_on, so a pay date past it is no
	// pay date at all; otherwise a finished job keeps reporting a pay day forever.
	if inc.EndedOn == nil || !next.After(normalizeDate(*inc.EndedOn)) {
		days := daysBetween(today, next)
		v.NextExpected = &next
		v.DaysUntil = &days
	}
	return v, nil
}

// incomeTotals rolls a returned income set into the expected-money figures. Only ACTIVE
// incomes count toward the money, and never one whose ended_on is before today: the API
// keeps ended_on to ended incomes, but a row written before it did must not inflate what
// is expected.
func incomeTotals(views []IncomeView, today time.Time) IncomeTotals {
	t := IncomeTotals{Count: len(views)}
	for _, v := range views {
		if v.Status != string(recurringincome.StatusActive) {
			continue
		}
		if v.EndedOn != nil && normalizeDate(*v.EndedOn).Before(today) {
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
// (ErrNotAPayDate otherwise). Either way the cycle must be one the credit can have paid:
// not before the anchor, not after ended_on, and at most earlyPayDays after the posted
// date, so a Friday credit settles the Sunday pay date even while that Sunday is still
// ahead (ErrCycleOutOfRange). The
// transaction must be a credit (ErrNotACredit) and, when the income names its account,
// posted to that account (ErrWrongAccount).
//
// A cycle that already has a receipt is refused with ErrIncomeCycleReceived rather than
// replaced, since every receipt is a human decision, and a credit that already settles a
// receipt of any income is refused with ErrTransactionReceived. Both are checked before
// the insert and again after a constraint error, so a concurrent link that wins the race
// is still reported as the conflict it is.
func LinkIncomeReceipt(ctx context.Context, client *ent.Client, incomeID, txnID int, occurrence *time.Time) (*ent.IncomeReceipt, error) {
	inc, err := client.RecurringIncome.Query().
		Where(recurringincome.IDEQ(incomeID)).
		WithAccount().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	t, err := client.Transaction.Query().
		Where(transaction.IDEQ(txnID)).
		WithAccount().
		Only(ctx)
	if err != nil {
		return nil, err
	}
	if t.Amount <= 0 {
		return nil, ErrNotACredit
	}
	if a := inc.Edges.Account; a != nil && (t.Edges.Account == nil || t.Edges.Account.ID != a.ID) {
		return nil, ErrWrongAccount
	}
	c := billCadence(inc.Cadence)
	occ := nearestOccurrence(inc.AnchorDate, c, t.PostedDate)
	if occurrence != nil {
		occ = normalizeDate(*occurrence)
		if !isOccurrence(inc.AnchorDate, c, occ) {
			return nil, ErrNotAPayDate
		}
	}
	if !cyclePayable(inc, occ, t.PostedDate) {
		return nil, ErrCycleOutOfRange
	}

	if err := receiptConflict(ctx, client, incomeID, txnID, occ); err != nil {
		return nil, err
	}
	r, err := client.IncomeReceipt.Create().
		SetIncomeID(incomeID).
		SetTransactionID(t.ID).
		SetOccurrenceDate(occ).
		Save(ctx)
	if ent.IsConstraintError(err) {
		// Someone linked between the check and the insert: name what they took.
		if conflict := receiptConflict(ctx, client, incomeID, txnID, occ); conflict != nil {
			return nil, conflict
		}
	}
	if err != nil {
		return nil, err
	}
	r.Edges.Transaction = t // already loaded, so the caller can render the receipt whole
	return r, nil
}

// IncomeReceiptView is one receipt with the transaction it links: the cycle it settles,
// and when and how much actually arrived, so a receipt can be recognised (and removed)
// without a second lookup.
type IncomeReceiptView struct {
	ID             int
	IncomeID       int
	TransactionID  int
	OccurrenceDate time.Time
	PostedDate     time.Time
	Amount         float64
	Description    string
	Merchant       string
	CreatedAt      time.Time
}

// NewIncomeReceiptView maps a receipt to its view. The transaction edge must be loaded.
func NewIncomeReceiptView(incomeID int, r *ent.IncomeReceipt) IncomeReceiptView {
	v := IncomeReceiptView{
		ID:             r.ID,
		IncomeID:       incomeID,
		OccurrenceDate: normalizeDate(r.OccurrenceDate),
		CreatedAt:      r.CreatedAt,
	}
	if t := r.Edges.Transaction; t != nil {
		v.TransactionID = t.ID
		v.PostedDate = normalizeDate(t.PostedDate)
		v.Amount = t.Amount
		v.Description = t.Description
		v.Merchant = t.Merchant
	}
	return v
}

// ListIncomeReceipts returns one income's receipts, newest cycle first. An unknown income
// is an ent not-found error rather than an empty list, so a caller can tell "no receipts
// yet" from "no such income".
func ListIncomeReceipts(ctx context.Context, client *ent.Client, incomeID int) ([]IncomeReceiptView, error) {
	if _, err := client.RecurringIncome.Get(ctx, incomeID); err != nil {
		return nil, err
	}
	rows, err := client.IncomeReceipt.Query().
		Where(incomereceipt.HasIncomeWith(recurringincome.IDEQ(incomeID))).
		WithTransaction().
		Order(ent.Desc(incomereceipt.FieldOccurrenceDate), ent.Desc(incomereceipt.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IncomeReceiptView, 0, len(rows))
	for _, r := range rows {
		out = append(out, NewIncomeReceiptView(incomeID, r))
	}
	return out, nil
}

// cyclePayable reports whether occ is a cycle that a credit posted on posted can have
// paid: on or after the anchor (there is no cycle before it), on or before ended_on when
// the income has one (nothing after it was expected), and at most earlyPayDays after the
// posted date (early pay, see earlyPayDays). Since a posted credit is never in the future,
// that last bound also caps the cycle at a week past today. occ is already known to be on
// the grid.
func cyclePayable(inc *ent.RecurringIncome, occ, posted time.Time) bool {
	if occ.Before(normalizeDate(inc.AnchorDate)) || occ.After(normalizeDate(posted).AddDate(0, 0, earlyPayDays)) {
		return false
	}
	return inc.EndedOn == nil || !occ.After(normalizeDate(*inc.EndedOn))
}

// receiptConflict names which unique rule a new receipt would break, or returns nil: the
// cycle already received (ErrIncomeCycleReceived) is checked first, then the credit
// already used by any income (ErrTransactionReceived).
func receiptConflict(ctx context.Context, client *ent.Client, incomeID, txnID int, occ time.Time) error {
	taken, err := client.IncomeReceipt.Query().
		Where(
			incomereceipt.HasIncomeWith(recurringincome.IDEQ(incomeID)),
			incomereceipt.OccurrenceDateEQ(occ),
		).
		Exist(ctx)
	if err != nil {
		return err
	}
	if taken {
		return ErrIncomeCycleReceived
	}
	used, err := client.IncomeReceipt.Query().
		Where(incomereceipt.HasTransactionWith(transaction.IDEQ(txnID))).
		Exist(ctx)
	if err != nil {
		return err
	}
	if used {
		return ErrTransactionReceived
	}
	return nil
}
