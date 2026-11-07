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
	if v.NextExpected == nil || !v.NextExpected.Equal(today) || v.DaysUntil == nil || *v.DaysUntil != 0 {
		t.Errorf("next_expected/days_until = %v/%v, want today/0", v.NextExpected, v.DaysUntil)
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

// TestListRecurringIncome_EndedReportsNoPastDate: an income that stopped expects nothing
// after ended_on. Once ended_on has passed there is no pay date left, so next_expected is
// absent rather than a past date dressed up as the next one.
func TestListRecurringIncome_EndedReportsNoPastDate(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	anchor := today.AddDate(0, 0, -70) // ten weeks back: cycles every 7 days up to today

	seedIncome(t, ctx, client, "Contract", 300, recurringincome.CadenceWeekly, anchor, func(c *ent.RecurringIncomeCreate) {
		c.SetStatus(recurringincome.StatusEnded).SetEndedOn(today.AddDate(0, 0, -10))
	})
	// Ending in ten days: today's cycle is still ahead of ended_on.
	seedIncome(t, ctx, client, "Notice period", 300, recurringincome.CadenceWeekly, anchor, func(c *ent.RecurringIncomeCreate) {
		c.SetStatus(recurringincome.StatusEnded).SetEndedOn(today.AddDate(0, 0, 3))
	})
	// Ending in three days, but the next monthly pay date is further out than that.
	seedIncome(t, ctx, client, "Last month", 300, recurringincome.CadenceMonthly, today.AddDate(0, 0, -10), func(c *ent.RecurringIncomeCreate) {
		c.SetStatus(recurringincome.StatusEnded).SetEndedOn(today.AddDate(0, 0, 3))
	})

	views, _, err := ListRecurringIncome(ctx, client, IncomeFilter{Status: "all"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byName := map[string]IncomeView{}
	for _, v := range views {
		byName[v.Name] = v
	}
	for _, name := range []string{"Contract", "Last month"} {
		if v := byName[name]; v.NextExpected != nil || v.DaysUntil != nil {
			t.Errorf("%s next_expected/days_until = %v/%v, want none (no pay date left)", name, v.NextExpected, v.DaysUntil)
		}
	}
	if v := byName["Notice period"]; v.NextExpected == nil || !v.NextExpected.Equal(today) || v.DaysUntil == nil || *v.DaysUntil != 0 {
		t.Errorf("notice period next_expected/days_until = %v/%v, want today/0", v.NextExpected, v.DaysUntil)
	}
}

// TestListRecurringIncome_PastEndedOnLeavesTheMoney: a row whose ended_on has passed is
// out of the totals even when its status still says active (written before the API
// cross-checked the two), and it reports no next pay date.
func TestListRecurringIncome_PastEndedOnLeavesTheMoney(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()

	seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, today)
	seedIncome(t, ctx, client, "Stale", 900, recurringincome.CadenceMonthly, today.AddDate(0, -6, 0), func(c *ent.RecurringIncomeCreate) {
		c.SetEndedOn(today.AddDate(0, 0, -1)) // status left at the active default
	})

	views, totals, err := ListRecurringIncome(ctx, client, IncomeFilter{Status: "active"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if totals.Count != 2 || totals.ExpectedTotal != 1000 || totals.MonthlyEquivalent != 1000 {
		t.Errorf("totals = %+v, want count 2 and only Salary's 1000 in the money", totals)
	}
	for _, v := range views {
		if v.Name == "Stale" && v.NextExpected != nil {
			t.Errorf("stale next_expected = %s, want none", v.NextExpected.Format(dateLayoutTest))
		}
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

	// Ordering is soonest first, so the default list leads with the nearest pay day, and
	// an income with no pay date left goes last.
	views, _, err = ListRecurringIncome(ctx, client, IncomeFilter{Status: "all"})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if got := names(views); !slices.Equal(got, []string{"Soon", "Later", "Finished"}) {
		t.Errorf("order = %v, want Soon, Later, Finished (days_until ascending, none last)", got)
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

// TestLinkIncomeReceipt_OneCreditOneCycle: a credit already settling one cycle cannot
// also settle another, named before the insert rather than left to the index.
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
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, txn.ID, &next); !errors.Is(err, ErrTransactionReceived) {
		t.Errorf("same credit on a second cycle = %v, want ErrTransactionReceived", err)
	}
}

// TestLinkIncomeReceipt_OneReceiptPerTransaction: one posted credit is one payment, so it
// settles at most one receipt across EVERY income, not just within one. The service names
// it; the unique index on the transaction edge is the backstop under a direct write.
func TestLinkIncomeReceipt_OneReceiptPerTransaction(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	anchor := day(2026, time.March, 15)
	salary := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, anchor)
	bonus := seedIncome(t, ctx, client, "Bonus", 1000, recurringincome.CadenceMonthly, anchor)
	seedTxn(t, ctx, client, acc, "pay-1", anchor, 1000, "PAY CREDIT", "")
	txn := client.Transaction.Query().OnlyX(ctx)

	if _, err := LinkIncomeReceipt(ctx, client, salary.ID, txn.ID, &anchor); err != nil {
		t.Fatalf("first link: %v", err)
	}
	if _, err := LinkIncomeReceipt(ctx, client, bonus.ID, txn.ID, &anchor); !errors.Is(err, ErrTransactionReceived) {
		t.Errorf("same credit on another income = %v, want ErrTransactionReceived", err)
	}
	_, err := client.IncomeReceipt.Create().
		SetIncomeID(bonus.ID).SetTransactionID(txn.ID).SetOccurrenceDate(anchor).
		Save(ctx)
	if !ent.IsConstraintError(err) {
		t.Errorf("direct second receipt on one transaction = %v, want a constraint error from the unique index", err)
	}
}

// TestLinkIncomeReceipt_OccurrenceWindow: being on the grid is not enough. A cycle before
// the anchor never existed, one after ended_on was never expected, and one still in the
// future has not been paid yet, whether the caller names it or it is derived as nearest.
func TestLinkIncomeReceipt_OccurrenceWindow(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	anchor := today.AddDate(0, 0, -28) // weekly: cycles at -28, -21, -14, -7, 0, +7 ...
	inc := seedIncome(t, ctx, client, "Contract", 300, recurringincome.CadenceWeekly, anchor)
	seedTxn(t, ctx, client, acc, "early", today.AddDate(0, 0, -33), 300, "PAY CREDIT", "")
	seedTxn(t, ctx, client, acc, "pay", today, 300, "PAY CREDIT", "")
	early := client.Transaction.Query().Order(ent.Asc("id")).FirstX(ctx)
	pay := client.Transaction.Query().Order(ent.Desc("id")).FirstX(ctx)

	beforeAnchor := today.AddDate(0, 0, -35)
	future := today.AddDate(0, 0, 7)
	for name, occ := range map[string]*time.Time{"before anchor": &beforeAnchor, "future": &future} {
		if _, err := LinkIncomeReceipt(ctx, client, inc.ID, pay.ID, occ); !errors.Is(err, ErrCycleOutOfRange) {
			t.Errorf("%s = %v, want ErrCycleOutOfRange", name, err)
		}
	}
	// Posted 33 days back: the nearest grid date is -35, before the anchor.
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, early.ID, nil); !errors.Is(err, ErrCycleOutOfRange) {
		t.Errorf("derived pre-anchor cycle = %v, want ErrCycleOutOfRange", err)
	}

	inc = inc.Update().SetStatus(recurringincome.StatusEnded).SetEndedOn(today.AddDate(0, 0, -14)).SaveX(ctx)
	afterEnd := today.AddDate(0, 0, -7)
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, pay.ID, &afterEnd); !errors.Is(err, ErrCycleOutOfRange) {
		t.Errorf("after ended_on = %v, want ErrCycleOutOfRange", err)
	}
	if n := client.IncomeReceipt.Query().CountX(ctx); n != 0 {
		t.Fatalf("receipts = %d after rejected links, want 0", n)
	}
	lastCycle := today.AddDate(0, 0, -14)
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, pay.ID, &lastCycle); err != nil {
		t.Errorf("the ended_on cycle itself failed: %v", err)
	}
}

// TestLinkIncomeReceipt_TodayIsLinkable: the window is inclusive of today, so a pay day's
// own credit links the same day it posts.
func TestLinkIncomeReceipt_TodayIsLinkable(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	inc := seedIncome(t, ctx, client, "Contract", 300, recurringincome.CadenceWeekly, today.AddDate(0, 0, -7))
	seedTxn(t, ctx, client, acc, "pay", today, 300, "PAY CREDIT", "")
	txn := client.Transaction.Query().OnlyX(ctx)
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, txn.ID, &today); err != nil {
		t.Errorf("today's cycle failed: %v", err)
	}
}

// TestLinkIncomeReceipt_AccountMustMatch: an income paid into a known account can only be
// settled by a credit posted to that account. An income with no account takes any.
func TestLinkIncomeReceipt_AccountMustMatch(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	everyday := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	savings := seedAccount(t, ctx, client, "Savings", account.ClassAsset, account.TypeSavings)
	anchor := day(2026, time.March, 15)
	inc := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, anchor, func(c *ent.RecurringIncomeCreate) {
		c.SetAccountID(everyday.ID)
	})
	loose := seedIncome(t, ctx, client, "Side work", 100, recurringincome.CadenceMonthly, anchor)
	seedTxn(t, ctx, client, savings, "wrong", anchor, 1000, "PAY CREDIT", "")
	seedTxn(t, ctx, client, everyday, "right", anchor, 1000, "PAY CREDIT", "")
	wrong := client.Transaction.Query().Order(ent.Asc("id")).FirstX(ctx)
	right := client.Transaction.Query().Order(ent.Desc("id")).FirstX(ctx)

	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, wrong.ID, &anchor); !errors.Is(err, ErrWrongAccount) {
		t.Errorf("credit on another account = %v, want ErrWrongAccount", err)
	}
	if _, err := LinkIncomeReceipt(ctx, client, inc.ID, right.ID, &anchor); err != nil {
		t.Errorf("credit on the income's account failed: %v", err)
	}
	if _, err := LinkIncomeReceipt(ctx, client, loose.ID, wrong.ID, &anchor); err != nil {
		t.Errorf("an income with no account refused a credit: %v", err)
	}
}

// raceReceipt registers a hook that, on the first receipt insert, writes a competing
// receipt first: the window between the pre-check and the insert that a concurrent link
// would land in.
func raceReceipt(client *ent.Client, incomeID, txnID int, occ time.Time) {
	fired := false
	client.IncomeReceipt.Use(func(next ent.Mutator) ent.Mutator {
		return ent.MutateFunc(func(ctx context.Context, m ent.Mutation) (ent.Value, error) {
			if !fired && m.Op().Is(ent.OpCreate) {
				fired = true
				client.IncomeReceipt.Create().
					SetIncomeID(incomeID).SetTransactionID(txnID).SetOccurrenceDate(occ).
					SaveX(ctx)
			}
			return next.Mutate(ctx, m)
		})
	})
}

// TestLinkIncomeReceipt_RaceNamesTheRealConflict: when a concurrent link wins the race
// past the pre-check, the constraint error is re-checked and reported as what it is: the
// cycle taken, or the transaction taken, never a raw constraint error.
func TestLinkIncomeReceipt_RaceNamesTheRealConflict(t *testing.T) {
	t.Run("cycle", func(t *testing.T) {
		client := newFinanceTestClient(t)
		ctx := context.Background()
		acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
		anchor := day(2026, time.March, 15)
		inc := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, anchor)
		seedTxn(t, ctx, client, acc, "mine", anchor, 1000, "PAY CREDIT", "")
		seedTxn(t, ctx, client, acc, "theirs", anchor, 1000, "PAY CREDIT", "")
		mine := client.Transaction.Query().Order(ent.Asc("id")).FirstX(ctx)
		theirs := client.Transaction.Query().Order(ent.Desc("id")).FirstX(ctx)

		raceReceipt(client, inc.ID, theirs.ID, anchor)
		if _, err := LinkIncomeReceipt(ctx, client, inc.ID, mine.ID, &anchor); !errors.Is(err, ErrIncomeCycleReceived) {
			t.Errorf("lost race on the cycle = %v, want ErrIncomeCycleReceived", err)
		}
	})
	t.Run("transaction", func(t *testing.T) {
		client := newFinanceTestClient(t)
		ctx := context.Background()
		acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
		anchor := day(2026, time.March, 15)
		salary := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, anchor)
		bonus := seedIncome(t, ctx, client, "Bonus", 1000, recurringincome.CadenceMonthly, anchor)
		seedTxn(t, ctx, client, acc, "pay", anchor, 1000, "PAY CREDIT", "")
		txn := client.Transaction.Query().OnlyX(ctx)

		raceReceipt(client, bonus.ID, txn.ID, anchor)
		if _, err := LinkIncomeReceipt(ctx, client, salary.ID, txn.ID, &anchor); !errors.Is(err, ErrTransactionReceived) {
			t.Errorf("lost race on the transaction = %v, want ErrTransactionReceived", err)
		}
	})
}
