package finance

import (
	"context"
	"testing"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/incomereceipt"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
)

// Income matching pass tests. All labels and amounts are invented.

func receiptFor(t *testing.T, ctx context.Context, client *ent.Client, incomeID int, occ time.Time) *ent.IncomeReceipt {
	t.Helper()
	r, err := client.IncomeReceipt.Query().
		Where(incomereceipt.HasIncomeWith(recurringincome.IDEQ(incomeID)), incomereceipt.OccurrenceDateEQ(occ)).
		WithTransaction().
		Only(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("receipt query: %v", err)
	}
	return r
}

// TestReconcileIncome_EarlyLateAndIdempotent: a Friday credit settles the Sunday pay date
// (posted before its cycle, and the cycle still ahead of today), a credit three days late
// settles its cycle, both as auto receipts, and a second pass links nothing more.
func TestReconcileIncome_EarlyLateAndIdempotent(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	sunday := today.AddDate(0, 0, 2)
	inc := seedIncome(t, ctx, client, "Salary", 2000, recurringincome.CadenceFortnightly, sunday.AddDate(0, 0, -28), func(c *ent.RecurringIncomeCreate) {
		c.SetMatchPattern("acme widgets")
	})
	seedTxn(t, ctx, client, acc, "late", sunday.AddDate(0, 0, -25), 2000, "Salary ACME WIDGETS", "") // 3 days late
	// Nine days after the middle cycle: too late for it, and further from the Sunday than
	// the Friday credit, so it settles nothing.
	seedTxn(t, ctx, client, acc, "nine-late", sunday.AddDate(0, 0, -14).AddDate(0, 0, 9), 2000, "Salary ACME WIDGETS", "")
	seedTxn(t, ctx, client, acc, "friday", today, 2010, "Salary ACME WIDGETS", "")

	sum, err := ReconcileIncome(ctx, client, ReconcileOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if sum.ReceiptsLinked != 2 {
		t.Errorf("linked = %d, want 2 (the late and the early credit)", sum.ReceiptsLinked)
	}
	if r := receiptFor(t, ctx, client, inc.ID, sunday); r == nil || r.Method != incomereceipt.MethodAuto || !r.Edges.Transaction.PostedDate.Equal(today) {
		t.Errorf("Sunday cycle = %+v, want today's credit as an auto receipt", r)
	}
	if r := receiptFor(t, ctx, client, inc.ID, sunday.AddDate(0, 0, -28)); r == nil || !r.Edges.Transaction.PostedDate.Equal(sunday.AddDate(0, 0, -25)) {
		t.Errorf("first cycle = %+v, want the credit three days late", r)
	}
	// The middle cycle's only candidate posted 9 days after it: too late, so it stays open.
	if r := receiptFor(t, ctx, client, inc.ID, sunday.AddDate(0, 0, -14)); r != nil {
		t.Errorf("middle cycle = %+v, want it left unsettled", r)
	}

	again, err := ReconcileIncome(ctx, client, ReconcileOptions{})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if again.ReceiptsLinked != 0 || client.IncomeReceipt.Query().CountX(ctx) != 2 {
		t.Errorf("second pass linked %d, want 0", again.ReceiptsLinked)
	}
}

// TestReconcileIncome_Rules: each clause can veto a credit. A debit, a credit on another
// account, one off by more than the tolerance and one without the pattern never settle;
// a cycle settled by hand is left alone, and a credit already receipted by another
// income is not reused.
func TestReconcileIncome_Rules(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	everyday := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	savings := seedAccount(t, ctx, client, "Savings", account.ClassAsset, account.TypeSavings)
	payday := recentDay(today, 1) // never past the 28th, so a month back stays on the grid
	inc := seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, payday.AddDate(0, -1, 0), func(c *ent.RecurringIncomeCreate) {
		c.SetMatchPattern("ACME").SetAccountID(everyday.ID)
	})
	seedTxn(t, ctx, client, everyday, "debit", payday, -1000, "ACME REFUND", "")
	seedTxn(t, ctx, client, savings, "elsewhere", payday, 1000, "ACME PAYROLL", "")
	seedTxn(t, ctx, client, everyday, "too-big", payday, 1200, "ACME PAYROLL", "")
	seedTxn(t, ctx, client, everyday, "no-pattern", payday, 1000, "PAYROLL", "")
	if sum, err := ReconcileIncome(ctx, client, ReconcileOptions{}); err != nil || sum.ReceiptsLinked != 0 {
		t.Fatalf("pass over vetoed rows = %+v (%v), want nothing linked", sum, err)
	}

	// A good credit for this cycle, but the cycle is already settled by hand with
	// another row: the pass must not replace it.
	seedTxn(t, ctx, client, everyday, "good", payday, 1000, "ACME PAYROLL", "")
	hand := client.Transaction.Query().Where(transaction.DedupHash("hash-no-pattern")).OnlyX(ctx)
	client.IncomeReceipt.Create().SetIncomeID(inc.ID).SetTransactionID(hand.ID).SetOccurrenceDate(payday).SaveX(ctx)
	// The earlier cycle's only matching credit is already another income's receipt.
	seedTxn(t, ctx, client, everyday, "taken", payday.AddDate(0, -1, 0), 1000, "ACME PAYROLL", "")
	other := seedIncome(t, ctx, client, "Other", 1000, recurringincome.CadenceMonthly, payday.AddDate(0, -1, 0))
	taken := client.Transaction.Query().Where(transaction.DedupHash("hash-taken")).OnlyX(ctx)
	client.IncomeReceipt.Create().SetIncomeID(other.ID).SetTransactionID(taken.ID).SetOccurrenceDate(payday.AddDate(0, -1, 0)).SaveX(ctx)

	sum, err := ReconcileIncome(ctx, client, ReconcileOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if sum.ReceiptsLinked != 0 {
		t.Errorf("linked = %d, want 0", sum.ReceiptsLinked)
	}
	if r := receiptFor(t, ctx, client, inc.ID, payday); r == nil || r.Edges.Transaction.ID != hand.ID || r.Method != incomereceipt.MethodManual {
		t.Errorf("hand-settled cycle = %+v, want the manual receipt untouched", r)
	}
}

// TestReconcileIncome_NoPatternNoPass: an income without a pattern is linked by hand
// only, so the pass does not even consider it.
func TestReconcileIncome_NoPatternNoPass(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedIncome(t, ctx, client, "Salary", 1000, recurringincome.CadenceMonthly, today)
	seedTxn(t, ctx, client, acc, "pay", today, 1000, "ACME PAYROLL", "")
	sum, err := ReconcileIncome(ctx, client, ReconcileOptions{})
	if err != nil || sum.IncomesScanned != 0 || sum.ReceiptsLinked != 0 {
		t.Errorf("pass = %+v (%v), want nothing scanned", sum, err)
	}
}

// TestContainsToken: a match pattern finds its text on word boundaries only. The edges
// are judged by the pattern's own first and last characters, so a letter edge needs a
// non-letter neighbour and a digit edge a non-digit one, and a reference number glued to
// a name does not hide it.
func TestContainsToken(t *testing.T) {
	cases := []struct {
		hay, needle string
		want        bool
	}{
		{"ato payg 0123", "ato", true},
		{"ato-0123", "ato", true},
		{"pay from ato", "ato", true},
		{"latte", "ato", false},
		{"potato farm", "ato", false},
		{"potato ato", "ato", true}, // a later occurrence on a boundary still counts
		{"salary acme0123", "acme", true},
		{"acmewidgets", "acme", false},
		{"salary acme widgets pty", "acme widgets", true},
		{"netflix.com", "netflix", true},
		{"uber *eats sydney", "*eats", true},
		{"ref 4411", "441", false},
		{"", "ato", false},
		{"ato", "", false},
	}
	for _, c := range cases {
		if got := containsToken(c.hay, c.needle); got != c.want {
			t.Errorf("containsToken(%q, %q) = %v, want %v", c.hay, c.needle, got, c.want)
		}
	}
}

// TestReconcileIncome_PatternOnWordBoundaries: an income matching "ATO" is not paid by a
// credit from a potato co-op at the same amount, and is paid by the ATO's own credit.
func TestReconcileIncome_PatternOnWordBoundaries(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	payday := recentDay(today, 1)
	inc := seedIncome(t, ctx, client, "Tax", 500, recurringincome.CadenceMonthly, payday.AddDate(0, -1, 0), func(c *ent.RecurringIncomeCreate) {
		c.SetMatchPattern("ATO")
	})
	seedTxn(t, ctx, client, acc, "potato", payday, 500, "POTATO CO-OP", "")
	seedTxn(t, ctx, client, acc, "ato", payday.AddDate(0, -1, 0), 500, "ATO 012345", "")
	if _, err := ReconcileIncome(ctx, client, ReconcileOptions{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r := receiptFor(t, ctx, client, inc.ID, payday); r != nil {
		t.Errorf("payday settled by %q, want it open: ATO is not a word in POTATO", r.Edges.Transaction.Description)
	}
	if r := receiptFor(t, ctx, client, inc.ID, payday.AddDate(0, -1, 0)); r == nil {
		t.Error("the ATO credit did not settle its cycle")
	}
}

// TestReconcileIncome_SkipsTransfersAndRefunds: a move in from the owner's own savings
// and a refund-looking credit at the income's amount never settle a pay cycle, even when
// the pattern matches; the real pay credit still does.
func TestReconcileIncome_SkipsTransfersAndRefunds(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	everyday := client.Account.Create().SetSource("commbank").SetName("Everyday").SetCurrency("AUD").
		SetMaskedNumber("xxxx 4242").SetType(account.TypeEveryday).SetClass(account.ClassAsset).SaveX(ctx)
	client.Account.Create().SetSource("commbank").SetName("Savings").SetCurrency("AUD").
		SetMaskedNumber("xxxx 5353").SetType(account.TypeSavings).SetClass(account.ClassAsset).SaveX(ctx)
	payday := recentDay(today, 1)
	anchor := payday.AddDate(0, -3, 0)
	inc := seedIncome(t, ctx, client, "Acme", 1000, recurringincome.CadenceMonthly, anchor, func(c *ent.RecurringIncomeCreate) {
		c.SetMatchPattern("acme")
	})
	seedTxn(t, ctx, client, everyday, "own", payday, 1000, "Transfer from xx5353 ACME pay buffer", "")
	seedTxn(t, ctx, client, everyday, "refund", payday.AddDate(0, -1, 0), 1000, "ACME REFUND 0042", "")
	seedTxn(t, ctx, client, everyday, "reversal", payday.AddDate(0, -2, 0), 1000, "Reversal ACME payroll", "")
	seedTxn(t, ctx, client, everyday, "chargeback", anchor, 1000, "ACME", "Acme Chargeback")
	if sum, err := ReconcileIncome(ctx, client, ReconcileOptions{}); err != nil || sum.ReceiptsLinked != 0 {
		t.Fatalf("pass = %+v (%v), want nothing linked from a transfer or a refund", sum, err)
	}
	seedTxn(t, ctx, client, everyday, "pay", payday, 1000, "Salary ACME", "")
	if sum, err := ReconcileIncome(ctx, client, ReconcileOptions{}); err != nil || sum.ReceiptsLinked != 1 {
		t.Fatalf("pass = %+v (%v), want the pay credit linked", sum, err)
	}
	if r := receiptFor(t, ctx, client, inc.ID, payday); r == nil || r.Edges.Transaction.Description != "Salary ACME" {
		t.Errorf("payday receipt = %+v, want the salary credit", r)
	}
}

// TestReconcileIncome_SharedPatternBestFit: incomes that share a payer's pattern do not
// steal each other's credits. Each credit goes to the income it fits best (fewest cents
// off the expected amount, then the nearest pay date), whichever income was declared
// first. The allowance is older and its tolerance admits the salary credit, which also
// posted nearer its pay date; the salary still gets it. Two incomes at one amount with
// pay dates three days apart each take the credit nearest their own date.
func TestReconcileIncome_SharedPatternBestFit(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	payday := recentDay(today, 4)
	anchor := payday.AddDate(0, -1, 0)
	pattern := func(c *ent.RecurringIncomeCreate) { c.SetMatchPattern("acme") }
	allowance := seedIncome(t, ctx, client, "Allowance", 2100, recurringincome.CadenceMonthly, anchor, pattern)
	salary := seedIncome(t, ctx, client, "Salary", 2000, recurringincome.CadenceMonthly, anchor, pattern)
	seedTxn(t, ctx, client, acc, "salary", payday, 2000, "ACME PAYROLL", "")
	seedTxn(t, ctx, client, acc, "allowance", payday.AddDate(0, 0, 1), 2100, "ACME PAYROLL", "")

	late := seedIncome(t, ctx, client, "Rent B", 900, recurringincome.CadenceMonthly, anchor.AddDate(0, 0, 3), func(c *ent.RecurringIncomeCreate) {
		c.SetMatchPattern("quoll")
	})
	early := seedIncome(t, ctx, client, "Rent A", 900, recurringincome.CadenceMonthly, anchor, func(c *ent.RecurringIncomeCreate) {
		c.SetMatchPattern("quoll")
	})
	seedTxn(t, ctx, client, acc, "rent-a", payday.AddDate(0, 0, 1), 900, "RENT QUOLL", "")

	sum, err := ReconcileIncome(ctx, client, ReconcileOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if r := receiptFor(t, ctx, client, salary.ID, payday); r == nil || r.Edges.Transaction.Amount != 2000 {
		t.Errorf("salary receipt = %+v, want the 2000 credit", r)
	}
	if r := receiptFor(t, ctx, client, allowance.ID, payday); r == nil || r.Edges.Transaction.Amount != 2100 {
		t.Errorf("allowance receipt = %+v, want the 2100 credit", r)
	}
	if r := receiptFor(t, ctx, client, early.ID, payday); r == nil {
		t.Error("the rent credit a day after Rent A's pay date did not settle Rent A")
	}
	if r := receiptFor(t, ctx, client, late.ID, payday.AddDate(0, 0, 3)); r != nil {
		t.Errorf("Rent B took %+v, want it left: the credit is nearer Rent A's date", r)
	}
	if sum.ReceiptsLinked != 3 || sum.Ambiguous != 0 {
		t.Errorf("summary = %+v, want 3 linked, none ambiguous", sum)
	}
}

// TestReconcileIncome_AmbiguousCreditLeftForHand: two incomes share the pattern, the
// amount and the pay date, so nothing says which one a credit pays. It is not guessed:
// it is left for a hand link and counted as ambiguous.
func TestReconcileIncome_AmbiguousCreditLeftForHand(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	payday := recentDay(today, 1)
	pattern := func(c *ent.RecurringIncomeCreate) { c.SetMatchPattern("quoll") }
	seedIncome(t, ctx, client, "Room 1", 900, recurringincome.CadenceMonthly, payday, pattern)
	seedIncome(t, ctx, client, "Room 2", 900, recurringincome.CadenceMonthly, payday, pattern)
	seedTxn(t, ctx, client, acc, "rent", payday, 900, "RENT QUOLL", "")
	sum, err := ReconcileIncome(ctx, client, ReconcileOptions{})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if sum.ReceiptsLinked != 0 || sum.Ambiguous != 1 || client.IncomeReceipt.Query().CountX(ctx) != 0 {
		t.Errorf("summary = %+v, want nothing linked and 1 ambiguous", sum)
	}
}
