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
// tracks/artists/playlists barely move, so they refresh daily. Both are well
// inside their cache TTLs (see spotify.go), so a missed tick never blanks the
// panel — stale data just lingers until the next successful refresh.
const (
	nowPlayingRefreshInterval = 60 * time.Second
	slowRefreshInterval       = 24 * time.Hour
	// defaultBackoff is the minimum 429 backoff. We honor Retry-After when it's
	// larger (it can be hours during a penalty), but never back off for less than
	// this — a small Retry-After would otherwise let us re-poll within seconds and
	// re-trigger/escalate the penalty. Also used when the header is absent.
	defaultBackoff = 5 * time.Minute
)

// SpotifyRefresher periodically pulls Alif's Spotify data into Redis on the
// backend's own schedule. The HTTP handlers then only ever READ this cache, so
// visitor traffic never reaches Spotify: no per-request fetch on the response
// path, and no cache stampede when a key expires. This is the deliberate split
// from the old cache-aside handlers (see ADR 0008).
type SpotifyRefresher struct {
	redis *redis.Client
	sp    *spotify.Client

	// lastTrack is the most recent track we saw playing live. When nothing is
	// live we re-show it as the "last played" view instead of calling Spotify's
	// recently-played endpoint — one fewer call per tick on the rate-limited
	// budget, and no second endpoint to get 429'd. Process-local: empty until the
	// first live track after boot. Only touched from Run's single goroutine.
	lastTrack *trackBody
	// backoffUntil is set from a 429's Retry-After; we skip hitting Spotify until
	// it passes so we don't re-trigger and escalate the penalty.
	backoffUntil time.Time
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

	// Recover the last-known track from Redis so a restart (e.g. a deploy) during
	// a long backoff doesn't lose it and let the panel expire to blank.
	r.hydrateLastTrack(ctx)

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
	// Inside a rate-limit backoff: don't touch Spotify, but keep the cache warm
	// with the last known track so the panel doesn't blank while we wait out the
	// penalty (the TTL would otherwise expire it).
	if time.Now().Before(r.backoffUntil) {
		r.writeLastKnown(ctx)
		return
	}

	track, err := r.sp.NowPlaying(ctx)
	if errors.Is(err, spotify.ErrNotConfigured) {
		return
	}
	var rl *spotify.RateLimitError
	if errors.As(err, &rl) {
		// Clamp to defaultBackoff: honor a longer Retry-After, but never re-poll
		// sooner than the floor even if Spotify hands back a small value.
		wait := rl.RetryAfter
		if wait < defaultBackoff {
			wait = defaultBackoff
		}
		r.backoffUntil = time.Now().Add(wait)
		slog.Warn("spotify rate limited; backing off", "endpoint", rl.Endpoint, "retry_after", wait)
		r.writeLastKnown(ctx)
		return
	}
	if err != nil {
		// Transient error: keep the last known track on screen rather than
		// letting the cache expire to empty.
		slog.Warn("spotify refresh now-playing", "err", err)
		r.writeLastKnown(ctx)
		return
	}

	if track != nil {
		tb := trackBodyFrom(track)
		r.lastTrack = &tb
		var out nowPlayingOutput
		out.Body.Track = &tb
		out.Body.Source = "now-playing"
		r.write(ctx, nowPlayingCacheKey, out.Body, nowPlayingCacheTTL)
		return
	}
	// Nothing live — re-show the last track we saw playing.
	r.writeLastKnown(ctx)
}

// writeLastKnown caches lastTrack as the idle "last played" view, refreshing the
// TTL. No-ops when we've never seen a live track, so it never blanks a warm
// cache. The source stays "recently-played" so the frontend's "Last played"
// label is unchanged — it now means "last track we saw live", not the
// recently-played endpoint.
func (r *SpotifyRefresher) writeLastKnown(ctx context.Context) {
	// Cold start (or just after a deploy): we haven't seen a live track yet, so
	// we have nothing to write. Leave any value already in Redis to ride its TTL
	// rather than clobbering a still-warm panel with an empty body.
	if r.lastTrack == nil {
		return
	}
	fallback := *r.lastTrack
	fallback.IsPlaying = false
	var out nowPlayingOutput
	out.Body.Track = &fallback
	out.Body.Source = "recently-played"
	r.write(ctx, nowPlayingCacheKey, out.Body, nowPlayingCacheTTL)
}

// hydrateLastTrack seeds lastTrack from whatever the previous process left in
// the now-playing cache. Without this, a restart wipes the in-memory last-known
// track, and an idle/backoff tick would then have nothing to keep warm until a
// live track is seen — blanking the panel once the old value's TTL expires.
func (r *SpotifyRefresher) hydrateLastTrack(ctx context.Context) {
	cached, err := r.redis.Get(ctx, nowPlayingCacheKey).Bytes()
	if err != nil {
		return
	}
	var body struct {
		Track *trackBody `json:"track"`
	}
	if err := json.Unmarshal(cached, &body); err != nil || body.Track == nil {
		return
	}
	t := *body.Track
	t.IsPlaying = false
	r.lastTrack = &t
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
