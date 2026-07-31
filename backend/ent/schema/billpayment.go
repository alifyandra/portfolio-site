package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// BillPayment links one RecurringBill cycle to the posted Transaction that paid it. It is
// the whole reconciliation record: expected lives on the bill, actual lives on the
// transaction, and this row is the join, so the two can never drift out of agreement. The
// unique indexes make the matching pass idempotent and re-runnable. See portfolio-site#125.
type BillPayment struct {
	ent.Schema
}

// Fields of the BillPayment.
func (BillPayment) Fields() []ent.Field {
	return []ent.Field{
		field.Time("occurrence_date").
			Comment("The derived due date (UTC midnight) this payment settles, NOT the posted date: it is the cycle identity, so one cycle can be paid once"),
		field.Enum("method").
			Values("auto", "manual").
			Default("auto").
			Comment("auto: the matching pass found it from match_pattern. manual: the owner linked it by hand, which the pass must never overwrite"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the BillPayment. Both FKs live here; both required, since a link row with
// either side missing is meaningless.
func (BillPayment) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("bill", RecurringBill.Type).
			Ref("payments").
			Unique().
			Required(),
		edge.From("transaction", Transaction.Type).
			Ref("bill_payments").
			Unique().
			Required(),
	}
}

// Indexes of the BillPayment. Two unique keys, both load-bearing for idempotency:
// (bill, occurrence_date) so one cycle is paid at most once, and (bill, transaction) so a
// re-run of the matching pass conflicts instead of inserting a duplicate link.
func (BillPayment) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("occurrence_date").Edges("bill").Unique(),
		index.Edges("bill", "transaction").Unique(),
	}
}
