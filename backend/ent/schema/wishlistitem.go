package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// WishlistItem is one thing the owner wants to buy or pay for ONCE: a pending car
// service payment, a new pair of glasses, a bag, a computer. It is not a bill and not
// a budget: recurring obligations live in their own entity, and nothing here is
// reconciled against actual spend. Items are never deleted on resolution, they move to
// status=bought or status=abandoned, because the history ("I decided against this
// already") is the point. Its reason to exist is LLM context: the read side feeds the
// list_wishlist MCP tool so a model can weigh a new purchase against what is already
// queued. See portfolio-site#123 and ADR 0017.
type WishlistItem struct {
	ent.Schema
}

// Fields of the WishlistItem.
func (WishlistItem) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(200).
			Comment(`What the thing is, short enough to scan in a list, e.g. "new glasses" or "car service"`),
		field.Text("description").
			Optional().
			Comment("Longer free-form note: why it is wanted, which model, what was already ruled out"),
		field.Float("amount").
			Optional().
			Nillable().
			Comment("Expected cost, positive. Nil means the price is unknown (NOT free): the read layer counts these separately instead of summing them as zero"),
		field.Bool("amount_is_estimate").
			Default(true).
			Comment("True while amount is a guess; flipped off once there is a real quoted price. Defaults true because most entries start as a rough number"),
		field.String("currency").
			Default("AUD").
			Comment("ISO currency code; defaults to AUD, matching Account.currency"),
		field.Enum("priority").
			Values("low", "medium", "high").
			Default("medium").
			Comment("Coarse bucket, not a strict rank: an LLM orders on this plus amount and deadline (see the issue for why not an integer rank)"),
		field.Enum("status").
			Values("wanted", "bought", "abandoned").
			Default("wanted").
			Comment("Lifecycle. Only wanted rows answer \"what do I still want\"; bought/abandoned stay for history and are never deleted"),
		field.Time("deadline").
			Optional().
			Nillable().
			Comment("Soft \"want it by\" date, normalized to UTC midnight in Go before writing. Nil means no date and implies no urgency; nothing enforces it"),
		field.Time("resolved_at").
			Optional().
			Nillable().
			Comment("When status last left wanted (bought or abandoned); nil while still wanted. Stamped server-side so the UI cannot forget it"),
		field.String("link").
			Optional().
			Comment("Product/reference URL. Never fetched by the backend; it is a note to self and a hint for the model"),
		field.String("image_key").
			Optional().
			Comment("Single S3 object key for the item's picture, uploaded via the presigned direct-to-S3 path (see internal/api/admin_uploads.go)"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the WishlistItem. None: a want is standalone, not tied to an account or to
// the transaction that eventually paid for it.
func (WishlistItem) Edges() []ent.Edge {
	return nil
}
