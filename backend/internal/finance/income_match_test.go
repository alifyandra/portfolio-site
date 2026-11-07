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
