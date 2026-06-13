package spotify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
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

// New constructs a Spotify client. Credentials may be empty in local dev, in
// which case calls return ErrNotConfigured.
func New(clientID, clientSecret, refreshToken string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		refreshToken: refreshToken,
		http:         &http.Client{Timeout: 10 * time.Second},
	}
}

// ErrNotConfigured is returned when Spotify credentials are absent.
var ErrNotConfigured = fmt.Errorf("spotify: not configured")

// Track is the trimmed, frontend-facing shape we expose.
type Track struct {
	IsPlaying  bool     `json:"is_playing"`
	Title      string   `json:"title"`
	Artists    []string `json:"artists"`
	Album      string   `json:"album"`
	AlbumImage string   `json:"album_image,omitempty"`
	SongURL    string   `json:"song_url,omitempty"`
}

func (c *Client) configured() bool {
	return c.clientID != "" && c.clientSecret != "" && c.refreshToken != ""
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
// playing.
func (c *Client) NowPlaying(ctx context.Context) (*Track, error) {
	if !c.configured() {
		return nil, ErrNotConfigured
	}
	tok, err := c.token(ctx)
	if err != nil {
		return nil, err
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.spotify.com/v1/me/player/currently-playing", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// 204 = nothing playing.
	if resp.StatusCode == http.StatusNoContent {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify now-playing: status %d", resp.StatusCode)
	}

	var raw struct {
		IsPlaying bool `json:"is_playing"`
		Item      struct {
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
		} `json:"item"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	track := &Track{
		IsPlaying: raw.IsPlaying,
		Title:     raw.Item.Name,
		Album:     raw.Item.Album.Name,
		SongURL:   raw.Item.ExternalURLs.Spotify,
	}
	for _, a := range raw.Item.Artists {
		track.Artists = append(track.Artists, a.Name)
	}
	if len(raw.Item.Album.Images) > 0 {
		track.AlbumImage = raw.Item.Album.Images[0].URL
	}
	return track, nil
}
