package finance

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
)

// seedAccount creates a bare account (no snapshot) for the read tests.
func seedAccount(t *testing.T, ctx context.Context, client *ent.Client, name string, class account.Class, typ account.Type) *ent.Account {
	t.Helper()
	return client.Account.Create().
		SetSource("commbank").
		SetName(name).
		SetType(typ).
		SetClass(class).
		SetCurrency("AUD").
		SaveX(ctx)
}

// seedSnapshot appends a balance snapshot at as_of.
func seedSnapshot(t *testing.T, ctx context.Context, client *ent.Client, acc *ent.Account, balance float64, asOf time.Time) {
	t.Helper()
	client.BalanceSnapshot.Create().
		SetBalance(balance).
		SetAsOf(asOf).
		SetAccountID(acc.ID).
		SaveX(ctx)
}

// seedTxn appends a posted transaction (unique dedup_hash from the caller's tag).
func seedTxn(t *testing.T, ctx context.Context, client *ent.Client, acc *ent.Account, tag string, date time.Time, amount float64, desc, merchant string) {
	t.Helper()
	client.Transaction.Create().
		SetDedupHash("hash-" + tag).
		SetPostedDate(date).
		SetAmount(amount).
		SetDescription(desc).
		SetMerchant(merchant).
		SetAccountID(acc.ID).
		SaveX(ctx)
}

// day (y,m,d -> UTC midnight) is defined in window_test.go and reused here.

// TestSummary_NetWorthMath covers the three load-bearing rules: assets sum the
// LATEST asset snapshot, liabilities sum the NEGATED latest liability snapshot, and
// an account with no snapshot still counts toward account_count without moving the
// numbers. The latest-snapshot selection is proven by giving the asset account two
// snapshots (an older 1000 and a newer 1500): only 1500 must count.
func TestSummary_NetWorthMath(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	asset := seedAccount(t, ctx, client, "Smart Access", account.ClassAsset, account.TypeEveryday)
	seedSnapshot(t, ctx, client, asset, 1000.00, time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC))
	seedSnapshot(t, ctx, client, asset, 1500.00, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)) // newer wins

	liab := seedAccount(t, ctx, client, "Low Rate CC", account.ClassLiability, account.TypeCreditCard)
	seedSnapshot(t, ctx, client, liab, -400.00, time.Date(2026, 7, 9, 9, 0, 0, 0, time.UTC)) // owed 400

	// An account with no snapshot: counts but contributes nothing.
	seedAccount(t, ctx, client, "NetBank Saver", account.ClassAsset, account.TypeSavings)

	s, err := NetWorthSummary(ctx, client)
	if err != nil {
		t.Fatalf("NetWorthSummary: %v", err)
	}
	if s.Assets != 1500.00 {
		t.Errorf("assets = %.2f, want 1500.00 (latest snapshot only)", s.Assets)
	}
	if s.Liabilities != 400.00 {
		t.Errorf("liabilities = %.2f, want 400.00 (negated latest liability balance: -(-400))", s.Liabilities)
	}
	if s.NetWorth != 1100.00 {
		t.Errorf("net_worth = %.2f, want 1100.00 (1500 - 400)", s.NetWorth)
	}
	if s.AccountCount != 3 {
		t.Errorf("account_count = %d, want 3 (snapshot-less account still counts)", s.AccountCount)
	}
	if s.Currency != "AUD" {
		t.Errorf("currency = %q, want AUD", s.Currency)
	}
	// AsOf is the freshest reading across latest snapshots: the asset's 2026-07-10.
	if s.AsOf == nil || !s.AsOf.Equal(time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("as_of = %v, want 2026-07-10T09:00:00Z (the freshest reading)", s.AsOf)
	}
}

// TestSummary_InCreditLiability locks the sign fix (ADR 0017 review): a liability
// account carrying a POSITIVE (overpaid, in-credit) balance must ADD to net worth, not
// subtract. Negating the balance books a +150 credit as -150 liabilities, so an
// otherwise-400-owed card in credit by 150 nets to 250 owed. abs() would have wrongly
// reported 550 owed.
func TestSummary_InCreditLiability(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	asset := seedAccount(t, ctx, client, "Smart Access", account.ClassAsset, account.TypeEveryday)
	seedSnapshot(t, ctx, client, asset, 1000.00, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC))

	owed := seedAccount(t, ctx, client, "Low Rate CC", account.ClassLiability, account.TypeCreditCard)
	seedSnapshot(t, ctx, client, owed, -400.00, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)) // owes 400

	credit := seedAccount(t, ctx, client, "StepPay", account.ClassLiability, account.TypeSteppay)
	seedSnapshot(t, ctx, client, credit, 150.00, time.Date(2026, 7, 10, 9, 0, 0, 0, time.UTC)) // in credit 150

	s, err := NetWorthSummary(ctx, client)
	if err != nil {
		t.Fatalf("NetWorthSummary: %v", err)
	}
	if s.Liabilities != 250.00 {
		t.Errorf("liabilities = %.2f, want 250.00 (400 owed minus 150 in credit)", s.Liabilities)
	}
	if s.NetWorth != 750.00 {
		t.Errorf("net_worth = %.2f, want 750.00 (1000 - 250)", s.NetWorth)
	}
}

// TestSummary_NoSnapshotsNilAsOf: with accounts but no snapshots at all, the roll-up
// is zeroed and as_of is nil (nothing to stamp staleness from).
func TestSummary_NoSnapshotsNilAsOf(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	seedAccount(t, ctx, client, "Smart Access", account.ClassAsset, account.TypeEveryday)

	s, err := NetWorthSummary(ctx, client)
	if err != nil {
		t.Fatalf("NetWorthSummary: %v", err)
	}
	if s.NetWorth != 0 || s.Assets != 0 || s.Liabilities != 0 {
		t.Errorf("roll-up = %+v, want all zero", s)
	}
	if s.AsOf != nil {
		t.Errorf("as_of = %v, want nil (no snapshots)", s.AsOf)
	}
	if s.AccountCount != 1 {
		t.Errorf("account_count = %d, want 1", s.AccountCount)
	}
}

// TestAccounts_LatestSnapshotJoined: each account carries its LATEST snapshot; a
// snapshot-less account has nil balance pointers.
func TestAccounts_LatestSnapshotJoined(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "Smart Access", account.ClassAsset, account.TypeEveryday)
	seedSnapshot(t, ctx, client, a, 100, day(2026, 7, 1))
	seedSnapshot(t, ctx, client, a, 250, day(2026, 7, 5)) // latest
	seedAccount(t, ctx, client, "Zzz Saver", account.ClassAsset, account.TypeSavings)

	accs, err := Accounts(ctx, client)
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accs) != 2 {
		t.Fatalf("accounts = %d, want 2", len(accs))
	}
	// Ordered by name: "Smart Access" before "Zzz Saver".
	if accs[0].Name != "Smart Access" || accs[1].Name != "Zzz Saver" {
		t.Fatalf("order = [%s,%s], want name asc", accs[0].Name, accs[1].Name)
	}
	if accs[0].Balance == nil || *accs[0].Balance != 250 {
		t.Errorf("Smart Access balance = %v, want 250 (latest)", accs[0].Balance)
	}
	if accs[1].Balance != nil {
		t.Errorf("Zzz Saver balance = %v, want nil (no snapshot)", accs[1].Balance)
	}
}

// TestTransactions_FilterAndPaging exercises the account filter, date range, ordering
// (posted_date desc), the total count (pre-paging), and limit/offset.
func TestTransactions_FilterAndPaging(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	b := seedAccount(t, ctx, client, "B", account.ClassAsset, account.TypeSavings)

	// Account A: five posted rows across five days.
	for i := 0; i < 5; i++ {
		seedTxn(t, ctx, client, a, fmt.Sprintf("a%d", i), day(2026, 7, 1+i), float64(-(i + 1)), fmt.Sprintf("A txn %d", i), "MerchA")
	}
	// Account B: two rows, to prove the account filter excludes them.
	seedTxn(t, ctx, client, b, "b0", day(2026, 7, 3), -99, "B txn", "MerchB")
	seedTxn(t, ctx, client, b, "b1", day(2026, 7, 4), -99, "B txn", "MerchB")

	// Unfiltered: 7 total, newest first.
	all, total, err := ListTransactions(ctx, client, TxnFilter{})
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if total != 7 || len(all) != 7 {
		t.Fatalf("all = %d rows / total %d, want 7/7", len(all), total)
	}
	// Newest first: the first row's posted_date is the max seeded (2026-07-05).
	if !all[0].PostedDate.Equal(day(2026, 7, 5)) {
		t.Errorf("first row date = %v, want 2026-07-05 (desc order)", all[0].PostedDate)
	}

	// Account filter: only A's five rows.
	onlyA, totalA, err := ListTransactions(ctx, client, TxnFilter{AccountID: a.ID})
	if err != nil {
		t.Fatalf("Transactions(A): %v", err)
	}
	if totalA != 5 || len(onlyA) != 5 {
		t.Fatalf("A = %d rows / total %d, want 5/5", len(onlyA), totalA)
	}
	for _, r := range onlyA {
		if r.AccountName != "A" {
			t.Errorf("account filter leaked row from %q", r.AccountName)
		}
	}

	// Date range [2026-07-02, 2026-07-04] on A: three rows (2,3,4 July).
	from := day(2026, 7, 2)
	to := day(2026, 7, 4)
	ranged, totalR, err := ListTransactions(ctx, client, TxnFilter{AccountID: a.ID, From: &from, To: &to})
	if err != nil {
		t.Fatalf("Transactions(range): %v", err)
	}
	if totalR != 3 || len(ranged) != 3 {
		t.Fatalf("range = %d rows / total %d, want 3/3", len(ranged), totalR)
	}

	// Paging: limit 2, offset 2 over all 7. Total stays 7; page has 2 rows.
	page, totalP, err := ListTransactions(ctx, client, TxnFilter{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("Transactions(page): %v", err)
	}
	if totalP != 7 {
		t.Errorf("paged total = %d, want 7 (count is pre-paging)", totalP)
	}
	if len(page) != 2 {
		t.Errorf("page size = %d, want 2", len(page))
	}
	// The offset-2 page continues the desc order from the third-newest row.
	if !page[0].PostedDate.Before(all[1].PostedDate.Add(time.Nanosecond)) {
		t.Errorf("page not continuing desc order: page[0]=%v", page[0].PostedDate)
	}
}

// TestTransactions_LimitClamped: a limit over the cap is clamped to maxTxnLimit.
func TestTransactions_LimitClamped(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedTxn(t, ctx, client, a, "x", day(2026, 7, 1), -1, "d", "m")

	// A huge limit must not error; it is clamped internally. One row present.
	rows, _, err := ListTransactions(ctx, client, TxnFilter{Limit: 100000})
	if err != nil {
		t.Fatalf("Transactions: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("rows = %d, want 1", len(rows))
	}
}

// TestSearchMerchant_CaseInsensitive matches merchant OR description, case-insensitive.
func TestSearchMerchant_CaseInsensitive(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedTxn(t, ctx, client, a, "1", day(2026, 7, 1), -10, "EFTPOS WOOLWORTHS", "Woolworths")
	seedTxn(t, ctx, client, a, "2", day(2026, 7, 2), -20, "COFFEE CO", "Coffee Co")
	seedTxn(t, ctx, client, a, "3", day(2026, 7, 3), -30, "SALARY", "Employer")

	// Case-insensitive merchant hit.
	hits, err := SearchMerchant(ctx, client, "woolworths", 50)
	if err != nil {
		t.Fatalf("SearchMerchant: %v", err)
	}
	if len(hits) != 1 || hits[0].Merchant != "Woolworths" {
		t.Fatalf("hits = %+v, want the Woolworths row", hits)
	}

	// Description hit ("coffee" is only in the description casing here).
	descHits, err := SearchMerchant(ctx, client, "COFFEE", 50)
	if err != nil {
		t.Fatalf("SearchMerchant desc: %v", err)
	}
	if len(descHits) != 1 {
		t.Fatalf("desc hits = %d, want 1", len(descHits))
	}

	// Empty query returns nothing (not everything).
	none, err := SearchMerchant(ctx, client, "   ", 50)
	if err != nil {
		t.Fatalf("SearchMerchant empty: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("empty query returned %d rows, want 0", len(none))
	}
}

// TestMonthlySummary_Buckets: income sums positives, spend sums the magnitude of
// negatives, net = income - spend, always N buckets oldest-first.
func TestMonthlySummary_Buckets(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)

	now := time.Now().UTC()
	thisMonth := time.Date(now.Year(), now.Month(), 15, 0, 0, 0, 0, time.UTC)
	seedTxn(t, ctx, client, a, "in", thisMonth, 2000, "SALARY", "Employer")
	seedTxn(t, ctx, client, a, "out1", thisMonth, -500, "RENT", "Landlord")
	seedTxn(t, ctx, client, a, "out2", thisMonth, -50, "COFFEE", "Cafe")

	buckets, err := MonthlySummary(ctx, client, 0, 3)
	if err != nil {
		t.Fatalf("MonthlySummary: %v", err)
	}
	if len(buckets) != 3 {
		t.Fatalf("buckets = %d, want 3 (fixed N)", len(buckets))
	}
	last := buckets[len(buckets)-1] // current month is the newest bucket
	if last.Income != 2000 {
		t.Errorf("income = %.2f, want 2000", last.Income)
	}
	if last.Spend != 550 {
		t.Errorf("spend = %.2f, want 550 (500 + 50, as positive)", last.Spend)
	}
	if last.Net != 1450 {
		t.Errorf("net = %.2f, want 1450 (2000 - 550)", last.Net)
	}
	if last.Month != thisMonth.Format("2006-01") {
		t.Errorf("month key = %q, want %q", last.Month, thisMonth.Format("2006-01"))
	}
}

// TestMonthlySummary_ExcludesInternalTransfers: a "Transfer to/from" whose counterparty is
// one of the owner's OWN accounts (matched by last-4) is internal and drops out of both
// income and spend (surfaced as Transfers), as does a StepPay repayment. Salary, rent, and
// crucially a "Transfer to <another person>" are external and survive. Net is unchanged by
// the exclusion; only the inflated gross columns are corrected.
func TestMonthlySummary_ExcludesInternalTransfers(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	checking := client.Account.Create().SetSource("commbank").SetName("Smart Access").
		SetMaskedNumber("xxxx 1775").SetType(account.TypeEveryday).SetClass(account.ClassAsset).SaveX(ctx)
	saver := client.Account.Create().SetSource("commbank").SetName("NetBank Saver").
		SetMaskedNumber("xxxx 2158").SetType(account.TypeSavings).SetClass(account.ClassAsset).SaveX(ctx)
	steppay := client.Account.Create().SetSource("commbank").SetName("StepPay").
		SetMaskedNumber("xxxx 2218").SetType(account.TypeSteppay).SetClass(account.ClassLiability).SaveX(ctx)

	now := time.Now().UTC()
	d := time.Date(now.Year(), now.Month(), 15, 0, 0, 0, 0, time.UTC)

	seedTxn(t, ctx, client, checking, "salary", d, 7200, "Salary Foundit Tech", "Foundit") // external in
	seedTxn(t, ctx, client, checking, "rent", d, -2000, "RENT DIRECT DEBIT", "Landlord")   // external out
	// Payment to another person is external and must stay as spend (the case pure
	// amount-matching would get wrong).
	seedTxn(t, ctx, client, checking, "friend", d, -300, "Transfer to Peter CommBank App", "")
	// Internal transfer checking -> saver, matched by the owner's own last-4.
	seedTxn(t, ctx, client, checking, "xfer-out", d, -5000, "Transfer to xx2158 CommBank App", "")
	seedTxn(t, ctx, client, saver, "xfer-in", d, 5000, "Transfer from xx1775 CommBank App", "")
	// StepPay repayment: funded from checking, lands on StepPay. Both legs internal.
	seedTxn(t, ctx, client, checking, "sp-out", d, -120, "StepPay Repayment", "")
	seedTxn(t, ctx, client, steppay, "sp-in", d, 120, "STEPPAY PYMT-THANK YOU", "")

	buckets, err := MonthlySummary(ctx, client, 0, 1)
	if err != nil {
		t.Fatalf("MonthlySummary: %v", err)
	}
	b := buckets[len(buckets)-1]
	if b.Income != 7200 {
		t.Errorf("income = %.2f, want 7200 (salary only; internal transfer-in and StepPay credit excluded)", b.Income)
	}
	if b.Spend != 2300 {
		t.Errorf("spend = %.2f, want 2300 (rent 2000 + payment to Peter 300; internal legs excluded)", b.Spend)
	}
	if b.Transfers != 5120 {
		t.Errorf("transfers = %.2f, want 5120 (5000 account move + 120 StepPay repayment, outbound legs)", b.Transfers)
	}
	if b.Net != 4900 {
		t.Errorf("net = %.2f, want 4900 (7200 - 2300)", b.Net)
	}
}

// TestBalanceHistory_OrderedAndFiltered: ascending by as_of, with an optional from.
func TestBalanceHistory_OrderedAndFiltered(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	a := seedAccount(t, ctx, client, "A", account.ClassAsset, account.TypeEveryday)
	seedSnapshot(t, ctx, client, a, 100, day(2026, 7, 1))
	seedSnapshot(t, ctx, client, a, 200, day(2026, 7, 5))
	seedSnapshot(t, ctx, client, a, 300, day(2026, 7, 9))

	all, err := BalanceHistory(ctx, client, a.ID, nil)
	if err != nil {
		t.Fatalf("BalanceHistory: %v", err)
	}
	if len(all) != 3 || all[0].Balance != 100 || all[2].Balance != 300 {
		t.Fatalf("history = %+v, want ascending 100,200,300", all)
	}

	from := day(2026, 7, 5)
	filtered, err := BalanceHistory(ctx, client, a.ID, &from)
	if err != nil {
		t.Fatalf("BalanceHistory(from): %v", err)
	}
	if len(filtered) != 2 || filtered[0].Balance != 200 {
		t.Fatalf("filtered = %+v, want 200,300", filtered)
	}
}
