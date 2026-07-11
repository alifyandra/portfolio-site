package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// PendingTransaction is a volatile, not-yet-settled ledger entry. Pending is a
// disposable side-set: each ingest run REPLACES an account's whole pending set
// (delete then insert), so there is no idempotency key and no updated_at. A
// pending row that later posts reappears in Transaction via the overlap window and
// drops out here. See the ingest contract (portfolio-site#84).
type PendingTransaction struct {
	ent.Schema
}

// Fields of the PendingTransaction.
func (PendingTransaction) Fields() []ent.Field {
	return []ent.Field{
		field.Time("date").
			Comment("The pending transaction's date, normalized to UTC midnight in Go before writing"),
		field.Float("amount").
			Comment("Signed amount: money out negative, money in positive (see the ingest contract)"),
		field.String("amount_raw").
			Optional().
			Comment("The bank's original rendering of the amount"),
		field.Text("description").
			Comment("Transaction narrative as the bank provides it"),
		field.String("merchant").
			Optional().
			Comment("Parsed merchant name when available"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
	}
}

// Edges of the PendingTransaction. The FK lives here (on pending_transactions).
func (PendingTransaction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("pending_transactions").
			Unique().
			Required(),
	}
}
