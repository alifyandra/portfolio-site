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
// There is no method column yet because every receipt is made by hand. When an
// automatic pass arrives it adds method with a "manual" default, which is what every
// existing row already is.
type IncomeReceipt struct {
	ent.Schema
}

// Fields of the IncomeReceipt.
func (IncomeReceipt) Fields() []ent.Field {
	return []ent.Field{
		field.Time("occurrence_date").
			Comment("The derived pay date (UTC midnight) this receipt settles, NOT the posted date: it is the cycle identity, so one cycle is received once"),
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
// most once, and (income, transaction) so one credit cannot settle two cycles of the
// same income.
func (IncomeReceipt) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("occurrence_date").Edges("income").Unique(),
		index.Edges("income", "transaction").Unique(),
	}
}
