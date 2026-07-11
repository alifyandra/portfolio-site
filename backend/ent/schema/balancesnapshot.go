package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// BalanceSnapshot is an append-only point-in-time reading of an account's balance.
// Balances are the bank's authoritative figures, never derived from summing
// transactions, and each ingest run appends one snapshot per account that carries
// a balance (a second run legitimately adds a second snapshot). See the ingest
// contract (portfolio-site#84).
type BalanceSnapshot struct {
	ent.Schema
}

// Fields of the BalanceSnapshot.
func (BalanceSnapshot) Fields() []ent.Field {
	return []ent.Field{
		field.Float("balance").
			Comment("The bank's authoritative balance figure; never derived from transactions (see the ingest contract)"),
		field.Float("available").
			Optional().
			Nillable().
			Comment("Available (spendable) balance when the bank supplies one; null otherwise"),
		field.Float("credit_limit").
			Optional().
			Nillable().
			Comment("Credit limit for a credit_card account; null otherwise"),
		field.Time("as_of").
			Comment("The full timestamp the bank reported the balance at (RFC3339, stored verbatim, NOT date-only normalized): it is the intra-day reading time and, with the account, the idempotency key"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("Row insertion time; orders snapshots independently of as_of"),
	}
}

// Edges of the BalanceSnapshot. The FK lives here (on balance_snapshots).
func (BalanceSnapshot) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("account", Account.Type).
			Ref("balance_snapshots").
			Unique().
			Required(),
	}
}

// Indexes of the BalanceSnapshot. Unique on (account, as_of) so a redelivered
// identical run (same reading time) dedups via OnConflict...DoNothing and stays
// idempotent, while a genuinely later reading (distinct as_of) appends a new row.
func (BalanceSnapshot) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("as_of").Edges("account").Unique(),
	}
}
