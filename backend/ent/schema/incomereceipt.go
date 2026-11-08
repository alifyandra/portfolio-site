package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// IncomeReceipt links one RecurringIncome cycle to the posted Transaction that paid it,
// the way a BillPayment does for a bill. It carries no amount: the expectation lives on
// the income and the actual figure lives on the transaction, so the two can never drift
// apart. "Is this credit regular?" is answered by this link, never by a flag on the
// transaction (ADR 0019).
//
// method records who made the link. It defaults to manual, which is what every row made
// before the matcher existed already was.
type IncomeReceipt struct {
	ent.Schema
}

// Fields of the IncomeReceipt.
func (IncomeReceipt) Fields() []ent.Field {
	return []ent.Field{
		field.Time("occurrence_date").
			Comment("The derived pay date (UTC midnight) this receipt settles, NOT the posted date: it is the cycle identity, so one cycle is received once"),
		field.Enum("method").
			Values("manual", "auto").
			Default("manual").
			Comment("manual: the owner linked it by hand. auto: the matching pass found it, or it was evidence on an accepted Recurrence Suggestion. The pass never touches a settled cycle of either kind"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the IncomeReceipt. Both FKs live here; both required, since a link row with
// either side missing is meaningless.
func (IncomeReceipt) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("income", RecurringIncome.Type).
			Ref("receipts").
			Unique().
			Required(),
		edge.From("transaction", Transaction.Type).
			Ref("income_receipts").
			Unique().
			Required(),
	}
}

// Indexes of the IncomeReceipt. (income, occurrence_date) so one cycle is received at
// most once, and the transaction alone so one posted credit settles at most one receipt
// across EVERY income: a credit is one payment, so it cannot pay two cycles, nor two
// incomes. The transaction index replaces an earlier (income, transaction) one, which it
// implies; the table had no rows when it changed.
func (IncomeReceipt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("occurrence_date").Edges("income").Unique(),
		index.Edges("transaction").Unique(),
	}
}
