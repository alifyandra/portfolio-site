package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// RecurringIncome is a declared repeating receipt (salary, rent received, a regular
// transfer in): the same shape as a RecurringBill (cadence, anchor date, expected amount,
// optional account) in the opposite direction. It is its own entity rather than a signed
// bill because a bill is a commitment and income is not (ADR 0019). Like a bill it is
// owner-authored: nothing in the ingest or any detection pass writes one; accepting a
// Recurrence Suggestion is the owner creating it. The next
// expected date is NOT stored, it is derived from (cadence, anchor_date) on read.
type RecurringIncome struct {
	ent.Schema
}

// Fields of the RecurringIncome. The declaration plus one owner-typed match pattern for
// the auto-matcher (ReconcileIncome). Unlike a bill there is no per-income tolerance or
// window: the matcher uses fixed ones (see internal/finance/income_match.go), since pay
// is regular enough that one rule fits, and an absent column cannot drift.
func (RecurringIncome) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120).
			Comment(`Human label and the human identity key, e.g. "Salary"; unique, since two incomes with one name is always a data-entry mistake`),
		field.Float("expected_amount").
			Positive().
			Comment("Expected receipt per cycle as a POSITIVE magnitude in currency, unlike Transaction.amount which is signed"),
		field.String("currency").
			Default("AUD").
			Comment("ISO currency code; defaults to AUD, matching Account.currency"),
		field.Enum("cadence").
			Values("weekly", "fortnightly", "monthly", "quarterly", "annual").
			Default("monthly").
			Comment("How often the income recurs; the same value set as RecurringBill.cadence, so one occurrence generator serves both"),
		field.Time("anchor_date").
			Comment("One known pay date (UTC midnight). Every occurrence is derived by stepping the cadence from here, so this is the only date the schema stores"),
		field.String("match_pattern").
			Optional().
			MaxLen(200).
			Comment("Case-insensitive text matched on word boundaries against a posted credit's description OR merchant (\"ATO\" does not match \"POTATO\"); empty means the income is never auto-matched and every receipt is linked by hand. RecurringBill.match_pattern uses the same rule (containsToken in internal/finance/income_match.go)"),
		field.Enum("status").
			Values("active", "paused", "ended").
			Default("active").
			Comment("active: expected, counts toward the totals. paused: temporarily not arriving (unpaid leave), kept out of the totals. ended: finished for good, retained so historical cycles keep their receipts"),
		field.Time("ended_on").
			Optional().
			Nillable().
			Comment("Date the income stopped, for status=ended: no occurrence after it is expected. Null while active (null and the zero time are distinct here, so nillable)"),
		field.Text("notes").
			Optional().
			Comment("Free text for the human"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the RecurringIncome. The account FK lives here and is OPTIONAL, for the same
// reason as on a bill: requiring it would let account discovery block data entry.
func (RecurringIncome) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("recurring_incomes").
			Unique(),
		edge.To("receipts", IncomeReceipt.Type),
		// The suggestion this income was accepted from, if any; the FK lives on the
		// suggestion, so this adds no column here.
		edge.To("suggestions", RecurrenceSuggestion.Type),
	}
}

// Indexes of the RecurringIncome. name is the human key, and a duplicate is always a
// mistake, so it carries the unique index.
func (RecurringIncome) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("name").Unique(),
	}
}
