package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// RecurringBill is a declared repeating commitment (rent, insurance, a subscription, a
// utility), not a ledger row. It carries a cadence and an anchor rather than a deadline,
// it never completes, and it is reconciled against posted transactions so the system can
// report paid / missed / repriced. Contrast with a wishlist item, which is a one-off want
// with a terminal state. next_due is NOT stored: it is derived from (cadence, anchor_date)
// on read. See portfolio-site#125.
type RecurringBill struct {
	ent.Schema
}

// Fields of the RecurringBill.
func (RecurringBill) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120).
			Comment(`Human label and the human identity key, e.g. "Rent" or "Car insurance"; unique, since two bills with one name is always a data-entry mistake`),
		field.String("payee").
			Optional().
			MaxLen(120).
			Comment("Who actually gets paid, when that differs from the label (an agent, an insurer's billing entity); display only, never matched against"),
		field.Float("expected_amount").
			Positive().
			Comment("Expected charge per cycle as a POSITIVE magnitude in currency, unlike Transaction.amount which is signed; for a variable bill this is an estimate only (see amount_variable)"),
		field.String("currency").
			Default("AUD").
			Comment("ISO currency code; defaults to AUD, matching Account.currency"),
		field.Enum("cadence").
			Values("weekly", "fortnightly", "monthly", "quarterly", "annual").
			Default("monthly").
			Comment("How often the commitment recurs; with anchor_date it generates every occurrence, so no cron or RRULE string is stored"),
		field.Time("anchor_date").
			Comment("One known due date (UTC midnight, normalized in Go like Transaction.posted_date). Every occurrence is derived by stepping the cadence from here, in both directions, so this is the only date the schema stores"),
		field.Bool("amount_variable").
			Default(false).
			Comment("True for a bill whose amount changes every cycle (utilities): the matcher then skips the amount check entirely and expected_amount is read as an estimate. Deliberately a flag, not amount_tolerance_pct=0, because 'exact' and 'do not check' are opposite intents"),
		field.Float("amount_tolerance_pct").
			Default(10).
			Min(0).
			Comment("Percent either side of expected_amount a posted row may differ and still match; ignored when amount_variable is true. Min(0) rather than NonNegative(), which ent only offers on integer fields"),
		field.String("match_pattern").
			Optional().
			MaxLen(200).
			Comment("Case-insensitive substring matched against a posted transaction's description OR merchant; empty means this bill is never auto-matched and is reconciled by hand. Same rules-based approach as isInternalTransfer (internal/finance/read.go)"),
		field.Int("match_window_days").
			Default(5).
			NonNegative().
			Comment("How many days either side of a derived occurrence a posted row may land and still count as that cycle's payment; direct debits drift a day or two"),
		field.Enum("status").
			Values("active", "paused", "ended").
			Default("active").
			Comment("active: live commitment, generates occurrences and matches. paused: temporarily not billing (a suspended subscription), kept out of committed-money totals. ended: finished for good, retained so historical cycles still reconcile"),
		field.Time("ended_on").
			Optional().
			Nillable().
			Comment("Date the commitment stopped, for status=ended: occurrences after it are not expected, so an absence past this date is not a missed bill. Null while active (null and the zero time are distinct here, so nillable)"),
		field.Text("notes").
			Optional().
			Comment("Free text for the human: the policy number, the renewal quirk, why the price changed"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the RecurringBill. The account FK lives here and is OPTIONAL: a bill may be
// paid from whichever card is to hand, and requiring it would let account discovery block
// data entry. When set it narrows auto-match scope.
func (RecurringBill) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("recurring_bills").
			Unique(),
		edge.To("payments", BillPayment.Type),
	}
}

// Indexes of the RecurringBill. name is the human key the admin UI edits by, and a
// duplicate is always a mistake, so it carries the unique index.
func (RecurringBill) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
	}
}
