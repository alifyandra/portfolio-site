package finance

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/billpayment"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
)

// Recurring-bill tests (portfolio-site#125). Two halves: the occurrence generator (pure
// arithmetic, table-driven over the nasty calendar cases) and the matching pass (each AND
// clause on its own, plus idempotency and the manual-link rule). All amounts and labels
// here are invented.

// seedBill creates a bill with sane defaults, applying the caller's overrides last.
func seedBill(t *testing.T, ctx context.Context, client *ent.Client, name string, amount float64, cadence recurringbill.Cadence, anchor time.Time, opts ...func(*ent.RecurringBillCreate)) *ent.RecurringBill {
	t.Helper()
	create := client.RecurringBill.Create().
		SetName(name).
		SetExpectedAmount(amount).
		SetCadence(cadence).
		SetAnchorDate(anchor)
	for _, o := range opts {
		o(create)
	}
	return create.SaveX(ctx)
}

// --- occurrence generation ---

// TestOccurrence_MonthlyClampsToMonthEnd is the 31st-anchor case: stepping a monthly bill
// through February must clamp to the month's last day and then return to the 31st, never
// roll forward into the next month the way time.AddDate would.
func TestOccurrence_MonthlyClampsToMonthEnd(t *testing.T) {
	anchor := day(2026, time.January, 31)
	cases := []struct {
		n    int
		want time.Time
	}{
		{0, day(2026, time.January, 31)},
		{1, day(2026, time.February, 28)}, // clamped, NOT 3 March
		{2, day(2026, time.March, 31)},    // back to the anchor day
		{3, day(2026, time.April, 30)},    // clamped
		{4, day(2026, time.May, 31)},
		{13, day(2027, time.February, 28)},
		{25, day(2028, time.February, 29)}, // leap year: the clamp follows the calendar
		{-1, day(2025, time.December, 31)}, // occurrences run backwards too
		{-2, day(2025, time.November, 30)},
	}
	for _, c := range cases {
		if got := occurrenceAt(anchor, recurringbill.CadenceMonthly, c.n); !got.Equal(c.want) {
			t.Errorf("occurrence n=%d = %s, want %s", c.n, got.Format(dateLayoutTest), c.want.Format(dateLayoutTest))
		}
	}
}

// TestOccurrence_QuarterlyAndAnnualClamp: the same clamp rule applies to the other
// calendar-month cadences (a 31st quarterly anchor hits 30 April, an annual 29 February
// anchor hits 28 February in a common year).
func TestOccurrence_QuarterlyAndAnnualClamp(t *testing.T) {
	if got := occurrenceAt(day(2026, time.January, 31), recurringbill.CadenceQuarterly, 1); !got.Equal(day(2026, time.April, 30)) {
		t.Errorf("quarterly from 31 Jan = %s, want 2026-04-30", got.Format(dateLayoutTest))
	}
	if got := occurrenceAt(day(2028, time.February, 29), recurringbill.CadenceAnnual, 1); !got.Equal(day(2029, time.February, 28)) {
		t.Errorf("annual from 29 Feb = %s, want 2029-02-28", got.Format(dateLayoutTest))
	}
}

// TestOccurrence_FortnightlyAcrossYearBoundary: a fortnightly bill steps in exact 14-day
// multiples, so it must cross a year boundary (and a leap February) without drifting onto
// a different weekday.
func TestOccurrence_FortnightlyAcrossYearBoundary(t *testing.T) {
	anchor := day(2026, time.December, 18)
	cases := []struct {
		n    int
		want time.Time
	}{
		{1, day(2027, time.January, 1)},
		{2, day(2027, time.January, 15)},
		{3, day(2027, time.January, 29)},
		{4, day(2027, time.February, 12)},
		{-1, day(2026, time.December, 4)},
	}
	for _, c := range cases {
		got := occurrenceAt(anchor, recurringbill.CadenceFortnightly, c.n)
		if !got.Equal(c.want) {
			t.Errorf("fortnightly n=%d = %s, want %s", c.n, got.Format(dateLayoutTest), c.want.Format(dateLayoutTest))
		}
		if got.Weekday() != anchor.Weekday() {
			t.Errorf("fortnightly n=%d landed on %s, want %s (14-day multiples never change weekday)", c.n, got.Weekday(), anchor.Weekday())
		}
	}
	// A whole year of steps stays on the same weekday and lands exactly 26 cycles on.
	if got := occurrenceAt(anchor, recurringbill.CadenceFortnightly, 26); !got.Equal(anchor.AddDate(0, 0, 26*14)) {
		t.Errorf("26 fortnightly steps = %s, want %s", got.Format(dateLayoutTest), anchor.AddDate(0, 0, 26*14).Format(dateLayoutTest))
	}
}

// TestNextPrevDue covers the two definitions directly: next_due is the earliest occurrence
// ON OR AFTER today (so an occurrence today is next_due, not prev_due), and prev_due is
// the latest one strictly before it. Checked across day-based and month-based cadences.
func TestNextPrevDue(t *testing.T) {
	cases := []struct {
		name     string
		anchor   time.Time
		cadence  recurringbill.Cadence
		today    time.Time
		wantNext time.Time
		wantPrev time.Time
	}{
		{
			name: "monthly mid-cycle", anchor: day(2026, time.January, 15), cadence: recurringbill.CadenceMonthly,
			today: day(2026, time.July, 20), wantNext: day(2026, time.August, 15), wantPrev: day(2026, time.July, 15),
		},
		{
			name: "occurrence today counts as next", anchor: day(2026, time.January, 15), cadence: recurringbill.CadenceMonthly,
			today: day(2026, time.July, 15), wantNext: day(2026, time.July, 15), wantPrev: day(2026, time.June, 15),
		},
		{
			// Occurrences run backwards from the anchor too, so a future anchor still has a
			// next_due before it (1 August, one step back from 1 September).
			name: "anchor in the future", anchor: day(2026, time.September, 1), cadence: recurringbill.CadenceMonthly,
			today: day(2026, time.July, 20), wantNext: day(2026, time.August, 1), wantPrev: day(2026, time.July, 1),
		},
		{
			name: "31st anchor lands in February", anchor: day(2026, time.January, 31), cadence: recurringbill.CadenceMonthly,
			today: day(2026, time.February, 10), wantNext: day(2026, time.February, 28), wantPrev: day(2026, time.January, 31),
		},
		{
			name: "weekly", anchor: day(2026, time.July, 6), cadence: recurringbill.CadenceWeekly,
			today: day(2026, time.July, 20), wantNext: day(2026, time.July, 20), wantPrev: day(2026, time.July, 13),
		},
		{
			name: "fortnightly before the anchor", anchor: day(2026, time.July, 3), cadence: recurringbill.CadenceFortnightly,
			today: day(2026, time.June, 25), wantNext: day(2026, time.July, 3), wantPrev: day(2026, time.June, 19),
		},
		{
			name: "annual", anchor: day(2020, time.March, 9), cadence: recurringbill.CadenceAnnual,
			today: day(2026, time.July, 20), wantNext: day(2027, time.March, 9), wantPrev: day(2026, time.March, 9),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextDue(c.anchor, c.cadence, c.today); !got.Equal(c.wantNext) {
				t.Errorf("nextDue = %s, want %s", got.Format(dateLayoutTest), c.wantNext.Format(dateLayoutTest))
			}
			if got := prevDue(c.anchor, c.cadence, c.today); !got.Equal(c.wantPrev) {
				t.Errorf("prevDue = %s, want %s", got.Format(dateLayoutTest), c.wantPrev.Format(dateLayoutTest))
			}
		})
	}
}

// TestOccurrencesBetween_EndedOnMidCycle: ended_on landing in the middle of a cycle caps
// the window at the end date, so the cycle whose due date falls after it is not expected
// and never generated. The reconcile window is where that clip is applied.
func TestOccurrencesBetween_EndedOnMidCycle(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	anchor := day(2026, time.January, 10)
	ended := day(2026, time.April, 25) // between the 10 April and 10 May occurrences

	b := seedBill(t, ctx, client, "Streaming", 20, recurringbill.CadenceMonthly, anchor, func(c *ent.RecurringBillCreate) {
		c.SetStatus(recurringbill.StatusEnded).SetEndedOn(ended).SetMatchPattern("streaming")
	})

	got := reconcileWindow(b, day(2026, time.July, 1))
	want := []time.Time{
		day(2026, time.January, 10), day(2026, time.February, 10), day(2026, time.March, 10), day(2026, time.April, 10),
	}
	if len(got) != len(want) {
		t.Fatalf("cycles = %d (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if !got[i].Equal(want[i]) {
			t.Errorf("cycle %d = %s, want %s", i, got[i].Format(dateLayoutTest), want[i].Format(dateLayoutTest))
		}
	}

	// The raw generator is unclipped by design: asked for the same window it still yields
	// the post-end cycle, so the clip is the caller's rule, not the arithmetic's.
	raw := occurrencesBetween(anchor, recurringbill.CadenceMonthly, anchor, day(2026, time.May, 31))
	if len(raw) != 5 {
		t.Errorf("unclipped occurrences = %d, want 5 (Jan..May)", len(raw))
	}
}

// TestMonthlyEquivalent: every cadence normalises to a per-month figure so a mixed set can
// be summed.
func TestMonthlyEquivalent(t *testing.T) {
	cases := []struct {
		cadence recurringbill.Cadence
		amount  float64
		want    float64
	}{
		{recurringbill.CadenceWeekly, 120, 120 * 52 / 12},
		{recurringbill.CadenceFortnightly, 120, 120 * 26 / 12},
		{recurringbill.CadenceMonthly, 120, 120},
		{recurringbill.CadenceQuarterly, 120, 40},
		{recurringbill.CadenceAnnual, 120, 10},
	}
	for _, c := range cases {
		if got := monthlyEquivalent(c.amount, c.cadence); got != c.want {
			t.Errorf("%s monthly equivalent = %v, want %v", c.cadence, got, c.want)
		}
	}
}

// --- read layer ---

// TestListRecurringBills_TotalsExcludePaused: a paused subscription still lists (with
// status=all) but is kept out of both committed-money figures, since it is not billing.
// An ended commitment is out for the same reason.
func TestListRecurringBills_TotalsExcludePaused(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := normalizeDate(time.Now())

	seedBill(t, ctx, client, "Rent", 600, recurringbill.CadenceFortnightly, today)
	seedBill(t, ctx, client, "Gym", 50, recurringbill.CadenceMonthly, today, func(c *ent.RecurringBillCreate) {
		c.SetStatus(recurringbill.StatusPaused)
	})
	seedBill(t, ctx, client, "Old policy", 90, recurringbill.CadenceMonthly, today, func(c *ent.RecurringBillCreate) {
		c.SetStatus(recurringbill.StatusEnded).SetEndedOn(today.AddDate(0, 0, -30))
	})

	views, totals, err := ListRecurringBills(ctx, client, BillFilter{Status: "all"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(views) != 3 || totals.Count != 3 {
		t.Fatalf("views = %d / count = %d, want 3/3", len(views), totals.Count)
	}
	if totals.CommittedTotal != 600 {
		t.Errorf("committed_total = %v, want 600 (only the active bill)", totals.CommittedTotal)
	}
	if want := 600.0 * 26 / 12; totals.MonthlyEquivalent != want {
		t.Errorf("monthly_equivalent = %v, want %v", totals.MonthlyEquivalent, want)
	}

	// status=active filters the set as well as the money.
	views, totals, err = ListRecurringBills(ctx, client, BillFilter{Status: "active"})
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(views) != 1 || views[0].Name != "Rent" {
		t.Fatalf("active views = %v, want just Rent", views)
	}
	if totals.CommittedTotal != 600 || totals.Count != 1 {
		t.Errorf("active totals = %+v, want 600 / 1", totals)
	}
}

// TestListRecurringBills_DerivedFields: an anchor today means next_due is today and
// days_until is 0, and the account edge joins its name in. A bad status is an error, not a
// silently unfiltered list.
func TestListRecurringBills_DerivedFields(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := normalizeDate(time.Now())
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)

	seedBill(t, ctx, client, "Insurance", 42.5, recurringbill.CadenceMonthly, today, func(c *ent.RecurringBillCreate) {
		c.SetAccountID(acc.ID).SetPayee("Placeholder Insurer")
	})

	views, _, err := ListRecurringBills(ctx, client, BillFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	v := views[0]
	if !v.NextDue.Equal(today) || v.DaysUntil != 0 {
		t.Errorf("next_due/days_until = %s/%d, want today/0", v.NextDue.Format(dateLayoutTest), v.DaysUntil)
	}
	if v.AccountID != acc.ID || v.AccountName != "Everyday" {
		t.Errorf("account = %d/%q, want %d/Everyday", v.AccountID, v.AccountName, acc.ID)
	}
	if v.Overdue || v.LastPaidDate != nil || v.LastPaidAmount != nil {
		t.Errorf("unreconciled bill = %+v, want not overdue and no last paid", v)
	}
	if v.ExpectedMonthly != 42.5 {
		t.Errorf("expected_monthly = %v, want 42.5", v.ExpectedMonthly)
	}

	if _, _, err := ListRecurringBills(ctx, client, BillFilter{Status: "nonsense"}); err == nil {
		t.Error("an unknown status returned no error; want a rejection rather than an unfiltered list")
	}
}

// TestListRecurringBills_WithinDaysAndAccountFilter: within_days keeps only what falls in
// the window (an overdue bill always passes, since its days_until is negative), and
// account_id excludes bills with no account set.
func TestListRecurringBills_WithinDaysAndAccountFilter(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := normalizeDate(time.Now())
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)

	// Due in 3 days, on the account.
	seedBill(t, ctx, client, "Soon", 30, recurringbill.CadenceAnnual, today.AddDate(0, 0, 3), func(c *ent.RecurringBillCreate) {
		c.SetAccountID(acc.ID)
	})
	// Due in 200 days, no account.
	seedBill(t, ctx, client, "Later", 300, recurringbill.CadenceAnnual, today.AddDate(0, 0, 200))
	// Overdue: annual cycle 40 days back, past the 5-day window, nothing linked.
	seedBill(t, ctx, client, "Missed", 80, recurringbill.CadenceAnnual, today.AddDate(0, 0, -40))

	views, totals, err := ListRecurringBills(ctx, client, BillFilter{WithinDays: 30})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := map[string]int{}
	for _, v := range views {
		names[v.Name] = v.DaysUntil
	}
	if _, ok := names["Later"]; ok {
		t.Errorf("within_days=30 returned a bill 200 days out: %v", names)
	}
	if d, ok := names["Missed"]; !ok || d >= 0 {
		t.Errorf("overdue bill = %v (days_until %d), want present with a negative days_until", ok, d)
	}
	if _, ok := names["Soon"]; !ok {
		t.Errorf("within_days=30 dropped a bill due in 3 days: %v", names)
	}
	// Most urgent first: the overdue one leads.
	if views[0].Name != "Missed" {
		t.Errorf("first view = %q, want the overdue bill first", views[0].Name)
	}
	if totals.CommittedTotal != 110 {
		t.Errorf("committed_total = %v, want 110 (30 + 80)", totals.CommittedTotal)
	}

	views, _, err = ListRecurringBills(ctx, client, BillFilter{AccountID: acc.ID})
	if err != nil {
		t.Fatalf("list by account: %v", err)
	}
	if len(views) != 1 || views[0].Name != "Soon" {
		t.Fatalf("account filter = %v, want only the bill on that account (no-account bills excluded)", views)
	}
}

// TestBillView_OverdueGoesAwayOnceLinked: the overdue flag is an absence, so linking a
// payment for the missed cycle clears it and flips days_until positive again, with
// last_paid_* reporting the ledger's own figures.
func TestBillView_OverdueGoesAwayOnceLinked(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := normalizeDate(time.Now())
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)

	anchor := today.AddDate(0, 0, -40)
	b := seedBill(t, ctx, client, "Subscription", 25, recurringbill.CadenceAnnual, anchor, func(c *ent.RecurringBillCreate) {
		c.SetMatchPattern("subscription")
	})

	views, _, err := ListRecurringBills(ctx, client, BillFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !views[0].Overdue || views[0].DaysUntil != -40 {
		t.Fatalf("bill = overdue %v / days_until %d, want overdue with -40", views[0].Overdue, views[0].DaysUntil)
	}

	// Link the missed cycle to a real posted row two days late.
	paid := anchor.AddDate(0, 0, 2)
	seedTxn(t, ctx, client, acc, "sub-1", paid, -27.5, "SUBSCRIPTION MONTHLY", "")
	txn := client.Transaction.Query().OnlyX(ctx)
	client.BillPayment.Create().SetBillID(b.ID).SetTransactionID(txn.ID).
		SetOccurrenceDate(anchor).SetMethod(billpayment.MethodAuto).SaveX(ctx)

	views, _, err = ListRecurringBills(ctx, client, BillFilter{})
	if err != nil {
		t.Fatalf("list after link: %v", err)
	}
	v := views[0]
	if v.Overdue || v.DaysUntil <= 0 {
		t.Errorf("after linking = overdue %v / days_until %d, want not overdue with a future due date", v.Overdue, v.DaysUntil)
	}
	if v.LastPaidDate == nil || !v.LastPaidDate.Equal(paid) {
		t.Errorf("last_paid_date = %v, want %s (the posted date, not the cycle date)", v.LastPaidDate, paid.Format(dateLayoutTest))
	}
	// The magnitude, so it compares straight against the unsigned expected_amount.
	if v.LastPaidAmount == nil || *v.LastPaidAmount != 27.5 {
		t.Errorf("last_paid_amount = %v, want 27.5", v.LastPaidAmount)
	}
}

// --- matcher ---

// TestBillMatches_EachClause walks the four AND clauses one at a time: each case starts
// from a row that matches and breaks exactly one clause, so a hit and a miss are proven
// per clause rather than in aggregate.
func TestBillMatches_EachClause(t *testing.T) {
	occ := day(2026, time.July, 10)
	bill := &ent.RecurringBill{
		ExpectedAmount:     100,
		AmountTolerancePct: 10,
		MatchPattern:       "Placeholder Co",
		MatchWindowDays:    5,
		Cadence:            recurringbill.CadenceMonthly,
		AnchorDate:         occ,
	}
	txn := func(mut func(*ent.Transaction)) *ent.Transaction {
		t := &ent.Transaction{
			PostedDate:  occ,
			Amount:      -100,
			Description: "DIRECT DEBIT PLACEHOLDER CO 123",
		}
		if mut != nil {
			mut(t)
		}
		return t
	}

	if !billMatches(bill, 0, txn(nil), occ) {
		t.Fatal("the baseline row did not match; the rest of this test proves nothing")
	}

	cases := []struct {
		name  string
		bill  func(*ent.RecurringBill)
		txn   func(*ent.Transaction)
		scope int
		want  bool
	}{
		{name: "pattern matches merchant instead of description", txn: func(tr *ent.Transaction) {
			tr.Description = "CARD PURCHASE"
			tr.Merchant = "placeholder co"
		}, want: true},
		{name: "pattern absent from both fields", txn: func(tr *ent.Transaction) {
			tr.Description = "SOME OTHER DEBIT"
			tr.Merchant = "another payee"
		}, want: false},
		{name: "empty pattern never matches", bill: func(b *ent.RecurringBill) { b.MatchPattern = "" }, want: false},
		{name: "account scope satisfied", scope: 7, txn: func(tr *ent.Transaction) {
			tr.Edges.Account = &ent.Account{ID: 7}
		}, want: true},
		{name: "account scope violated", scope: 7, txn: func(tr *ent.Transaction) {
			tr.Edges.Account = &ent.Account{ID: 8}
		}, want: false},
		{name: "amount inside tolerance", txn: func(tr *ent.Transaction) { tr.Amount = -109 }, want: true},
		{name: "amount outside tolerance", txn: func(tr *ent.Transaction) { tr.Amount = -111 }, want: false},
		{name: "positive amount compares on magnitude", txn: func(tr *ent.Transaction) { tr.Amount = 100 }, want: true},
		{name: "date at the window edge", txn: func(tr *ent.Transaction) { tr.PostedDate = occ.AddDate(0, 0, -5) }, want: true},
		{name: "date past the window", txn: func(tr *ent.Transaction) { tr.PostedDate = occ.AddDate(0, 0, 6) }, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := *bill
			if c.bill != nil {
				c.bill(&b)
			}
			if got := billMatches(&b, c.scope, txn(c.txn), occ); got != c.want {
				t.Errorf("billMatches = %v, want %v", got, c.want)
			}
		})
	}
}

// TestBillMatches_AmountVariableSkipsAmountCheck: with amount_variable set, a row nowhere
// near expected_amount still matches on pattern plus window, and the tolerance is ignored
// entirely rather than being read as 0%.
func TestBillMatches_AmountVariableSkipsAmountCheck(t *testing.T) {
	occ := day(2026, time.July, 10)
	bill := &ent.RecurringBill{
		ExpectedAmount:     150,
		AmountTolerancePct: 10,
		MatchPattern:       "utility",
		MatchWindowDays:    5,
	}
	txn := &ent.Transaction{PostedDate: occ, Amount: -412.87, Description: "UTILITY BILL"}

	if billMatches(bill, 0, txn, occ) {
		t.Fatal("a fixed-amount bill matched a row far outside tolerance")
	}
	bill.AmountVariable = true
	if !billMatches(bill, 0, txn, occ) {
		t.Error("amount_variable did not skip the amount check")
	}
}

// TestReconcileBills_IdempotentAndRespectsManual is the whole pass end to end: it links a
// cycle from a real stored row, a second run changes nothing (the unique indexes make a
// re-run a no-op), a hand-made `manual` link survives the pass untouched, and a paused
// bill is never matched.
func TestReconcileBills_IdempotentAndRespectsManual(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := normalizeDate(time.Now())
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)

	// Monthly bill anchored two cycles back, so there are cycles to settle.
	anchor := today.AddDate(0, -2, 0)
	rent := seedBill(t, ctx, client, "Housing", 600, recurringbill.CadenceMonthly, anchor, func(c *ent.RecurringBillCreate) {
		c.SetMatchPattern("housing debit").SetAccountID(acc.ID)
	})
	// Paused bill whose pattern would otherwise match the same row set.
	seedBill(t, ctx, client, "Paused thing", 600, recurringbill.CadenceMonthly, anchor, func(c *ent.RecurringBillCreate) {
		c.SetMatchPattern("housing debit").SetStatus(recurringbill.StatusPaused)
	})

	// One posted row per cycle, each a day late.
	for i := 0; i < 3; i++ {
		occ := occurrenceAt(anchor, recurringbill.CadenceMonthly, i)
		if occ.After(today) {
			break
		}
		seedTxn(t, ctx, client, acc, fmt.Sprintf("housing-%d", i), occ.AddDate(0, 0, 1), -600, "HOUSING DEBIT REF 9", "")
	}

	first, err := ReconcileBills(ctx, client)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if first.PaymentsLinked == 0 {
		t.Fatal("first pass linked nothing")
	}
	if first.BillsScanned != 1 {
		t.Errorf("bills_scanned = %d, want 1 (the paused bill must not be matched)", first.BillsScanned)
	}
	linked := client.BillPayment.Query().CountX(ctx)

	second, err := ReconcileBills(ctx, client)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if second.PaymentsLinked != 0 {
		t.Errorf("second pass linked %d, want 0 (the pass must be idempotent)", second.PaymentsLinked)
	}
	if got := client.BillPayment.Query().CountX(ctx); got != linked {
		t.Errorf("payments after re-run = %d, want %d unchanged", got, linked)
	}

	// A manual link on a cycle the matcher could otherwise fill must survive a pass.
	seedTxn(t, ctx, client, acc, "housing-manual", today, -615, "HOUSING DEBIT REF 9", "")
	manualTxn := client.Transaction.Query().Order(ent.Desc("id")).FirstX(ctx)
	client.BillPayment.Delete().Where(billpayment.HasBillWith(recurringbill.IDEQ(rent.ID))).ExecX(ctx)
	client.BillPayment.Create().SetBillID(rent.ID).SetTransactionID(manualTxn.ID).
		SetOccurrenceDate(anchor).SetMethod(billpayment.MethodManual).SaveX(ctx)

	if _, err := ReconcileBills(ctx, client); err != nil {
		t.Fatalf("reconcile after manual link: %v", err)
	}
	kept := client.BillPayment.Query().
		Where(billpayment.HasBillWith(recurringbill.IDEQ(rent.ID)), billpayment.OccurrenceDateEQ(anchor)).
		WithTransaction().
		OnlyX(ctx)
	if kept.Method != billpayment.MethodManual {
		t.Errorf("cycle method = %s, want manual (the pass overwrote a hand-made link)", kept.Method)
	}
	if kept.Edges.Transaction.ID != manualTxn.ID {
		t.Errorf("cycle points at txn %d, want the hand-linked %d", kept.Edges.Transaction.ID, manualTxn.ID)
	}
}

// TestReconcileBills_SkipsUnmatchable: a bill with no pattern is never auto-matched, and a
// bill scoped to another account does not steal a row from the account it is not on.
func TestReconcileBills_SkipsUnmatchable(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := normalizeDate(time.Now())
	one := seedAccount(t, ctx, client, "Card A", account.ClassLiability, account.TypeCreditCard)
	two := seedAccount(t, ctx, client, "Card B", account.ClassLiability, account.TypeCreditCard)

	seedBill(t, ctx, client, "No pattern", 40, recurringbill.CadenceMonthly, today.AddDate(0, 0, -3))
	seedBill(t, ctx, client, "Other card", 40, recurringbill.CadenceMonthly, today.AddDate(0, 0, -3), func(c *ent.RecurringBillCreate) {
		c.SetMatchPattern("service fee").SetAccountID(two.ID)
	})
	seedTxn(t, ctx, client, one, "fee-1", today.AddDate(0, 0, -3), -40, "SERVICE FEE", "")

	sum, err := ReconcileBills(ctx, client)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if sum.PaymentsLinked != 0 {
		t.Errorf("linked %d, want 0 (no pattern, and the only candidate row is on another account)", sum.PaymentsLinked)
	}
	if sum.BillsScanned != 1 {
		t.Errorf("bills_scanned = %d, want 1 (the pattern-less bill is not a candidate)", sum.BillsScanned)
	}
}

// TestLinkBillPayment_DefaultsToNearestCycleAndBeatsAuto: a hand link with no cycle named
// picks the closest occurrence, replaces an existing AUTO link, and then refuses to
// silently replace itself.
func TestLinkBillPayment_DefaultsToNearestCycleAndBeatsAuto(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	anchor := day(2026, time.March, 5)
	b := seedBill(t, ctx, client, "Levy", 75, recurringbill.CadenceMonthly, anchor)

	// Posted 2 June: the nearest cycle is 5 June, not 5 May.
	seedTxn(t, ctx, client, acc, "levy-1", day(2026, time.June, 2), -75, "LEVY PAYMENT", "")
	txn := client.Transaction.Query().OnlyX(ctx)

	p, err := LinkBillPayment(ctx, client, b.ID, txn.ID, nil)
	if err != nil {
		t.Fatalf("link: %v", err)
	}
	if !p.OccurrenceDate.Equal(day(2026, time.June, 5)) {
		t.Errorf("cycle = %s, want 2026-06-05 (nearest occurrence)", p.OccurrenceDate.Format(dateLayoutTest))
	}
	if p.Method != billpayment.MethodManual {
		t.Errorf("method = %s, want manual", p.Method)
	}

	// A manual link replaces an auto one on the same cycle...
	client.BillPayment.DeleteOne(p).ExecX(ctx)
	client.BillPayment.Create().SetBillID(b.ID).SetTransactionID(txn.ID).
		SetOccurrenceDate(day(2026, time.June, 5)).SetMethod(billpayment.MethodAuto).SaveX(ctx)
	if _, err := LinkBillPayment(ctx, client, b.ID, txn.ID, nil); err != nil {
		t.Fatalf("link over an auto payment: %v", err)
	}
	// ...but never another manual one.
	if _, err := LinkBillPayment(ctx, client, b.ID, txn.ID, nil); !errors.Is(err, ErrCycleAlreadyLinked) {
		t.Errorf("second manual link error = %v, want ErrCycleAlreadyLinked", err)
	}
}

// dateLayoutTest keeps the test failure messages readable as dates.
const dateLayoutTest = "2006-01-02"
