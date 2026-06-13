package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// Project is a piece of work shown on the portfolio. See CONTEXT.md.
type Project struct {
	ent.Schema
}

// Fields of the Project.
func (Project) Fields() []ent.Field {
	return []ent.Field{
		field.String("slug").
			Unique().
			NotEmpty().
			Comment("URL-friendly identifier, e.g. \"openresim-analytics\""),
		field.String("title").
			NotEmpty(),
		field.String("summary").
			MaxLen(280).
			Comment("Short one-liner shown in cards/lists"),
		field.Text("description").
			Optional().
			Comment("Longer markdown body"),
		field.Strings("tags").
			Optional().
			Comment("Free-form tags, e.g. [\"Go\", \"Next.js\", \"AWS\"]"),
		field.String("repo_url").
			Optional(),
		field.String("live_url").
			Optional(),
		field.Strings("image_keys").
			Optional().
			Comment("S3 object keys for project images"),
		field.Bool("featured").
			Default(false),
		field.Int("sort_order").
			Default(0).
			Comment("Lower sorts first"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Project.
func (Project) Edges() []ent.Edge {
	return nil
}
