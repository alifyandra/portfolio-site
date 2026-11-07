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
	"github.com/alifyandra/portfolio-site/backend/ent/predicate"
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
//     the description reduced to its distinctive words, so a varying reference number,
//     date, month name or trailing city does not split a group and the bank's own words
//     do not join two (counterpartyKey). A merchant-less row whose name (the words before
//     its first number) is a merchant parsed on the same account joins that merchant's
//     group. A row with nothing distinctive left is not grouped.
//  3. Within a group, walk back from a recent row (the head) keeping each older row
//     whose magnitude is within suggestAmountTolerancePct of the run's median and whose
//     gap to the previous kept row fits the cadence band (settledChain walks against the
//     head's amount first, then re-walks against the median until every kept row fits
//     it, the head included); a row too close is skipped as noise
//     (a one-off at the same merchant). A gap of exactly two cycles (twice the band) is
//     one skipped cycle and is kept and counted in gaps; any other gap too wide ends the
//     run. Bands are tried finest first, so a weekly run is never reported as fortnightly.
//  4. A run needs suggestMinRun rows, more on-band intervals than skipped cycles (so a
//     fortnightly run is not a weekly one missing every other week), and its newest row
//     must be live: no older than one band maximum plus suggestStaleGraceDays, so a
//     cancelled subscription is not offered.
//
// Nothing here rounds money; the API rounds at its DTO.

const (
	// suggestLookbackDays bounds the scan to a constant window, so its cost does not grow
	// with the ledger's age. 800 days holds three annual cycles with slack.
	suggestLookbackDays = 800
	// suggestMinRun is the fewest rows a run needs: two gaps, so "near-constant interval"
	// is a measurement and not one coincidence.
	suggestMinRun = 3
	// suggestAmountTolerancePct is how far (as a percentage of the run's median magnitude,
	// which becomes the declaration's expected amount) every evidence row may differ. It is
	// the bills' default amount_tolerance_pct and the income matcher's
	// incomeAmountTolerancePct, computed the same way, so an accepted declaration's own
	// matcher accepts every evidence row on amount.
	suggestAmountTolerancePct = 10
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
	"and": true, "for": true, "of": true, "aus": true, "au": true, "pty": true, "ltd": true, "fast": true,
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
	gaps           int                // cycles the run skipped (see chainFrom)
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

// locationWords are the place names a card terminal appends after a payee's name: the
// Australian states and territories, their capitals, and the country codes overseas
// subscriptions carry. They are dropped only from the END of a key and never down to
// nothing, so "ZYLO STREAMING SYDNEY AU" and "ZYLO STREAMING MELBOURNE" are one payee
// while "PERTH MINT" keeps its first word.
var locationWords = map[string]bool{
	"vic": true, "nsw": true, "qld": true, "wa": true, "sa": true, "tas": true, "nt": true,
	"act": true, "melbourne": true, "sydney": true, "brisbane": true, "perth": true,
	"adelaide": true, "hobart": true, "darwin": true, "canberra": true, "australia": true,
	"us": true, "usa": true, "gb": true, "gbr": true, "uk": true, "nz": true, "nzl": true,
	"ie": true, "irl": true, "sg": true, "sgp": true, "nl": true, "nld": true,
}

// normalizeCounterparty reduces text to its lowercase letter words, dropping month names,
// single letters and the bank's own vocabulary (genericWords), so the parts of a narrative
// that vary cycle to cycle (reference numbers, dates, amounts) do not split one
// counterparty into many, and the channel it was paid through ("direct credit",
// "transfer to") does not join two. Trailing locationWords go too. Empty when nothing
// distinctive is left.
func normalizeCounterparty(s string) string {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) })
	kept := words[:0]
	for _, w := range words {
		if utf8.RuneCountInString(w) < 2 || monthWords[w] || genericWords[w] {
			continue
		}
		kept = append(kept, w)
	}
	for len(kept) > 1 && locationWords[kept[len(kept)-1]] {
		kept = kept[:len(kept)-1]
	}
	return truncateBytes(strings.Join(kept, " "), suggestPatternMaxBytes)
}

// nameSegment is the payee's name as a narrative spells it: the normalised words before
// the first token carrying a digit (a store number, a reference), taken from the first
// such segment with a distinctive word left. "KESTREL ENERGY 8812 MELBOURNE" gives
// "kestrel energy" and "DIRECT DEBIT 004411 ZYLO STREAMING REF 4411" gives "zylo
// streaming". It is only used to fold a narrative onto a merchant the bank parsed.
func nameSegment(desc string) string {
	var seg []string
	flush := func() string {
		k := normalizeCounterparty(strings.Join(seg, " "))
		seg = seg[:0]
		return k
	}
	for _, tok := range strings.Fields(desc) {
		if strings.ContainsFunc(tok, unicode.IsDigit) {
			if k := flush(); k != "" {
				return k
			}
			continue
		}
		seg = append(seg, tok)
	}
	return flush()
}

// counterpartyKey is a row's own grouping key: the merchant when the bank parsed one, else
// the description, in one namespace so a payee whose merchant is parsed on some rows and
// not others is still one group when the two read the same. Empty when neither has a
// distinctive word in it, and a row with no key is never grouped: a run of bare "DIRECT
// CREDIT" rows says nothing about who paid. detectRuns may then fold a merchant-less row
// onto a parsed merchant (see there).
func counterpartyKey(t *ent.Transaction) (key string, fromMerchant bool) {
	if m := normalizeCounterparty(t.Merchant); m != "" {
		return m, true
	}
	return normalizeCounterparty(t.Description), false
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
	return math.Abs(a-ref) <= ref*suggestAmountTolerancePct/100
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
	type keyedRow struct {
		t            *ent.Transaction
		k            groupKey
		fromMerchant bool
	}
	// First pass: each row's own key, and the merchant keys the bank parsed per
	// (account, kind). Second pass: a merchant-less row whose name segment IS one of those
	// merchants joins it, so "KESTREL ENERGY 8812 MELBOURNE" with no merchant groups with
	// the rows the bank parsed as "Kestrel Energy". Only an exact name match folds, which
	// keeps "ZYLO INSURANCE 5501" apart from a merchant "Zylo".
	var keyed []keyedRow
	merchants := map[groupKey]bool{}
	for _, t := range rows {
		if t.Amount == 0 || isInternalTransfer(t.Description, own4) {
			continue
		}
		cp, fromMerchant := counterpartyKey(t)
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
		if fromMerchant {
			merchants[k] = true
		}
		keyed = append(keyed, keyedRow{t: t, k: k, fromMerchant: fromMerchant})
	}
	groups := map[groupKey][]*ent.Transaction{}
	for _, r := range keyed {
		if !r.fromMerchant {
			folded := r.k
			folded.counterparty = nameSegment(r.t.Description)
			if folded.counterparty != "" && merchants[folded] {
				r.k = folded
			}
		}
		groups[r.k] = append(groups[r.k], r.t)
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
		chain, gaps, band, ok := findRun(txns, today)
		if !ok {
			continue
		}
		run := buildRun(chain, band.cadence, k.counterparty)
		run.kind, run.accountID, run.gaps = k.kind, k.accountID, gaps
		runs = append(runs, run)
	}
	return runs
}

// findRun tries each band finest first and, within a band, each live head newest first,
// returning the first chain that counts as a run (runShape) and how many cycles it
// skipped. txns is one group, oldest first.
func findRun(txns []*ent.Transaction, today time.Time) ([]*ent.Transaction, int, cadenceBand, bool) {
	for _, band := range cadenceBands {
		for h := len(txns) - 1; h >= 0; h-- {
			if daysBetween(txns[h].PostedDate, today) > band.max+suggestStaleGraceDays {
				break // every older head is staler still
			}
			if chain, gaps, ok := settledChain(txns, h, band); ok {
				return chain, gaps, band, true
			}
		}
	}
	return nil, 0, cadenceBand{}, false
}

// runShape is the rule a chain must meet to be a run: suggestMinRun rows, and more
// on-band intervals than skipped cycles, so the interval is measured and not inferred
// from the gaps.
func runShape(chain []*ent.Transaction, gaps int) bool {
	return len(chain) >= suggestMinRun && len(chain)-1-gaps > gaps
}

// settledChain is the chain from head h whose every row, the head included, is near the
// median the run would declare. The first walk is against the head's magnitude; each
// later one is against the previous chain's median, until a walk's rows all fit its own
// median. A head that is itself an outlier against that median is refused, so an older
// head is tried instead. Bounded, since the reference only settles on real data.
func settledChain(txns []*ent.Transaction, h int, band cadenceBand) ([]*ent.Transaction, int, bool) {
	head := math.Abs(txns[h].Amount)
	ref := head
	for range 4 {
		chain, gaps := chainFrom(txns, h, band, ref)
		if !runShape(chain, gaps) {
			return nil, 0, false
		}
		m := medianMagnitude(chain)
		if !nearAmount(head, m) {
			return nil, 0, false
		}
		fits := true
		for _, t := range chain {
			if !nearAmount(math.Abs(t.Amount), m) {
				fits = false
				break
			}
		}
		if fits {
			return chain, gaps, true
		}
		ref = m
	}
	return nil, 0, false
}

// chainFrom walks back from txns[h], keeping each older row near ref whose gap to the
// last kept row fits the band, or fits it twice over (one skipped cycle, counted in
// gaps). The head is always kept. Returned oldest first.
func chainFrom(txns []*ent.Transaction, h int, band cadenceBand, ref float64) ([]*ent.Transaction, int) {
	head := txns[h]
	chain := []*ent.Transaction{head}
	last := head.PostedDate
	gaps := 0
walk:
	for i := h - 1; i >= 0; i-- {
		t := txns[i]
		if !nearAmount(math.Abs(t.Amount), ref) {
			continue
		}
		switch gap := daysBetween(t.PostedDate, last); {
		case gap < band.min:
			continue
		case gap <= band.max:
		case gap >= 2*band.min && gap <= 2*band.max:
			gaps++
		default:
			break walk
		}
		chain = append(chain, t)
		last = t.PostedDate
	}
	slices.Reverse(chain)
	return chain, gaps
}

// medianMagnitude is the median of the chain's unsigned amounts, the run's expected
// amount. The chain is never empty.
func medianMagnitude(chain []*ent.Transaction) float64 {
	amounts := make([]float64, 0, len(chain))
	for _, t := range chain {
		amounts = append(amounts, math.Abs(t.Amount))
	}
	slices.Sort(amounts)
	if len(amounts)%2 == 0 {
		return (amounts[len(amounts)/2-1] + amounts[len(amounts)/2]) / 2
	}
	return amounts[len(amounts)/2]
}

// buildRun derives what a suggestion stores from its chain: the median magnitude, the
// best-fitting grid, and a label and match pattern.
func buildRun(chain []*ent.Transaction, c recurringbill.Cadence, counterparty string) detectedRun {
	label, pattern := describeRun(chain, counterparty)
	return detectedRun{
		counterparty:   counterparty,
		cadence:        c,
		evidence:       chain,
		expectedAmount: medianMagnitude(chain),
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
func describeRun(chain []*ent.Transaction, counterparty string) (label, pattern string) {
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
	return truncateBytes(counterparty, suggestLabelMaxBytes), ""
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
	// Declared counts runs left alone because a live declaration already covers them
	// (one of its rows is linked to it, or its match pattern finds the newest row; an
	// ended declaration covers only up to its ended_on plus a grace week), or because
	// the suggestion was accepted earlier and its declaration has not ended.
	Declared int
	// Dismissed counts runs the owner dismissed before; they are not offered again.
	Dismissed int
	// Reopened counts accepted suggestions put back to pending because the declaration
	// they created has ended and the run carried on past it.
	Reopened int
	// Dropped counts pending suggestions removed because their run is no longer found or
	// is now declared.
	Dropped int
}

// ScanRecurrences runs the detection pass over the last suggestLookbackDays of the posted
// ledger and reconciles the stored suggestions with what it found, in one transaction:
// a new run becomes a pending suggestion, a pending one is refreshed with the current
// amount, grid and evidence, a dismissed one is left exactly as the owner left it, an
// accepted one too unless its declaration has ended and the run carried on past it (then
// it is reopened as pending), and a pending one whose run is gone or is now covered by a
// live declaration is dropped. It never creates or edits a declaration.
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
	existing, err := tx.RecurrenceSuggestion.Query().
		WithBill(func(q *ent.RecurringBillQuery) { q.Select(recurringbill.FieldID) }).
		WithIncome(func(q *ent.RecurringIncomeQuery) { q.Select(recurringincome.FieldID) }).
		All(ctx)
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
		case cur != nil && cur.Status == recurrencesuggestion.StatusAccepted && !covered.endedUnder(cur, run):
			sum.Declared++
		case covered.covers(run):
			sum.Declared++
			if cur != nil && cur.Status == recurrencesuggestion.StatusPending {
				if err := dropSuggestion(ctx, tx, cur); err != nil {
					return sum, rollback(tx, err)
				}
				sum.Dropped++
			}
		case cur != nil:
			// Pending, or accepted under a declaration that has since ended while the run
			// carried on: refresh it, and reopen the accepted one for the owner.
			update := tx.RecurrenceSuggestion.UpdateOne(cur).
				SetLabel(run.label).
				SetMatchPattern(run.matchPattern).
				SetExpectedAmount(run.expectedAmount).
				SetAnchorDate(run.anchor).
				SetGaps(run.gaps).
				ClearEvidence().
				AddEvidence(run.evidence...)
			if cur.Status == recurrencesuggestion.StatusAccepted {
				update.SetStatus(recurrencesuggestion.StatusPending).ClearDecidedAt().ClearBill().ClearIncome()
				sum.Reopened++
			} else {
				sum.Refreshed++
			}
			if err := update.Exec(ctx); err != nil {
				return sum, rollback(tx, err)
			}
		default:
			if err := insertSuggestion(ctx, tx, fp, run); err != nil {
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

// insertSuggestion stores a new run as a pending suggestion. Two scans can both read
// "nothing stored for this run" and both insert it, so the insert is an upsert on the
// unique fingerprint that changes nothing on conflict; the row that is then stored under
// the fingerprint is refreshed with this run, if it is still pending. A second scan is
// therefore harmless instead of a unique-index 500.
func insertSuggestion(ctx context.Context, tx *ent.Tx, fp string, run detectedRun) error {
	create := tx.RecurrenceSuggestion.Create().
		SetFingerprint(fp).
		SetKind(run.kind).
		SetCounterparty(run.counterparty).
		SetLabel(run.label).
		SetMatchPattern(run.matchPattern).
		SetCadence(recurrencesuggestion.Cadence(run.cadence)).
		SetExpectedAmount(run.expectedAmount).
		SetAnchorDate(run.anchor).
		SetGaps(run.gaps)
	if run.accountID != 0 {
		create.SetAccountID(run.accountID)
	}
	if err := create.OnConflictColumns(recurrencesuggestion.FieldFingerprint).Ignore().Exec(ctx); err != nil {
		return err
	}
	_, err := tx.RecurrenceSuggestion.Update().
		Where(recurrencesuggestion.FingerprintEQ(fp), recurrencesuggestion.StatusEQ(recurrencesuggestion.StatusPending)).
		SetLabel(run.label).
		SetMatchPattern(run.matchPattern).
		SetExpectedAmount(run.expectedAmount).
		SetAnchorDate(run.anchor).
		SetGaps(run.gaps).
		ClearEvidence().
		AddEvidence(run.evidence...).
		Save(ctx)
	return err
}

// dropSuggestion removes a pending suggestion and its evidence links. Only pending rows
// are ever dropped: they are derived, and the next scan would rebuild them. Both writes
// are conditional and affect nothing if a concurrent scan dropped it first.
func dropSuggestion(ctx context.Context, tx *ent.Tx, s *ent.RecurrenceSuggestion) error {
	pending := []predicate.RecurrenceSuggestion{
		recurrencesuggestion.IDEQ(s.ID), recurrencesuggestion.StatusEQ(recurrencesuggestion.StatusPending),
	}
	if _, err := tx.RecurrenceSuggestion.Update().Where(pending...).ClearEvidence().Save(ctx); err != nil {
		return err
	}
	_, err := tx.RecurrenceSuggestion.Delete().Where(pending...).Exec(ctx)
	return err
}

// coverDecl is one declaration as the coverage check sees it. endedOn is set only for an
// ended declaration; a paused one still speaks for its counterparty (it may resume), so it
// covers like an active one.
type coverDecl struct {
	kind    recurrencesuggestion.Kind
	needle  string
	scopeID int
	endedOn *time.Time
}

// liveFor reports whether the declaration still speaks for a run whose newest row posted
// on newest: always, unless it ended more than suggestStaleGraceDays before that row (a
// last charge can land a few days after the declared end). Past that, the run carried on
// without it, and that is a new question for the owner.
func (d coverDecl) liveFor(newest time.Time) bool {
	if d.endedOn == nil {
		return true
	}
	return !normalizeDate(newest).After(normalizeDate(*d.endedOn).AddDate(0, 0, suggestStaleGraceDays))
}

// coverage answers "is this run already declared?" without a query per run.
type coverage struct {
	decls  []coverDecl
	bills  map[int]int   // bill id -> index in decls
	income map[int]int   // income id -> index in decls
	linked map[int][]int // transaction id -> the decls it settles a cycle of
}

func newCoverDecl(kind recurrencesuggestion.Kind, pattern string, acc *ent.Account, ended bool, endedOn *time.Time) coverDecl {
	d := coverDecl{kind: kind, needle: strings.ToLower(strings.TrimSpace(pattern))}
	if acc != nil {
		d.scopeID = acc.ID
	}
	if ended && endedOn != nil {
		d.endedOn = endedOn
	}
	return d
}

// newCoverage loads every declaration (whatever its status, so liveFor can judge an
// ended one) and which rows since from are already linked to which.
func newCoverage(ctx context.Context, client *ent.Client, from time.Time) (*coverage, error) {
	c := &coverage{bills: map[int]int{}, income: map[int]int{}, linked: map[int][]int{}}
	bills, err := client.RecurringBill.Query().WithAccount().All(ctx)
	if err != nil {
		return nil, err
	}
	for _, b := range bills {
		c.bills[b.ID] = len(c.decls)
		c.decls = append(c.decls, newCoverDecl(recurrencesuggestion.KindBill, b.MatchPattern, b.Edges.Account, b.Status == recurringbill.StatusEnded, b.EndedOn))
	}
	incomes, err := client.RecurringIncome.Query().WithAccount().All(ctx)
	if err != nil {
		return nil, err
	}
	for _, inc := range incomes {
		c.income[inc.ID] = len(c.decls)
		c.decls = append(c.decls, newCoverDecl(recurrencesuggestion.KindIncome, inc.MatchPattern, inc.Edges.Account, inc.Status == recurringincome.StatusEnded, inc.EndedOn))
	}
	payments, err := client.BillPayment.Query().
		Where(billpayment.HasTransactionWith(transaction.PostedDateGTE(from))).
		WithBill(func(q *ent.RecurringBillQuery) { q.Select(recurringbill.FieldID) }).
		WithTransaction(func(q *ent.TransactionQuery) { q.Select(transaction.FieldID) }).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range payments {
		if p.Edges.Bill != nil && p.Edges.Transaction != nil {
			c.linked[p.Edges.Transaction.ID] = append(c.linked[p.Edges.Transaction.ID], c.bills[p.Edges.Bill.ID])
		}
	}
	receipts, err := client.IncomeReceipt.Query().
		Where(incomereceipt.HasTransactionWith(transaction.PostedDateGTE(from))).
		WithIncome(func(q *ent.RecurringIncomeQuery) { q.Select(recurringincome.FieldID) }).
		WithTransaction(func(q *ent.TransactionQuery) { q.Select(transaction.FieldID) }).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range receipts {
		if r.Edges.Income != nil && r.Edges.Transaction != nil {
			c.linked[r.Edges.Transaction.ID] = append(c.linked[r.Edges.Transaction.ID], c.income[r.Edges.Income.ID])
		}
	}
	return c, nil
}

// covers reports whether a declaration that is still live for the run's newest row
// (coverDecl.liveFor) already accounts for it: one of its evidence rows is linked to that
// declaration, or the declaration is of the same kind and its pattern and account scope
// find the newest row.
func (c *coverage) covers(run detectedRun) bool {
	newestTxn := run.evidence[len(run.evidence)-1]
	newest := newestTxn.PostedDate
	for _, t := range run.evidence {
		for _, i := range c.linked[t.ID] {
			if c.decls[i].liveFor(newest) {
				return true
			}
		}
	}
	cand := newCandidate(newestTxn)
	for _, d := range c.decls {
		if d.kind == run.kind && d.liveFor(newest) && patternFinds(d.needle, d.scopeID, cand) {
			return true
		}
	}
	return false
}

// endedUnder reports whether an accepted suggestion's own declaration has ended and the
// run carried on past it, which reopens the suggestion. A suggestion whose declaration is
// gone (deleted before deletes reopened suggestions) is left as accepted.
func (c *coverage) endedUnder(s *ent.RecurrenceSuggestion, run detectedRun) bool {
	i, ok := -1, false
	switch {
	case s.Edges.Bill != nil:
		i, ok = c.bills[s.Edges.Bill.ID]
	case s.Edges.Income != nil:
		i, ok = c.income[s.Edges.Income.ID]
	}
	return ok && !c.decls[i].liveFor(run.evidence[len(run.evidence)-1].PostedDate)
}

// patternFinds is the pattern and account-scope half of a declaration's match rule, with
// no amount or date check: a declaration whose pattern finds the run's newest row
// already speaks for that counterparty, whatever its amount or grid now says. The pattern
// is matched on word boundaries (containsToken), like the income matcher.
func patternFinds(needle string, scopeID int, c candidate) bool {
	if needle == "" {
		return false
	}
	if !containsToken(c.descLower, needle) && !containsToken(c.merchLower, needle) {
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
// date its grid expects, so the owner can judge it before accepting. Evidence holds every
// row from GetSuggestion and only the newest suggestListEvidence from ListSuggestions;
// EvidenceCount and EvidenceSince (the oldest row's date) always describe all of it.
type SuggestionView struct {
	ID             int
	Kind           string
	Label          string
	MatchPattern   string
	Cadence        string
	ExpectedAmount float64
	AnchorDate     time.Time
	NextExpected   time.Time
	Gaps           int
	Status         string
	DecidedAt      *time.Time
	AccountID      int
	AccountName    string
	BillID         int
	IncomeID       int
	Evidence       []SuggestionEvidence
	EvidenceCount  int
	EvidenceSince  time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// suggestListEvidence is how many evidence rows (the newest) each suggestion carries in
// the list: enough to recognise the run at a glance. The detail read has the rest.
const suggestListEvidence = 5

// newestFirst orders evidence rows by posted date, then id, both descending.
func newestFirst(a, b *ent.Transaction) int {
	if c := b.PostedDate.Compare(a.PostedDate); c != 0 {
		return c
	}
	return b.ID - a.ID
}

// suggestionsQuery is the read shared by the list and the detail: the suggestion with
// its account and the ids of what accepting it created. Evidence is loaded by the caller.
func suggestionsQuery(client *ent.Client) *ent.RecurrenceSuggestionQuery {
	return client.RecurrenceSuggestion.Query().
		WithAccount().
		WithBill(func(q *ent.RecurringBillQuery) { q.Select(recurringbill.FieldID) }).
		WithIncome(func(q *ent.RecurringIncomeQuery) { q.Select(recurringincome.FieldID) })
}

// ListSuggestions returns the stored suggestions with the given status ("pending",
// "accepted", "dismissed", or "" / "all"), ordered by label, each with its newest
// suggestListEvidence evidence rows. It reads only the id and date of every evidence row
// (to count them and pick the newest), then the full rows of just those picked.
func ListSuggestions(ctx context.Context, client *ent.Client, status string) ([]SuggestionView, error) {
	q := suggestionsQuery(client).
		WithEvidence(func(tq *ent.TransactionQuery) { tq.Select(transaction.FieldPostedDate) })
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
	var pick []int
	for _, s := range rows {
		ev := s.Edges.Evidence
		slices.SortFunc(ev, newestFirst)
		for _, t := range ev[:min(len(ev), suggestListEvidence)] {
			pick = append(pick, t.ID)
		}
	}
	full := map[int]*ent.Transaction{}
	if len(pick) > 0 {
		txns, err := client.Transaction.Query().Where(transaction.IDIn(pick...)).All(ctx)
		if err != nil {
			return nil, err
		}
		for _, t := range txns {
			full[t.ID] = t
		}
	}
	today := localToday()
	out := make([]SuggestionView, 0, len(rows))
	for _, s := range rows {
		ev := s.Edges.Evidence
		shown := make([]*ent.Transaction, 0, suggestListEvidence)
		for _, t := range ev[:min(len(ev), suggestListEvidence)] {
			if f := full[t.ID]; f != nil {
				shown = append(shown, f)
			}
		}
		out = append(out, suggestionView(s, shown, ev, today))
	}
	return out, nil
}

// GetSuggestion returns one stored suggestion with all of its evidence, newest first. A
// missing id is ent's not-found error.
func GetSuggestion(ctx context.Context, client *ent.Client, id int) (SuggestionView, error) {
	s, err := suggestionsQuery(client).
		Where(recurrencesuggestion.IDEQ(id)).
		WithEvidence().
		Only(ctx)
	if err != nil {
		return SuggestionView{}, err
	}
	ev := s.Edges.Evidence
	slices.SortFunc(ev, newestFirst)
	return suggestionView(s, ev, ev, localToday()), nil
}

// suggestionView builds the view from a loaded suggestion: shown are the evidence rows to
// return (full rows, newest first) and all is every evidence row, newest first, for the
// count and the oldest date.
func suggestionView(s *ent.RecurrenceSuggestion, shown, all []*ent.Transaction, today time.Time) SuggestionView {
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
		Gaps:           s.Gaps,
		Status:         string(s.Status),
		DecidedAt:      s.DecidedAt,
		EvidenceCount:  len(all),
		CreatedAt:      s.CreatedAt,
		UpdatedAt:      s.UpdatedAt,
	}
	if len(all) > 0 {
		v.EvidenceSince = normalizeDate(all[len(all)-1].PostedDate)
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
	for _, t := range shown {
		v.Evidence = append(v.Evidence, SuggestionEvidence{
			TransactionID: t.ID,
			PostedDate:    normalizeDate(t.PostedDate),
			Amount:        t.Amount,
			Description:   t.Description,
		})
	}
	return v
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
// transaction: it claims the suggestion (a status write conditional on it still being
// pending, so two concurrent accepts make one declaration and the other gets
// ErrSuggestionDecided), creates the Recurring Bill or Recurring Income from it (name
// defaults to the label; cadence, anchor, amount and account as suggested), links each
// evidence row to its cycle as method auto, and links the suggestion to what it created.
//
// matchPattern is the owner's, taken as given and never copied from the suggestion: a
// declaration's match pattern is owner-typed (CONTEXT.md, Bill Payment) and owner-authored
// fields are never inferred (ADR 0019). The suggested pattern is only shown to the owner,
// who confirms, edits or clears it; empty means the declaration is linked by hand.
//
// Each evidence row still has to pass the rules a hand link would: one row settles one
// cycle, a row already linked anywhere is skipped, and an income receipt keeps to the
// early-pay bound (cyclePayable). Skipped rows are counted, not errors.
func AcceptSuggestion(ctx context.Context, client *ent.Client, id int, name, matchPattern string) (AcceptResult, error) {
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
	// Claim it before creating anything. The read above is not a lock: under READ
	// COMMITTED a second accept can read pending too. This write is conditional on the
	// row still being pending, so the second one waits on the first's row lock, then
	// re-checks the condition against the committed row and claims nothing.
	claimed, err := tx.RecurrenceSuggestion.Update().
		Where(recurrencesuggestion.IDEQ(id), recurrencesuggestion.StatusEQ(recurrencesuggestion.StatusPending)).
		SetStatus(recurrencesuggestion.StatusAccepted).
		SetDecidedAt(time.Now()).
		Save(ctx)
	if err != nil {
		return res, rollback(tx, err)
	}
	if claimed == 0 {
		return res, rollback(tx, ErrSuggestionDecided)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = s.Label
	}
	matchPattern = strings.TrimSpace(matchPattern)
	res.Kind = string(s.Kind)
	c := recurringbill.Cadence(s.Cadence)
	anchor := normalizeDate(s.AnchorDate)
	accept := tx.RecurrenceSuggestion.UpdateOneID(id)
	evidenceIDs := make([]int, 0, len(s.Edges.Evidence))
	for _, t := range s.Edges.Evidence {
		evidenceIDs = append(evidenceIDs, t.ID)
	}

	switch s.Kind {
	case recurrencesuggestion.KindBill:
		create := tx.RecurringBill.Create().
			SetName(name).
			SetExpectedAmount(s.ExpectedAmount).
			SetCadence(c).
			SetAnchorDate(anchor).
			SetMatchPattern(matchPattern)
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
		used, err := idSet(tx.BillPayment.Query().
			Where(billpayment.HasTransactionWith(transaction.IDIn(evidenceIDs...))).
			QueryTransaction().IDs(ctx))
		if err != nil {
			return res, rollback(tx, err)
		}
		taken := map[int64]bool{}
		var links []*ent.BillPaymentCreate
		for _, t := range s.Edges.Evidence {
			occ := nearestOccurrence(anchor, c, t.PostedDate)
			if t.Amount >= 0 || occ.Before(anchor) || taken[occ.Unix()] || used[t.ID] {
				res.Skipped++
				continue
			}
			links = append(links, tx.BillPayment.Create().
				SetBillID(b.ID).SetTransactionID(t.ID).SetOccurrenceDate(occ).
				SetMethod(billpayment.MethodAuto))
			taken[occ.Unix()] = true
		}
		if len(links) > 0 {
			if err := tx.BillPayment.CreateBulk(links...).Exec(ctx); err != nil {
				return res, rollback(tx, err)
			}
		}
		res.Linked = len(links)

	case recurrencesuggestion.KindIncome:
		create := tx.RecurringIncome.Create().
			SetName(name).
			SetExpectedAmount(s.ExpectedAmount).
			SetCadence(recurringincome.Cadence(s.Cadence)).
			SetAnchorDate(anchor).
			SetMatchPattern(matchPattern)
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
		used, err := idSet(tx.IncomeReceipt.Query().
			Where(incomereceipt.HasTransactionWith(transaction.IDIn(evidenceIDs...))).
			QueryTransaction().IDs(ctx))
		if err != nil {
			return res, rollback(tx, err)
		}
		taken := map[int64]bool{}
		var links []*ent.IncomeReceiptCreate
		for _, t := range s.Edges.Evidence {
			occ := nearestOccurrence(anchor, c, t.PostedDate)
			if t.Amount <= 0 || !cyclePayable(inc, occ, t.PostedDate) || taken[occ.Unix()] || used[t.ID] {
				res.Skipped++
				continue
			}
			links = append(links, tx.IncomeReceipt.Create().
				SetIncomeID(inc.ID).SetTransactionID(t.ID).SetOccurrenceDate(occ).
				SetMethod(incomereceipt.MethodAuto))
			taken[occ.Unix()] = true
		}
		if len(links) > 0 {
			if err := tx.IncomeReceipt.CreateBulk(links...).Exec(ctx); err != nil {
				return res, rollback(tx, err)
			}
		}
		res.Linked = len(links)
	}

	if err := accept.Exec(ctx); err != nil {
		return res, rollback(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	return res, nil
}

// idSet turns an id query's result into a set, passing its error through.
func idSet(ids []int, err error) (map[int]bool, error) {
	if err != nil {
		return nil, err
	}
	set := make(map[int]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set, nil
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
