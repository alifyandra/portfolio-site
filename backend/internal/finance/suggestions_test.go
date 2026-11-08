package finance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/billpayment"
	"github.com/alifyandra/portfolio-site/backend/ent/hook"
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
	if r.counterparty != "zylo streaming" {
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
	if r.gaps != 0 {
		t.Errorf("gaps = %d, want 0: a fortnightly run is not a weekly one missing every other week", r.gaps)
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

// TestDetectRuns_ToleratesOneSkippedCycle: a monthly charge that missed September is
// still one run, with the skipped cycle counted in gaps; two cycles missed in a row end
// the run, so what is left is too short to offer.
func TestDetectRuns_ToleratesOneSkippedCycle(t *testing.T) {
	today := day(2026, time.November, 8)
	monthly := func(dates ...time.Time) []*ent.Transaction {
		var rows []*ent.Transaction
		for i, d := range dates {
			rows = append(rows, memTxn(i+1, 1, d, -9.99, "PODCAST PLUS", ""))
		}
		return rows
	}
	runs := detectRuns(monthly(day(2026, time.June, 3), day(2026, time.July, 3), day(2026, time.August, 3),
		day(2026, time.October, 3), day(2026, time.November, 3)), nil, today)
	if len(runs) != 1 || runs[0].cadence != recurringbill.CadenceMonthly || len(runs[0].evidence) != 5 || runs[0].gaps != 1 {
		t.Fatalf("runs = %+v, want one monthly run of 5 rows with 1 gap", runs)
	}
	if !runs[0].anchor.Equal(day(2026, time.June, 3)) {
		t.Errorf("anchor = %s, want 2026-06-03", runs[0].anchor.Format(dateLayoutTest))
	}
	runs = detectRuns(monthly(day(2026, time.June, 3), day(2026, time.July, 3), day(2026, time.August, 3),
		day(2026, time.November, 3)), nil, today)
	if len(runs) != 0 {
		t.Errorf("two skipped cycles: runs = %+v, want none", runs)
	}
}

// TestDetectRuns_EvidenceFitsTheMedian: every evidence row is within the tolerance of
// the expected amount (the run's median), so the accepted declaration's own matcher takes
// each one. October's 105 is near the newest row's 98 but not near the median 90, so it
// is left out and the run keeps the other four with September to November as one gap.
func TestDetectRuns_EvidenceFitsTheMedian(t *testing.T) {
	today := day(2026, time.November, 8)
	var rows []*ent.Transaction
	for i, a := range []float64{-90, -90, -90, -105, -98} {
		rows = append(rows, memTxn(i+1, 1, day(2026, time.July, 4).AddDate(0, i, 0), a, "GYM FLEXIFIT", ""))
	}
	runs := detectRuns(rows, nil, today)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	r := runs[0]
	for _, e := range r.evidence {
		if math.Abs(math.Abs(e.Amount)-r.expectedAmount) > r.expectedAmount*suggestAmountTolerancePct/100 {
			t.Errorf("evidence %v is outside the tolerance of the expected %v", e.Amount, r.expectedAmount)
		}
	}
	if r.expectedAmount != 90 || len(r.evidence) != 4 || r.gaps != 1 {
		t.Errorf("run = %v over %d rows with %d gaps, want 90 over 4 with 1", r.expectedAmount, len(r.evidence), r.gaps)
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
	if r := runs[0]; r.label != "Kestrel Energy" || r.matchPattern != "Kestrel Energy" || r.counterparty != "kestrel energy" {
		t.Errorf("label/pattern/counterparty = %q/%q/%q, want the merchant", r.label, r.matchPattern, r.counterparty)
	}
}

// runSizes maps each run's counterparty to its evidence count, for the grouping tests.
func runSizes(runs []detectedRun) map[string]int {
	out := map[string]int{}
	for _, r := range runs {
		out[r.counterparty] = len(r.evidence)
	}
	return out
}

// TestDetectRuns_OnePayeeOneGroup: one payee's rows group together whether or not the
// bank parsed a merchant on them, and whatever city the card terminal appended. Kestrel
// has its merchant parsed on alternate months only, with a store number between its name
// and the city; Zylo has no merchant and a different city most months.
func TestDetectRuns_OnePayeeOneGroup(t *testing.T) {
	today := day(2026, time.November, 8)
	var rows []*ent.Transaction
	for i := 0; i < 4; i++ {
		merchant := ""
		if i%2 == 0 {
			merchant = "Kestrel Energy"
		}
		rows = append(rows, memTxn(10+i, 1, day(2026, time.October, 20).AddDate(0, -i, 0), -64.5,
			"KESTREL ENERGY "+itoaTest(8812+i)+" MELBOURNE", merchant))
	}
	for i, city := range []string{"SYDNEY AU", "MELBOURNE AU", "SYDNEY", "BRISBANE QLD"} {
		rows = append(rows, memTxn(20+i, 1, day(2026, time.November, 2).AddDate(0, -i, 0), -15.99, "ZYLO STREAMING "+city, ""))
	}
	got := runSizes(detectRuns(rows, nil, today))
	want := map[string]int{"kestrel energy": 4, "zylo streaming": 4}
	if !maps.Equal(got, want) {
		t.Errorf("runs = %v, want %v", got, want)
	}
}

// TestDetectRuns_DifferentPayeesStayApart: the folding never merges two payees. A
// merchant-less narrative folds onto a parsed merchant only when its whole name (the
// words before its first number) is that merchant, so "ZYLO INSURANCE" stays apart from
// a merchant "Zylo" at the same amount, and two salaries keep the employer that follows
// the shared word.
func TestDetectRuns_DifferentPayeesStayApart(t *testing.T) {
	today := day(2026, time.November, 8)
	var rows []*ent.Transaction
	for i := 0; i < 4; i++ {
		rows = append(rows,
			memTxn(10+i, 1, day(2026, time.November, 3).AddDate(0, -i, 0), -15.99, "ZYLO "+itoaTest(4471+i)+" SYDNEY", "Zylo"),
			memTxn(20+i, 1, day(2026, time.October, 25).AddDate(0, -i, 0), -15.99, "ZYLO INSURANCE "+itoaTest(5501+i)+" SYDNEY", ""),
			memTxn(30+i, 1, day(2026, time.October, 28).AddDate(0, -i, 0), -15.99, "ZYLO STREAMING PREMIUM "+itoaTest(6601+i), ""),
		)
	}
	for i := 0; i < 5; i++ {
		rows = append(rows,
			memTxn(40+i, 1, day(2026, time.November, 5).AddDate(0, 0, -14*i), 2000, "SALARY 0123"+itoaTest(i)+" ACME", ""),
			memTxn(50+i, 1, day(2026, time.November, 6).AddDate(0, 0, -14*i), 2000, "SALARY 0999"+itoaTest(i)+" BETA CORP", ""),
		)
	}
	got := runSizes(detectRuns(rows, nil, today))
	want := map[string]int{"zylo": 4, "zylo insurance": 4, "zylo streaming premium": 4, "salary acme": 5, "salary beta corp": 5}
	if !maps.Equal(got, want) {
		t.Errorf("runs = %v, want %v", got, want)
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
// stays a clean monthly one whatever day today is pinned to.
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
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")

	sum, err := ScanRecurrences(ctx, client, today)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if sum.RunsFound != 1 || sum.Created != 1 {
		t.Fatalf("first scan = %+v, want 1 run created", sum)
	}
	views, err := ListSuggestions(ctx, client, "pending", today)
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

	sum, err = ScanRecurrences(ctx, client, today)
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
	sum, err = ScanRecurrences(ctx, client, today)
	if err != nil {
		t.Fatalf("scan after dismissal: %v", err)
	}
	if sum.Dismissed != 1 || sum.Created != 0 {
		t.Errorf("scan after dismissal = %+v, want it remembered", sum)
	}
	if views, _ := ListSuggestions(ctx, client, "pending", today); len(views) != 0 {
		t.Errorf("pending after dismissal = %d, want 0", len(views))
	}
}

// TestScanRecurrences_StoresGaps: a run with one skipped cycle is stored with its gap
// count, so the owner sees the history is not unbroken before accepting it.
func TestScanRecurrences_StoresGaps(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	last := recentDay(today, 3)
	for _, back := range []int{0, 1, 3, 4} {
		seedTxn(t, ctx, client, acc, "pod-"+itoaTest(back), last.AddDate(0, -back, 0), -9.99, "PODCAST PLUS", "")
	}
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	views, err := ListSuggestions(ctx, client, "pending", today)
	if err != nil || len(views) != 1 {
		t.Fatalf("pending = %d (%v), want 1", len(views), err)
	}
	if v := views[0]; v.Gaps != 1 || len(v.Evidence) != 4 {
		t.Errorf("suggestion gaps/evidence = %d/%d, want 1/4", v.Gaps, len(v.Evidence))
	}
}

// TestListSuggestions_NewestEvidenceOnly: the list carries each suggestion's evidence
// count, its oldest date and only its newest suggestListEvidence rows; GetSuggestion
// returns all of them, newest first.
func TestListSuggestions_NewestEvidenceOnly(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	newest := today.AddDate(0, 0, -2)
	for i := 0; i < 8; i++ {
		seedTxn(t, ctx, client, acc, "tutor-"+itoaTest(i), newest.AddDate(0, 0, -7*i), -60, "TUTORING KOALA LEARNING", "")
	}
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	views, err := ListSuggestions(ctx, client, "pending", today)
	if err != nil || len(views) != 1 {
		t.Fatalf("list = %d (%v), want 1", len(views), err)
	}
	v := views[0]
	if v.EvidenceCount != 8 || len(v.Evidence) != suggestListEvidence || !v.EvidenceSince.Equal(newest.AddDate(0, 0, -49)) {
		t.Fatalf("list view = %d of %d since %s, want %d of 8 since the oldest row", len(v.Evidence), v.EvidenceCount,
			v.EvidenceSince.Format(dateLayoutTest), suggestListEvidence)
	}
	for i, e := range v.Evidence {
		if want := newest.AddDate(0, 0, -7*i); !e.PostedDate.Equal(want) || e.Amount != -60 || e.Description == "" {
			t.Errorf("list evidence[%d] = %+v, want the row posted %s", i, e, want.Format(dateLayoutTest))
		}
	}
	full, err := GetSuggestion(ctx, client, v.ID, today)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if full.EvidenceCount != 8 || len(full.Evidence) != 8 || !full.Evidence[7].PostedDate.Equal(newest.AddDate(0, 0, -49)) {
		t.Errorf("detail = %d of %d, want all 8 newest first", len(full.Evidence), full.EvidenceCount)
	}
	if _, err := GetSuggestion(ctx, client, 9999, today); !ent.IsNotFound(err) {
		t.Errorf("get unknown = %v, want not found", err)
	}
}

// TestScanRecurrences_SkipsDeclaredRuns: a run a declaration already covers is not
// suggested, whether a bill's match pattern finds it or an income receipt already links
// one of its rows.
func TestScanRecurrences_SkipsDeclaredRuns(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
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

	sum, err := ScanRecurrences(ctx, client, today)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if sum.RunsFound != 2 || sum.Declared != 2 || sum.Created != 0 {
		t.Errorf("scan = %+v, want both runs found and both left as declared", sum)
	}
}

// TestScanRecurrences_CoverageOnWordBoundaries: an income matching "ATO" does not cover
// a regular credit from a potato co-op, so that run is still suggested.
func TestScanRecurrences_CoverageOnWordBoundaries(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "potato", recentDay(today, 2), 4, 500, "POTATO CO-OP")
	seedIncome(t, ctx, client, "Tax", 500, recurringincome.CadenceMonthly, today, func(c *ent.RecurringIncomeCreate) {
		c.SetMatchPattern("ato")
	})
	sum, err := ScanRecurrences(ctx, client, today)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if sum.Created != 1 || sum.Declared != 0 {
		t.Errorf("scan = %+v, want the co-op run suggested, not declared", sum)
	}
}

// TestScanRecurrences_EndedDeclarationsStopCovering: a paused declaration still speaks
// for its counterparty, but an ended one covers only rows up to its ended_on plus a
// grace week. A run that carries on past that is suggested again, even when its older
// rows are linked to the ended declaration.
func TestScanRecurrences_EndedDeclarationsStopCovering(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	last := recentDay(today, 3)
	seedMonthlyRun(t, ctx, client, acc, "zylo", last, 4, -15.99, "ZYLO STREAMING")
	seedMonthlyRun(t, ctx, client, acc, "gym", recentDay(today, 2), 4, -20, "GYM FLEXIFIT")
	endedOn := last.AddDate(0, -2, 0)
	ended := seedBill(t, ctx, client, "Streaming", 15.99, recurringbill.CadenceMonthly, last.AddDate(0, -3, 0), func(c *ent.RecurringBillCreate) {
		c.SetMatchPattern("zylo").SetStatus(recurringbill.StatusEnded).SetEndedOn(endedOn)
	})
	oldest := client.Transaction.Query().Where(transaction.DedupHash("hash-zylo-3")).OnlyX(ctx)
	client.BillPayment.Create().SetBillID(ended.ID).SetTransactionID(oldest.ID).SetOccurrenceDate(last.AddDate(0, -3, 0)).SaveX(ctx)
	seedBill(t, ctx, client, "Gym", 20, recurringbill.CadenceMonthly, today, func(c *ent.RecurringBillCreate) {
		c.SetMatchPattern("flexifit").SetStatus(recurringbill.StatusPaused)
	})

	sum, err := ScanRecurrences(ctx, client, today)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if sum.Created != 1 || sum.Declared != 1 {
		t.Fatalf("scan = %+v, want the run past the ended bill suggested and the paused one declared", sum)
	}
	if s := client.RecurrenceSuggestion.Query().OnlyX(ctx); s.Counterparty != "zylo streaming" {
		t.Errorf("suggested %q, want the streaming run", s.Counterparty)
	}

	// Ending it later than the newest row (within the grace week) covers the run again.
	client.RecurringBill.UpdateOne(ended).SetEndedOn(last.AddDate(0, 0, -5)).ExecX(ctx)
	if sum, err := ScanRecurrences(ctx, client, today); err != nil || sum.Declared != 2 || sum.Dropped != 1 {
		t.Errorf("scan = %+v (%v), want both declared and the pending one dropped", sum, err)
	}
}

// TestScanRecurrences_ReopensAcceptedRunAfterItsDeclarationEnded: the owner accepted a
// run, then ended the bill, and the charges carried on. The accepted suggestion is
// reopened as pending so the owner sees it again; it no longer points at the ended bill.
func TestScanRecurrences_ReopensAcceptedRunAfterItsDeclarationEnded(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	last := recentDay(today, 3)
	seedMonthlyRun(t, ctx, client, acc, "zylo", last, 4, -15.99, "ZYLO STREAMING")
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)
	res, err := AcceptSuggestion(ctx, client, s.ID, "Streaming", s.MatchPattern)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	client.RecurringBill.UpdateOneID(res.BillID).SetStatus(recurringbill.StatusEnded).SetEndedOn(last.AddDate(0, -2, 0)).ExecX(ctx)

	sum, err := ScanRecurrences(ctx, client, today)
	if err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if sum.Reopened != 1 || sum.Declared != 0 {
		t.Errorf("rescan = %+v, want the accepted run reopened", sum)
	}
	s = client.RecurrenceSuggestion.Query().WithBill().OnlyX(ctx)
	if s.Status != recurrencesuggestion.StatusPending || s.DecidedAt != nil || s.Edges.Bill != nil {
		t.Errorf("suggestion = %+v (bill %v), want pending, undecided and unlinked", s, s.Edges.Bill)
	}
}

// TestScanRecurrences_DropsVanishedPending: a pending suggestion whose run the pass no
// longer finds is removed, since it is derived; decided ones are never touched.
func TestScanRecurrences_DropsVanishedPending(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	stale := func(fp string, status recurrencesuggestion.Status) {
		client.RecurrenceSuggestion.Create().
			SetFingerprint(fp).SetKind(recurrencesuggestion.KindBill).SetCounterparty("d:gone").
			SetLabel("Gone").SetCadence(recurrencesuggestion.CadenceMonthly).SetExpectedAmount(5).
			SetAnchorDate(today).SetStatus(status).SaveX(ctx)
	}
	stale("pending-gone", recurrencesuggestion.StatusPending)
	stale("dismissed-gone", recurrencesuggestion.StatusDismissed)

	sum, err := ScanRecurrences(ctx, client, today)
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

// TestScanRecurrences_ConcurrentInsertIsHarmless: two scans can both read "no suggestion
// for this run yet" and both insert it. The hook plays the other scan committing its row
// between this scan's read and its insert; the insert must not trip the unique
// fingerprint (a 500), and the one row left carries this scan's run and evidence.
func TestScanRecurrences_ConcurrentInsertIsHarmless(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	fired := false
	client.RecurrenceSuggestion.Use(func(next ent.Mutator) ent.Mutator {
		return hook.RecurrenceSuggestionFunc(func(ctx context.Context, m *ent.RecurrenceSuggestionMutation) (ent.Value, error) {
			if fp, ok := m.Fingerprint(); ok && !fired && m.Op().Is(ent.OpCreate) {
				fired = true
				if err := m.Client().RecurrenceSuggestion.Create().
					SetFingerprint(fp).SetKind(recurrencesuggestion.KindBill).SetCounterparty("zylo streaming").
					SetLabel("From the other scan").SetCadence(recurrencesuggestion.CadenceMonthly).
					SetExpectedAmount(1).SetAnchorDate(today).Exec(ctx); err != nil {
					return nil, err
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan racing another scan's insert = %v, want no error", err)
	}
	rows := client.RecurrenceSuggestion.Query().WithEvidence().AllX(ctx)
	if len(rows) != 1 || rows[0].Label != "ZYLO STREAMING" || rows[0].ExpectedAmount != 15.99 || len(rows[0].Edges.Evidence) != 4 {
		t.Errorf("suggestions = %+v, want one row refreshed to this scan's run with 4 evidence rows", rows)
	}
}

// TestScanRecurrences_ConcurrentDropIsHarmless: two scans can both decide to drop the
// same vanished pending suggestion. The hook plays the other scan deleting it first; this
// scan's drop must then be a no-op, not a not-found error.
func TestScanRecurrences_ConcurrentDropIsHarmless(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	stale := client.RecurrenceSuggestion.Create().
		SetFingerprint("gone").SetKind(recurrencesuggestion.KindBill).SetCounterparty("gone").
		SetLabel("Gone").SetCadence(recurrencesuggestion.CadenceMonthly).SetExpectedAmount(5).
		SetAnchorDate(today).SaveX(ctx)
	fired := false
	client.RecurrenceSuggestion.Use(func(next ent.Mutator) ent.Mutator {
		return hook.RecurrenceSuggestionFunc(func(ctx context.Context, m *ent.RecurrenceSuggestionMutation) (ent.Value, error) {
			if !fired && m.Op().Is(ent.OpUpdate|ent.OpUpdateOne|ent.OpDelete|ent.OpDeleteOne) {
				if ids, err := m.IDs(ctx); err == nil && slices.Contains(ids, stale.ID) {
					fired = true
					if err := m.Client().RecurrenceSuggestion.DeleteOneID(stale.ID).Exec(ctx); err != nil {
						return nil, err
					}
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan racing another scan's drop = %v, want no error", err)
	}
	if n := client.RecurrenceSuggestion.Query().CountX(ctx); n != 0 {
		t.Errorf("suggestions left = %d, want 0", n)
	}
}

// TestAcceptSuggestion_Bill: accepting creates the bill as suggested (name overridable),
// links every evidence row to its cycle as an auto payment, and records what it made. A
// second accept is refused and the next scan treats the run as declared.
func TestAcceptSuggestion_Bill(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)

	res, err := AcceptSuggestion(ctx, client, s.ID, "Streaming", "ZYLO STREAMING")
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
	if _, err := AcceptSuggestion(ctx, client, s.ID, "", ""); !errors.Is(err, ErrSuggestionDecided) {
		t.Errorf("second accept = %v, want ErrSuggestionDecided", err)
	}
	if _, err := AcceptSuggestion(ctx, client, 9999, "", ""); !ent.IsNotFound(err) {
		t.Errorf("unknown accept = %v, want not found", err)
	}
	sum, err := ScanRecurrences(ctx, client, today)
	if err != nil || sum.Created != 0 || sum.Declared != 1 {
		t.Errorf("scan after accept = %+v (%v), want the run left as declared", sum, err)
	}
}

// TestAcceptSuggestion_LosesARaceCleanly: under READ COMMITTED two accepts can both read
// the suggestion as pending. The hook plays the other accept committing between this
// one's read and its status write; the status change must then fail as already decided,
// and nothing this accept created may survive.
func TestAcceptSuggestion_LosesARaceCleanly(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)
	fired := false
	client.RecurrenceSuggestion.Use(func(next ent.Mutator) ent.Mutator {
		return hook.RecurrenceSuggestionFunc(func(ctx context.Context, m *ent.RecurrenceSuggestionMutation) (ent.Value, error) {
			if !fired && m.Op().Is(ent.OpUpdate|ent.OpUpdateOne) {
				fired = true
				if err := m.Client().RecurrenceSuggestion.UpdateOneID(s.ID).SetStatus(recurrencesuggestion.StatusAccepted).Exec(ctx); err != nil {
					return nil, err
				}
			}
			return next.Mutate(ctx, m)
		})
	})
	if _, err := AcceptSuggestion(ctx, client, s.ID, "Streaming", ""); !errors.Is(err, ErrSuggestionDecided) {
		t.Fatalf("accept after another accept won = %v, want ErrSuggestionDecided", err)
	}
	if n := client.RecurringBill.Query().CountX(ctx); n != 0 {
		t.Errorf("bills after the lost race = %d, want 0", n)
	}
	if n := client.BillPayment.Query().CountX(ctx); n != 0 {
		t.Errorf("payments after the lost race = %d, want 0", n)
	}
}

// TestAcceptSuggestion_TwoConcurrentAccepts: two accepts of one suggestion at once, under
// different names, make exactly one declaration; the other is told it was decided.
func TestAcceptSuggestion_TwoConcurrentAccepts(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"Streaming A", "Streaming B"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := AcceptSuggestion(ctx, client, s.ID, name, "")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	won, decided := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			won++
		case errors.Is(err, ErrSuggestionDecided):
			decided++
		default:
			t.Errorf("accept: %v", err)
		}
	}
	if won != 1 || decided != 1 {
		t.Errorf("won/decided = %d/%d, want 1/1", won, decided)
	}
	if n := client.RecurringBill.Query().CountX(ctx); n != 1 {
		t.Errorf("bills = %d, want 1", n)
	}
	if n := client.BillPayment.Query().CountX(ctx); n != 4 {
		t.Errorf("payments = %d, want 4", n)
	}
}

// stmtLog records every statement a debug driver runs, so a test can pin how many
// queries an operation makes.
type stmtLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *stmtLog) add(_ context.Context, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprint(v...))
}

func (l *stmtLog) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = nil
}

// selectsFrom counts the logged SELECTs that read FROM table (quoted either way, since
// SQLite's dialect quotes with backticks and Postgres's with double quotes).
func (l *stmtLog) selectsFrom(table string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, line := range l.lines {
		if strings.Contains(line, "SELECT") &&
			(strings.Contains(line, "FROM `"+table+"`") || strings.Contains(line, `FROM "`+table+`"`)) {
			n++
		}
	}
	return n
}

// newLoggedFinanceTestClient is newFinanceTestClient with every statement logged.
func newLoggedFinanceTestClient(t *testing.T) (*ent.Client, *stmtLog) {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	log := &stmtLog{}
	client := ent.NewClient(ent.Driver(dialect.DebugWithContext(entsql.OpenDB(dialect.SQLite, db), log.add)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return client, log
}

// TestAcceptSuggestion_OneLinkedLookup: accepting checks which evidence rows are already
// linked with one query, not one per row, for a bill and for an income.
func TestAcceptSuggestion_OneLinkedLookup(t *testing.T) {
	client, log := newLoggedFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 6, -15.99, "ZYLO STREAMING")
	for i := 0; i < 6; i++ {
		seedTxn(t, ctx, client, acc, "pay-"+itoaTest(i), today.AddDate(0, 0, -2-14*i), 2450, "Salary ACME WIDGETS", "")
	}
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	for _, s := range client.RecurrenceSuggestion.Query().AllX(ctx) {
		log.reset()
		res, err := AcceptSuggestion(ctx, client, s.ID, "", s.MatchPattern)
		if err != nil || res.Linked != 6 {
			t.Fatalf("accept %s = %+v (%v), want 6 linked", s.Kind, res, err)
		}
		table := "bill_payments"
		if s.Kind == recurrencesuggestion.KindIncome {
			table = "income_receipts"
		}
		if n := log.selectsFrom(table); n > 1 {
			t.Errorf("accept %s read %s %d times, want once", s.Kind, table, n)
		}
	}
}

// TestAcceptSuggestion_IncomeLinksEarlyPay: an accepted income run gets its match pattern
// and its history as auto receipts, including a cycle paid early.
func TestAcceptSuggestion_IncomeLinksEarlyPay(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	// Fortnightly, newest two days ago; the one before it posted two days early.
	payday := today.AddDate(0, 0, -2)
	for i, offset := range []int{0, -16, -28, -42} {
		seedTxn(t, ctx, client, acc, "pay-"+itoaTest(i), payday.AddDate(0, 0, offset), 2450, "Salary ACME WIDGETS", "")
	}
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)
	if s.Kind != recurrencesuggestion.KindIncome || s.Cadence != recurrencesuggestion.CadenceFortnightly {
		t.Fatalf("suggestion = %s/%s, want income/fortnightly", s.Kind, s.Cadence)
	}

	res, err := AcceptSuggestion(ctx, client, s.ID, "", " acme widgets ")
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if res.Kind != "income" || res.IncomeID == 0 || res.Linked != 4 {
		t.Fatalf("accept = %+v, want an income with 4 receipts", res)
	}
	inc := client.RecurringIncome.GetX(ctx, res.IncomeID)
	if inc.Name != "Salary ACME WIDGETS" || inc.MatchPattern != "acme widgets" {
		t.Errorf("income = %q / %q, want the label as name and the owner's pattern trimmed", inc.Name, inc.MatchPattern)
	}
	early := client.IncomeReceipt.Query().
		Where(incomereceipt.OccurrenceDateEQ(payday.AddDate(0, 0, -14))).
		WithTransaction().
		OnlyX(ctx)
	if early.Method != incomereceipt.MethodAuto || !early.Edges.Transaction.PostedDate.Equal(payday.AddDate(0, 0, -16)) {
		t.Errorf("early cycle = %+v, want the credit posted two days before it, method auto", early)
	}
}

// TestAcceptSuggestion_PatternIsTheOwners: the declaration takes the match pattern the
// owner gave, never the suggested one. Clearing it makes a hand-linked bill, and the
// evidence is still linked as its history.
func TestAcceptSuggestion_PatternIsTheOwners(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)
	if s.MatchPattern != "ZYLO STREAMING" {
		t.Fatalf("suggested pattern = %q, want ZYLO STREAMING", s.MatchPattern)
	}
	res, err := AcceptSuggestion(ctx, client, s.ID, "Streaming", "   ")
	if err != nil || res.Linked != 4 {
		t.Fatalf("accept = %+v (%v), want 4 linked", res, err)
	}
	if b := client.RecurringBill.GetX(ctx, res.BillID); b.MatchPattern != "" {
		t.Errorf("bill pattern = %q, want empty: the owner cleared it", b.MatchPattern)
	}
}

// TestAcceptSuggestion_NameTaken: a name already used by a bill is a refusal the owner
// can retry, and nothing is half-created.
func TestAcceptSuggestion_NameTaken(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := day(2026, time.November, 8)
	acc := seedAccount(t, ctx, client, "Everyday", account.ClassAsset, account.TypeEveryday)
	seedMonthlyRun(t, ctx, client, acc, "zylo", recentDay(today, 3), 4, -15.99, "ZYLO STREAMING")
	seedBill(t, ctx, client, "Streaming", 30, recurringbill.CadenceAnnual, today)
	if _, err := ScanRecurrences(ctx, client, today); err != nil {
		t.Fatalf("scan: %v", err)
	}
	s := client.RecurrenceSuggestion.Query().OnlyX(ctx)
	if _, err := AcceptSuggestion(ctx, client, s.ID, "Streaming", ""); !errors.Is(err, ErrDeclarationNameTaken) {
		t.Fatalf("accept onto a taken name = %v, want ErrDeclarationNameTaken", err)
	}
	if n := client.BillPayment.Query().CountX(ctx); n != 0 {
		t.Errorf("payments after a refused accept = %d, want 0", n)
	}
	if s = client.RecurrenceSuggestion.GetX(ctx, s.ID); s.Status != recurrencesuggestion.StatusPending {
		t.Errorf("status after a refused accept = %s, want pending", s.Status)
	}
}
