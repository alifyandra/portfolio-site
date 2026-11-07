package finance

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	entgo "entgo.io/ent"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/ent/schema"
)

// Recurring Income tests (ADR 0019). The occurrence arithmetic is the bill generator's
// and is tested in bills_test.go; these cover what income adds on top: the read view and
// totals, the ended/paused rules, and the hand-made receipt link. All labels and amounts
// are invented.

// seedIncome creates an income with sane defaults, applying the caller's overrides last.
func seedIncome(t *testing.T, ctx context.Context, client *ent.Client, name string, amount float64, cadence recurringincome.Cadence, anchor time.Time, opts ...func(*ent.RecurringIncomeCreate)) *ent.RecurringIncome {
	t.Helper()
	create := client.RecurringIncome.Create().
		SetName(name).
		SetExpectedAmount(amount).
		SetCadence(cadence).
		SetAnchorDate(anchor)
	for _, o := range opts {
		o(create)
	}
	return create.SaveX(ctx)
}

// TestIncomeCadence_SameValuesAsBillCadence guards the one shortcut income takes: it
// drives the bill's occurrence generator by converting its cadence. That is only sound
// while the two enums carry the same value set, so a value added to one and not the other
// fails here instead of silently stepping monthly.
func TestIncomeCadence_SameValuesAsBillCadence(t *testing.T) {
	bill := enumValues(t, schema.RecurringBill{}.Fields(), "cadence")
	income := enumValues(t, schema.RecurringIncome{}.Fields(), "cadence")
	if !slices.Equal(bill, income) {
		t.Fatalf("income cadences %v differ from bill cadences %v; the shared generator would misstep", income, bill)
	}
}

// enumValues reads one enum field's declared values straight from the schema, sorted.
func enumValues(t *testing.T, fields []entgo.Field, name string) []string {
	t.Helper()
	for _, f := range fields {
		d := f.Descriptor()
		if d.Name != name {
			continue
		}
		out := make([]string, 0, len(d.Enums))
		for _, e := range d.Enums {
			out = append(out, e.V)
		}
		slices.Sort(out)
		return out
	}
	t.Fatalf("no field %q in schema", name)
	return nil
}

// TestListRecurringIncome_DerivedFields: an anchor today means next_expected is today and
// days_until is 0, the account edge joins its name in, nothing is received yet, and a bad
// status is an error rather than a silently unfiltered list.
func TestListRecurringIncome_DerivedFields(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)

	seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceFortnightly, today, func(c *ent.RecurringIncomeCreate) {
		c.SetAccountID(acc.ID)
	})

	views, _, err := ListRecurringIncome(ctx, client, IncomeFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %d, want 1", len(views))
	}
	v := views[0]
	if !v.NextExpected.Equal(today) || v.DaysUntil != 0 {
		t.Errorf("next_expected/days_until = %s/%d, want today/0", v.NextExpected.Format(dateLayoutTest), v.DaysUntil)
	}
	if v.AccountID != acc.ID || v.AccountName != "Everyday" {
		t.Errorf("account = %d/%q, want %d/Everyday", v.AccountID, v.AccountName, acc.ID)
	}
	if v.LastReceivedDate != nil || v.LastReceivedAmount != nil {
		t.Errorf("unlinked income = %+v, want no last received", v)
	}
	if want := 1000.0 * 26 / 12; v.ExpectedMonthly != want {
		t.Errorf("expected_monthly = %v, want %v", v.ExpectedMonthly, want)
	}
	if v.Currency != "AUD" || v.Status != "active" {
		t.Errorf("defaults = %s/%s, want AUD/active", v.Currency, v.Status)
	}

	if _, _, err := ListRecurringIncome(ctx, client, IncomeFilter{Status: "nonsense"}); err == nil {
		t.Error("an unknown status returned no error; want a rejection rather than an unfiltered list")
	}
}

// TestListRecurringIncome_TotalsCountActiveOnly: a paused or ended income stays in the
// list but never in the money, so a job that finished cannot inflate what is expected.
func TestListRecurringIncome_TotalsCountActiveOnly(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()

	seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceFortnightly, today)
	seedIncome(t, ctx, client, "Side work", 200, recurringincome.CadenceMonthly, today, func(c *ent.RecurringIncomeCreate) {
		c.SetStatus(recurringincome.StatusPaused)
	})
	seedIncome(t, ctx, client, "Old job", 900, recurringincome.CadenceMonthly, today.AddDate(0, -6, 0), func(c *ent.RecurringIncomeCreate) {
		c.SetStatus(recurringincome.StatusEnded).SetEndedOn(today.AddDate(0, 0, -30))
	})

	views, totals, err := ListRecurringIncome(ctx, client, IncomeFilter{Status: "all"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(views) != 3 || totals.Count != 3 {
		t.Fatalf("views = %d / count = %d, want 3/3", len(views), totals.Count)
	}
	if totals.ExpectedTotal != 1000 {
		t.Errorf("expected_total = %v, want 1000 (only the active income)", totals.ExpectedTotal)
	}
	if want := 1000.0 * 26 / 12; totals.MonthlyEquivalent != want {
		t.Errorf("monthly_equivalent = %v, want %v", totals.MonthlyEquivalent, want)
	}

	views, totals, err = ListRecurringIncome(ctx, client, IncomeFilter{Status: "active"})
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(views) != 1 || views[0].Name != "Salary" || totals.Count != 1 {
		t.Fatalf("active views = %v / count %d, want just Salary", views, totals.Count)
	}
}

// TestListRecurringIncome_EndedStopsAtEndedOn: an income that stopped expects nothing
// after ended_on, so its reported date is the last cycle it was ever paid, not a fresh
// future pay day that will never come.
func TestListRecurringIncome_EndedStopsAtEndedOn(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	anchor := today.AddDate(0, 0, -70) // ten weeks back: cycles every 7 days up to today
	endedOn := today.AddDate(0, 0, -10)

	seedIncome(t, ctx, client, "Contract", 300, recurringincome.CadenceWeekly, anchor, func(c *ent.RecurringIncomeCreate) {
		c.SetStatus(recurringincome.StatusEnded).SetEndedOn(endedOn)
	})

	views, _, err := ListRecurringIncome(ctx, client, IncomeFilter{Status: "all"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	v := views[0]
	if want := today.AddDate(0, 0, -14); !v.NextExpected.Equal(want) {
		t.Errorf("next_expected = %s, want %s (the last cycle on or before ended_on)", v.NextExpected.Format(dateLayoutTest), want.Format(dateLayoutTest))
	}
	if v.DaysUntil != -14 {
		t.Errorf("days_until = %d, want -14", v.DaysUntil)
	}
}

// TestListRecurringIncome_WithinDaysAndAccountFilter: within_days keeps only pay days in
// the window ahead (an ended income's final cycle is history, not upcoming), and an
// account filter excludes incomes with no account set.
func TestListRecurringIncome_WithinDaysAndAccountFilter(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)

	seedIncome(t, ctx, client, "Soon", 100, recurringincome.CadenceMonthly, today.AddDate(0, 0, 3), func(c *ent.RecurringIncomeCreate) {
		c.SetAccountID(acc.ID)
	})
	seedIncome(t, ctx, client, "Later", 100, recurringincome.CadenceAnnual, today.AddDate(0, 0, 100))
	seedIncome(t, ctx, client, "Finished", 100, recurringincome.CadenceMonthly, today.AddDate(0, -3, 0), func(c *ent.RecurringIncomeCreate) {
		c.SetStatus(recurringincome.StatusEnded).SetEndedOn(today.AddDate(0, 0, -5))
	})

	views, totals, err := ListRecurringIncome(ctx, client, IncomeFilter{Status: "all", WithinDays: 30})
	if err != nil {
		t.Fatalf("list within: %v", err)
	}
	if len(views) != 1 || views[0].Name != "Soon" {
		t.Fatalf("within 30 days = %v, want just Soon", names(views))
	}
	if totals.ExpectedTotal != 100 || totals.Count != 1 {
		t.Errorf("within totals = %+v, want 100 / 1", totals)
	}

	views, _, err = ListRecurringIncome(ctx, client, IncomeFilter{Status: "all", AccountID: acc.ID})
	if err != nil {
		t.Fatalf("list by account: %v", err)
	}
	if len(views) != 1 || views[0].Name != "Soon" {
		t.Errorf("by account = %v, want just Soon (no-account incomes are excluded)", names(views))
	}

	// Ordering is soonest first, so the default list leads with the nearest pay day.
	views, _, err = ListRecurringIncome(ctx, client, IncomeFilter{Status: "all"})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if got := names(views); !slices.Equal(got, []string{"Finished", "Soon", "Later"}) {
		t.Errorf("order = %v, want Finished, Soon, Later (days_until ascending)", got)
	}
}

func names(views []IncomeView) []string {
	out := make([]string, 0, len(views))
	for _, v := range views {
		out = append(out, v.Name)
	}
	return out
}

// TestLinkIncomeReceipt_NearestCycleAndLastReceived: a hand link with no cycle named goes
// to the pay date nearest the credit's posted date, and the view then reports when the
// money actually arrived and how much. A second link to the same cycle is refused.
func TestLinkIncomeReceipt_NearestCycleAndLastReceived(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	inc := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, day(2026, time.March, 15))

	// Posted 13 June: the nearest pay date is 15 June, not 15 May.
	seedTxn(t, ctx, client, acc, "pay-1", day(2026, time.June, 13), 1012.5, "PAY CREDIT", "")
	txn := client.Transaction.Query().OnlyX(ctx)

	r, err := LinkIncomeReceipt(ctx, client, inc.ID, txn.ID, nil)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if !r.OccurrenceDate.Equal(day(2026, time.June, 15)) {
		t.Errorf("cycle = %s, want 2026-06-15 (nearest occurrence)", r.OccurrenceDate.Format(dateLayoutTest))
	}

	views, _, err := ListRecurringIncome(ctx, client, IncomeFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	v := views[0]
	if v.LastReceivedDate == nil || !v.LastReceivedDate.Equal(day(2026, time.June, 13)) {
		t.Errorf("last_received_date = %v, want 2026-06-13 (the posted date, not the cycle)", v.LastReceivedDate)
	}
	if v.LastReceivedAmount == nil || *v.LastReceivedAmount != 1012.5 {
		t.Errorf("last_received_amount = %v, want 1012.5 (the actual, which may differ from expected)", v.LastReceivedAmount)
	}

	// Another credit on the same cycle: the cycle is already received.
	seedTxn(t, ctx, client, acc, "pay-2", day(2026, time.June, 16), 1000, "PAY CREDIT", "")
	other := client.Transaction.Query().Order(ent.Desc("id")).FirstX(ctx)
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, other.ID, nil); !errors.Is(err, ErrIncomeCycleReceived) {
		t.Errorf("second link to one cycle = %v, want ErrIncomeCycleReceived", err)
	}
}

// TestLinkIncomeReceipt_RejectsOffGridDate: a date that is not one of the income's pay
// dates is refused, since it would settle a cycle that does not exist.
func TestLinkIncomeReceipt_RejectsOffGridDate(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	anchor := day(2026, time.March, 15)
	inc := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, anchor)
	seedTxn(t, ctx, client, acc, "pay-1", day(2026, time.March, 16), 1000, "PAY CREDIT", "")
	txn := client.Transaction.Query().OnlyX(ctx)

	offGrid := day(2026, time.March, 16)
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, txn.ID, &offGrid); !errors.Is(err, ErrNotAPayDate) {
		t.Fatalf("off-grid link = %v, want ErrNotAPayDate", err)
	}
	if n := client.IncomeReceipt.Query().CountX(ctx); n != 0 {
		t.Errorf("receipts = %d after a rejected link, want 0", n)
	}
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, txn.ID, &anchor); err != nil {
		t.Errorf("on-grid link failed: %v", err)
	}
}

// TestLinkIncomeReceipt_RejectsDebit is the direction rule. Income is money in, so a
// debit can never be the transaction that paid a cycle; linking one is a data-entry slip
// that would otherwise report money out as last received.
func TestLinkIncomeReceipt_RejectsDebit(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	anchor := day(2026, time.March, 15)
	inc := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, anchor)
	seedTxn(t, ctx, client, acc, "debit-1", anchor, -1000, "TRANSFER OUT", "")
	txn := client.Transaction.Query().OnlyX(ctx)

	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, txn.ID, nil); !errors.Is(err, ErrNotACredit) {
		t.Fatalf("debit link = %v, want ErrNotACredit", err)
	}
	if n := client.IncomeReceipt.Query().CountX(ctx); n != 0 {
		t.Errorf("receipts = %d after a rejected link, want 0", n)
	}
}

// TestLinkIncomeReceipt_OneCreditOneCycle: the (income, transaction) unique index means a
// credit already settling one cycle cannot also settle another.
func TestLinkIncomeReceipt_OneCreditOneCycle(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	anchor := day(2026, time.March, 15)
	inc := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, anchor)
	seedTxn(t, ctx, client, acc, "pay-1", anchor, 1000, "PAY CREDIT", "")
	txn := client.Transaction.Query().OnlyX(ctx)

	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, txn.ID, &anchor); err != nil {
		t.Fatalf("first link: %v", err)
	}
	next := day(2026, time.April, 15)
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, txn.ID, &next); !ent.IsConstraintError(err) {
		t.Errorf("same credit on a second cycle = %v, want a constraint error", err)
	}
}
