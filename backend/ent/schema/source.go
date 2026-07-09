package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// Source is a public, unauthenticated origin the Digest job reads from (an RSS
// feed or a web page). Read-only and credential-free, which keeps digest.build out
// of any per-user OAuth. See CONTEXT.md ("Source") and ADR 0013.
type Source struct {
	ent.Schema
}

// Fields of the Source.
func (Source) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").
			NotEmpty().
			MaxLen(120).
			Comment("Human label shown in the digest section header, e.g. \"Hacker News\""),
		field.String("url").
			NotEmpty().
			MaxLen(2048).
			Comment("The public URL fetched over plain HTTP; no credentials are ever attached"),
		field.Enum("type").
			Values("rss", "web").
			Default("web").
			Comment("Coarse content hint: an RSS/Atom feed or a plain web page"),
		field.Bool("active").
			Default(true).
			Comment("Only active Sources are fetched; flip false to retire one without deleting it"),
		field.Time("created_at").
			Default(time.Now).
			Immutable().
			Comment("Row creation time"),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			Comment("Last write time"),
	}
}

// Edges of the Source. None: the Digest job reads active Sources at run time, it
// does not relate them to a Digest row (see ADR 0013).
func (Source) Edges() []ent.Edge {
	return nil
}
