package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

// Playlist is a hand-curated Spotify playlist shown in the Music panel, in
// sort_order. It replaces the old hardcoded featuredPlaylistIDs list so the
// curated set is editable from the Admin Console (no redeploy). The
// SpotifyRefresher reads these IDs and hydrates the "spotify:playlists" cache.
// See ADR 0008.
type Playlist struct {
	ent.Schema
}

// Fields of the Playlist.
func (Playlist) Fields() []ent.Field {
	return []ent.Field{
		field.String("spotify_id").
			NotEmpty().
			Unique().
			Comment("Spotify playlist ID (the part after /playlist/ in the share URL)"),
		field.Int("sort_order").
			Default(0).
			Comment("lower sorts first"),
		field.Time("created_at").
			Default(time.Now).
			Immutable(),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now),
	}
}

// Edges of the Playlist.
func (Playlist) Edges() []ent.Edge {
	return nil
}
