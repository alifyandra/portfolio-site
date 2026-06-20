package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/alifyandra/portfolio-site/backend/internal/spotify"
)

// --- test plumbing ---

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func httpResp(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}
}

// stubSpotify builds a Spotify client whose token call is canned and whose
// currently-playing call is supplied per test. npCalls (if non-nil) counts how
// many times currently-playing was hit, so a test can assert it was skipped.
func stubSpotify(npCalls *int, nowPlaying func() *http.Response) *spotify.Client {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Host, "accounts.spotify.com"):
			return httpResp(http.StatusOK, `{"access_token":"t","expires_in":3600}`, nil), nil
		case strings.Contains(r.URL.Path, "currently-playing"):
			if npCalls != nil {
				*npCalls++
			}
			return nowPlaying(), nil
		default:
			return httpResp(http.StatusNotFound, "{}", nil), nil
		}
	})
	return spotify.New("id", "secret", "refresh", spotify.WithHTTPClient(&http.Client{Transport: rt}))
}

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	mr := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: mr.Addr()})
}

type cachedNowPlaying struct {
	Track  *trackBody `json:"track"`
	Source string     `json:"source"`
}

func readNowPlaying(t *testing.T, rdb *redis.Client) cachedNowPlaying {
	t.Helper()
	b, err := rdb.Get(context.Background(), nowPlayingCacheKey).Bytes()
	if err != nil {
		t.Fatalf("cache get: %v", err)
	}
	var out cachedNowPlaying
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("cache unmarshal: %v", err)
	}
	return out
}

func livePlaying() *http.Response {
	return httpResp(http.StatusOK,
		`{"is_playing":true,"item":{"name":"Song A","artists":[{"name":"Artist A"}]}}`, nil)
}

// --- tests ---

func TestRefreshNowPlaying_Live(t *testing.T) {
	rdb := testRedis(t)
	r := &SpotifyRefresher{redis: rdb, sp: stubSpotify(nil, livePlaying)}

	r.refreshNowPlaying(context.Background())

	got := readNowPlaying(t, rdb)
	if got.Source != "now-playing" {
		t.Fatalf("source = %q, want now-playing", got.Source)
	}
	if got.Track == nil || got.Track.Title != "Song A" || !got.Track.IsPlaying {
		t.Fatalf("track = %+v, want live 'Song A'", got.Track)
	}
	if r.lastTrack == nil || r.lastTrack.Title != "Song A" {
		t.Fatalf("lastTrack = %+v, want it remembered", r.lastTrack)
	}
}

func TestRefreshNowPlaying_IdleReusesLastTrack(t *testing.T) {
	rdb := testRedis(t)
	// First a live track, then nothing playing (204) — the panel should re-show
	// the last live track as "recently-played", not blank.
	state := "live"
	sp := stubSpotify(nil, func() *http.Response {
		if state == "live" {
			return livePlaying()
		}
		return httpResp(http.StatusNoContent, "", nil)
	})
	r := &SpotifyRefresher{redis: rdb, sp: sp}

	r.refreshNowPlaying(context.Background()) // live -> remembers Song A
	state = "idle"
	r.refreshNowPlaying(context.Background()) // idle -> reuse

	got := readNowPlaying(t, rdb)
	if got.Source != "recently-played" {
		t.Fatalf("source = %q, want recently-played", got.Source)
	}
	if got.Track == nil || got.Track.Title != "Song A" {
		t.Fatalf("track = %+v, want last live 'Song A'", got.Track)
	}
	if got.Track.IsPlaying {
		t.Errorf("idle fallback should have is_playing=false")
	}
}

func TestWriteLastKnown_NilDoesNotClobberWarmCache(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	// A warm value from a previous process.
	warm := `{"track":{"title":"Old Song"},"source":"recently-played"}`
	if err := rdb.Set(ctx, nowPlayingCacheKey, warm, nowPlayingCacheTTL).Err(); err != nil {
		t.Fatal(err)
	}

	r := &SpotifyRefresher{redis: rdb, sp: stubSpotify(nil, livePlaying)} // lastTrack nil
	r.writeLastKnown(ctx)

	got := readNowPlaying(t, rdb)
	if got.Track == nil || got.Track.Title != "Old Song" {
		t.Fatalf("warm cache was clobbered: %+v", got)
	}
}

func TestRefreshNowPlaying_BackoffFlooredOnSmallRetryAfter(t *testing.T) {
	rdb := testRedis(t)
	h := http.Header{}
	h.Set("Retry-After", "2") // tiny — must be clamped up to the floor
	sp := stubSpotify(nil, func() *http.Response {
		return httpResp(http.StatusTooManyRequests, "Too many requests", h)
	})
	r := &SpotifyRefresher{redis: rdb, sp: sp}

	before := time.Now()
	r.refreshNowPlaying(context.Background())

	gotWait := r.backoffUntil.Sub(before)
	if gotWait < defaultBackoff-time.Second {
		t.Fatalf("backoff = %s, want clamped to >= %s (small Retry-After must not shorten it)", gotWait, defaultBackoff)
	}
}

func TestRefreshNowPlaying_BackoffHonorsLargeRetryAfter(t *testing.T) {
	rdb := testRedis(t)
	h := http.Header{}
	h.Set("Retry-After", "3600")
	sp := stubSpotify(nil, func() *http.Response {
		return httpResp(http.StatusTooManyRequests, "Too many requests", h)
	})
	r := &SpotifyRefresher{redis: rdb, sp: sp}

	before := time.Now()
	r.refreshNowPlaying(context.Background())

	gotWait := r.backoffUntil.Sub(before)
	if gotWait < time.Hour-time.Minute {
		t.Fatalf("backoff = %s, want it to honor the ~1h Retry-After", gotWait)
	}
}

func TestRefreshNowPlaying_SkipsSpotifyDuringBackoff(t *testing.T) {
	rdb := testRedis(t)
	calls := 0
	sp := stubSpotify(&calls, livePlaying)
	r := &SpotifyRefresher{
		redis:        rdb,
		sp:           sp,
		lastTrack:    &trackBody{Title: "Cached", IsPlaying: false},
		backoffUntil: time.Now().Add(time.Hour),
	}

	r.refreshNowPlaying(context.Background())

	if calls != 0 {
		t.Fatalf("currently-playing was hit %d times during backoff, want 0", calls)
	}
	// The cache is kept warm with the last known track instead of blanking.
	got := readNowPlaying(t, rdb)
	if got.Track == nil || got.Track.Title != "Cached" || got.Source != "recently-played" {
		t.Fatalf("cache during backoff = %+v, want warm 'Cached'", got)
	}
}

func TestHydrateLastTrack(t *testing.T) {
	rdb := testRedis(t)
	ctx := context.Background()
	seed := `{"track":{"title":"Seeded","is_playing":true},"source":"now-playing"}`
	if err := rdb.Set(ctx, nowPlayingCacheKey, seed, nowPlayingCacheTTL).Err(); err != nil {
		t.Fatal(err)
	}

	r := &SpotifyRefresher{redis: rdb, sp: stubSpotify(nil, livePlaying)}
	r.hydrateLastTrack(ctx)

	if r.lastTrack == nil || r.lastTrack.Title != "Seeded" {
		t.Fatalf("lastTrack = %+v, want hydrated 'Seeded'", r.lastTrack)
	}
	if r.lastTrack.IsPlaying {
		t.Errorf("hydrated track should be marked not playing")
	}
}

func TestHydrateLastTrack_EmptyCacheLeavesNil(t *testing.T) {
	rdb := testRedis(t)
	r := &SpotifyRefresher{redis: rdb, sp: stubSpotify(nil, livePlaying)}
	r.hydrateLastTrack(context.Background())
	if r.lastTrack != nil {
		t.Fatalf("lastTrack = %+v, want nil on cold cache", r.lastTrack)
	}
}
