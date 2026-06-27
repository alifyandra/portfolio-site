package spotify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client talks to the Spotify Web API using the Authorization Code refresh-token
// flow. The secret never leaves the backend (see CONTEXT.md "Spotify Proxy").
type Client struct {
	clientID     string
	clientSecret string
	refreshToken string
	http         *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// Option customizes a Client at construction.
type Option func(*Client)

// WithHTTPClient overrides the underlying *http.Client. Mainly a test seam for
// injecting a transport that stubs the Spotify API.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.http = hc }
}

// New constructs a Spotify client. Credentials may be empty in local dev, in
// which case calls return ErrNotConfigured.
func New(clientID, clientSecret, refreshToken string, opts ...Option) *Client {
	c := &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		refreshToken: refreshToken,
		http:         &http.Client{Timeout: 10 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// ErrNotConfigured is returned when Spotify credentials are absent.
var ErrNotConfigured = fmt.Errorf("spotify: not configured")

// RateLimitError is returned when Spotify responds 429. Endpoint names the call
// that hit the limit (so 429 logs stay as actionable as other status errors),
// and RetryAfter carries the Retry-After header (0 if absent) so callers can
// back off for exactly as long as Spotify asks instead of re-polling and
// escalating the penalty (Spotify grows Retry-After into the hours when an app
// keeps hammering through a 429).
type RateLimitError struct {
	Endpoint   string
	RetryAfter time.Duration
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("spotify %s: rate limited (retry after %s)", e.Endpoint, e.RetryAfter)
}

// statusError maps a non-OK Spotify response to an error. A 429 becomes a
// *RateLimitError carrying the endpoint label and Retry-After; anything else is
// a generic status error labelled with the calling endpoint.
func statusError(resp *http.Response, label string) error {
	if resp.StatusCode == http.StatusTooManyRequests {
		return &RateLimitError{Endpoint: label, RetryAfter: parseRetryAfter(resp)}
	}
	return fmt.Errorf("spotify %s: status %d", label, resp.StatusCode)
}

// parseRetryAfter reads the Retry-After header in either RFC 9110 form:
// delta-seconds (what Spotify sends) or an HTTP-date (in case a proxy ever uses
// it). Returns 0 when absent or unparseable, leaving the caller to apply its
// backoff floor.
func parseRetryAfter(resp *http.Response) time.Duration {
	v := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

// Track is the trimmed, frontend-facing shape we expose.
type Track struct {
	IsPlaying  bool     `json:"is_playing"`
	Title      string   `json:"title"`
	Artists    []string `json:"artists"`
	Album      string   `json:"album"`
	AlbumImage string   `json:"album_image,omitempty"`
	SongURL    string   `json:"song_url,omitempty"`
}

// rawTrack mirrors the slice of Spotify's track object we care about. Spotify
// returns this same shape under different keys (now-playing `item`,
// recently-played `items[].track`, top-tracks `items[]`), so we parse it once.
type rawTrack struct {
	Name         string `json:"name"`
	ExternalURLs struct {
		Spotify string `json:"spotify"`
	} `json:"external_urls"`
	Artists []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Album struct {
		Name   string `json:"name"`
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	} `json:"album"`
}

func (rt rawTrack) toTrack(isPlaying bool) *Track {
	t := &Track{
		IsPlaying: isPlaying,
		Title:     rt.Name,
		Album:     rt.Album.Name,
		SongURL:   rt.ExternalURLs.Spotify,
	}
	for _, a := range rt.Artists {
		t.Artists = append(t.Artists, a.Name)
	}
	if len(rt.Album.Images) > 0 {
		t.AlbumImage = rt.Album.Images[0].URL
	}
	return t
}

// get issues an authenticated GET to the Spotify Web API.
func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	if !c.configured() {
		return nil, ErrNotConfigured
	}
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	return c.http.Do(req)
}

func (c *Client) configured() bool {
	return c.clientID != "" && c.clientSecret != "" && c.refreshToken != ""
}

// Configured reports whether Spotify credentials are present. Lets callers (e.g.
// the background refresher) skip work entirely instead of hitting ErrNotConfigured.
func (c *Client) Configured() bool {
	return c.configured()
}

// token returns a valid access token, refreshing if necessary.
func (c *Client) token(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.expiresAt) {
		return c.accessToken, nil
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", c.refreshToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://accounts.spotify.com/api/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	basic := base64.StdEncoding.EncodeToString([]byte(c.clientID + ":" + c.clientSecret))
	req.Header.Set("Authorization", "Basic "+basic)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("spotify token: status %d", resp.StatusCode)
	}

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	c.accessToken = body.AccessToken
	c.expiresAt = time.Now().Add(time.Duration(body.ExpiresIn-30) * time.Second)
	return c.accessToken, nil
}

// NowPlaying returns the currently-playing track, or (nil, nil) if nothing is
// playing. Callers re-show the last track seen live to avoid a dead view.
func (c *Client) NowPlaying(ctx context.Context) (*Track, error) {
	resp, err := c.get(ctx, "https://api.spotify.com/v1/me/player/currently-playing")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 204 = nothing playing.
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp, "now-playing")
	}

	var raw struct {
		IsPlaying bool     `json:"is_playing"`
		Item      rawTrack `json:"item"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	// Spotify returns 200 with the paused track and is_playing:false when you
	// pause (it only 204s when no device is active). Treat paused as "nothing
	// live" so the caller re-shows the last track seen playing instead of a
	// stale LIVE state.
	if !raw.IsPlaying {
		return nil, nil
	}
	return raw.Item.toTrack(raw.IsPlaying), nil
}

// TopTracks returns Alif's top tracks for the given time range
// (short_term ~4 weeks, medium_term ~6 months, long_term ~1 year).
// Requires the user-top-read scope.
func (c *Client) TopTracks(ctx context.Context, limit int, timeRange string) ([]Track, error) {
	url := fmt.Sprintf(
		"https://api.spotify.com/v1/me/top/tracks?limit=%d&time_range=%s", limit, timeRange)
	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp, "top-tracks")
	}

	var raw struct {
		Items []rawTrack `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	tracks := make([]Track, 0, len(raw.Items))
	for _, it := range raw.Items {
		tracks = append(tracks, *it.toTrack(false))
	}
	return tracks, nil
}

// Artist is the trimmed, frontend-facing shape for a Spotify artist.
// (Spotify no longer returns the genres field for newer apps, so it's omitted.)
type Artist struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
	URL   string `json:"url,omitempty"`
}

// TopArtists returns Alif's top artists for the given time range.
// Requires the user-top-read scope.
func (c *Client) TopArtists(ctx context.Context, limit int, timeRange string) ([]Artist, error) {
	url := fmt.Sprintf(
		"https://api.spotify.com/v1/me/top/artists?limit=%d&time_range=%s", limit, timeRange)
	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp, "top-artists")
	}

	var raw struct {
		Items []struct {
			Name         string `json:"name"`
			ExternalURLs struct {
				Spotify string `json:"spotify"`
			} `json:"external_urls"`
			Images []struct {
				URL string `json:"url"`
			} `json:"images"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	artists := make([]Artist, 0, len(raw.Items))
	for _, it := range raw.Items {
		a := Artist{
			Name: it.Name,
			URL:  it.ExternalURLs.Spotify,
		}
		if len(it.Images) > 0 {
			a.Image = it.Images[0].URL
		}
		artists = append(artists, a)
	}
	return artists, nil
}

// Playlist is the trimmed, frontend-facing shape for a Spotify playlist.
type Playlist struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
	URL   string `json:"url,omitempty"`
}

// PlaylistByID fetches a single playlist by its Spotify ID. Used for the
// hand-curated "playlists I love" list — the reliable way to control exactly
// which playlists appear (the public users/{id}/playlists endpoint is
// restricted for this app, and the per-playlist `public` flag is unreliable).
func (c *Client) PlaylistByID(ctx context.Context, id string) (*Playlist, error) {
	url := fmt.Sprintf(
		"https://api.spotify.com/v1/playlists/%s?fields=name,external_urls,images", id)
	resp, err := c.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp, "playlist "+id)
	}

	var raw struct {
		Name         string `json:"name"`
		ExternalURLs struct {
			Spotify string `json:"spotify"`
		} `json:"external_urls"`
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	p := &Playlist{
		Name: raw.Name,
		URL:  raw.ExternalURLs.Spotify,
	}
	if len(raw.Images) > 0 {
		p.Image = raw.Images[0].URL
	}
	return p, nil
}
