package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// RecurrenceSuggestion is a proposal, derived from history, that a run of posted
// transactions is one Recurring Bill or Recurring Income: the same counterparty, a
// near-constant amount and a near-constant interval, with the transactions attached as
// evidence (ADR 0019). The detection pass only ever SUGGESTS; a declaration is created
// when the owner accepts, never by the pass, because declarations are owner-authored. A
// dismissal is kept so the same run is not offered again.
//
// A suggestion is not a declaration and feeds nothing (no totals, no Projection) until it
// is accepted. Pending rows are derived and the pass refreshes or drops them; only the
// owner's decisions (accepted, dismissed) are durable.
type RecurrenceSuggestion struct {
	ent.Schema
}

// Fields of the RecurrenceSuggestion.
func (RecurrenceSuggestion) Fields() []ent.Field {
	return []ent.Field{
		field.String("fingerprint").
			NotEmpty().
			Unique().
			Immutable().
			Comment("SHA-256 hex over (kind, account, counterparty, cadence): the identity of 'the same run'. A re-scan refreshes the pending row with this fingerprint and never re-offers a dismissed one"),
		field.Enum("kind").
			Values("bill", "income").
			Immutable().
			Comment("What accepting creates: bill for a run of debits, income for a run of credits"),
		field.String("counterparty").
			NotEmpty().
			Immutable().
			Comment("The normalised key the run was grouped by: the parsed merchant when the bank gave one, else the description with digits, punctuation, month names, trailing place names and the bank's own words (direct, credit, transfer...) removed; a merchant-less row whose name matches a parsed merchant takes that merchant's key"),
		field.String("label").
			NotEmpty().
			Comment("Suggested name for the declaration, at most 120 bytes; the owner may replace it when accepting"),
		field.String("match_pattern").
			Optional().
			Comment("Suggested match pattern: text that every evidence row's description or merchant contains, case-insensitively. Empty when the run shares no distinctive text, and the declaration is then reconciled by hand"),
		field.Enum("cadence").
			Values("weekly", "fortnightly", "monthly", "quarterly", "annual").
			Immutable().
			Comment("The cadence the run's intervals fit; the same value set as RecurringBill.cadence"),
		field.Float("expected_amount").
			Positive().
			Comment("The median magnitude of the evidence, positive like RecurringBill.expected_amount"),
		field.Time("anchor_date").
			Comment("The pay or due date grid that best fits the evidence, placed on the oldest run's cycle so every evidence row settles a cycle on or after it (UTC midnight)"),
		field.Enum("status").
			Values("pending", "accepted", "dismissed").
			Default("pending").
			Comment("pending: awaiting the owner, and refreshed or dropped by each scan. accepted: the owner created the declaration. dismissed: the owner said no, remembered so the run is not offered again"),
		field.Time("decided_at").
			Optional().
			Nillable().
			Comment("When the owner accepted or dismissed it; null while pending"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the RecurrenceSuggestion. The account is the one every evidence row posted to
// (a run never spans accounts). evidence is many-to-many through a join table, so it adds
// no column to transactions. bill and income record what accepting created; both FKs
// live here and are optional, so deleting that declaration later only clears the link.
func (RecurrenceSuggestion) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("recurrence_suggestions").
			Unique(),
		edge.To("evidence", Transaction.Type),
		edge.From("bill", RecurringBill.Type).
			Ref("suggestions").
			Unique(),
		edge.From("income", RecurringIncome.Type).
			Ref("suggestions").
			Unique(),
	}
}
