package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Transaction is one posted (settled) ledger entry. Posted rows are an immutable
// ledger: the ingest upserts by dedup_hash and never mutates an existing row, so a
// re-ingested overlap window is a no-op. See the ingest contract (portfolio-site#84).
type Transaction struct {
	ent.Schema
}

// Fields of the Transaction.
func (Transaction) Fields() []ent.Field {
	return []ent.Field{
		field.String("dedup_hash").
			NotEmpty().
			Unique().
			Comment("Server-computed SHA-256 idempotency key over the posted fields (see internal/finance.DedupHash); the immutable-ledger upsert conflicts on this column, so it carries the unique index"),
		field.Time("posted_date").
			Comment("The day this transaction posted, normalized to UTC midnight in Go before writing (like Digest.date)"),
		field.Float("amount").
			Comment("Signed amount: money out negative, money in positive, uniform across account types (see the ingest contract)"),
		field.String("amount_raw").
			Optional().
			Comment(`The bank's original rendering of the amount, e.g. "$42.10"`),
		field.Text("description").
			Comment("Transaction narrative as the bank provides it"),
		field.String("merchant").
			Optional().
			Comment("Parsed merchant name when available"),
		field.Float("balance_after").
			Optional().
			Nillable().
			Comment("Running balance after this transaction if the bank supplies one; null otherwise (a null and a 0 are distinct, so it is nillable)"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Transaction. The FK lives here (on transactions), not on accounts.
func (Transaction) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("transactions").
			Unique().
			Required(),
	}
}
