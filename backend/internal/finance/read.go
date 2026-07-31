package finance

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/balancesnapshot"
	"github.com/alifyandra/portfolio-site/backend/ent/pendingtransaction"
	"github.com/alifyandra/portfolio-site/backend/ent/predicate"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
	"github.com/alifyandra/portfolio-site/backend/ent/wishlistitem"
)

// This is the read side of the finance ledger (ADR 0017). Pure query functions over
// the Ent client that BOTH the admin-gated HTTP dashboard endpoints and the remote
// MCP tools call in-process. Nothing here mutates; nothing here rounds money (the
// caller formats). The dataset is single-tenant (Alif's) and small (a handful of
// accounts), so a per-account "latest snapshot" query is bounded and clear rather
// than a window-function join.

// defaultTxnLimit / maxTxnLimit bound a transaction page. A zero/negative request
// falls back to the default; anything over the cap is clamped, so a caller can never
// ask the box to materialize an unbounded result set.
const (
	defaultTxnLimit = 50
	maxTxnLimit     = 500
	reportCurrency  = "AUD"
)

// externalScanCap bounds how many rows the external_only path materializes when no lower
// date bound (From) keeps the window finite. The internal-transfer classifier runs in Go,
// so external_only cannot LIMIT in SQL without risking a silent undercount (internal rows
// eating the page); instead it loads the window and filters in memory. A From bound already
// bounds the scan, so the cap only applies to an unbounded query; it is set well above the
// whole ledger size, and truncates to the NEWEST rows. Hitting it is NOT silent: the caller
// gets truncated=true and a slog.Warn fires (no-silent-caps principle). It is a var, not a
// const, so a test can lower it to exercise the cap without seeding tens of thousands of rows.
var externalScanCap = 20000

// SummaryView is the net-worth roll-up across every account's latest balance.
type SummaryView struct {
	NetWorth     float64
	Assets       float64
	Liabilities  float64
	Currency     string
	AccountCount int
	// AsOf is the most recent as_of across the latest snapshots (a staleness stamp);
	// nil when no account has any snapshot yet.
	AsOf *time.Time
}

// AccountView is one account plus its latest balance snapshot (nil-safe: the balance
// pointers stay nil when the account has no snapshot yet).
type AccountView struct {
	ID              int
	Name            string
	MaskedNumber    string
	Type            string
	Class           string
	Currency        string
	Balance         *float64
	Available       *float64
	CreditLimit     *float64
	BalanceAsOf     *time.Time
	PostedWatermark *time.Time
}

// BalancePoint is one point on an account's balance history line.
type BalancePoint struct {
	AsOf    time.Time
	Balance float64
}

// TxnView is one posted transaction with its owning account joined in.
type TxnView struct {
	ID           int
	AccountID    int
	AccountName  string
	PostedDate   time.Time
	Amount       float64
	Description  string
	Merchant     string
	BalanceAfter *float64
}

// PendingView is one pending (not-yet-settled) transaction with its account joined in.
type PendingView struct {
	ID          int
	AccountID   int
	AccountName string
	Date        time.Time
	Amount      float64
	Description string
	Merchant    string
}

// TxnFilter narrows a posted-transaction query. A zero AccountID means "all
// accounts"; nil From/To leave that bound open; Limit/Offset page the result.
// ExternalOnly drops internal money moves (see isInternalTransfer) from the result;
// because that classifier runs in Go over descriptions rather than as a SQL predicate,
// the external path materializes the window before paging (see listExternalOnly).
type TxnFilter struct {
	AccountID    int
	From         *time.Time
	To           *time.Time
	Limit        int
	Offset       int
	ExternalOnly bool
}

// MonthBucket is one calendar month's income/spend roll-up. Income and Spend count only
// EXTERNAL money movement: internal transfers between the owner's own accounts (identified
// from the transaction description, see isInternalTransfer) are excluded from both, so
// shuffling money around does not inflate the figures. Spend is a positive magnitude;
// Net = Income - Spend. Transfers is the excluded internal-transfer volume (outbound legs)
// for the month, reported for transparency.
type MonthBucket struct {
	Month     string // "YYYY-MM"
	Income    float64
	Spend     float64
	Net       float64
	Transfers float64
}

// internalTransferRe matches a CommBank "Transfer to/from xxNNNN" leg, capturing the last
// four digits of the counterparty account. The caller checks those against the owner's own
// accounts, so a transfer to someone else ("Transfer to Peter") is left as real spend.
var internalTransferRe = regexp.MustCompile(`(?i)\btransfer (?:to|from) x+(\d{4})\b`)

// steppayRepaymentRe matches either leg of a StepPay repayment ("StepPay Repayment" and
// "STEPPAY PYMT-THANK YOU"), which move the owner's money onto the StepPay balance.
var steppayRepaymentRe = regexp.MustCompile(`(?i)steppay (?:pymt|repayment)`)

// trailingDigitsRe pulls the last run of four digits from a masked account number such as
// "xxxx 4242".
var trailingDigitsRe = regexp.MustCompile(`(\d{4})\D*$`)

// ownAccountLast4 is the set of last-four-digit strings across the owner's accounts, used
// to tell an internal transfer from a payment to someone else.
func ownAccountLast4(accs []*ent.Account) map[string]bool {
	set := make(map[string]bool, len(accs))
	for _, a := range accs {
		if m := trailingDigitsRe.FindStringSubmatch(a.MaskedNumber); m != nil {
			set[m[1]] = true
		}
	}
	return set
}

// isInternalTransfer reports whether a transaction is one leg of a movement between the
// owner's own accounts rather than external income or spend. It keys off CommBank's
// descriptions: an own-account "Transfer to/from xxNNNN", a credit-card "PAYMENT RECEIVED",
// or a StepPay repayment. own4 holds the last four digits of every account the owner holds.
// A transfer whose counterparty is not one of the owner's accounts (a payment to another
// person) is deliberately not internal, so real outbound payments still count as spend.
func isInternalTransfer(desc string, own4 map[string]bool) bool {
	if strings.Contains(strings.ToLower(desc), "payment received") {
		return true
	}
	if steppayRepaymentRe.MatchString(desc) {
		return true
	}
	if m := internalTransferRe.FindStringSubmatch(desc); m != nil {
		return own4[m[1]]
	}
	return false
}

// loadOwnLast4 loads the owner's account last-4 set for the internal-transfer classifier.
// The set spans every account regardless of any per-account query filter, so an internal
// transfer is still recognised when the query is scoped to one side of the move.
func loadOwnLast4(ctx context.Context, client *ent.Client) (map[string]bool, error) {
	accs, err := client.Account.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	return ownAccountLast4(accs), nil
}

// spendTally accumulates external income/spend and the excluded internal-transfer volume
// over a set of posted transactions. It is the single source of the internal-transfer
// exclusion for both MonthlySummary (one tally per calendar month) and SpendingSummary
// (one tally over an arbitrary window), so the two never fork the #120 classifier logic.
type spendTally struct {
	Income    float64
	Spend     float64
	Transfers float64 // excluded internal-transfer volume (outbound legs only)
	Count     int     // every posted row folded in, internal transfers included
}

// add folds one posted transaction into the tally, applying the internal-transfer
// exclusion (see isInternalTransfer): an internal leg counts toward Transfers on its
// outbound leg only and is kept out of income/spend; an external positive is income; an
// external negative is spend as a positive magnitude.
func (s *spendTally) add(amount float64, desc string, own4 map[string]bool) {
	s.Count++
	if isInternalTransfer(desc, own4) {
		if amount < 0 {
			s.Transfers += -amount // count each transfer once, on its outbound leg
		}
		return
	}
	if amount >= 0 {
		s.Income += amount
	} else {
		s.Spend += -amount
	}
}

// latestSnapshot returns an account's most recent balance snapshot by as_of (ties
// broken by insertion id), or (nil, nil) when the account has none yet. The dataset
// is tiny, so one query per account is cheaper to reason about than a lateral join.
func latestSnapshot(ctx context.Context, client *ent.Client, accountID int) (*ent.BalanceSnapshot, error) {
	s, err := client.BalanceSnapshot.Query().
		Where(balancesnapshot.HasAccountWith(account.IDEQ(accountID))).
		Order(ent.Desc(balancesnapshot.FieldAsOf), ent.Desc(balancesnapshot.FieldID)).
		First(ctx)
	if ent.IsNotFound(err) {
		return nil, nil
	}
	return s, err
}

// NetWorthSummary rolls every account's latest balance into net worth. Assets sum the
// latest balance of asset-class accounts; liabilities sum the NEGATED latest balance of
// liability-class accounts. A debt is stored as a negative balance, so negating it
// yields a positive amount owed; an overpaid, in-credit liability yields a negative
// contribution and correctly reduces what is owed (abs() would have wrongly booked it
// as more debt). Net worth is assets - liabilities. AsOf is the freshest reading seen,
// a staleness stamp.
// (Named NetWorthSummary, not Summary, since the ingest tally already owns Summary.)
func NetWorthSummary(ctx context.Context, client *ent.Client) (SummaryView, error) {
	accs, err := client.Account.Query().All(ctx)
	if err != nil {
		return SummaryView{}, err
	}
	view := SummaryView{Currency: reportCurrency, AccountCount: len(accs)}
	var maxAsOf *time.Time
	for _, acc := range accs {
		snap, err := latestSnapshot(ctx, client, acc.ID)
		if err != nil {
			return SummaryView{}, err
		}
		if snap == nil {
			continue
		}
		switch acc.Class {
		case account.ClassAsset:
			view.Assets += snap.Balance
		case account.ClassLiability:
			view.Liabilities += -snap.Balance
		}
		if maxAsOf == nil || snap.AsOf.After(*maxAsOf) {
			t := snap.AsOf
			maxAsOf = &t
		}
	}
	view.NetWorth = view.Assets - view.Liabilities
	view.AsOf = maxAsOf
	return view, nil
}

// Accounts lists every account (ordered by name) with its latest snapshot's balance,
// available, credit limit and reading time. Balance pointers stay nil for an account
// that has never carried a snapshot.
func Accounts(ctx context.Context, client *ent.Client) ([]AccountView, error) {
	accs, err := client.Account.Query().
		Order(ent.Asc(account.FieldName)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]AccountView, 0, len(accs))
	for _, acc := range accs {
		av := AccountView{
			ID:              acc.ID,
			Name:            acc.Name,
			MaskedNumber:    acc.MaskedNumber,
			Type:            string(acc.Type),
			Class:           string(acc.Class),
			Currency:        acc.Currency,
			PostedWatermark: acc.PostedWatermark,
		}
		snap, err := latestSnapshot(ctx, client, acc.ID)
		if err != nil {
			return nil, err
		}
		if snap != nil {
			b := snap.Balance
			av.Balance = &b
			av.Available = snap.Available
			av.CreditLimit = snap.CreditLimit
			asOf := snap.AsOf
			av.BalanceAsOf = &asOf
		}
		views = append(views, av)
	}
	return views, nil
}

// BalanceHistory returns an account's balance snapshots ordered by as_of ascending,
// optionally filtered to on-or-after from. Suitable for plotting a balance line.
func BalanceHistory(ctx context.Context, client *ent.Client, accountID int, from *time.Time) ([]BalancePoint, error) {
	q := client.BalanceSnapshot.Query().
		Where(balancesnapshot.HasAccountWith(account.IDEQ(accountID)))
	if from != nil {
		q = q.Where(balancesnapshot.AsOfGTE(*from))
	}
	snaps, err := q.
		Order(ent.Asc(balancesnapshot.FieldAsOf), ent.Asc(balancesnapshot.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	points := make([]BalancePoint, 0, len(snaps))
	for _, s := range snaps {
		points = append(points, BalancePoint{AsOf: s.AsOf, Balance: s.Balance})
	}
	return points, nil
}

// ListTransactions returns a page of posted transactions matching the filter, newest
// first (posted_date desc, then id desc for a stable tiebreak), plus the total count
// for the same filter (before paging) so a caller can render pagination. (Named
// ListTransactions, not Transactions, since the ingest payload already owns the
// Transactions type.)
// The bool return is `truncated`: true only on the external_only path when the unbounded
// safety cap (externalScanCap) was hit and older external rows were dropped; always false
// on the normal SQL-paged path (which never drops rows). The caller can surface it so an
// over-cap ledger is not read as a complete answer.
func ListTransactions(ctx context.Context, client *ent.Client, f TxnFilter) ([]TxnView, int, bool, error) {
	preds := txnPredicates(f)

	if f.ExternalOnly {
		return listExternalOnly(ctx, client, f, preds)
	}

	total, err := client.Transaction.Query().Where(preds...).Count(ctx)
	if err != nil {
		return nil, 0, false, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultTxnLimit
	}
	if limit > maxTxnLimit {
		limit = maxTxnLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	rows, err := client.Transaction.Query().
		Where(preds...).
		WithAccount().
		Order(ent.Desc(transaction.FieldPostedDate), ent.Desc(transaction.FieldID)).
		Limit(limit).
		Offset(offset).
		All(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	return toTxnViews(rows), total, false, nil
}

// listExternalOnly serves ListTransactions when ExternalOnly is set. The internal-transfer
// classifier is a Go function over the description, not a SQL predicate, so applying the
// LIMIT in SQL and filtering afterwards could return fewer than `limit` real external rows
// while more exist just past the page edge — a silent undercount that would corrupt spend
// analysis. Instead it materializes the whole matching window (following MonthlySummary's
// load-the-window pattern), drops internal transfers in Go, and only then pages. A From
// bound already bounds the scan; an unbounded query is capped at externalScanCap of the
// newest rows so a caller can never force the box to load the entire ledger unboundedly.
// The returned total is the count of EXTERNAL rows in the scanned window (accurate for the
// filtered set, which is what a "how many real payments" caller wants). The bool return is
// truncated: when the unbounded cap is hit, older rows are dropped, so it is set true (and a
// slog.Warn fires) rather than the omission being silent — the caller must not read an
// over-cap answer as complete.
func listExternalOnly(ctx context.Context, client *ent.Client, f TxnFilter, preds []predicate.Transaction) ([]TxnView, int, bool, error) {
	own4, err := loadOwnLast4(ctx, client)
	if err != nil {
		return nil, 0, false, err
	}

	q := client.Transaction.Query().
		Where(preds...).
		WithAccount().
		Order(ent.Desc(transaction.FieldPostedDate), ent.Desc(transaction.FieldID))
	if f.From == nil {
		// No lower date bound to keep the window finite: cap the materialization. Scan ONE
		// past the cap so "exactly cap rows, complete" is distinguishable from "cap hit,
		// older rows dropped" — otherwise the truncation signal would be a guess.
		q = q.Limit(externalScanCap + 1)
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, 0, false, err
	}

	truncated := false
	if f.From == nil && len(rows) > externalScanCap {
		truncated = true
		rows = rows[:externalScanCap]
		slog.Warn("finance: external_only scan hit the row cap; older external rows omitted from this page and total",
			"cap", externalScanCap)
	}

	external := make([]*ent.Transaction, 0, len(rows))
	for _, t := range rows {
		if isInternalTransfer(t.Description, own4) {
			continue
		}
		external = append(external, t)
	}
	total := len(external)

	limit := f.Limit
	if limit <= 0 {
		limit = defaultTxnLimit
	}
	if limit > maxTxnLimit {
		limit = maxTxnLimit
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > len(external) {
		offset = len(external)
	}
	end := offset + limit
	if end > len(external) {
		end = len(external)
	}
	return toTxnViews(external[offset:end]), total, truncated, nil
}

// txnPredicates builds the shared where-clause for a TxnFilter so the count query and
// the page query stay in lockstep.
func txnPredicates(f TxnFilter) []predicate.Transaction {
	var preds []predicate.Transaction
	if f.AccountID != 0 {
		preds = append(preds, transaction.HasAccountWith(account.IDEQ(f.AccountID)))
	}
	if f.From != nil {
		preds = append(preds, transaction.PostedDateGTE(*f.From))
	}
	if f.To != nil {
		preds = append(preds, transaction.PostedDateLTE(*f.To))
	}
	return preds
}

// Pending returns pending transactions, newest first, optionally scoped to one
// account (0 = all). Pending is a volatile side-set the ingest replaces each run.
func Pending(ctx context.Context, client *ent.Client, accountID int) ([]PendingView, error) {
	q := client.PendingTransaction.Query().WithAccount()
	if accountID != 0 {
		q = q.Where(pendingtransaction.HasAccountWith(account.IDEQ(accountID)))
	}
	rows, err := q.
		Order(ent.Desc(pendingtransaction.FieldDate), ent.Desc(pendingtransaction.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	views := make([]PendingView, 0, len(rows))
	for _, p := range rows {
		pv := PendingView{
			ID:          p.ID,
			Date:        p.Date,
			Amount:      p.Amount,
			Description: p.Description,
			Merchant:    p.Merchant,
		}
		if a := p.Edges.Account; a != nil {
			pv.AccountID = a.ID
			pv.AccountName = a.Name
		}
		views = append(views, pv)
	}
	return views, nil
}

// SearchMerchant finds posted transactions whose merchant OR description contains the
// query (case-insensitive substring), newest first, optionally within an inclusive
// from/to posted-date range (nil bounds are open). An empty query returns nothing.
func SearchMerchant(ctx context.Context, client *ent.Client, query string, limit int, from, to *time.Time) ([]TxnView, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []TxnView{}, nil
	}
	if limit <= 0 {
		limit = defaultTxnLimit
	}
	if limit > maxTxnLimit {
		limit = maxTxnLimit
	}
	preds := []predicate.Transaction{
		transaction.Or(
			transaction.MerchantContainsFold(query),
			transaction.DescriptionContainsFold(query),
		),
	}
	if from != nil {
		preds = append(preds, transaction.PostedDateGTE(*from))
	}
	if to != nil {
		preds = append(preds, transaction.PostedDateLTE(*to))
	}
	rows, err := client.Transaction.Query().
		Where(preds...).
		WithAccount().
		Order(ent.Desc(transaction.FieldPostedDate), ent.Desc(transaction.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	return toTxnViews(rows), nil
}

// MonthlySummary buckets posted transactions into the last N calendar months (UTC),
// summing income (positive amounts) and spend (magnitude of negative amounts) per month
// with Net = Income - Spend. Internal transfers between the owner's own accounts are
// excluded from income and spend first (see isInternalTransfer) so account-to-account
// movements, credit-card payments and StepPay repayments do not inflate either column;
// their outbound volume is reported separately as Transfers. It always returns exactly N
// buckets oldest-first, zero-filled for quiet months, so a caller can render a fixed axis.
// accountID 0 spans every account.
func MonthlySummary(ctx context.Context, client *ent.Client, accountID int, months int) ([]MonthBucket, error) {
	if months <= 0 {
		months = 6
	}
	if months > 60 {
		months = 60
	}
	now := time.Now().UTC()
	curMonthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := curMonthStart.AddDate(0, -(months - 1), 0)

	// The owner's own account numbers distinguish an internal transfer from a payment to
	// someone else, so load them regardless of the accountID filter.
	own4, err := loadOwnLast4(ctx, client)
	if err != nil {
		return nil, err
	}

	preds := []predicate.Transaction{transaction.PostedDateGTE(start)}
	if accountID != 0 {
		preds = append(preds, transaction.HasAccountWith(account.IDEQ(accountID)))
	}
	rows, err := client.Transaction.Query().Where(preds...).All(ctx)
	if err != nil {
		return nil, err
	}

	order := make([]string, 0, months)
	idx := make(map[string]*spendTally, months)
	for i := 0; i < months; i++ {
		key := start.AddDate(0, i, 0).Format("2006-01")
		idx[key] = &spendTally{}
		order = append(order, key)
	}
	for _, t := range rows {
		tally, ok := idx[t.PostedDate.UTC().Format("2006-01")]
		if !ok {
			continue
		}
		tally.add(t.Amount, t.Description, own4)
	}
	out := make([]MonthBucket, 0, months)
	for _, key := range order {
		tl := idx[key]
		out = append(out, MonthBucket{
			Month:     key,
			Income:    tl.Income,
			Spend:     tl.Spend,
			Net:       tl.Income - tl.Spend,
			Transfers: tl.Transfers,
		})
	}
	return out, nil
}

// SpendBucket is the external-spending roll-up over one arbitrary window (SpendingSummary).
// Income and Spend count only EXTERNAL money: internal transfers between the owner's own
// accounts, credit-card payments and StepPay repayments are excluded from both (their
// outbound volume is reported as Transfers, the same rule MonthlySummary applies). Spend is
// a positive magnitude; Net = Income - Spend. TxnCount is every posted row in the window,
// internal transfers included.
type SpendBucket struct {
	From      time.Time
	To        time.Time
	Income    float64
	Spend     float64
	Net       float64
	Transfers float64
	TxnCount  int
}

// SpendingSummary rolls posted transactions in the inclusive [from, to] window into a
// single external income/spend bucket, applying the exact internal-transfer exclusion of
// MonthlySummary (shared via spendTally) so "how much did I really spend last week/month"
// is one call instead of the model summing a row list. from/to are UTC dates; accountID 0
// spans every account. It loads the whole window (bounded by the date range) and folds it
// in Go, mirroring MonthlySummary.
func SpendingSummary(ctx context.Context, client *ent.Client, accountID int, from, to time.Time) (SpendBucket, error) {
	own4, err := loadOwnLast4(ctx, client)
	if err != nil {
		return SpendBucket{}, err
	}

	preds := []predicate.Transaction{
		transaction.PostedDateGTE(from),
		transaction.PostedDateLTE(to),
	}
	if accountID != 0 {
		preds = append(preds, transaction.HasAccountWith(account.IDEQ(accountID)))
	}
	rows, err := client.Transaction.Query().Where(preds...).All(ctx)
	if err != nil {
		return SpendBucket{}, err
	}

	var tally spendTally
	for _, t := range rows {
		tally.add(t.Amount, t.Description, own4)
	}
	return SpendBucket{
		From:      from,
		To:        to,
		Income:    tally.Income,
		Spend:     tally.Spend,
		Net:       tally.Income - tally.Spend,
		Transfers: tally.Transfers,
		TxnCount:  tally.Count,
	}, nil
}

// --- wishlist (portfolio-site#123) ---

// defaultWishlistLimit / maxWishlistLimit bound a wishlist read the same way the
// transaction limits do: a zero/negative request falls back to the default, anything
// over the cap is clamped. The table holds tens of rows, so the default is generous
// enough that a normal read is never cut short.
const (
	defaultWishlistLimit = 100
	maxWishlistLimit     = 500
)

// WishlistStatusAll is the Status value that spans every lifecycle state instead of
// filtering to one.
const WishlistStatusAll = "all"

// WishlistFilter narrows a wishlist query. Status "" (the default) means wanted only;
// "all" spans every status; any single status value filters to it.
type WishlistFilter struct {
	Status string
	Limit  int
}

// WishlistView is one wishlist item. Amount stays a pointer so "unknown price"
// survives to the wire instead of collapsing to 0.
type WishlistView struct {
	ID               int
	Name             string
	Description      string
	Amount           *float64
	AmountIsEstimate bool
	Currency         string
	Priority         string
	Status           string
	Deadline         *time.Time
	ResolvedAt       *time.Time
	Link             string
	ImageKey         string
}

// WishlistTotals is the roll-up the model would otherwise have to compute (and get
// wrong when amounts are null). KnownCostTotal sums the non-nil amounts of the returned
// rows that are denominated in Currency; UnknownCostCount is how many rows had no amount
// at all. CurrencyMismatchCount is how many priced rows were left OUT of KnownCostTotal
// because they carry a different currency: the total is a single-currency figure, and a
// foreign-currency item would otherwise be added to it as if the codes matched. A row
// with no amount is only counted as unknown, since there is no figure to convert.
type WishlistTotals struct {
	ItemCount             int
	KnownCostTotal        float64
	UnknownCostCount      int
	CurrencyMismatchCount int
	Currency              string
}

// wishlistPriorityRank ranks the priority enum for ordering. The enum is stored as a
// string, so a SQL ORDER BY would sort it alphabetically (high, low, medium); ranking
// in Go keeps high-to-low correct on both Postgres and the SQLite used by tests.
var wishlistPriorityRank = map[wishlistitem.Priority]int{
	wishlistitem.PriorityHigh:   3,
	wishlistitem.PriorityMedium: 2,
	wishlistitem.PriorityLow:    1,
}

// Wishlist lists the owner's one-off wants: priority high to low, then nearest
// deadline (rows with no deadline last), then newest first. Ordering and the roll-up
// both run in Go rather than SQL, because the priority enum does not sort
// alphabetically and nulls-last differs per dialect. Totals describe the rows actually
// returned, so a limited read never reports a cost total for rows the caller cannot
// see. An unrecognised Status is an error rather than a silent full scan.
//
// The bool return is `truncated`: true only when the row limit actually dropped rows, so
// its absence means the answer (and the roll-up) covers the whole list. Truncation drops
// the tail of the read order, which is the lowest-priority end. A caller can raise Limit
// (up to maxWishlistLimit) for a complete answer. Following the no-silent-caps rule the
// external_only path set (see externalScanCap).
func Wishlist(ctx context.Context, client *ent.Client, f WishlistFilter) ([]WishlistView, WishlistTotals, bool, error) {
	q := client.WishlistItem.Query()
	switch status := strings.ToLower(strings.TrimSpace(f.Status)); status {
	case "":
		q = q.Where(wishlistitem.StatusEQ(wishlistitem.StatusWanted))
	case WishlistStatusAll:
		// Every status: no predicate.
	case string(wishlistitem.StatusWanted), string(wishlistitem.StatusBought), string(wishlistitem.StatusAbandoned):
		q = q.Where(wishlistitem.StatusEQ(wishlistitem.Status(status)))
	default:
		return nil, WishlistTotals{}, false, fmt.Errorf("unknown status %q (want wanted, bought, abandoned or all)", f.Status)
	}

	rows, err := q.All(ctx)
	if err != nil {
		return nil, WishlistTotals{}, false, err
	}

	sort.SliceStable(rows, func(i, j int) bool { return lessWishlistItem(rows[i], rows[j]) })

	limit := f.Limit
	if limit <= 0 {
		limit = defaultWishlistLimit
	}
	if limit > maxWishlistLimit {
		limit = maxWishlistLimit
	}
	truncated := false
	if len(rows) > limit {
		truncated = true
		slog.Warn("finance: wishlist read hit the row limit; the lowest-priority items are omitted from this page and from its totals",
			"limit", limit, "matched", len(rows))
		rows = rows[:limit]
	}

	views := make([]WishlistView, 0, len(rows))
	totals := WishlistTotals{ItemCount: len(rows), Currency: reportCurrency}
	for _, w := range rows {
		views = append(views, WishlistView{
			ID:               w.ID,
			Name:             w.Name,
			Description:      w.Description,
			Amount:           w.Amount,
			AmountIsEstimate: w.AmountIsEstimate,
			Currency:         w.Currency,
			Priority:         string(w.Priority),
			Status:           string(w.Status),
			Deadline:         w.Deadline,
			ResolvedAt:       w.ResolvedAt,
			Link:             w.Link,
			ImageKey:         w.ImageKey,
		})
		if w.Amount == nil {
			totals.UnknownCostCount++
			continue
		}
		if w.Currency != totals.Currency {
			// Keep a foreign-currency figure out of a single-currency total, and report
			// how many were left out so the omission is visible rather than silent.
			totals.CurrencyMismatchCount++
			continue
		}
		totals.KnownCostTotal += *w.Amount
	}
	return views, totals, truncated, nil
}

// lessWishlistItem is the wishlist read order: priority high to low, then deadline
// ascending with no-deadline rows last, then created_at descending (id descending as a
// stable tiebreak). Pressing items come first, so a truncated read still shows them.
func lessWishlistItem(a, b *ent.WishlistItem) bool {
	if ra, rb := wishlistPriorityRank[a.Priority], wishlistPriorityRank[b.Priority]; ra != rb {
		return ra > rb
	}
	if a.Deadline != nil || b.Deadline != nil {
		switch {
		case a.Deadline == nil:
			return false
		case b.Deadline == nil:
			return true
		case !a.Deadline.Equal(*b.Deadline):
			return a.Deadline.Before(*b.Deadline)
		}
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// toTxnViews maps posted transaction rows (with their account edge eager-loaded) to
// the flat view the callers format.
func toTxnViews(rows []*ent.Transaction) []TxnView {
	views := make([]TxnView, 0, len(rows))
	for _, t := range rows {
		tv := TxnView{
			ID:           t.ID,
			PostedDate:   t.PostedDate,
			Amount:       t.Amount,
			Description:  t.Description,
			Merchant:     t.Merchant,
			BalanceAfter: t.BalanceAfter,
		}
		if a := t.Edges.Account; a != nil {
			tv.AccountID = a.ID
			tv.AccountName = a.Name
		}
		views = append(views, tv)
	}
	return views
}
