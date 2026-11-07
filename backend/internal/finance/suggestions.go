package finance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/billpayment"
	"github.com/alifyandra/portfolio-site/backend/ent/incomereceipt"
	"github.com/alifyandra/portfolio-site/backend/ent/recurrencesuggestion"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringbill"
	"github.com/alifyandra/portfolio-site/backend/ent/recurringincome"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
)

// Recurrence Suggestions (ADR 0019). A deterministic arithmetic pass over the posted
// ledger that finds runs of transactions with the same counterparty, a near-constant
// amount and a near-constant interval fitting one supported cadence, and stores each as a
// suggestion with the transactions attached as evidence. It never creates a declaration:
// the owner accepts (AcceptSuggestion creates the Recurring Bill or Recurring Income and
// links the evidence as its history) or dismisses (remembered by fingerprint, so the same
// run is not offered again). No LLM and no categorisation: recurrence is dates and
// amounts, which is why ADR 0019 keeps it out of the #89 batch.
//
// The rule, in order:
//
//  1. Rows: posted in the last suggestLookbackDays, not zero, and not a move between the
//     owner's own accounts (isInternalTransfer, the spending roll-ups' rule): a transfer
//     to savings is not a bill, and declaring one would project money leaving net worth.
//  2. Group by (account, direction, counterparty). Debits make bill runs, credits make
//     income runs. The counterparty is the parsed merchant when the bank gave one, else
//     the description reduced to its letters, so a varying reference number, date or
//     month name does not split a group (counterpartyKey).
//  3. Within a group, walk back from a recent row (the head) keeping each older row
//     whose magnitude is within suggestAmountTolerance of the head's and whose gap to
//     the previous kept row fits the cadence band; a row too close is skipped as noise
//     (a one-off at the same merchant), a gap too wide ends the run. Bands are tried
//     finest first, so a weekly run is never reported as fortnightly.
//  4. A run needs suggestMinRun rows, and its newest row must be live: no older than one
//     band maximum plus suggestStaleGraceDays, so a cancelled subscription is not offered.
//
// Nothing here rounds money; the API rounds at its DTO.

const (
	// suggestLookbackDays bounds the scan to a constant window, so its cost does not grow
	// with the ledger's age. 800 days holds three annual cycles with slack.
	suggestLookbackDays = 800
	// suggestMinRun is the fewest rows a run needs: two gaps, so "near-constant interval"
	// is a measurement and not one coincidence.
	suggestMinRun = 3
	// suggestAmountTolerance is how far (as a fraction of the head row's magnitude) a row
	// may differ and still belong to the run: the bills' default amount_tolerance_pct, so
	// an accepted bill's own matcher accepts every evidence row.
	suggestAmountTolerance = 0.10
	// suggestStaleGraceDays is how far past one band maximum the newest row may be and the
	// run still count as live: a daily sync that has not landed yet, or a late cycle.
	suggestStaleGraceDays = 7
	// suggestLabelMaxBytes and suggestPatternMaxBytes are the declaration's own caps (the
	// name's MaxLen(120) and the match pattern's MaxLen(200)), both in bytes like Ent.
	suggestLabelMaxBytes   = 120
	suggestPatternMaxBytes = 200
	// suggestPatternMinLetters keeps a match pattern from being one or two letters, which
	// would match half the ledger.
	suggestPatternMinLetters = 3
)

// cadenceBand is the range of day gaps between consecutive rows that fits one cadence.
// Each band holds the cadence's real spread (28 to 31 days for a month) plus a few days
// either way for a payment moved off a weekend or a holiday. The bands do not overlap.
type cadenceBand struct {
	cadence  recurringbill.Cadence
	min, max int
}

var cadenceBands = []cadenceBand{
	{recurringbill.CadenceWeekly, 6, 8},
	{recurringbill.CadenceFortnightly, 12, 16},
	{recurringbill.CadenceMonthly, 25, 35},
	{recurringbill.CadenceQuarterly, 84, 98},
	{recurringbill.CadenceAnnual, 358, 372},
}

// genericWords are the bank's own vocabulary, which a match pattern must not START on:
// "direct credit" or "transfer from" would match every row of that kind, not one
// counterparty. A pattern may still contain them after a distinctive word.
var genericWords = map[string]bool{
	"direct": true, "debit": true, "credit": true, "transfer": true, "transfers": true,
	"from": true, "to": true, "payment": true, "payments": true, "pay": true, "card": true,
	"value": true, "date": true, "netbank": true, "commbank": true, "app": true, "ref": true,
	"reference": true, "deposit": true, "osko": true, "bpay": true, "eftpos": true,
	"visa": true, "mastercard": true, "purchase": true, "online": true, "the": true,
	"and": true, "for": true, "aus": true, "au": true, "pty": true, "ltd": true, "fast": true,
	"received": true, "sent": true, "via": true, "bank": true, "account": true, "acct": true,
	"recurring": true, "periodic": true, "standing": true, "order": true,
}

// monthWords are dropped from a description's counterparty key: a narrative that names
// the month it pays for ("RENT MARCH") is the same counterparty every month.
var monthWords = map[string]bool{
	"jan": true, "feb": true, "mar": true, "apr": true, "may": true, "jun": true, "jul": true,
	"aug": true, "sep": true, "sept": true, "oct": true, "nov": true, "dec": true,
	"january": true, "february": true, "march": true, "april": true, "june": true,
	"july": true, "august": true, "september": true, "october": true, "november": true,
	"december": true,
}

// detectedRun is one run the pass found, before it is stored.
type detectedRun struct {
	kind           recurrencesuggestion.Kind
	accountID      int
	counterparty   string
	cadence        recurringbill.Cadence
	evidence       []*ent.Transaction // oldest first
	expectedAmount float64
	anchor         time.Time
	label          string
	matchPattern   string
}

// fingerprint is the identity of "the same run" across scans: what it pays (kind), from
// where (account), to or from whom (counterparty), and how often (cadence). The amount
// and the evidence are deliberately not part of it, so a repriced subscription or one
// more cycle refreshes the pending suggestion, and a dismissal survives both.
func (r detectedRun) fingerprint() string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%s\x00%s", r.kind, r.accountID, r.counterparty, r.cadence))
	return hex.EncodeToString(sum[:])
}

// normalizeCounterparty reduces text to its lowercase letter words, dropping month names
// and single letters, so the parts of a narrative that vary cycle to cycle (reference
// numbers, dates, amounts) do not split one counterparty into many.
func normalizeCounterparty(s string) string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) })
	kept := words[:0]
	for _, w := range words {
		if utf8.RuneCountInString(w) < 2 || monthWords[w] {
			continue
		}
		kept = append(kept, w)
	}
	return truncateBytes(strings.Join(kept, " "), suggestPatternMaxBytes)
}

// counterpartyKey is the grouping key: the merchant when the bank parsed one (prefixed so
// a merchant and a description that happen to read the same stay apart), else the
// description. Empty when neither has a letter in it.
func counterpartyKey(t *ent.Transaction) string {
	if m := normalizeCounterparty(t.Merchant); m != "" {
		return "m:" + m
	}
	if d := normalizeCounterparty(t.Description); d != "" {
		return "d:" + d
	}
	return ""
}

// truncateBytes cuts s to at most n bytes without splitting a UTF-8 rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

func nearAmount(a, ref float64) bool {
	return math.Abs(a-ref) <= ref*suggestAmountTolerance
}

// detectRuns is the pure half of the pass: rows in, runs out, in a fixed order (account,
// kind, counterparty) so two scans of one ledger agree. Every row's account edge must be
// loaded. own4 is the owner's account last-4 set for the internal-transfer rule.
func detectRuns(rows []*ent.Transaction, own4 map[string]bool, today time.Time) []detectedRun {
	type groupKey struct {
		accountID    int
		kind         recurrencesuggestion.Kind
		counterparty string
	}
	groups := map[groupKey][]*ent.Transaction{}
	for _, t := range rows {
		if t.Amount == 0 || isInternalTransfer(t.Description, own4) {
			continue
		}
		cp := counterpartyKey(t)
		if cp == "" {
			continue
		}
		k := groupKey{counterparty: cp, kind: recurrencesuggestion.KindIncome}
		if t.Amount < 0 {
			k.kind = recurrencesuggestion.KindBill
		}
		if a := t.Edges.Account; a != nil {
			k.accountID = a.ID
		}
		groups[k] = append(groups[k], t)
	}

	keys := make([]groupKey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.accountID != b.accountID {
			return a.accountID < b.accountID
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		return a.counterparty < b.counterparty
	})

	var runs []detectedRun
	for _, k := range keys {
		txns := groups[k]
		sort.SliceStable(txns, func(i, j int) bool {
			di, dj := normalizeDate(txns[i].PostedDate), normalizeDate(txns[j].PostedDate)
			if !di.Equal(dj) {
				return di.Before(dj)
			}
			return txns[i].ID < txns[j].ID
		})
		chain, band, ok := findRun(txns, today)
		if !ok {
			continue
		}
		run := buildRun(chain, band.cadence)
		run.kind, run.accountID, run.counterparty = k.kind, k.accountID, k.counterparty
		runs = append(runs, run)
	}
	return runs
}

// findRun tries each band finest first and, within a band, each live head newest first,
// returning the first chain long enough to count. txns is one group, oldest first.
func findRun(txns []*ent.Transaction, today time.Time) ([]*ent.Transaction, cadenceBand, bool) {
	for _, band := range cadenceBands {
		for h := len(txns) - 1; h >= 0; h-- {
			if daysBetween(txns[h].PostedDate, today) > band.max+suggestStaleGraceDays {
				break // every older head is staler still
			}
			if chain := chainFrom(txns, h, band); len(chain) >= suggestMinRun {
				return chain, band, true
			}
		}
	}
	return nil, cadenceBand{}, false
}

// chainFrom walks back from txns[h], keeping each older row near the head's amount whose
// gap to the last kept row fits the band. Returned oldest first.
func chainFrom(txns []*ent.Transaction, h int, band cadenceBand) []*ent.Transaction {
	head := txns[h]
	ref := math.Abs(head.Amount)
	chain := []*ent.Transaction{head}
	last := head.PostedDate
	for i := h - 1; i >= 0; i-- {
		t := txns[i]
		if !nearAmount(math.Abs(t.Amount), ref) {
			continue
		}
		gap := daysBetween(t.PostedDate, last)
		if gap < band.min {
			continue
		}
		if gap > band.max {
			break
		}
		chain = append(chain, t)
		last = t.PostedDate
	}
	slices.Reverse(chain)
	return chain
}

// buildRun derives what a suggestion stores from its chain: the median magnitude, the
// best-fitting grid, and a label and match pattern.
func buildRun(chain []*ent.Transaction, c recurringbill.Cadence) detectedRun {
	amounts := make([]float64, 0, len(chain))
	for _, t := range chain {
		amounts = append(amounts, math.Abs(t.Amount))
	}
	slices.Sort(amounts)
	median := amounts[len(amounts)/2]
	if len(amounts)%2 == 0 {
		median = (amounts[len(amounts)/2-1] + amounts[len(amounts)/2]) / 2
	}
	label, pattern := describeRun(chain)
	return detectedRun{
		cadence:        c,
		evidence:       chain,
		expectedAmount: median,
		anchor:         bestAnchor(chain, c),
		label:          label,
		matchPattern:   pattern,
	}
}

// bestAnchor picks the cadence grid that fits the evidence best and places it on the
// oldest row's cycle. Each row's date is tried as a grid; the one with the smallest total
// distance from every row to its nearest pay date wins (ties keep the most recent row's
// grid), so one payment moved off a weekend does not shift the whole grid. Placing the
// anchor at the oldest cycle matters because there is no cycle before an anchor: every
// evidence row must settle a cycle on or after it.
//
// A month-stepped grid anchored on the 29th to 31st is clamped in short months, and an
// anchor stored on a clamped date would step from the clamped day instead (30 April
// steps to 30 May, not 31 May). So the anchor walks back to the nearest cycle that kept
// the grid's own day.
func bestAnchor(chain []*ent.Transaction, c recurringbill.Cadence) time.Time {
	var best time.Time
	bestCost := -1
	for i := len(chain) - 1; i >= 0; i-- {
		cand := normalizeDate(chain[i].PostedDate)
		cost := 0
		for _, t := range chain {
			d := normalizeDate(t.PostedDate)
			cost += absInt(daysBetween(nearestOccurrence(cand, c, d), d))
		}
		if bestCost < 0 || cost < bestCost {
			best, bestCost = cand, cost
		}
	}
	oldest := normalizeDate(chain[0].PostedDate)
	n := dueOrdinal(best, c, nearestOccurrence(best, c, oldest))
	if _, months := cadenceStep(c); months > 0 {
		for i := 0; i < 12 && occurrenceAt(best, c, n).Day() != best.Day(); i++ {
			n--
		}
	}
	return occurrenceAt(best, c, n)
}

// describeRun suggests a label and a match pattern. When every row carries the same
// merchant, that is both. Otherwise the pattern starts at the first distinctive word that
// every row's description contains and extends over the following words while they stay
// distinctive and common, so "DIRECT CREDIT 012345 ACME WIDGETS" gives "ACME WIDGETS"
// rather than "DIRECT CREDIT". With no such word the pattern is empty (the declaration is
// then reconciled by hand) and the label falls back to the counterparty's words.
func describeRun(chain []*ent.Transaction) (label, pattern string) {
	newest := chain[len(chain)-1]
	if m := strings.TrimSpace(newest.Merchant); m != "" {
		shared := true
		for _, t := range chain {
			if !strings.EqualFold(strings.TrimSpace(t.Merchant), m) {
				shared = false
				break
			}
		}
		if shared {
			return truncateBytes(m, suggestLabelMaxBytes), truncateBytes(m, suggestPatternMaxBytes)
		}
	}

	lowers := make([]string, len(chain))
	for i, t := range chain {
		lowers[i] = strings.ToLower(t.Description)
	}
	src := newest.Description
	lower := strings.ToLower(src)
	if len(lower) != len(src) {
		src = lower // a case change altered byte offsets; fall back to the lowercase text
	}
	common := func(s string) bool {
		for _, l := range lowers {
			if !strings.Contains(l, s) {
				return false
			}
		}
		return true
	}
	spans := wordSpans(lower)
	for i, sp := range spans {
		if !distinctiveWord(lower[sp[0]:sp[1]]) || !common(lower[sp[0]:sp[1]]) {
			continue
		}
		end := sp[1]
		for _, next := range spans[i+1:] {
			if !distinctiveWord(lower[next[0]:next[1]]) || !common(lower[sp[0]:next[1]]) {
				break
			}
			end = next[1]
		}
		p := strings.TrimFunc(src[sp[0]:end], func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
		if letterCount(p) >= suggestPatternMinLetters {
			return truncateBytes(p, suggestLabelMaxBytes), truncateBytes(p, suggestPatternMaxBytes)
		}
	}
	key := strings.TrimPrefix(strings.TrimPrefix(counterpartyKey(newest), "m:"), "d:")
	return truncateBytes(key, suggestLabelMaxBytes), ""
}

// wordSpans returns the [start, end) byte offsets of each whitespace-separated word.
func wordSpans(s string) [][2]int {
	var out [][2]int
	start := -1
	for i, r := range s {
		if unicode.IsSpace(r) {
			if start >= 0 {
				out = append(out, [2]int{start, i})
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, [2]int{start, len(s)})
	}
	return out
}

// distinctiveWord reports whether a word can carry a match pattern: no digits (those
// vary cycle to cycle), at least suggestPatternMinLetters letters, and neither the bank's
// own vocabulary nor a month name.
func distinctiveWord(w string) bool {
	if strings.ContainsFunc(w, unicode.IsDigit) {
		return false
	}
	core := strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) })
	return letterCount(core) >= suggestPatternMinLetters && !genericWords[core] && !monthWords[core]
}

func letterCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// --- persistence ---

// ScanSummary reports what one scan did, so the "scan" button can say whether anything
// new turned up.
type ScanSummary struct {
	RowsScanned int
	RunsFound   int
	Created     int
	Refreshed   int
	// Declared counts runs left alone because a declaration already covers them: a
	// row is already linked as a payment or receipt, a declaration's match pattern finds
	// the newest row, or the suggestion was accepted earlier.
	Declared int
	// Dismissed counts runs the owner dismissed before; they are not offered again.
	Dismissed int
	// Dropped counts pending suggestions removed because their run is no longer found or
	// is now declared.
	Dropped int
}

// ScanRecurrences runs the detection pass over the last suggestLookbackDays of the posted
// ledger and reconciles the stored suggestions with what it found, in one transaction:
// a new run becomes a pending suggestion, a pending one is refreshed with the current
// amount, grid and evidence, a dismissed or accepted one is left exactly as the owner
// left it, and a pending one whose run is gone is dropped. It never creates or edits a
// declaration.
func ScanRecurrences(ctx context.Context, client *ent.Client) (ScanSummary, error) {
	sum := ScanSummary{}
	today := localToday()
	from := today.AddDate(0, 0, -suggestLookbackDays)
	rows, err := client.Transaction.Query().
		Where(transaction.PostedDateGTE(from)).
		WithAccount().
		Order(ent.Asc(transaction.FieldPostedDate), ent.Asc(transaction.FieldID)).
		All(ctx)
	if err != nil {
		return sum, err
	}
	sum.RowsScanned = len(rows)
	own4, err := loadOwnLast4(ctx, client)
	if err != nil {
		return sum, err
	}
	runs := detectRuns(rows, own4, today)
	sum.RunsFound = len(runs)

	covered, err := newCoverage(ctx, client, from)
	if err != nil {
		return sum, err
	}

	tx, err := client.Tx(ctx)
	if err != nil {
		return sum, err
	}
	existing, err := tx.RecurrenceSuggestion.Query().All(ctx)
	if err != nil {
		return sum, rollback(tx, err)
	}
	byFP := make(map[string]*ent.RecurrenceSuggestion, len(existing))
	for _, s := range existing {
		byFP[s.Fingerprint] = s
	}
	seen := make(map[string]bool, len(runs))
	for _, run := range runs {
		fp := run.fingerprint()
		seen[fp] = true
		cur := byFP[fp]
		switch {
		case cur != nil && cur.Status == recurrencesuggestion.StatusDismissed:
			sum.Dismissed++
		case cur != nil && cur.Status == recurrencesuggestion.StatusAccepted:
			sum.Declared++
		case covered.covers(run):
			sum.Declared++
			if cur != nil {
				if err := dropSuggestion(ctx, tx, cur); err != nil {
					return sum, rollback(tx, err)
				}
				sum.Dropped++
			}
		case cur != nil:
			if err := tx.RecurrenceSuggestion.UpdateOne(cur).
				SetLabel(run.label).
				SetMatchPattern(run.matchPattern).
				SetExpectedAmount(run.expectedAmount).
				SetAnchorDate(run.anchor).
				ClearEvidence().
				AddEvidence(run.evidence...).
				Exec(ctx); err != nil {
				return sum, rollback(tx, err)
			}
			sum.Refreshed++
		default:
			create := tx.RecurrenceSuggestion.Create().
				SetFingerprint(fp).
				SetKind(run.kind).
				SetCounterparty(run.counterparty).
				SetLabel(run.label).
				SetMatchPattern(run.matchPattern).
				SetCadence(recurrencesuggestion.Cadence(run.cadence)).
				SetExpectedAmount(run.expectedAmount).
				SetAnchorDate(run.anchor).
				AddEvidence(run.evidence...)
			if run.accountID != 0 {
				create.SetAccountID(run.accountID)
			}
			if err := create.Exec(ctx); err != nil {
				return sum, rollback(tx, err)
			}
			sum.Created++
		}
	}
	for _, s := range existing {
		if s.Status == recurrencesuggestion.StatusPending && !seen[s.Fingerprint] {
			if err := dropSuggestion(ctx, tx, s); err != nil {
				return sum, rollback(tx, err)
			}
			sum.Dropped++
		}
	}
	if err := tx.Commit(); err != nil {
		return sum, err
	}
	return sum, nil
}

// dropSuggestion removes a pending suggestion and its evidence links. Only pending rows
// are ever dropped: they are derived, and the next scan would rebuild them.
func dropSuggestion(ctx context.Context, tx *ent.Tx, s *ent.RecurrenceSuggestion) error {
	if err := tx.RecurrenceSuggestion.UpdateOne(s).ClearEvidence().Exec(ctx); err != nil {
		return err
	}
	return tx.RecurrenceSuggestion.DeleteOne(s).Exec(ctx)
}

// coverage answers "is this run already declared?" without a query per run.
type coverage struct {
	linked map[int]bool // transaction ids already a BillPayment or IncomeReceipt
	bills  []billMatcher
	income []incomeMatcher
}

// newCoverage loads which rows since from are already linked, and every declaration's
// match pattern, whatever its status: a run an ended bill's pattern still finds is a
// declaration question for the owner, not a new suggestion.
func newCoverage(ctx context.Context, client *ent.Client, from time.Time) (*coverage, error) {
	c := &coverage{linked: map[int]bool{}}
	paid, err := client.BillPayment.Query().
		Where(billpayment.HasTransactionWith(transaction.PostedDateGTE(from))).
		QueryTransaction().
		IDs(ctx)
	if err != nil {
		return nil, err
	}
	received, err := client.IncomeReceipt.Query().
		Where(incomereceipt.HasTransactionWith(transaction.PostedDateGTE(from))).
		QueryTransaction().
		IDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, id := range append(paid, received...) {
		c.linked[id] = true
	}
	bills, err := client.RecurringBill.Query().
		Where(recurringbill.MatchPatternNEQ("")).
		WithAccount().
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range bills {
		c.bills = append(c.bills, newBillMatcher(b))
	}
	incomes, err := client.RecurringIncome.Query().
		Where(recurringincome.MatchPatternNEQ("")).
		WithAccount().
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, inc := range incomes {
		c.income = append(c.income, newIncomeMatcher(inc))
	}
	return c, nil
}

// covers reports whether a declaration already accounts for the run: any evidence row
// already linked, or a declaration of the same kind whose pattern and account scope find
// the run's newest row.
func (c *coverage) covers(run detectedRun) bool {
	for _, t := range run.evidence {
		if c.linked[t.ID] {
			return true
		}
	}
	newest := newCandidate(run.evidence[len(run.evidence)-1])
	if run.kind == recurrencesuggestion.KindIncome {
		for _, m := range c.income {
			if patternFinds(m.needle, m.scopeID, newest) {
				return true
			}
		}
		return false
	}
	for _, m := range c.bills {
		if patternFinds(m.needle, m.scopeID, newest) {
			return true
		}
	}
	return false
}

// patternFinds is the pattern and account-scope half of a declaration's match rule, with
// no amount or date check: a declaration whose pattern finds the run's newest row
// already speaks for that counterparty, whatever its amount or grid now says.
func patternFinds(needle string, scopeID int, c candidate) bool {
	if needle == "" {
		return false
	}
	if !strings.Contains(c.descLower, needle) && !strings.Contains(c.merchLower, needle) {
		return false
	}
	return scopeID == 0 || scopeID == c.accountID
}

// --- read ---

// SuggestionEvidence is one evidence row as the owner reviews it.
type SuggestionEvidence struct {
	TransactionID int
	PostedDate    time.Time
	Amount        float64
	Description   string
}

// SuggestionView is one stored suggestion with its evidence (newest first) and the next
// date its grid expects, so the owner can judge it before accepting.
type SuggestionView struct {
	ID             int
	Kind           string
	Label          string
	MatchPattern   string
	Cadence        string
	ExpectedAmount float64
	AnchorDate     time.Time
	NextExpected   time.Time
	Status         string
	DecidedAt      *time.Time
	AccountID      int
	AccountName    string
	BillID         int
	IncomeID       int
	Evidence       []SuggestionEvidence
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ListSuggestions returns the stored suggestions with the given status ("pending",
// "accepted", "dismissed", or "" / "all"), ordered by label.
func ListSuggestions(ctx context.Context, client *ent.Client, status string) ([]SuggestionView, error) {
	q := client.RecurrenceSuggestion.Query().
		WithAccount().
		WithBill().
		WithIncome().
		WithEvidence(func(tq *ent.TransactionQuery) {
			tq.Order(ent.Desc(transaction.FieldPostedDate), ent.Desc(transaction.FieldID))
		})
	switch s := strings.ToLower(strings.TrimSpace(status)); s {
	case "", "all":
	case string(recurrencesuggestion.StatusPending), string(recurrencesuggestion.StatusAccepted), string(recurrencesuggestion.StatusDismissed):
		q = q.Where(recurrencesuggestion.StatusEQ(recurrencesuggestion.Status(s)))
	default:
		return nil, fmt.Errorf("finance: invalid suggestion status %q (want pending, accepted, dismissed or all)", status)
	}
	rows, err := q.Order(ent.Asc(recurrencesuggestion.FieldLabel), ent.Asc(recurrencesuggestion.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	today := localToday()
	out := make([]SuggestionView, 0, len(rows))
	for _, s := range rows {
		c := recurringbill.Cadence(s.Cadence)
		v := SuggestionView{
			ID:             s.ID,
			Kind:           string(s.Kind),
			Label:          s.Label,
			MatchPattern:   s.MatchPattern,
			Cadence:        string(s.Cadence),
			ExpectedAmount: s.ExpectedAmount,
			AnchorDate:     normalizeDate(s.AnchorDate),
			NextExpected:   nextDue(s.AnchorDate, c, today),
			Status:         string(s.Status),
			DecidedAt:      s.DecidedAt,
			CreatedAt:      s.CreatedAt,
			UpdatedAt:      s.UpdatedAt,
		}
		if a := s.Edges.Account; a != nil {
			v.AccountID, v.AccountName = a.ID, a.Name
		}
		if b := s.Edges.Bill; b != nil {
			v.BillID = b.ID
		}
		if inc := s.Edges.Income; inc != nil {
			v.IncomeID = inc.ID
		}
		for _, t := range s.Edges.Evidence {
			v.Evidence = append(v.Evidence, SuggestionEvidence{
				TransactionID: t.ID,
				PostedDate:    normalizeDate(t.PostedDate),
				Amount:        t.Amount,
				Description:   t.Description,
			})
		}
		out = append(out, v)
	}
	return out, nil
}

// --- decisions ---

// ErrSuggestionDecided is returned when accepting or dismissing a suggestion the owner
// already accepted or dismissed. The API maps it to a 409.
var ErrSuggestionDecided = errors.New("finance: that suggestion has already been accepted or dismissed")

// ErrDeclarationNameTaken is returned when accepting would create a bill or income whose
// name is already used. The API maps it to a 409 so the owner can pick another name.
var ErrDeclarationNameTaken = errors.New("finance: a declaration with that name already exists; accept with a different name")

// AcceptResult reports what accepting created.
type AcceptResult struct {
	Kind     string
	BillID   int
	IncomeID int
	// Linked counts evidence rows linked as the declaration's payments or receipts;
	// Skipped counts rows left unlinked because something else claimed them since the
	// scan or they do not settle a cycle the rules allow.
	Linked  int
	Skipped int
}

// AcceptSuggestion turns a pending suggestion into the owner's declaration, in one
// transaction: it creates the Recurring Bill or Recurring Income from the suggestion
// (name defaults to the label; cadence, anchor, amount, account and match pattern as
// suggested), links each evidence row to its cycle as method auto, and marks the
// suggestion accepted with a link to what it created.
//
// Each evidence row still has to pass the rules a hand link would: one row settles one
// cycle, a row already linked anywhere is skipped, and an income receipt keeps to the
// early-pay bound (cyclePayable). Skipped rows are counted, not errors.
func AcceptSuggestion(ctx context.Context, client *ent.Client, id int, name string) (AcceptResult, error) {
	res := AcceptResult{}
	tx, err := client.Tx(ctx)
	if err != nil {
		return res, err
	}
	s, err := tx.RecurrenceSuggestion.Query().
		Where(recurrencesuggestion.IDEQ(id)).
		WithAccount().
		WithEvidence(func(tq *ent.TransactionQuery) {
			tq.Order(ent.Asc(transaction.FieldPostedDate), ent.Asc(transaction.FieldID))
		}).
		Only(ctx)
	if err != nil {
		return res, rollback(tx, err)
	}
	if s.Status != recurrencesuggestion.StatusPending {
		return res, rollback(tx, ErrSuggestionDecided)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = s.Label
	}
	res.Kind = string(s.Kind)
	c := recurringbill.Cadence(s.Cadence)
	anchor := normalizeDate(s.AnchorDate)
	accept := tx.RecurrenceSuggestion.UpdateOne(s).
		SetStatus(recurrencesuggestion.StatusAccepted).
		SetDecidedAt(time.Now())

	switch s.Kind {
	case recurrencesuggestion.KindBill:
		create := tx.RecurringBill.Create().
			SetName(name).
			SetExpectedAmount(s.ExpectedAmount).
			SetCadence(c).
			SetAnchorDate(anchor).
			SetMatchPattern(s.MatchPattern)
		if a := s.Edges.Account; a != nil {
			create.SetAccountID(a.ID)
		}
		b, err := create.Save(ctx)
		if ent.IsConstraintError(err) {
			return res, rollback(tx, ErrDeclarationNameTaken)
		}
		if err != nil {
			return res, rollback(tx, err)
		}
		res.BillID = b.ID
		accept.SetBillID(b.ID)
		taken := map[int64]bool{}
		for _, t := range s.Edges.Evidence {
			occ := nearestOccurrence(anchor, c, t.PostedDate)
			used, err := tx.BillPayment.Query().Where(billpayment.HasTransactionWith(transaction.IDEQ(t.ID))).Exist(ctx)
			if err != nil {
				return res, rollback(tx, err)
			}
			if t.Amount >= 0 || occ.Before(anchor) || taken[occ.Unix()] || used {
				res.Skipped++
				continue
			}
			if err := tx.BillPayment.Create().
				SetBillID(b.ID).SetTransactionID(t.ID).SetOccurrenceDate(occ).
				SetMethod(billpayment.MethodAuto).
				Exec(ctx); err != nil {
				return res, rollback(tx, err)
			}
			taken[occ.Unix()] = true
			res.Linked++
		}

	case recurrencesuggestion.KindIncome:
		create := tx.RecurringIncome.Create().
			SetName(name).
			SetExpectedAmount(s.ExpectedAmount).
			SetCadence(recurringincome.Cadence(s.Cadence)).
			SetAnchorDate(anchor).
			SetMatchPattern(s.MatchPattern)
		if a := s.Edges.Account; a != nil {
			create.SetAccountID(a.ID)
		}
		inc, err := create.Save(ctx)
		if ent.IsConstraintError(err) {
			return res, rollback(tx, ErrDeclarationNameTaken)
		}
		if err != nil {
			return res, rollback(tx, err)
		}
		res.IncomeID = inc.ID
		accept.SetIncomeID(inc.ID)
		taken := map[int64]bool{}
		for _, t := range s.Edges.Evidence {
			occ := nearestOccurrence(anchor, c, t.PostedDate)
			used, err := tx.IncomeReceipt.Query().Where(incomereceipt.HasTransactionWith(transaction.IDEQ(t.ID))).Exist(ctx)
			if err != nil {
				return res, rollback(tx, err)
			}
			if t.Amount <= 0 || !cyclePayable(inc, occ, t.PostedDate) || taken[occ.Unix()] || used {
				res.Skipped++
				continue
			}
			if err := tx.IncomeReceipt.Create().
				SetIncomeID(inc.ID).SetTransactionID(t.ID).SetOccurrenceDate(occ).
				SetMethod(incomereceipt.MethodAuto).
				Exec(ctx); err != nil {
				return res, rollback(tx, err)
			}
			taken[occ.Unix()] = true
			res.Linked++
		}
	}

	if err := accept.Exec(ctx); err != nil {
		return res, rollback(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}

// DismissSuggestion records the owner's "no": the suggestion is kept as dismissed, so
// the next scan recognises its fingerprint and does not offer the run again. Only a
// pending suggestion can be dismissed (ErrSuggestionDecided otherwise).
func DismissSuggestion(ctx context.Context, client *ent.Client, id int) error {
	err := client.RecurrenceSuggestion.UpdateOneID(id).
		Where(recurrencesuggestion.StatusEQ(recurrencesuggestion.StatusPending)).
		SetStatus(recurrencesuggestion.StatusDismissed).
		SetDecidedAt(time.Now()).
		Exec(ctx)
	if !ent.IsNotFound(err) {
		return err
	}
	exists, xerr := client.RecurrenceSuggestion.Query().Where(recurrencesuggestion.IDEQ(id)).Exist(ctx)
	if xerr != nil {
		return xerr
	}
	if exists {
		return ErrSuggestionDecided
	}
	return err
}
