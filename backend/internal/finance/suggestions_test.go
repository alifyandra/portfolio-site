package finance

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/billpayment"
	"github.com/alifyandra/portfolio-site/backend/ent/incomereceipt"
	"github.com/alifyandra/portfolio-site/backend/ent/recurrencesuggestion"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
)

// Recurrence Suggestion tests (ADR 0019). Every counterparty, amount and reference below
// is invented; none is a real merchant or account.

// memTxn builds an in-memory posted row with its account edge set, for the pure
// detection tests that need no database.
func memTxn(id, accID int, posted time.Time, amount float64, desc, merchant string) *ent.Transaction {
	return &ent.Transaction{
		ID:          id,
		PostedDate:  posted,
		Amount:      amount,
		Description: desc,
		Merchant:    merchant,
		Edges:       ent.TransactionEdges{Account: &ent.Account{ID: accID}},
	}
}

// TestDetectRuns_MonthlyDebitAroundWeekends: a subscription charged on the 1st, moved
// to the Friday before when the 1st is a Saturday and to the Monday after when it is a
// Sunday, with a one-off at the same counterparty in between. It is one monthly bill run
// of five rows: the one-off is skipped on its amount, the grid sits on the 1st despite
// the two moved rows, and the pattern starts at the counterparty, not the bank's words.
func TestDetectRuns_MonthlyDebitAroundWeekends(t *testing.T) {
	today := day(2026, time.November, 8)
	desc := func(ref string) string { return "Direct Debit 00" + ref + " ZYLO STREAMING REF " + ref }
	rows := []*ent.Transaction{
		memTxn(1, 7, day(2026, time.July, 1), -15.99, desc("4411"), ""),
		memTxn(2, 7, day(2026, time.July, 31), -15.99, desc("4412"), ""),   // 1 Aug is a Saturday
		memTxn(3, 7, day(2026, time.August, 15), -42.00, desc("4413"), ""), // a one-off
		memTxn(4, 7, day(2026, time.September, 1), -15.99, desc("4414"), ""),
		memTxn(5, 7, day(2026, time.October, 1), -15.99, desc("4415"), ""),
		memTxn(6, 7, day(2026, time.November, 2), -15.99, desc("4416"), ""), // 1 Nov is a Sunday
	}
	runs := detectRuns(rows, nil, today)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1: %+v", len(runs), runs)
	}
	r := runs[0]
	if r.kind != recurrencesuggestion.KindBill || r.cadence != recurringbill.CadenceMonthly || r.accountID != 7 {
		t.Errorf("run = %s/%s/account %d, want bill/monthly/7", r.kind, r.cadence, r.accountID)
	}
	if len(r.evidence) != 5 {
		t.Errorf("evidence = %d rows, want 5 (the one-off left out)", len(r.evidence))
	}
	for _, e := range r.evidence {
		if e.ID == 3 {
			t.Error("the one-off at a different amount is in the evidence")
		}
	}
	if r.expectedAmount != 15.99 {
		t.Errorf("expected amount = %v, want 15.99", r.expectedAmount)
	}
	if !r.anchor.Equal(day(2026, time.July, 1)) {
		t.Errorf("anchor = %s, want 2026-07-01 (the 1st, on the oldest cycle)", r.anchor.Format(dateLayoutTest))
	}
	if r.matchPattern != "ZYLO STREAMING" || r.label != "ZYLO STREAMING" {
		t.Errorf("label/pattern = %q/%q, want ZYLO STREAMING for both", r.label, r.matchPattern)
	}
	if r.counterparty != "d:zylo streaming" {
		t.Errorf("counterparty = %q, want the description's distinctive words only", r.counterparty)
	}
}

// TestDetectRuns_FortnightlySalaryPaidEarly: a fortnightly credit with one cycle paid a
// day early and one with a little overtime is one fortnightly income run, its grid on
// the regular Thursday and its amount the median.
func TestDetectRuns_FortnightlySalaryPaidEarly(t *testing.T) {
	today := day(2026, time.November, 8)
	desc := "Salary ACME WIDGETS PTY 000123"
	rows := []*ent.Transaction{
		memTxn(1, 2, day(2026, time.August, 13), 2450, desc, ""),
		memTxn(2, 2, day(2026, time.August, 27), 2450, desc, ""),
		memTxn(3, 2, day(2026, time.September, 10), 2475.5, desc, ""),
		memTxn(4, 2, day(2026, time.September, 23), 2450, desc, ""), // a Wednesday, a day early
		memTxn(5, 2, day(2026, time.October, 8), 2450, desc, ""),
		memTxn(6, 2, day(2026, time.October, 22), 2450, desc, ""),
		memTxn(7, 2, day(2026, time.November, 5), 2450, desc, ""),
	}
	runs := detectRuns(rows, nil, today)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	r := runs[0]
	if r.kind != recurrencesuggestion.KindIncome || r.cadence != recurringbill.CadenceFortnightly || len(r.evidence) != 7 {
		t.Errorf("run = %s/%s/%d rows, want income/fortnightly/7", r.kind, r.cadence, len(r.evidence))
	}
	if r.expectedAmount != 2450 {
		t.Errorf("expected amount = %v, want the median 2450", r.expectedAmount)
	}
	if !r.anchor.Equal(day(2026, time.August, 13)) {
		t.Errorf("anchor = %s, want 2026-08-13 (the Thursday grid)", r.anchor.Format(dateLayoutTest))
	}
	if r.matchPattern != "Salary ACME WIDGETS" {
		t.Errorf("pattern = %q, want %q (stopping at the bank's PTY)", r.matchPattern, "Salary ACME WIDGETS")
	}
}

// TestDetectRuns_WeeklyIsNotFortnightly: the finest band that fits wins, so a weekly run
// is not also reported as a fortnightly one built from every other row.
func TestDetectRuns_WeeklyIsNotFortnightly(t *testing.T) {
	today := day(2026, time.November, 8)
	var rows []*ent.Transaction
	for i := 0; i < 5; i++ {
		rows = append(rows, memTxn(i+1, 1, day(2026, time.October, 9).AddDate(0, 0, 7*i), 300, "TUTORING KOALA LEARNING", ""))
	}
	runs := detectRuns(rows, nil, today)
	if len(runs) != 1 || runs[0].cadence != recurringbill.CadenceWeekly || len(runs[0].evidence) != 5 {
		t.Fatalf("runs = %+v, want one weekly run of 5", runs)
	}
}

// TestDetectRuns_MerchantIsLabelAndPattern: when the bank parsed the same merchant on
// every row, the merchant is the counterparty, the label and the pattern.
func TestDetectRuns_MerchantIsLabelAndPattern(t *testing.T) {
	today := day(2026, time.November, 8)
	rows := []*ent.Transaction{
		memTxn(1, 1, day(2026, time.August, 20), -64.5, "KESTREL ENERGY 8812 MELBOURNE", "Kestrel Energy"),
		memTxn(2, 1, day(2026, time.September, 20), -64.5, "KESTREL ENERGY 9921 MELBOURNE", "Kestrel Energy"),
		memTxn(3, 1, day(2026, time.October, 20), -64.5, "KESTREL ENERGY 1034 MELBOURNE", "Kestrel Energy"),
	}
	runs := detectRuns(rows, nil, today)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	if r := runs[0]; r.label != "Kestrel Energy" || r.matchPattern != "Kestrel Energy" || r.counterparty != "m:kestrel energy" {
		t.Errorf("label/pattern/counterparty = %q/%q/%q, want the merchant", r.label, r.matchPattern, r.counterparty)
	}
}

// TestDetectRuns_BankWordsAreNotACounterparty: the bank's own vocabulary is dropped
// before keying, so a run whose narrative is nothing but bank words and reference numbers
// names no counterparty and is not offered: it would group every payee paid that way.
func TestDetectRuns_BankWordsAreNotACounterparty(t *testing.T) {
	today := day(2026, time.November, 8)
	var rows []*ent.Transaction
	for i := 0; i < 4; i++ {
		posted := day(2026, time.November, 3).AddDate(0, -i, 0)
		rows = append(rows,
			memTxn(10+i, 1, posted, 250, "Direct Credit 0"+itoaTest(4410+i), ""),
			memTxn(20+i, 1, posted, -40, "Transfer To 012345 NetBank Ref "+itoaTest(77+i), ""),
		)
	}
	if runs := detectRuns(rows, nil, today); len(runs) != 0 {
		t.Errorf("runs = %+v, want none: bank words alone name no counterparty", runs)
	}
}

// TestDetectRuns_NotARun covers each way a group fails the rule: irregular gaps, an
// amount that is not near-constant, a run that stopped, too few rows, a move between the
// owner's own accounts, and a run split across two accounts.
func TestDetectRuns_NotARun(t *testing.T) {
	today := day(2026, time.November, 8)
	monthly := func(accID int, last time.Time, amounts []float64, desc string) []*ent.Transaction {
		var out []*ent.Transaction
		for i, a := range amounts {
			out = append(out, memTxn(100*accID+i, accID, last.AddDate(0, -(len(amounts)-1-i), 0), a, desc, ""))
		}
		return out
	}
	cases := map[string][]*ent.Transaction{
		"irregular gaps": {
			memTxn(1, 1, day(2026, time.August, 1), -20, "GYM FLEXIFIT", ""),
			memTxn(2, 1, day(2026, time.August, 11), -20, "GYM FLEXIFIT", ""),
			memTxn(3, 1, day(2026, time.September, 20), -20, "GYM FLEXIFIT", ""),
			memTxn(4, 1, day(2026, time.October, 10), -20, "GYM FLEXIFIT", ""),
			memTxn(5, 1, day(2026, time.November, 6), -20, "GYM FLEXIFIT", ""),
		},
		"amount varies": monthly(1, day(2026, time.November, 3), []float64{-80, -120, -95, -140}, "WATERWORKS UTILITY"),
		"stopped":       monthly(1, day(2026, time.August, 3), []float64{-9.99, -9.99, -9.99, -9.99}, "PODCAST PLUS"),
		"two rows":      monthly(1, day(2026, time.November, 3), []float64{-9.99, -9.99}, "PODCAST PLUS"),
		"own transfer":  monthly(1, day(2026, time.November, 3), []float64{-500, -500, -500, -500}, "Transfer to xx9999 CommBank app"),
		"split across accounts": {
			memTxn(1, 1, day(2026, time.September, 3), -12, "CLOUDBOX STORAGE", ""),
			memTxn(2, 2, day(2026, time.October, 3), -12, "CLOUDBOX STORAGE", ""),
			memTxn(3, 1, day(2026, time.November, 3), -12, "CLOUDBOX STORAGE", ""),
		},
	}
	own4 := map[string]bool{"9999": true}
	for name, rows := range cases {
		if runs := detectRuns(rows, own4, today); len(runs) != 0 {
			t.Errorf("%s: runs = %+v, want none", name, runs)
		}
	}
}

// TestBestAnchor_MonthEndGridKeepsItsDay: a bill due on the 31st is clamped to the 30th
// in short months. The anchor must sit on a 31st, or the stored grid would step from the
// 30th forever after; every evidence row then lands exactly on a pay date.
func TestBestAnchor_MonthEndGridKeepsItsDay(t *testing.T) {
	rows := []*ent.Transaction{
		memTxn(1, 1, day(2026, time.June, 30), -30, "x", ""),
		memTxn(2, 1, day(2026, time.July, 31), -30, "x", ""),
		memTxn(3, 1, day(2026, time.August, 31), -30, "x", ""),
		memTxn(4, 1, day(2026, time.September, 30), -30, "x", ""),
		memTxn(5, 1, day(2026, time.October, 31), -30, "x", ""),
	}
	anchor := bestAnchor(rows, recurringbill.CadenceMonthly)
	if anchor.Day() != 31 || anchor.After(day(2026, time.June, 30)) {
		t.Fatalf("anchor = %s, want a 31st on or before the oldest row", anchor.Format(dateLayoutTest))
	}
	for _, r := range rows {
		if !isOccurrence(anchor, recurringbill.CadenceMonthly, r.PostedDate) {
			t.Errorf("%s is not on the grid from %s", r.PostedDate.Format(dateLayoutTest), anchor.Format(dateLayoutTest))
		}
	}
}

// TestFingerprint_IgnoresAmountAndEvidence: a repriced run with one more cycle is the
// same run; another cadence or another account is not.
func TestFingerprint_IgnoresAmountAndEvidence(t *testing.T) {
	base := detectedRun{kind: recurrencesuggestion.KindBill, accountID: 1, counterparty: "d:zylo", cadence: recurringbill.CadenceMonthly, expectedAmount: 10}
	repriced := base
	repriced.expectedAmount = 12
	repriced.evidence = []*ent.Transaction{{ID: 9}}
	if base.fingerprint() != repriced.fingerprint() {
		t.Error("a repriced run got a new fingerprint")
	}
	other := base
	other.cadence = recurringbill.CadenceAnnual
	moved := base
	moved.accountID = 2
	if base.fingerprint() == other.fingerprint() || base.fingerprint() == moved.fingerprint() {
		t.Error("another cadence or account shares the fingerprint")
	}
}

// --- persistence ---

// seedMonthlyRun posts n monthly rows ending on last, one per month, into acc.
func seedMonthlyRun(t *testing.T, ctx context.Context, client *ent.Client, acc *ent.Account, tag string, last time.Time, n int, amount float64, desc string) {
	t.Helper()
	for i := 0; i < n; i++ {
		seedTxn(t, ctx, client, acc, tag+"-"+itoaTest(i), last.AddDate(0, -i, 0), amount, desc, "")
	}
}

func itoaTest(n int) string { return strconv.Itoa(n) }

// recentDay is a day about back days ago whose day of the month is at most the 28th, so
// stepping it back whole months never overflows into the next month and the seeded run
// stays a clean monthly one whatever day the test runs.
func recentDay(today time.Time, back int) time.Time {
	d := today.AddDate(0, 0, -back)
	if d.Day() > 28 {
		d = d.AddDate(0, 0, 28-d.Day())
	}
	return d
}

// TestScanRecurrences_CreatesRefreshesAndRemembersDismissal: a scan stores a pending
// suggestion with its evidence, a second scan refreshes it instead of adding another,
// and once dismissed the run is never offered again.
func TestScanRecurrences_CreatesRefreshesAndRemembersDismissal(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")

	sum, err := ScanRecurrences(ctx, client)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if sum.RunsFound != 1 || sum.Created != 1 {
		t.Fatalf("first scan = %+v, want 1 run created", sum)
	}
	views, err := ListSuggestions(ctx, client, "pending")
	if err != nil || len(views) != 1 {
		t.Fatalf("pending = %d (%v), want 1", len(views), err)
	}
	v := views[0]
	if v.Kind != "bill" || v.Cadence != "monthly" || v.ExpectedAmount != 15.99 || len(v.Evidence) != 4 || v.AccountID != acc.ID {
		t.Errorf("suggestion = %+v, want a monthly bill of 15.99 with 4 rows on the account", v)
	}
	if !v.Evidence[0].PostedDate.After(v.Evidence[3].PostedDate) {
		t.Error("evidence is not newest first")
	}

	sum, err = ScanRecurrences(ctx, client)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if sum.Created != 0 || sum.Refreshed != 1 || client.RecurrenceSuggestion.Query().CountX(ctx) != 1 {
		t.Errorf("rescan = %+v, want one refreshed and no new row", sum)
	}
	if n := client.RecurrenceSuggestion.Query().QueryEvidence().CountX(ctx); n != 4 {
		t.Errorf("evidence after a refresh = %d rows, want the same 4 (cleared and re-added)", n)
	}

	if err := DismissSuggestion(ctx, client, v.ID); err != nil {
		t.Fatalf("dismiss: %v", err)
	}
	if err := DismissSuggestion(ctx, client, v.ID); !errors.Is(err, ErrSuggestionDecided) {
		t.Errorf("second dismiss = %v, want ErrSuggestionDecided", err)
	}
	if err := DismissSuggestion(ctx, client, 9999); !ent.IsNotFound(err) {
		t.Errorf("unknown dismiss = %v, want not found", err)
	}
	sum, err = ScanRecurrences(ctx, client)
	if err != nil {
		t.Fatalf("scan after dismissal: %v", err)
	}
	if sum.Dismissed != 1 || sum.Created != 0 {
		t.Errorf("scan after dismissal = %+v, want it remembered", sum)
	}
	if views, _ := ListSuggestions(ctx, client, "pending"); len(views) != 0 {
		t.Errorf("pending after dismissal = %d, want 0", len(views))
	}
}

// TestScanRecurrences_SkipsDeclaredRuns: a run a declaration already covers is not
// suggested, whether a bill's match pattern finds it or an income receipt already links
// one of its rows.
func TestScanRecurrences_SkipsDeclaredRuns(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	rentDay := recentDay(today, 2)
	seedMonthlyRun(t, ctx, client, acc, "rent", rentDay, 4, 900, "RENT FROM TENANT QUOLL")
	seedBill(t, ctx, client, "Streaming", 15.99, recurringbill.CadenceMonthly, today, func(c *ent.RecurringBillCreate) {
		c.SetMatchPattern("zylo")
	})
	inc := seedIncome(t, ctx, client, "Rent in", 900, recurringincome.CadenceMonthly, rentDay)
	credit := client.Transaction.Query().Where(transaction.PostedDateEQ(rentDay), transaction.AmountGT(0)).OnlyX(ctx)
	client.IncomeReceipt.Create().SetIncomeID(inc.ID).SetTransactionID(credit.ID).SetOccurrenceDate(rentDay).SaveX(ctx)

	sum, err := ScanRecurrences(ctx, client)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if sum.RunsFound != 2 || sum.Declared != 2 || sum.Created != 0 {
		t.Errorf("scan = %+v, want both runs found and both left as declared", sum)
	}
}

// TestScanRecurrences_DropsVanishedPending: a pending suggestion whose run the pass no
// longer finds is removed, since it is derived; decided ones are never touched.
func TestScanRecurrences_DropsVanishedPending(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	stale := func(fp string, status recurrencesuggestion.Status) {
		client.RecurrenceSuggestion.Create().
			SetFingerprint(fp).SetKind(recurrencesuggestion.KindBill).SetCounterparty("d:gone").
			SetLabel("Gone").SetCadence(recurrencesuggestion.CadenceMonthly).SetExpectedAmount(5).
			SetAnchorDate(today).SetStatus(status).SaveX(ctx)
	}
	stale("pending-gone", recurrencesuggestion.StatusPending)
	stale("dismissed-gone", recurrencesuggestion.StatusDismissed)

	sum, err := ScanRecurrences(ctx, client)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if sum.Dropped != 1 {
		t.Errorf("dropped = %d, want 1", sum.Dropped)
	}
	left := client.RecurrenceSuggestion.Query().AllX(ctx)
	if len(left) != 1 || left[0].Fingerprint != "dismissed-gone" {
		t.Errorf("left = %+v, want only the dismissed one", left)
	}
}

// TestAcceptSuggestion_Bill: accepting creates the bill as suggested (name overridable),
// links every evidence row to its cycle as an auto payment, and records what it made. A
// second accept is refused and the next scan treats the run as declared.
func TestAcceptSuggestion_Bill(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	if _, err := ScanRecurrences(ctx, client); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)

	res, err := AcceptSuggestion(ctx, client, s.ID, "Streaming")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if res.Kind != "bill" || res.BillID == 0 || res.Linked != 4 || res.Skipped != 0 {
		t.Fatalf("accept = %+v, want a bill with 4 payments linked", res)
	}
	b := client.RecurringBill.Query().WithAccount().OnlyX(ctx)
	if b.Name != "Streaming" || b.ExpectedAmount != 15.99 || b.Cadence != recurringbill.CadenceMonthly ||
		b.MatchPattern != "ZYLO STREAMING" || b.Edges.Account == nil || b.Edges.Account.ID != acc.ID {
		t.Errorf("bill = %+v, want the suggestion's shape on the account", b)
	}
	if !b.AnchorDate.Equal(recentDay(today, 3).AddDate(0, -3, 0)) {
		t.Errorf("anchor = %s, want the oldest cycle", b.AnchorDate.Format(dateLayoutTest))
	}
	if n := client.BillPayment.Query().Where(billpayment.MethodEQ(billpayment.MethodAuto)).CountX(ctx); n != 4 {
		t.Errorf("auto payments = %d, want 4", n)
	}
	s = client.RecurrenceSuggestion.Query().WithBill().OnlyX(ctx)
	if s.Status != recurrencesuggestion.StatusAccepted || s.DecidedAt == nil || s.Edges.Bill == nil || s.Edges.Bill.ID != b.ID {
		t.Errorf("suggestion = %+v, want accepted and linked to the bill", s)
	}
	if _, err := AcceptSuggestion(ctx, client, s.ID, ""); !errors.Is(err, ErrSuggestionDecided) {
		t.Errorf("second accept = %v, want ErrSuggestionDecided", err)
	}
	if _, err := AcceptSuggestion(ctx, client, 9999, ""); !ent.IsNotFound(err) {
		t.Errorf("unknown accept = %v, want not found", err)
	}
	sum, err := ScanRecurrences(ctx, client)
	if err != nil || sum.Created != 0 || sum.Declared != 1 {
		t.Errorf("scan after accept = %+v (%v), want the run left as declared", sum, err)
	}
}

// TestAcceptSuggestion_IncomeLinksEarlyPay: an accepted income run gets its match pattern
// and its history as auto receipts, including a cycle paid early.
func TestAcceptSuggestion_IncomeLinksEarlyPay(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	// Fortnightly, newest two days ago; the one before it posted two days early.
	payday := today.AddDate(0, 0, -2)
	for i, offset := range []int{0, -16, -28, -42} {
		seedTxn(t, ctx, client, acc, "pay-"+itoaTest(i), payday.AddDate(0, 0, offset), 2450, "Salary ACME WIDGETS", "")
	}
	if _, err := ScanRecurrences(ctx, client); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)
	if s.Kind != recurrencesuggestion.KindIncome || s.Cadence != recurrencesuggestion.CadenceFortnightly {
		t.Fatalf("suggestion = %s/%s, want income/fortnightly", s.Kind, s.Cadence)
	}

	res, err := AcceptSuggestion(ctx, client, s.ID, "")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if res.Kind != "income" || res.IncomeID == 0 || res.Linked != 4 {
		t.Fatalf("accept = %+v, want an income with 4 receipts", res)
	}
	inc := client.RecurringIncome.GetX(ctx, res.IncomeID)
	if inc.Name != "Salary ACME WIDGETS" || inc.MatchPattern != "Salary ACME WIDGETS" {
		t.Errorf("income = %q / %q, want the label as name and the pattern", inc.Name, inc.MatchPattern)
	}
	early := client.IncomeReceipt.Query().
		Where(incomereceipt.OccurrenceDateEQ(payday.AddDate(0, 0, -14))).
		WithTransaction().
		OnlyX(ctx)
	if early.Method != incomereceipt.MethodAuto || !early.Edges.Transaction.PostedDate.Equal(payday.AddDate(0, 0, -16)) {
		t.Errorf("early cycle = %+v, want the credit posted two days before it, method auto", early)
	}
}

// TestAcceptSuggestion_NameTaken: a name already used by a bill is a refusal the owner
// can retry, and nothing is half-created.
func TestAcceptSuggestion_NameTaken(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := localToday()
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	seedBill(t, ctx, client, "Streaming", 30, recurringbill.CadenceAnnual, today)
	if _, err := ScanRecurrences(ctx, client); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)
	if _, err := AcceptSuggestion(ctx, client, s.ID, "Streaming"); !errors.Is(err, ErrDeclarationNameTaken) {
		t.Fatalf("accept onto a taken name = %v, want ErrDeclarationNameTaken", err)
	}
	if n := client.BillPayment.Query().CountX(ctx); n != 0 {
		t.Errorf("payments after a refused accept = %d, want 0", n)
	}
	if s = client.RecurrenceSuggestion.GetX(ctx, s.ID); s.Status != recurrencesuggestion.StatusPending {
		t.Errorf("status after a refused accept = %s, want pending", s.Status)
	}
}
