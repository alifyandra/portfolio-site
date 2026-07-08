package api

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
)

// Cache TTLs are deliberately longer than their refresh intervals (see
// spotify_refresher.go) so a transient Spotify hiccup on one refresh doesn't
// blank the panel — the last good value survives until the next success. The
// TTL is just a safety net for a dead refresher; the poller is what keeps the
// data fresh.
const (
	nowPlayingCacheKey = "spotify:now-playing"
	nowPlayingCacheTTL = 10 * time.Minute // refreshed every 60s; long TTL rides out a backoff/refresher gap
	topTracksCacheKey  = "spotify:top-tracks"
	topTracksCacheTTL  = 48 * time.Hour // refreshed daily
	topTracksLimit     = 5
	topTracksRange     = "short_term" // ~4 weeks, matches "lately"
	playlistsCacheKey  = "spotify:playlists"
	playlistsCacheTTL  = 48 * time.Hour // refreshed daily
	topArtistsCacheKey = "spotify:top-artists"
	topArtistsCacheTTL = 48 * time.Hour // refreshed daily
	topArtistsLimit    = 12
)

type trackBody struct {
	IsPlaying  bool     `json:"is_playing"`
	Title      string   `json:"title,omitempty"`
	Artists    []string `json:"artists,omitempty"`
	Album      string   `json:"album,omitempty"`
	AlbumImage string   `json:"album_image,omitempty"`
	SongURL    string   `json:"song_url,omitempty"`
}

func trackBodyFrom(t *spotify.Track) trackBody {
	return trackBody{
		IsPlaying:  t.IsPlaying,
		Title:      t.Title,
		Artists:    t.Artists,
		Album:      t.Album,
		AlbumImage: t.AlbumImage,
		SongURL:    t.SongURL,
	}
}

type nowPlayingOutput struct {
	Body struct {
		// Track is the live or most-recent track, or null when neither exists.
		Track *trackBody `json:"track,omitempty"`
		// Source is "now-playing" when live, "recently-played" for the fallback,
		// or "" when neither is available. Lets the frontend label the panel
		// ("Now playing" vs "Last played") without re-deriving from is_playing.
		Source string `json:"source"`
	}
}

type topTracksOutput struct {
	Body struct {
		Tracks []trackBody `json:"tracks"`
	}
}

type playlistBody struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
	URL   string `json:"url,omitempty"`
}

type playlistsOutput struct {
	Body struct {
		Playlists []playlistBody `json:"playlists"`
	}
}

type artistBody struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
	URL   string `json:"url,omitempty"`
}

type topArtistsOutput struct {
	Body struct {
		Artists []artistBody `json:"artists"`
	}
}

// The Spotify handlers are read-only: they serve whatever the SpotifyRefresher
// (see spotify_refresher.go) last wrote to Redis and NEVER call Spotify
// themselves. This keeps visitor traffic fully decoupled from the Spotify API —
// no per-request fetch, no cache stampede. A cold cache (just after boot, before
// the first refresh) returns an empty-but-valid body; the panel fills within a
// tick.
func (h *Handler) registerSpotify(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-spotify-now-playing",
		Method:      http.MethodGet,
		Path:        "/api/spotify/now-playing",
		Summary:     "Get Alif's currently-playing track, falling back to most recent",
		Tags:        []string{"spotify"},
	}, func(ctx context.Context, _ *struct{}) (*nowPlayingOutput, error) {
		out := &nowPlayingOutput{}
		h.readSpotifyCache(ctx, nowPlayingCacheKey, &out.Body)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-spotify-top-tracks",
		Method:      http.MethodGet,
		Path:        "/api/spotify/top-tracks",
		Summary:     "Get Alif's top tracks (~6 month window)",
		Tags:        []string{"spotify"},
	}, func(ctx context.Context, _ *struct{}) (*topTracksOutput, error) {
		out := &topTracksOutput{}
		out.Body.Tracks = []trackBody{}
		h.readSpotifyCache(ctx, topTracksCacheKey, &out.Body)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-spotify-playlists",
		Method:      http.MethodGet,
		Path:        "/api/spotify/playlists",
		Summary:     "Get Alif's public Spotify playlists",
		Tags:        []string{"spotify"},
	}, func(ctx context.Context, _ *struct{}) (*playlistsOutput, error) {
		out := &playlistsOutput{}
		out.Body.Playlists = []playlistBody{}
		h.readSpotifyCache(ctx, playlistsCacheKey, &out.Body)
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-spotify-top-artists",
		Method:      http.MethodGet,
		Path:        "/api/spotify/top-artists",
		Summary:     "Get Alif's top artists (~4 week window)",
		Tags:        []string{"spotify"},
	}, func(ctx context.Context, _ *struct{}) (*topArtistsOutput, error) {
		out := &topArtistsOutput{}
		out.Body.Artists = []artistBody{}
		h.readSpotifyCache(ctx, topArtistsCacheKey, &out.Body)
		return out, nil
	})
}

// readSpotifyCache unmarshals the cached value at key into dst. A miss or decode
// error leaves dst at its zero/default value (an empty-but-valid body), so the
// handler degrades gracefully when the cache is cold.
func (h *Handler) readSpotifyCache(ctx context.Context, key string, dst any) {
	cached, err := h.deps.Redis.Get(ctx, key).Bytes()
	if err != nil {
		return
	}
	_ = json.Unmarshal(cached, dst)
}
