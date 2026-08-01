package finance

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite" // pure-Go sqlite driver (no CGO) for in-memory test DBs

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/account"
)

// newFinanceTestClient builds an Ent client on a fresh in-memory SQLite DB with the
// schema migrated (mirrors the jobs/digest test seams), so Ingest can be exercised
// directly against a real ledger.
func newFinanceTestClient(t *testing.T) *ent.Client {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.SQLite, db)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return client
}

// ns / nf are the wire-type constructors for the nullable payload fields.
func ns(s string) NullString { return NullString{Value: &s} }
func nf(f float64) NullFloat { return NullFloat{Value: &f} }

// basePayload is a single-account reconciled window (no expected_posted, so it
// reconciles) whose window.to the caller supplies, so a test can drive the watermark
// advance. window.account is left null, so every resolved account advances.
func basePayload(windowTo string) *Payload {
	return &Payload{
		Source:    "commbank",
		ScrapedAt: "2026-07-11T09:00:00.000Z",
		Window:    &Window{From: "2026-04-12", To: windowTo},
		Accounts: []Account{
			{
				MaskedNumber: ns("xxxx xxxx 1234"),
				Name:         "Smart Access",
				Type:         "everyday",
				Class:        "asset",
				Currency:     "AUD",
				Balance:      &Balance{Balance: 1957.90, Available: nf(1900.00), AsOf: "2026-07-11T09:00:00.000Z"},
			},
		},
		Transactions: Transactions{
			Posted: []PostedRow{
				{Account: "Smart Access", PostedDate: "2026-07-10", Amount: -42.10, AmountRaw: "$42.10", Description: "EFTPOS PURCHASE SUPERMARKET", Merchant: ns("Supermarket"), BalanceAfter: nf(1957.90)},
				{Account: "Smart Access", PostedDate: "2026-07-09", Amount: -5.00, AmountRaw: "$5.00", Description: "COFFEE"},
			},
		},
	}
}

func getAccount(t *testing.T, client *ent.Client, name string) *ent.Account {
	t.Helper()
	return client.Account.Query().Where(account.NameEQ(name)).OnlyX(context.Background())
}

// TestIngest_AdvancesWatermarkOnReconciledRun: a real, reconciled run records the
// window's `to` (normalized to UTC-midnight) as the account's posted watermark.
func TestIngest_AdvancesWatermarkOnReconciledRun(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	today := normalizeDate(time.Now())
	to := today.AddDate(0, 0, -3)
	sum, err := Ingest(ctx, client, basePayload(to.Format("2006-01-02")))
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if !sum.Reconciled || sum.DryRun {
		t.Fatalf("summary = %+v, want reconciled real run", sum)
	}
	acc := getAccount(t, client, "Smart Access")
	if acc.PostedWatermark == nil {
		t.Fatalf("watermark = nil, want %v", to)
	}
	if !acc.PostedWatermark.Equal(to) {
		t.Errorf("watermark = %v, want %v (normalized window.to)", acc.PostedWatermark, to)
	}
}

// TestIngest_WatermarkClampedToToday: a window.to in the future is clamped to today,
// so a watermark never claims completeness into the future.
func TestIngest_WatermarkClampedToToday(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	today := normalizeDate(time.Now())
	future := today.AddDate(0, 0, 5)
	if _, err := Ingest(ctx, client, basePayload(future.Format("2006-01-02"))); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	acc := getAccount(t, client, "Smart Access")
	if acc.PostedWatermark == nil || !acc.PostedWatermark.Equal(today) {
		t.Errorf("watermark = %v, want %v (clamped to today)", acc.PostedWatermark, today)
	}
}

// TestIngest_NoWindowLeavesWatermarkNil: a payload with no window (or an empty
// window.to) never advances the watermark, keeping pre-#88 payloads unchanged.
func TestIngest_NoWindowLeavesWatermarkNil(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	p := basePayload("")
	p.Window = nil // no window at all
	if _, err := Ingest(ctx, client, p); err != nil {
		t.Fatalf("ingest (no window): %v", err)
	}
	if acc := getAccount(t, client, "Smart Access"); acc.PostedWatermark != nil {
		t.Errorf("watermark = %v, want nil (no window)", acc.PostedWatermark)
	}

	// Empty window.to is likewise a no-op.
	client2 := newFinanceTestClient(t)
	if _, err := Ingest(ctx, client2, basePayload("")); err != nil {
		t.Fatalf("ingest (empty window.to): %v", err)
	}
	if acc := getAccount(t, client2, "Smart Access"); acc.PostedWatermark != nil {
		t.Errorf("watermark = %v, want nil (empty window.to)", acc.PostedWatermark)
	}
}

// TestIngest_DryRunDoesNotAdvanceWatermark: after a committed run sets wm=D1, a later
// dry run rolls back its advance, so the stored watermark stays D1 (pairs with the
// api-package TestIngest_DryRunPersistsNothing).
func TestIngest_DryRunDoesNotAdvanceWatermark(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	today := normalizeDate(time.Now())
	d1 := today.AddDate(0, 0, -10)
	if _, err := Ingest(ctx, client, basePayload(d1.Format("2006-01-02"))); err != nil {
		t.Fatalf("seed ingest: %v", err)
	}

	dry := basePayload(today.Format("2006-01-02")) // a later window.to
	dry.DryRun = true
	sum, err := Ingest(ctx, client, dry)
	if err != nil {
		t.Fatalf("dry-run ingest: %v", err)
	}
	if !sum.DryRun {
		t.Fatalf("summary DryRun = false, want true")
	}
	if acc := getAccount(t, client, "Smart Access"); acc.PostedWatermark == nil || !acc.PostedWatermark.Equal(d1) {
		t.Errorf("watermark = %v, want %v (dry run must not advance)", acc.PostedWatermark, d1)
	}
}

// TestIngest_UnreconciledRealRunDoesNotAdvanceWatermark: a real run that fails
// reconciliation (expected_posted mismatch) is rolled back, so wm stays D1.
func TestIngest_UnreconciledRealRunDoesNotAdvanceWatermark(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	today := normalizeDate(time.Now())
	d1 := today.AddDate(0, 0, -10)
	if _, err := Ingest(ctx, client, basePayload(d1.Format("2006-01-02"))); err != nil {
		t.Fatalf("seed ingest: %v", err)
	}

	bad := basePayload(today.Format("2006-01-02"))
	mismatch := 5 // but only 2 posted rows are present
	bad.Transactions.ExpectedPosted = &mismatch
	sum, err := Ingest(ctx, client, bad)
	var recErr *ReconciliationError
	if err == nil || !errors.As(err, &recErr) {
		t.Fatalf("ingest = (%+v, %v), want ReconciliationError", sum, err)
	}
	if acc := getAccount(t, client, "Smart Access"); acc.PostedWatermark == nil || !acc.PostedWatermark.Equal(d1) {
		t.Errorf("watermark = %v, want %v (unreconciled run must not advance)", acc.PostedWatermark, d1)
	}
}

// TestIngest_WatermarkMonotonicNoBackward: a later run whose window.to is EARLIER
// than the stored watermark must not move it backward.
func TestIngest_WatermarkMonotonicNoBackward(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	today := normalizeDate(time.Now())
	if _, err := Ingest(ctx, client, basePayload(today.Format("2006-01-02"))); err != nil {
		t.Fatalf("seed ingest (to=today): %v", err)
	}
	earlier := today.AddDate(0, 0, -10)
	if _, err := Ingest(ctx, client, basePayload(earlier.Format("2006-01-02"))); err != nil {
		t.Fatalf("second ingest (earlier to): %v", err)
	}
	if acc := getAccount(t, client, "Smart Access"); acc.PostedWatermark == nil || !acc.PostedWatermark.Equal(today) {
		t.Errorf("watermark = %v, want %v (must not move backward)", acc.PostedWatermark, today)
	}
}

// TestIngest_GapRecoveryOverlapIdempotentAndAdvances: after wm=D1, a later window
// (D1-overlap)..today re-covers the already-stored rows (deduped, idempotent) and
// picks up a row dated after D1 that the first window missed; the new row lands and
// the watermark advances to today.
func TestIngest_GapRecoveryOverlapIdempotentAndAdvances(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	today := normalizeDate(time.Now())
	d1 := today.AddDate(0, 0, -10)
	rowA := d1.AddDate(0, 0, -2)
	rowB := d1
	missed := d1.AddDate(0, 0, 2) // settled after the first window closed

	// First sync: window ends at D1, two posted rows within it.
	first := basePayload(d1.Format("2006-01-02"))
	first.Window.From = d1.AddDate(0, 0, -10).Format("2006-01-02")
	first.Transactions.Posted = []PostedRow{
		{Account: "Smart Access", PostedDate: rowA.Format("2006-01-02"), Amount: -12.00, Description: "ROW A", BalanceAfter: nf(100.00)},
		{Account: "Smart Access", PostedDate: rowB.Format("2006-01-02"), Amount: -8.00, Description: "ROW B", BalanceAfter: nf(92.00)},
	}
	if _, err := Ingest(ctx, client, first); err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if acc := getAccount(t, client, "Smart Access"); acc.PostedWatermark == nil || !acc.PostedWatermark.Equal(d1) {
		t.Fatalf("watermark after first = %v, want %v", accWM(acc), d1)
	}
	if n := client.Transaction.Query().CountX(ctx); n != 2 {
		t.Fatalf("posted after first = %d, want 2", n)
	}

	// Second sync: overlap window re-covers rowA/rowB (identical => deduped) and adds
	// the missed row dated D1+2.
	second := basePayload(today.Format("2006-01-02"))
	second.Window.From = d1.AddDate(0, 0, -7).Format("2006-01-02")
	second.Transactions.Posted = []PostedRow{
		{Account: "Smart Access", PostedDate: rowA.Format("2006-01-02"), Amount: -12.00, Description: "ROW A", BalanceAfter: nf(100.00)},
		{Account: "Smart Access", PostedDate: rowB.Format("2006-01-02"), Amount: -8.00, Description: "ROW B", BalanceAfter: nf(92.00)},
		{Account: "Smart Access", PostedDate: missed.Format("2006-01-02"), Amount: -3.00, Description: "MISSED ROW", BalanceAfter: nf(89.00)},
	}
	sum, err := Ingest(ctx, client, second)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if sum.PostedInserted != 1 || sum.PostedDuplicatesSkipped != 2 {
		t.Errorf("second summary posted = inserted:%d skipped:%d, want 1 inserted / 2 skipped (overlap idempotent)", sum.PostedInserted, sum.PostedDuplicatesSkipped)
	}
	if n := client.Transaction.Query().CountX(ctx); n != 3 {
		t.Errorf("posted after second = %d, want 3 (missed row landed, no dupes)", n)
	}
	if acc := getAccount(t, client, "Smart Access"); acc.PostedWatermark == nil || !acc.PostedWatermark.Equal(today) {
		t.Errorf("watermark after second = %v, want %v (advanced to today)", accWM(acc), today)
	}
}

// TestIngest_NamedAccountAdvancesOnlyThatAccount: when window.account names one
// account, only that account's watermark advances; a sibling account in the same
// payload is left untouched.
func TestIngest_NamedAccountAdvancesOnlyThatAccount(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	today := normalizeDate(time.Now())
	to := today.AddDate(0, 0, -1)
	p := basePayload(to.Format("2006-01-02"))
	p.Window.Account = ns("Smart Access")
	p.Accounts = append(p.Accounts, Account{
		MaskedNumber: ns("xxxx xxxx 9999"),
		Name:         "NetBank Saver",
		Type:         "savings",
		Class:        "asset",
		Currency:     "AUD",
		Balance:      &Balance{Balance: 5000.00, AsOf: "2026-07-11T09:00:00.000Z"},
	})
	if _, err := Ingest(ctx, client, p); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	named := getAccount(t, client, "Smart Access")
	if named.PostedWatermark == nil || !named.PostedWatermark.Equal(to) {
		t.Errorf("named-account watermark = %v, want %v", accWM(named), to)
	}
	other := getAccount(t, client, "NetBank Saver")
	if other.PostedWatermark != nil {
		t.Errorf("non-named-account watermark = %v, want nil (untouched)", other.PostedWatermark)
	}
}

// TestIngest_NullWindowAdvancesOnlyScannedAccounts: with window.account null, only
// accounts actually scanned for the window advance = those declared in accounts[]
// (even quiet ones with zero posted rows) plus those referenced by a posted row. An
// account seen ONLY via a pending row is a volatile side-view whose posted history
// was not scanned, so its watermark must stay nil (else its future backfill is
// permanently suppressed). This FAILS against an all-of-byName advance.
func TestIngest_NullWindowAdvancesOnlyScannedAccounts(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	today := normalizeDate(time.Now())
	to := today.AddDate(0, 0, -1)

	p := basePayload(to.Format("2006-01-02")) // A = "Smart Access" declared, with posted rows
	// B: a declared but quiet account (no posted rows) -- must still advance.
	p.Accounts = append(p.Accounts, Account{
		MaskedNumber: ns("xxxx xxxx 5555"),
		Name:         "NetBank Saver",
		Type:         "savings",
		Class:        "asset",
		Currency:     "AUD",
		Balance:      &Balance{Balance: 5000.00, AsOf: "2026-07-11T09:00:00.000Z"},
	})
	// C: seen ONLY via a pending row -- not declared, no posted rows -- must NOT advance.
	p.Transactions.Pending = []PendingRow{
		{Account: "Wallet Pending", Date: to.Format("2006-01-02"), Amount: -9.99, Description: "PENDING COFFEE"},
	}
	if _, err := Ingest(ctx, client, p); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	a := getAccount(t, client, "Smart Access")
	if a.PostedWatermark == nil || !a.PostedWatermark.Equal(to) {
		t.Errorf("A (posted-referenced) watermark = %v, want %v", accWM(a), to)
	}
	b := getAccount(t, client, "NetBank Saver")
	if b.PostedWatermark == nil || !b.PostedWatermark.Equal(to) {
		t.Errorf("B (declared, quiet) watermark = %v, want %v", accWM(b), to)
	}
	c := getAccount(t, client, "Wallet Pending")
	if c.PostedWatermark != nil {
		t.Errorf("C (pending-only) watermark = %v, want nil (posted history never scanned)", c.PostedWatermark)
	}
}

// accWM renders a nullable watermark for test failure messages.
func accWM(a *ent.Account) any {
	if a.PostedWatermark == nil {
		return "<nil>"
	}
	return *a.PostedWatermark
}

// TestIngest_DoesNotClobberOwnerAuthoredFields is the #122 regression: description and
// drawdown_policy are typed in by the owner and the bank knows nothing about them, so a
// re-ingest of the same (source, name) must leave them exactly as they were while still
// refreshing every column the bank does own. The failure mode this guards is the upsert
// conflict clause being simplified to UpdateNewValues(), which would take the Create
// clause's empty description and unset policy and wipe both on every scheduled sync.
func TestIngest_DoesNotClobberOwnerAuthoredFields(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	today := normalizeDate(time.Now())

	if _, err := Ingest(ctx, client, basePayload(today.Format("2006-01-02"))); err != nil {
		t.Fatalf("seed ingest: %v", err)
	}

	// The owner labels the account from the admin console. updated_at is aged so the
	// re-ingest's advance is unambiguous rather than a sub-millisecond difference.
	const note = "sinking fund for one specific thing; must not be raided"
	stale := time.Now().Add(-time.Hour)
	seeded := getAccount(t, client, "Smart Access")
	client.Account.UpdateOne(seeded).
		SetDescription(note).
		SetDrawdownPolicy(account.DrawdownPolicyNoDrawdown).
		SetUpdatedAt(stale).
		SaveX(ctx)

	// A later run where the bank reports different attributes for the same account.
	p := basePayload(today.Format("2006-01-02"))
	p.Accounts[0].MaskedNumber = ns("xxxx xxxx 4242")
	p.Accounts[0].Type = "savings"
	p.Accounts[0].ProductCode = ns("NEWCODE")
	p.Accounts[0].GuessedType = true
	if _, err := Ingest(ctx, client, p); err != nil {
		t.Fatalf("re-ingest: %v", err)
	}

	got := getAccount(t, client, "Smart Access")
	if got.Description != note {
		t.Errorf("description = %q, want %q (owner-authored, ingest must not clobber)", got.Description, note)
	}
	if got.DrawdownPolicy != account.DrawdownPolicyNoDrawdown {
		t.Errorf("drawdown_policy = %q, want %q (owner-authored, ingest must not clobber)", got.DrawdownPolicy, account.DrawdownPolicyNoDrawdown)
	}
	// The other half of the assertion: the conflict clause still does its job.
	if got.MaskedNumber != "xxxx xxxx 4242" || got.Type != account.TypeSavings || got.ProductCode != "NEWCODE" || !got.GuessedType {
		t.Errorf("bank-owned fields = %q/%q/%q/%v, want the re-ingested values", got.MaskedNumber, got.Type, got.ProductCode, got.GuessedType)
	}
	if !got.UpdatedAt.After(stale) {
		t.Errorf("updated_at = %v, want it advanced past %v", got.UpdatedAt, stale)
	}
	if got.ID != seeded.ID {
		t.Errorf("account id = %d, want the same row %d (upsert, not a duplicate)", got.ID, seeded.ID)
	}
}

// TestIngest_PlaceholderDoesNotClobberOwnerAuthoredFields covers the other account
// creation path: a transaction-only run naming an account that is not in accounts[].
// placeholderAccount uses DO NOTHING, so an already-labelled account must come through
// a later transaction-only run untouched.
func TestIngest_PlaceholderDoesNotClobberOwnerAuthoredFields(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()

	// txnOnly names an account that accounts[] never declares, so the ingest creates it
	// through placeholderAccount.
	txnOnly := func(desc string) *Payload {
		return &Payload{
			Source:    "commbank",
			ScrapedAt: "2026-07-11T09:00:00.000Z",
			Transactions: Transactions{
				Posted: []PostedRow{
					{Account: "Undeclared Account", PostedDate: "2026-07-10", Amount: -9.00, AmountRaw: "$9.00", Description: desc},
				},
			},
		}
	}
	if _, err := Ingest(ctx, client, txnOnly("FIRST ROW")); err != nil {
		t.Fatalf("seed transaction-only ingest: %v", err)
	}

	const note = "everyday spending, replenished each pay"
	seeded := getAccount(t, client, "Undeclared Account")
	if !seeded.GuessedType {
		t.Fatalf("guessed_type = false, want the placeholder flagged for review")
	}
	client.Account.UpdateOne(seeded).
		SetDescription(note).
		SetDrawdownPolicy(account.DrawdownPolicyFlexible).
		SaveX(ctx)

	if _, err := Ingest(ctx, client, txnOnly("SECOND ROW")); err != nil {
		t.Fatalf("second transaction-only ingest: %v", err)
	}

	got := getAccount(t, client, "Undeclared Account")
	if got.Description != note {
		t.Errorf("description = %q, want %q (placeholder path must not clobber)", got.Description, note)
	}
	if got.DrawdownPolicy != account.DrawdownPolicyFlexible {
		t.Errorf("drawdown_policy = %q, want %q (placeholder path must not clobber)", got.DrawdownPolicy, account.DrawdownPolicyFlexible)
	}
	if got.ID != seeded.ID {
		t.Errorf("account id = %d, want the same row %d (DO NOTHING, not a duplicate)", got.ID, seeded.ID)
	}
}

// TestIngest_NewAccountTakesOwnerFieldDefaults: a freshly ingested account is honest
// about never having been labelled (empty description, drawdown_policy=unset) rather
// than defaulting to something that reads as spendable.
func TestIngest_NewAccountTakesOwnerFieldDefaults(t *testing.T) {
	client := newFinanceTestClient(t)
	ctx := context.Background()
	if _, err := Ingest(ctx, client, basePayload("")); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	acc := getAccount(t, client, "Smart Access")
	if acc.Description != "" {
		t.Errorf("description = %q, want empty (never written)", acc.Description)
	}
	if acc.DrawdownPolicy != account.DrawdownPolicyUnset {
		t.Errorf("drawdown_policy = %q, want unset", acc.DrawdownPolicy)
	}
}
