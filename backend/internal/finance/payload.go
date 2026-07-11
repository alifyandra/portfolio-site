package finance

import (
	"encoding/json"

	"github.com/danielgtaylor/huma/v2"
)

// Payload is the ingest wire contract (portfolio-site#84), the shape the broker
// POSTs to /api/finance/ingest. It doubles as the Huma request body, so Huma
// reflects the OpenAPI schema straight off these tags. Every key the broker sends
// is present here because Huma rejects unknown properties; a missing key would 422
// a real payload.
type Payload struct {
	Source       string       `json:"source" doc:"The broker/source, e.g. \"commbank\"; the identity key's source half"`
	ScrapedAt    string       `json:"scraped_at,omitempty" format:"date-time" doc:"ISO-8601 time the broker read the data; informational, not persisted in v1"`
	Window       *Window      `json:"window,omitempty" doc:"The window the broker was asked to cover; informational, not persisted in v1"`
	Accounts     []Account    `json:"accounts,omitempty" doc:"Declared accounts with their current balance; may be empty on a transaction-only run"`
	Transactions Transactions `json:"transactions,omitempty" doc:"The posted (immutable) and pending (replace-set) rows for this window"`
	// Ledger is the optional OFX-only closing figure. Accepted so an OFX-export
	// payload validates, but NOT persisted in v1 (see the ingest contract).
	Ledger *Ledger `json:"ledger,omitempty" doc:"Optional OFX-only closing balance; accepted but not persisted in v1"`
}

// Window is the scrape window the broker covered. Kept in the contract so the
// payload validates; none of it is persisted in v1.
type Window struct {
	From    string     `json:"from,omitempty" format:"date" doc:"Window start (YYYY-MM-DD)"`
	To      string     `json:"to,omitempty" format:"date" doc:"Window end (YYYY-MM-DD)"`
	Account NullString `json:"account,omitempty" doc:"Label of the nominated account, or null"`
}

// Account is a declared account and its current balance snapshot.
type Account struct {
	MaskedNumber NullString `json:"masked_number,omitempty" doc:"Masked account number; never the full number"`
	Name         string     `json:"name" doc:"Human label; the join key transactions reference"`
	Type         string     `json:"type" enum:"everyday,savings,credit_card,steppay,investment" doc:"Account product type"`
	Class        string     `json:"class" enum:"asset,liability" doc:"Balance interpretation; never flips transaction signs"`
	Currency     string     `json:"currency,omitempty" doc:"ISO currency code; defaults to AUD"`
	ProductCode  NullString `json:"product_code,omitempty" doc:"Bank product code, or null"`
	GuessedType  bool       `json:"guessed_type,omitempty" doc:"True when the classifier fell back; the cloud may flag for review"`
	Balance      *Balance   `json:"balance,omitempty" doc:"The account's current balance snapshot, when present"`
}

// Balance is an account's authoritative balance at scrape time.
type Balance struct {
	Balance     float64   `json:"balance" doc:"The bank's authoritative figure; not derived from transactions"`
	Available   NullFloat `json:"available,omitempty" doc:"Available balance, or null"`
	CreditLimit NullFloat `json:"credit_limit,omitempty" doc:"Credit limit (set for credit_card), or null"`
	AsOf        string    `json:"as_of" format:"date-time" doc:"ISO-8601 time the balance was read"`
}

// Transactions carries the two row sets for a window.
type Transactions struct {
	Posted  []PostedRow  `json:"posted,omitempty" doc:"Immutable ledger rows; upserted by dedup_hash"`
	Pending []PendingRow `json:"pending,omitempty" doc:"Volatile rows; the whole set is replaced per account"`
}

// PostedRow is one settled transaction the cloud lands in the immutable ledger.
type PostedRow struct {
	Account      string     `json:"account" doc:"Account name (joins to accounts[].name)"`
	PostedDate   string     `json:"posted_date" format:"date" doc:"Settlement date (YYYY-MM-DD); also the raw input to dedup_hash"`
	Amount       float64    `json:"amount" doc:"Signed amount: out negative, in positive"`
	AmountRaw    string     `json:"amount_raw,omitempty" doc:"The bank's original rendering, e.g. \"$42.10\""`
	Description  string     `json:"description" doc:"Transaction narrative"`
	Merchant     NullString `json:"merchant,omitempty" doc:"Parsed merchant, or null"`
	BalanceAfter NullFloat  `json:"balance_after,omitempty" doc:"Running balance after this row, or null"`
}

// PendingRow is one not-yet-settled transaction in an account's replace-set.
type PendingRow struct {
	Account     string     `json:"account" doc:"Account name (joins to accounts[].name)"`
	Date        string     `json:"date" format:"date" doc:"Pending date (YYYY-MM-DD)"`
	Amount      float64    `json:"amount" doc:"Signed amount: out negative, in positive"`
	AmountRaw   string     `json:"amount_raw,omitempty" doc:"The bank's original rendering"`
	Description string     `json:"description" doc:"Transaction narrative"`
	Merchant    NullString `json:"merchant,omitempty" doc:"Parsed merchant, or null"`
}

// Ledger is the optional OFX-only closing figure; accepted, never persisted (v1).
type Ledger struct {
	Value float64 `json:"value" doc:"Closing balance value"`
	// The ingest contract renders ledger.as_of date-only (e.g. "2026-07-10"), so it
	// is format:"date", not date-time: tagging it date-time would 422 a valid OFX
	// payload. Accepted but not persisted in v1 regardless.
	AsOf string `json:"as_of" format:"date" doc:"Date the ledger value is as of (YYYY-MM-DD)"`
}

// NullString is an optional, nullable JSON string. Huma's reflected schema rejects
// an explicit null for a plain *string (see api.optionalNullableString), yet the
// broker sends null for absent fields (product_code, merchant, ...), so the wire
// contract needs a type whose schema accepts both a string and null. Absent and
// null both land as a nil Value; ingest never needs to tell them apart.
type NullString struct {
	Value *string
}

// UnmarshalJSON accepts a string or an explicit null (nil Value). An absent key
// never calls this, leaving the zero value (nil Value).
func (n *NullString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		n.Value = nil
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	n.Value = &s
	return nil
}

// Schema declares an optional, nullable string so both a value and an explicit null
// pass request validation. Implements huma.SchemaProvider.
func (NullString) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeString, Nullable: true}
}

// String returns the value, or "" when null/absent.
func (n NullString) String() string {
	if n.Value == nil {
		return ""
	}
	return *n.Value
}

// NullFloat is an optional, nullable JSON number, the money counterpart to
// NullString (available, credit_limit, balance_after). Same rationale: Huma's
// reflected schema rejects an explicit null for a plain *float64.
type NullFloat struct {
	Value *float64
}

// UnmarshalJSON accepts a number or an explicit null (nil Value).
func (n *NullFloat) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		n.Value = nil
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	n.Value = &f
	return nil
}

// Schema declares an optional, nullable number so both a value and an explicit null
// pass request validation. Implements huma.SchemaProvider.
func (NullFloat) Schema(huma.Registry) *huma.Schema {
	return &huma.Schema{Type: huma.TypeNumber, Nullable: true}
}
