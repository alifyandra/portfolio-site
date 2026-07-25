package finance

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/balancesnapshot"
	"github.com/alifyandra/portfolio-site/backend/ent/pendingtransaction"
	"github.com/alifyandra/portfolio-site/backend/ent/predicate"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
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
type TxnFilter struct {
	AccountID int
	From      *time.Time
	To        *time.Time
	Limit     int
	Offset    int
}

// MonthBucket is one calendar month's income/spend roll-up. Spend is reported as a
// positive figure (the sum of the outgoing amounts' magnitudes); Net = Income - Spend.
type MonthBucket struct {
	Month  string // "YYYY-MM"
	Income float64
	Spend  float64
	Net    float64
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
// latest balance of asset-class accounts; liabilities sum the ABSOLUTE latest balance
// of liability-class accounts (reported as a positive amount owed), so a credit card
// carried as a negative balance still adds to what is owed. Net worth is
// assets - liabilities. AsOf is the freshest reading seen, a staleness stamp.
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
			view.Liabilities += math.Abs(snap.Balance)
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
func ListTransactions(ctx context.Context, client *ent.Client, f TxnFilter) ([]TxnView, int, error) {
	preds := txnPredicates(f)

	total, err := client.Transaction.Query().Where(preds...).Count(ctx)
	if err != nil {
		return nil, 0, err
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
		return nil, 0, err
	}
	return toTxnViews(rows), total, nil
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
// query (case-insensitive substring), newest first. An empty query returns nothing.
func SearchMerchant(ctx context.Context, client *ent.Client, query string, limit int) ([]TxnView, error) {
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
	rows, err := client.Transaction.Query().
		Where(transaction.Or(
			transaction.MerchantContainsFold(query),
			transaction.DescriptionContainsFold(query),
		)).
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
// summing income (positive amounts) and spend (magnitude of negative amounts) per
// month with Net = Income - Spend. It always returns exactly N buckets oldest-first,
// zero-filled for quiet months, so a caller can render a fixed axis. accountID 0
// spans every account.
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

	preds := []predicate.Transaction{transaction.PostedDateGTE(start)}
	if accountID != 0 {
		preds = append(preds, transaction.HasAccountWith(account.IDEQ(accountID)))
	}
	rows, err := client.Transaction.Query().Where(preds...).All(ctx)
	if err != nil {
		return nil, err
	}

	order := make([]string, 0, months)
	idx := make(map[string]*MonthBucket, months)
	for i := 0; i < months; i++ {
		key := start.AddDate(0, i, 0).Format("2006-01")
		b := &MonthBucket{Month: key}
		idx[key] = b
		order = append(order, key)
	}
	for _, t := range rows {
		b, ok := idx[t.PostedDate.UTC().Format("2006-01")]
		if !ok {
			continue
		}
		if t.Amount >= 0 {
			b.Income += t.Amount
		} else {
			b.Spend += -t.Amount
		}
	}
	out := make([]MonthBucket, 0, months)
	for _, key := range order {
		b := idx[key]
		b.Net = b.Income - b.Spend
		out = append(out, *b)
	}
	return out, nil
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
