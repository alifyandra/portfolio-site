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

const spotifyCacheKey = "spotify:now-playing"
const spotifyCacheTTL = 30 * time.Second

type nowPlayingOutput struct {
	Body struct {
		IsPlaying bool     `json:"is_playing"`
		Title     string   `json:"title,omitempty"`
		Artists   []string `json:"artists,omitempty"`
		Album     string   `json:"album,omitempty"`
		AlbumImage string  `json:"album_image,omitempty"`
		SongURL   string   `json:"song_url,omitempty"`
	}
}

func (h *Handler) registerSpotify(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-spotify-now-playing",
		Method:      http.MethodGet,
		Path:        "/api/spotify/now-playing",
		Summary:     "Get Alif's currently-playing Spotify track",
		Tags:        []string{"spotify"},
	}, func(ctx context.Context, _ *struct{}) (*nowPlayingOutput, error) {
		out := &nowPlayingOutput{}

		// Serve from cache when warm (Redis is cache-only; see ADR 0007).
		if cached, err := h.deps.Redis.Get(ctx, spotifyCacheKey).Bytes(); err == nil {
			if json.Unmarshal(cached, &out.Body) == nil {
				return out, nil
			}
		}

		track, err := h.deps.Spotify.NowPlaying(ctx)
		if errors.Is(err, spotify.ErrNotConfigured) {
			// Local dev without Spotify creds: report "not playing" gracefully.
			return out, nil
		}
		if err != nil {
			return nil, huma.Error502BadGateway("failed to reach Spotify", err)
		}

		if track != nil {
			out.Body.IsPlaying = track.IsPlaying
			out.Body.Title = track.Title
			out.Body.Artists = track.Artists
			out.Body.Album = track.Album
			out.Body.AlbumImage = track.AlbumImage
			out.Body.SongURL = track.SongURL
		}

		if b, err := json.Marshal(out.Body); err == nil {
			_ = h.deps.Redis.Set(ctx, spotifyCacheKey, b, spotifyCacheTTL).Err()
		}
		return out, nil
	})
}
