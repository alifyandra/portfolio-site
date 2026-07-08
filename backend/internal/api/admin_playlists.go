package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/ent"
	"github.com/alifyandra/portfolio-site/backend/ent/playlist"
	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
)

// spotifyPlaylistIDPattern accepts Spotify's base62 IDs (alphanumeric).
var spotifyPlaylistIDPattern = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// PlaylistDTO is the frontend-facing shape of a curated Playlist.
type PlaylistDTO struct {
	ID        int    `json:"id"`
	SpotifyID string `json:"spotify_id"`
	SortOrder int    `json:"sort_order"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func toPlaylistDTO(p *ent.Playlist) PlaylistDTO {
	return PlaylistDTO{
		ID:        p.ID,
		SpotifyID: p.SpotifyID,
		SortOrder: p.SortOrder,
		CreatedAt: p.CreatedAt.UTC().Format(http.TimeFormat),
		UpdatedAt: p.UpdatedAt.UTC().Format(http.TimeFormat),
	}
}

// parseSpotifyPlaylistID extracts a bare playlist ID from any of: a bare ID, a
// spotify:playlist:{id} URI, or an open.spotify.com/playlist/{id}?... URL.
func parseSpotifyPlaylistID(input string) (string, bool) {
	s := strings.TrimSpace(input)
	if s == "" {
		return "", false
	}
	switch {
	case strings.HasPrefix(s, "spotify:playlist:"):
		s = strings.TrimPrefix(s, "spotify:playlist:")
	case strings.Contains(s, "/playlist/"):
		if u, err := url.Parse(s); err == nil && strings.Contains(u.Path, "/playlist/") {
			s = u.Path[strings.Index(u.Path, "/playlist/")+len("/playlist/"):]
		} else {
			s = s[strings.Index(s, "/playlist/")+len("/playlist/"):]
		}
	}
	// Trim any trailing query, fragment, or path segment.
	if i := strings.IndexAny(s, "?/#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" || !spotifyPlaylistIDPattern.MatchString(s) {
		return "", false
	}
	return s, true
}

type listPlaylistsOutput struct {
	Body struct {
		Playlists []PlaylistDTO `json:"playlists"`
	}
}

type createPlaylistInput struct {
	Body struct {
		Input string `json:"input" minLength:"1" doc:"A Spotify playlist share URL, a spotify:playlist:{id} URI, or a bare playlist ID"`
	}
}

type playlistOutput struct {
	Body PlaylistDTO
}

type playlistIDInput struct {
	ID int `path:"id" doc:"Playlist DB ID"`
}

type reorderPlaylistsInput struct {
	Body struct {
		Items []struct {
			ID        int `json:"id"`
			SortOrder int `json:"sort_order"`
		} `json:"items" doc:"New sort_order for each playlist id"`
	}
}

// refreshPlaylistsCache repopulates the cached playlists from the current Playlist
// table after a mutation, so the public Music panel reflects the change right away
// instead of blanking until the next slow refresher tick (up to slowRefreshInterval).
// It reuses the refresher's own logic: a Spotify error leaves the previous cache
// intact (never blanks it), and an empty table writes an empty list. Without a
// configured Spotify client (local dev, no creds) there is nothing to fetch, so it
// just drops the key, matching the disabled refresher. Best-effort: any failure is
// logged inside the refresher and does not fail the mutation.
func (h *Handler) refreshPlaylistsCache(ctx context.Context) {
	if h.deps.Redis == nil {
		return
	}
	if h.deps.Spotify == nil || !h.deps.Spotify.Configured() {
		_ = h.deps.Redis.Del(ctx, playlistsCacheKey).Err()
		return
	}
	NewSpotifyRefresher(h.deps.Redis, h.deps.Spotify, h.deps.Ent).refreshPlaylists(ctx)
}

func (h *Handler) registerAdminPlaylists(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-playlists",
		Method:      http.MethodGet,
		Path:        "/api/admin/playlists",
		Summary:     "List the curated Spotify playlists",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, _ *struct{}) (*listPlaylistsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		rows, err := h.deps.Ent.Playlist.Query().Order(ent.Asc(playlist.FieldSortOrder)).All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to list playlists", err)
		}
		out := &listPlaylistsOutput{}
		out.Body.Playlists = make([]PlaylistDTO, 0, len(rows))
		for _, p := range rows {
			out.Body.Playlists = append(out.Body.Playlists, toPlaylistDTO(p))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "create-playlist",
		Method:        http.MethodPost,
		Path:          "/api/admin/playlists",
		Summary:       "Add a curated Spotify playlist (accepts a URL or bare ID)",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, in *createPlaylistInput) (*playlistOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		id, ok := parseSpotifyPlaylistID(in.Body.Input)
		if !ok {
			return nil, huma.Error422UnprocessableEntity("could not parse a Spotify playlist ID from the input")
		}

		// Validate the ID against Spotify so a typo is rejected up front.
		if h.deps.Spotify == nil || !h.deps.Spotify.Configured() {
			return nil, huma.Error503ServiceUnavailable("spotify is not configured; cannot validate the playlist")
		}
		if _, err := h.deps.Spotify.PlaylistByID(ctx, id); err != nil {
			var rl *spotify.RateLimitError
			if errors.As(err, &rl) {
				return nil, huma.Error503ServiceUnavailable("spotify is rate limited; try again shortly")
			}
			return nil, huma.Error422UnprocessableEntity("that Spotify playlist ID is not valid or not reachable")
		}

		// Append at the end (max sort_order + 1) so a new entry does not reshuffle.
		next := 0
		if last, err := h.deps.Ent.Playlist.Query().Order(ent.Desc(playlist.FieldSortOrder)).First(ctx); err == nil {
			next = last.SortOrder + 1
		} else if !ent.IsNotFound(err) {
			return nil, huma.Error500InternalServerError("failed to compute sort order", err)
		}

		p, err := h.deps.Ent.Playlist.Create().SetSpotifyID(id).SetSortOrder(next).Save(ctx)
		if ent.IsConstraintError(err) {
			return nil, huma.Error409Conflict("that playlist is already curated")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to add playlist", err)
		}
		h.refreshPlaylistsCache(ctx)
		return &playlistOutput{Body: toPlaylistDTO(p)}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "delete-playlist",
		Method:        http.MethodDelete,
		Path:          "/api/admin/playlists/{id}",
		Summary:       "Remove a curated Spotify playlist",
		Tags:          adminTags,
		Security:      cookieAuthSecurity,
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *playlistIDInput) (*struct{}, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		err := h.deps.Ent.Playlist.DeleteOneID(in.ID).Exec(ctx)
		if ent.IsNotFound(err) {
			return nil, huma.Error404NotFound("playlist not found")
		}
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to delete playlist", err)
		}
		h.refreshPlaylistsCache(ctx)
		return &struct{}{}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "reorder-playlists",
		Method:      http.MethodPost,
		Path:        "/api/admin/playlists/reorder",
		Summary:     "Set sort_order for a set of curated playlists in one call",
		Tags:        adminTags,
		Security:    cookieAuthSecurity,
	}, func(ctx context.Context, in *reorderPlaylistsInput) (*listPlaylistsOutput, error) {
		if _, err := requireAdmin(ctx); err != nil {
			return nil, err
		}
		tx, err := h.deps.Ent.Tx(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to reorder playlists", err)
		}
		for _, it := range in.Body.Items {
			if err := tx.Playlist.UpdateOneID(it.ID).SetSortOrder(it.SortOrder).Exec(ctx); err != nil {
				_ = tx.Rollback()
				if ent.IsNotFound(err) {
					return nil, huma.Error404NotFound("playlist not found")
				}
				return nil, huma.Error500InternalServerError("failed to reorder playlists", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, huma.Error500InternalServerError("failed to reorder playlists", err)
		}
		h.refreshPlaylistsCache(ctx)
		rows, err := h.deps.Ent.Playlist.Query().Order(ent.Asc(playlist.FieldSortOrder)).All(ctx)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to load playlists", err)
		}
		out := &listPlaylistsOutput{}
		out.Body.Playlists = make([]PlaylistDTO, 0, len(rows))
		for _, p := range rows {
			out.Body.Playlists = append(out.Body.Playlists, toPlaylistDTO(p))
		}
		return out, nil
	})
}
