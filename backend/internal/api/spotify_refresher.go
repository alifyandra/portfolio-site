package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
)

// Refresh cadence. now-playing changes constantly so it polls often; top
// tracks/artists/playlists barely move, so they refresh hourly. Both are well
// inside their cache TTLs (see spotify.go), so a missed tick never blanks the
// panel — stale data just lingers until the next successful refresh.
const (
	nowPlayingRefreshInterval = 30 * time.Second
	slowRefreshInterval       = time.Hour
)

// SpotifyRefresher periodically pulls Alif's Spotify data into Redis on the
// backend's own schedule. The HTTP handlers then only ever READ this cache, so
// visitor traffic never reaches Spotify: no per-request fetch on the response
// path, and no cache stampede when a key expires. This is the deliberate split
// from the old cache-aside handlers (see ADR 0008).
type SpotifyRefresher struct {
	redis *redis.Client
	sp    *spotify.Client
}

// NewSpotifyRefresher builds a refresher. A nil Spotify client (local dev
// without creds) makes Run a no-op.
func NewSpotifyRefresher(rdb *redis.Client, sp *spotify.Client) *SpotifyRefresher {
	return &SpotifyRefresher{redis: rdb, sp: sp}
}

// Run warms the cache once, then refreshes on a timer until ctx is cancelled.
// It blocks, so call it in a goroutine with the server's shutdown context.
func (r *SpotifyRefresher) Run(ctx context.Context) {
	if r.sp == nil || r.redis == nil || !r.sp.Configured() {
		slog.Info("spotify refresher disabled (no credentials)")
		return
	}

	// Warm immediately so the panel isn't empty for a full interval after boot.
	r.refreshNowPlaying(ctx)
	r.refreshSlow(ctx)

	nowTick := time.NewTicker(nowPlayingRefreshInterval)
	slowTick := time.NewTicker(slowRefreshInterval)
	defer nowTick.Stop()
	defer slowTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-nowTick.C:
			r.refreshNowPlaying(ctx)
		case <-slowTick.C:
			r.refreshSlow(ctx)
		}
	}
}

func (r *SpotifyRefresher) refreshSlow(ctx context.Context) {
	r.refreshTopTracks(ctx)
	r.refreshTopArtists(ctx)
	r.refreshPlaylists(ctx)
}

func (r *SpotifyRefresher) refreshNowPlaying(ctx context.Context) {
	var out nowPlayingOutput

	track, err := r.sp.NowPlaying(ctx)
	if errors.Is(err, spotify.ErrNotConfigured) {
		return
	}
	if err != nil {
		slog.Warn("spotify refresh now-playing", "err", err)
		return
	}

	if track != nil {
		tb := trackBodyFrom(track)
		out.Body.Track = &tb
		out.Body.Source = "now-playing"
	} else {
		// Nothing live — fall back to most recently played so the panel is
		// never dead.
		recent, err := r.sp.RecentlyPlayed(ctx)
		if err != nil {
			slog.Warn("spotify refresh recently-played", "err", err)
			return
		}
		if recent != nil {
			tb := trackBodyFrom(recent)
			out.Body.Track = &tb
			out.Body.Source = "recently-played"
		}
	}

	r.write(ctx, nowPlayingCacheKey, out.Body, nowPlayingCacheTTL)
}

func (r *SpotifyRefresher) refreshTopTracks(ctx context.Context) {
	var out topTracksOutput
	out.Body.Tracks = []trackBody{}

	tracks, err := r.sp.TopTracks(ctx, topTracksLimit, topTracksRange)
	if errors.Is(err, spotify.ErrNotConfigured) {
		return
	}
	if err != nil {
		slog.Warn("spotify refresh top-tracks", "err", err)
		return
	}

	for _, t := range tracks {
		tt := t
		out.Body.Tracks = append(out.Body.Tracks, trackBodyFrom(&tt))
	}

	r.write(ctx, topTracksCacheKey, out.Body, topTracksCacheTTL)
}

func (r *SpotifyRefresher) refreshTopArtists(ctx context.Context) {
	var out topArtistsOutput
	out.Body.Artists = []artistBody{}

	artists, err := r.sp.TopArtists(ctx, topArtistsLimit, topTracksRange)
	if errors.Is(err, spotify.ErrNotConfigured) {
		return
	}
	if err != nil {
		slog.Warn("spotify refresh top-artists", "err", err)
		return
	}

	for _, a := range artists {
		out.Body.Artists = append(out.Body.Artists, artistBody{
			Name:  a.Name,
			Image: a.Image,
			URL:   a.URL,
		})
	}

	r.write(ctx, topArtistsCacheKey, out.Body, topArtistsCacheTTL)
}

func (r *SpotifyRefresher) refreshPlaylists(ctx context.Context) {
	var out playlistsOutput
	out.Body.Playlists = []playlistBody{}

	for _, id := range featuredPlaylistIDs {
		p, err := r.sp.PlaylistByID(ctx, id)
		if errors.Is(err, spotify.ErrNotConfigured) {
			return
		}
		if err != nil {
			// Keep the existing cache rather than writing a partial list.
			slog.Warn("spotify refresh playlists", "id", id, "err", err)
			return
		}
		out.Body.Playlists = append(out.Body.Playlists, playlistBody{
			Name:  p.Name,
			Image: p.Image,
			URL:   p.URL,
		})
	}

	r.write(ctx, playlistsCacheKey, out.Body, playlistsCacheTTL)
}

// write marshals body and stores it under key with the given TTL. The TTL is a
// safety net: if the refresher dies, stale data expires instead of lingering
// forever.
func (r *SpotifyRefresher) write(ctx context.Context, key string, body any, ttl time.Duration) {
	b, err := json.Marshal(body)
	if err != nil {
		slog.Warn("spotify cache marshal", "key", key, "err", err)
		return
	}
	if err := r.redis.Set(ctx, key, b, ttl).Err(); err != nil {
		slog.Warn("spotify cache set", "key", key, "err", err)
	}
}
