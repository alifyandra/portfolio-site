package spotify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// roundTripFunc lets a test stub every outbound request the Client makes,
// keyed off the request URL, without touching the real Spotify API.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// jsonResponse builds a minimal *http.Response with the given status, headers
// and body for a stubbed round-trip.
func jsonResponse(status int, body string, header http.Header) *http.Response {
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     header,
	}
}

// stubClient returns a Client whose transport answers the token endpoint with a
// canned access token and routes everything else to nowPlaying.
func stubClient(nowPlaying *http.Response) *Client {
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case strings.Contains(r.URL.Host, "accounts.spotify.com"):
			return jsonResponse(http.StatusOK, `{"access_token":"tok","expires_in":3600}`, nil), nil
		case strings.Contains(r.URL.Path, "currently-playing"):
			return nowPlaying, nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`, nil), nil
		}
	})
	return New("id", "secret", "refresh", WithHTTPClient(&http.Client{Transport: rt}))
}

func TestParseRetryAfter(t *testing.T) {
	future := time.Now().Add(2 * time.Hour).UTC().Format(http.TimeFormat)

	tests := []struct {
		name   string
		header string
		// want is checked loosely for the date case via wantApprox.
		want       time.Duration
		wantApprox bool
	}{
		{name: "delta seconds", header: "120", want: 120 * time.Second},
		{name: "zero", header: "0", want: 0},
		{name: "absent", header: "", want: 0},
		{name: "negative", header: "-5", want: 0},
		{name: "garbage", header: "soon", want: 0},
		{name: "http date in future", header: future, want: 2 * time.Hour, wantApprox: true},
		{name: "http date in past", header: "Mon, 02 Jan 2006 15:04:05 GMT", want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			if tt.header != "" {
				h.Set("Retry-After", tt.header)
			}
			got := parseRetryAfter(&http.Response{Header: h})
			if tt.wantApprox {
				// Allow a minute of slack for the now() delta in the date math.
				if got < tt.want-time.Minute || got > tt.want+time.Minute {
					t.Fatalf("parseRetryAfter(%q) = %s, want ~%s", tt.header, got, tt.want)
				}
				return
			}
			if got != tt.want {
				t.Fatalf("parseRetryAfter(%q) = %s, want %s", tt.header, got, tt.want)
			}
		})
	}
}

func TestStatusError_RateLimit(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "300")
	err := statusError(&http.Response{StatusCode: http.StatusTooManyRequests, Header: h}, "now-playing")

	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("statusError(429) = %v, want *RateLimitError", err)
	}
	if rl.Endpoint != "now-playing" {
		t.Errorf("Endpoint = %q, want now-playing", rl.Endpoint)
	}
	if rl.RetryAfter != 300*time.Second {
		t.Errorf("RetryAfter = %s, want 5m", rl.RetryAfter)
	}
	if !strings.Contains(rl.Error(), "now-playing") {
		t.Errorf("Error() = %q, want it to name the endpoint", rl.Error())
	}
}

func TestStatusError_GenericNotRateLimit(t *testing.T) {
	err := statusError(&http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{}}, "top-tracks")
	var rl *RateLimitError
	if errors.As(err, &rl) {
		t.Fatalf("statusError(500) = %v, want a non-RateLimitError", err)
	}
	if !strings.Contains(err.Error(), "top-tracks") || !strings.Contains(err.Error(), "500") {
		t.Errorf("Error() = %q, want endpoint and status", err.Error())
	}
}

func TestNowPlaying_NoContent(t *testing.T) {
	c := stubClient(jsonResponse(http.StatusNoContent, "", nil))
	track, err := c.NowPlaying(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if track != nil {
		t.Fatalf("got track %+v, want nil for 204", track)
	}
}

func TestNowPlaying_PausedTreatedAsNothingLive(t *testing.T) {
	// Spotify 200s with the paused track and is_playing:false; we treat that as
	// "nothing live" so the caller re-shows the last seen-live track.
	body := `{"is_playing":false,"item":{"name":"Paused Song"}}`
	c := stubClient(jsonResponse(http.StatusOK, body, nil))
	track, err := c.NowPlaying(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if track != nil {
		t.Fatalf("got track %+v, want nil for paused", track)
	}
}

func TestNowPlaying_Live(t *testing.T) {
	body := `{"is_playing":true,"item":{"name":"Live Song",
		"artists":[{"name":"A"},{"name":"B"}],
		"album":{"name":"Alb","images":[{"url":"http://img"}]},
		"external_urls":{"spotify":"http://song"}}}`
	c := stubClient(jsonResponse(http.StatusOK, body, nil))
	track, err := c.NowPlaying(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if track == nil {
		t.Fatal("got nil track, want a live track")
	}
	if !track.IsPlaying || track.Title != "Live Song" {
		t.Errorf("track = %+v, want IsPlaying live 'Live Song'", track)
	}
	if len(track.Artists) != 2 || track.Artists[0] != "A" {
		t.Errorf("artists = %v, want [A B]", track.Artists)
	}
	if track.AlbumImage != "http://img" || track.SongURL != "http://song" {
		t.Errorf("album/url = %q/%q", track.AlbumImage, track.SongURL)
	}
}

func TestNowPlaying_RateLimited(t *testing.T) {
	h := http.Header{}
	h.Set("Retry-After", "42")
	c := stubClient(jsonResponse(http.StatusTooManyRequests, "Too many requests", h))
	_, err := c.NowPlaying(context.Background())

	var rl *RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("NowPlaying on 429 = %v, want *RateLimitError", err)
	}
	if rl.RetryAfter != 42*time.Second {
		t.Errorf("RetryAfter = %s, want 42s", rl.RetryAfter)
	}
}

func TestNowPlaying_NotConfigured(t *testing.T) {
	c := New("", "", "") // no creds
	_, err := c.NowPlaying(context.Background())
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("NowPlaying without creds = %v, want ErrNotConfigured", err)
	}
}
