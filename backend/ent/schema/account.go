package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Account is one bank account the finance ingest tracks. Its (source, name) pair
// is the stable identity key transaction rows join on and the ingest upserts by,
// so a re-ingested window updates the same account instead of duplicating it. See
// the ingest contract (portfolio-site#84) and CONTEXT.md ("finance ingest").
//
// Two fields are owner-authored and ingest-immutable: description and
// drawdown_policy (portfolio-site#122). They exist only because the bank cannot
// know them; they are typed in once from the admin console and the ingest must
// never write them. The upsert conflict clause (internal/finance/ingest.go) sets an
// explicit column list precisely so these two are left alone.
type Account struct {
	ent.Schema
}

// Fields of the Account.
func (Account) Fields() []ent.Field {
	return []ent.Field{
		field.String("source").
			NotEmpty().
			Comment(`Broker/source that owns this account, e.g. "commbank"; the first half of the (source, name) identity key`),
		field.String("masked_number").
			Optional().
			Comment("Masked account number as the bank renders it; never the full number"),
		field.String("name").
			NotEmpty().
			Comment("Human label for the account and the join key transactions reference; the second half of the (source, name) identity key"),
		field.Enum("type").
			Values("everyday", "savings", "credit_card", "steppay", "investment").
			Comment("Account product type; drives dashboard grouping"),
		field.Enum("class").
			Values("asset", "liability").
			Comment("Balance interpretation for net-worth aggregation; never flips transaction signs (see the ingest contract)"),
		field.String("currency").
			Default("AUD").
			Comment("ISO currency code; defaults to AUD"),
		field.String("product_code").
			Optional().
			Comment("Bank product code when the source provides one"),
		field.Bool("guessed_type").
			Default(false).
			Comment("True when the classifier fell back or this row is an auto-created placeholder (a transaction-only run referenced an account not in accounts[]); a review flag"),
		field.Text("description").
			Optional().
			MaxLen(2000).
			Comment("Owner-authored note on what this account is FOR, in my own words; never written by the ingest. Surfaced to the MCP so an LLM can tell two structurally identical accounts apart"),
		field.Enum("drawdown_policy").
			Values("unset", "flexible", "no_drawdown", "emergency_only").
			Default("unset").
			Comment("Whether this balance is spendable: flexible => money moves in and out, no_drawdown => the balance must not fall, emergency_only => reachable but not for ordinary spend, unset => never labelled"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
		field.Time("posted_watermark").Optional().Nillable().
			Comment("Date (UTC-midnight) through which posted txns are known complete; nil => never synced => full backfill"),
	}
}

// Edges of the Account. The FKs live on the child rows (transactions, pending,
// snapshots, recurring bills), so an account owns four fan-out edges. The
// recurring_bills back-reference is optional on the bill side (a bill may be paid from
// whichever card is to hand), so it adds no column here.
func (Account) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("transactions", Transaction.Type),
		edge.To("pending_transactions", PendingTransaction.Type),
		edge.To("balance_snapshots", BalanceSnapshot.Type),
		edge.To("recurring_bills", RecurringBill.Type),
	}
}

// Indexes of the Account. The (source, name) identity key: unique so the ingest
// can upsert an account by it (OnConflict on these two columns), and never end up
// with two rows for the same real account.
func (Account) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source", "name").Unique(),
	}
}
