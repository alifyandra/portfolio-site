package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
)

const (
	nowPlayingCacheKey = "spotify:now-playing"
	nowPlayingCacheTTL = 30 * time.Second
	topTracksCacheKey  = "spotify:top-tracks"
	topTracksCacheTTL  = time.Hour
	topTracksLimit     = 5
	topTracksRange     = "short_term" // ~4 weeks, matches "lately"
	playlistsCacheKey  = "spotify:playlists"
	playlistsCacheTTL  = time.Hour
	topArtistsCacheKey = "spotify:top-artists"
	topArtistsCacheTTL = time.Hour
	topArtistsLimit    = 12
)

// featuredPlaylistIDs is the hand-curated list of playlists shown in the Music
// panel, in display order. These are the Spotify playlist IDs (the part after
// /playlist/ in the share URL). Edit this list to change what appears — it's
// the reliable way to control the set, since Spotify's API won't expose the
// "on my profile" playlists for this app.
var featuredPlaylistIDs = []string{
	"6PQYqQbW6AYd2QRiJImcJF",
	"3rE4pg8uhwsL1T0NhbLJnR",
	"3ZmZ7wHmzm2CvF9ZqyGuVs",
	"0dqCnKQCRLstotAISODoQO",
	"1fU0ZWngfA6A9t6Yh0uvCI",
	"13jonvKyZsTWcabIINLzWc",
}

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

func (h *Handler) registerSpotify(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-spotify-now-playing",
		Method:      http.MethodGet,
		Path:        "/api/spotify/now-playing",
		Summary:     "Get Alif's currently-playing track, falling back to most recent",
		Tags:        []string{"spotify"},
	}, func(ctx context.Context, _ *struct{}) (*nowPlayingOutput, error) {
		out := &nowPlayingOutput{}

		// Serve from cache when warm (Redis is cache-only; see ADR 0007).
		if cached, err := h.deps.Redis.Get(ctx, nowPlayingCacheKey).Bytes(); err == nil {
			if json.Unmarshal(cached, &out.Body) == nil {
				return out, nil
			}
		}

		track, err := h.deps.Spotify.NowPlaying(ctx)
		if errors.Is(err, spotify.ErrNotConfigured) {
			// Local dev without Spotify creds: report nothing gracefully.
			return out, nil
		}
		if err != nil {
			return nil, huma.Error502BadGateway("failed to reach Spotify", err)
		}

		if track != nil {
			tb := trackBodyFrom(track)
			out.Body.Track = &tb
			out.Body.Source = "now-playing"
		} else {
			// Nothing live — fall back to most recently played so the panel is
			// never dead.
			recent, err := h.deps.Spotify.RecentlyPlayed(ctx)
			if err != nil {
				return nil, huma.Error502BadGateway("failed to reach Spotify", err)
			}
			if recent != nil {
				tb := trackBodyFrom(recent)
				out.Body.Track = &tb
				out.Body.Source = "recently-played"
			}
		}

		if b, err := json.Marshal(out.Body); err == nil {
			_ = h.deps.Redis.Set(ctx, nowPlayingCacheKey, b, nowPlayingCacheTTL).Err()
		}
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

		if cached, err := h.deps.Redis.Get(ctx, topTracksCacheKey).Bytes(); err == nil {
			if json.Unmarshal(cached, &out.Body) == nil {
				return out, nil
			}
		}

		tracks, err := h.deps.Spotify.TopTracks(ctx, topTracksLimit, topTracksRange)
		if errors.Is(err, spotify.ErrNotConfigured) {
			return out, nil
		}
		if err != nil {
			return nil, huma.Error502BadGateway("failed to reach Spotify", err)
		}

		for _, t := range tracks {
			tt := t
			out.Body.Tracks = append(out.Body.Tracks, trackBodyFrom(&tt))
		}

		if b, err := json.Marshal(out.Body); err == nil {
			_ = h.deps.Redis.Set(ctx, topTracksCacheKey, b, topTracksCacheTTL).Err()
		}
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

		if cached, err := h.deps.Redis.Get(ctx, playlistsCacheKey).Bytes(); err == nil {
			if json.Unmarshal(cached, &out.Body) == nil {
				return out, nil
			}
		}

		for _, id := range featuredPlaylistIDs {
			p, err := h.deps.Spotify.PlaylistByID(ctx, id)
			if errors.Is(err, spotify.ErrNotConfigured) {
				return out, nil
			}
			if err != nil {
				return nil, huma.Error502BadGateway("failed to reach Spotify", err)
			}
			out.Body.Playlists = append(out.Body.Playlists, playlistBody{
				Name:  p.Name,
				Image: p.Image,
				URL:   p.URL,
			})
		}

		if b, err := json.Marshal(out.Body); err == nil {
			_ = h.deps.Redis.Set(ctx, playlistsCacheKey, b, playlistsCacheTTL).Err()
		}
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

		if cached, err := h.deps.Redis.Get(ctx, topArtistsCacheKey).Bytes(); err == nil {
			if json.Unmarshal(cached, &out.Body) == nil {
				return out, nil
			}
		}

		artists, err := h.deps.Spotify.TopArtists(ctx, topArtistsLimit, topTracksRange)
		if errors.Is(err, spotify.ErrNotConfigured) {
			return out, nil
		}
		if err != nil {
			return nil, huma.Error502BadGateway("failed to reach Spotify", err)
		}

		for _, a := range artists {
			out.Body.Artists = append(out.Body.Artists, artistBody{
				Name:  a.Name,
				Image: a.Image,
				URL:   a.URL,
			})
		}

		if b, err := json.Marshal(out.Body); err == nil {
			_ = h.deps.Redis.Set(ctx, topArtistsCacheKey, b, topArtistsCacheTTL).Err()
		}
		return out, nil
	})
}
