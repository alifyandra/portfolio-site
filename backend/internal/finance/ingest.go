package finance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
	"github.com/alifyandra/portfolio-site/backend/ent/balancesnapshot"
	"github.com/alifyandra/portfolio-site/backend/ent/pendingtransaction"
	"github.com/alifyandra/portfolio-site/backend/ent/transaction"
)

// Summary is the per-run tally returned to the caller, so the broker can log what
// a window ingest actually changed (and confirm idempotency: a re-POST is all
// duplicates skipped and no new posted rows).
type Summary struct {
	AccountsUpserted         int `json:"accounts_upserted"`
	BalanceSnapshotsInserted int `json:"balance_snapshots_inserted"`
	PostedInserted           int `json:"posted_inserted"`
	PostedDuplicatesSkipped  int `json:"posted_duplicates_skipped"`
	PendingAccountsReplaced  int `json:"pending_accounts_replaced"`
	PendingRowsInserted      int `json:"pending_rows_inserted"`
}

// Ingest lands one scraped window into the ledger inside a single transaction, so a
// bad row commits nothing and the whole endpoint stays idempotent and retriable
// (ingest contract, portfolio-site#84). Order: upsert declared accounts, append a
// balance snapshot per account, upsert posted rows by dedup_hash, then replace the
// pending set for each account named in the pending payload. Source is fixed for a
// run, so accounts key off name alone here.
func Ingest(ctx context.Context, client *ent.Client, p *Payload) (*Summary, error) {
	if client == nil {
		return nil, errors.New("finance: ent client is nil")
	}
	source := strings.TrimSpace(p.Source)
	sum := &Summary{}

	tx, err := client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("finance: begin tx: %w", err)
	}

	// The run's account set, keyed by name; grows as posted/pending rows resolve
	// placeholders for accounts absent from accounts[].
	byName := make(map[string]*ent.Account, len(p.Accounts))

	// (1) Upsert every declared account by its (source, name) identity key.
	for i := range p.Accounts {
		a := &p.Accounts[i]
		acc, err := upsertAccount(ctx, tx, source, a)
		if err != nil {
			return nil, rollback(tx, err)
		}
		byName[a.Name] = acc
		sum.AccountsUpserted++
	}

	// resolve returns the run's account for name, auto-creating a review-flagged
	// placeholder when a transaction-only row names an account neither in accounts[]
	// nor already in the DB, so ingest stays robust for transaction-only runs.
	resolve := func(name string) (*ent.Account, error) {
		if acc, ok := byName[name]; ok {
			return acc, nil
		}
		acc, err := placeholderAccount(ctx, tx, source, name)
		if err != nil {
			return nil, err
		}
		byName[name] = acc
		return acc, nil
	}

	// (2) One balance snapshot per declared account that carries a balance, upserted
	// on (account, as_of) with DO NOTHING: a redelivered identical run (same reading
	// time) dedups and stays idempotent, while a genuinely later reading (distinct
	// as_of) appends. The sql.ErrNoRows is the "did nothing" signal (as in step 3).
	// as_of keeps its full timestamp; it is NOT date-only normalized.
	for i := range p.Accounts {
		a := &p.Accounts[i]
		if a.Balance == nil {
			continue
		}
		asOf, err := parseDateTime(a.Balance.AsOf)
		if err != nil {
			return nil, rollback(tx, fmt.Errorf("finance: balance as_of for %q: %w", a.Name, err))
		}
		err = tx.BalanceSnapshot.Create().
			SetBalance(a.Balance.Balance).
			SetNillableAvailable(a.Balance.Available.Value).
			SetNillableCreditLimit(a.Balance.CreditLimit.Value).
			SetAsOf(asOf).
			SetAccountID(byName[a.Name].ID).
			OnConflictColumns(balancesnapshot.FieldAsOf, balancesnapshot.AccountColumn).
			DoNothing().
			Exec(ctx)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// same reading already recorded: idempotent no-op.
		case err != nil:
			return nil, rollback(tx, fmt.Errorf("finance: insert balance snapshot for %q: %w", a.Name, err))
		default:
			sum.BalanceSnapshotsInserted++
		}
	}

	// (3) Posted rows: immutable ledger, upsert by dedup_hash. ON CONFLICT DO NOTHING
	// means a re-seen row (identical by construction) is skipped, surfaced as the
	// sql.ErrNoRows "did nothing" signal (same pattern as digest.Persist).
	for i := range p.Transactions.Posted {
		row := &p.Transactions.Posted[i]
		acc, err := resolve(row.Account)
		if err != nil {
			return nil, rollback(tx, err)
		}
		postedDate, err := parseDate(row.PostedDate)
		if err != nil {
			return nil, rollback(tx, fmt.Errorf("finance: posted_date for %q: %w", row.Account, err))
		}
		hash := DedupHash(row.Account, row.PostedDate, row.Amount, row.Description, row.BalanceAfter.Value)
		err = tx.Transaction.Create().
			SetDedupHash(hash).
			SetPostedDate(postedDate).
			SetAmount(row.Amount).
			SetAmountRaw(row.AmountRaw).
			SetDescription(row.Description).
			SetMerchant(row.Merchant.String()).
			SetNillableBalanceAfter(row.BalanceAfter.Value).
			SetAccountID(acc.ID).
			OnConflictColumns(transaction.FieldDedupHash).
			DoNothing().
			Exec(ctx)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			sum.PostedDuplicatesSkipped++
		case err != nil:
			return nil, rollback(tx, fmt.Errorf("finance: upsert posted transaction: %w", err))
		default:
			sum.PostedInserted++
		}
	}

	// (4) Pending replace-set: for each account named in the pending payload only,
	// drop its existing pending rows and insert the current set. Accounts absent from
	// the pending payload are untouched. First-appearance order keeps it deterministic.
	pendingByName := make(map[string][]*PendingRow)
	var pendingOrder []string
	for i := range p.Transactions.Pending {
		row := &p.Transactions.Pending[i]
		if _, ok := pendingByName[row.Account]; !ok {
			pendingOrder = append(pendingOrder, row.Account)
		}
		pendingByName[row.Account] = append(pendingByName[row.Account], row)
	}
	for _, name := range pendingOrder {
		acc, err := resolve(name)
		if err != nil {
			return nil, rollback(tx, err)
		}
		if _, err := tx.PendingTransaction.Delete().
			Where(pendingtransaction.HasAccountWith(account.IDEQ(acc.ID))).
			Exec(ctx); err != nil {
			return nil, rollback(tx, fmt.Errorf("finance: clear pending for %q: %w", name, err))
		}
		for _, row := range pendingByName[name] {
			d, err := parseDate(row.Date)
			if err != nil {
				return nil, rollback(tx, fmt.Errorf("finance: pending date for %q: %w", name, err))
			}
			if err := tx.PendingTransaction.Create().
				SetDate(d).
				SetAmount(row.Amount).
				SetAmountRaw(row.AmountRaw).
				SetDescription(row.Description).
				SetMerchant(row.Merchant.String()).
				SetAccountID(acc.ID).
				Exec(ctx); err != nil {
				return nil, rollback(tx, fmt.Errorf("finance: insert pending for %q: %w", name, err))
			}
			sum.PendingRowsInserted++
		}
		sum.PendingAccountsReplaced++
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finance: commit: %w", err)
	}
	return sum, nil
}

// upsertAccount idempotently writes a declared account keyed on (source, name),
// updating its mutable attributes on conflict so a re-ingest reflects the latest
// classification. The conflict clause sets ONLY the mutable columns (never
// created_at: Immutable does not guard the upsert path, and UpdateNewValues would
// rewrite the account's first-seen date on every re-ingest); updated_at is set
// explicitly since UpdateDefault does not fire on the ON CONFLICT path. Returns the
// persisted row, re-read by the identity key (portable across Postgres and SQLite).
func upsertAccount(ctx context.Context, tx *ent.Tx, source string, a *Account) (*ent.Account, error) {
	currency := a.Currency
	if currency == "" {
		currency = "AUD"
	}
	now := time.Now()
	if err := tx.Account.Create().
		SetSource(source).
		SetName(a.Name).
		SetMaskedNumber(a.MaskedNumber.String()).
		SetType(account.Type(a.Type)).
		SetClass(account.Class(a.Class)).
		SetCurrency(currency).
		SetProductCode(a.ProductCode.String()).
		SetGuessedType(a.GuessedType).
		OnConflictColumns(account.FieldSource, account.FieldName).
		Update(func(u *ent.AccountUpsert) {
			u.SetMaskedNumber(a.MaskedNumber.String())
			u.SetType(account.Type(a.Type))
			u.SetClass(account.Class(a.Class))
			u.SetCurrency(currency)
			u.SetProductCode(a.ProductCode.String())
			u.SetGuessedType(a.GuessedType)
			u.SetUpdatedAt(now)
		}).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("finance: upsert account %q: %w", a.Name, err)
	}
	return tx.Account.Query().
		Where(account.SourceEQ(source), account.NameEQ(a.Name)).
		Only(ctx)
}

// placeholderAccount get-or-creates a minimal account for a transaction-only row
// that references a name not in accounts[] or the DB. It is flagged guessed_type so
// it shows up for review, and uses DO NOTHING so it never clobbers a real account
// that already exists (the sql.ErrNoRows is the "already there" signal).
func placeholderAccount(ctx context.Context, tx *ent.Tx, source, name string) (*ent.Account, error) {
	if err := tx.Account.Create().
		SetSource(source).
		SetName(name).
		SetType(account.TypeEveryday).
		SetClass(account.ClassAsset).
		SetGuessedType(true).
		OnConflictColumns(account.FieldSource, account.FieldName).
		DoNothing().
		Exec(ctx); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("finance: create placeholder account %q: %w", name, err)
	}
	return tx.Account.Query().
		Where(account.SourceEQ(source), account.NameEQ(name)).
		Only(ctx)
}

// rollback rolls the tx back and returns the causing error (joined with any
// rollback failure), so every error path in Ingest is one line: the atomic-commit
// invariant means a partial ingest never lands.
func rollback(tx *ent.Tx, cause error) error {
	if rbErr := tx.Rollback(); rbErr != nil {
		return errors.Join(cause, fmt.Errorf("finance: rollback: %w", rbErr))
	}
	return cause
}
