// Package notify sends the finance refresh-handshake notification (ADR 0016). A
// scheduled finance sync pauses in awaiting_ack until a human approves the refresh;
// this package delivers that prompt as an ntfy message carrying an action button
// that calls the ack endpoint.
//
// It follows the SES/Spotify/queue graceful-degradation precedent: when the ntfy
// base URL or topic is unconfigured the client logs and returns nil (no error), so
// the scheduler still creates the awaiting_ack run and it can be acked directly.
// Local and dev are therefore unaffected without any ntfy setup.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Config is the notify client's settings, all sourced from env (see config.Config).
type Config struct {
	// BaseURL is the ntfy server, e.g. "https://ntfy.sh". Blank disables notifications.
	BaseURL string
	// Topic is the ntfy topic to publish to. Blank disables notifications.
	Topic string
	// AckURL is the absolute ack endpoint the action button targets, e.g.
	// "https://api.example.dev/api/finance/sync/ack". The run id and token are added
	// as query params. Blank => the notification is sent without an action button
	// (the operator acks by hand).
	AckURL string
	// AckToken is the shared secret the ack endpoint checks; it rides in the action
	// URL, so it is distinct from the finance.sync runner bearer.
	AckToken string
}

// Client posts refresh-handshake notifications to ntfy.
type Client struct {
	cfg  Config
	http *http.Client
	log  *slog.Logger
}

// New builds a notify Client. A nil logger falls back to slog.Default().
func New(cfg Config, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 10 * time.Second},
		log:  log,
	}
}

// Configured reports whether the client can actually send: an ntfy base URL and a
// topic are both required. When false, NotifyRefresh is a logged no-op.
func (c *Client) Configured() bool {
	return c.cfg.BaseURL != "" && c.cfg.Topic != ""
}

// ntfyMessage is the JSON publish body ntfy accepts when POSTed to the server base.
type ntfyMessage struct {
	Topic   string       `json:"topic"`
	Title   string       `json:"title"`
	Message string       `json:"message"`
	Actions []ntfyAction `json:"actions,omitempty"`
}

// ntfyAction is one notification action button. "http" makes ntfy issue the request
// when the button is tapped.
type ntfyAction struct {
	Action string `json:"action"`
	Label  string `json:"label"`
	URL    string `json:"url"`
	Method string `json:"method,omitempty"`
	Clear  bool   `json:"clear,omitempty"`
}

// NotifyRefresh sends the refresh-handshake prompt for an awaiting_ack run. When
// unconfigured it logs and returns nil (graceful no-op). When an AckURL is set it
// attaches an "Approve refresh" button that POSTs the ack endpoint with the run id
// and token, so approving is one tap from the notification.
func (c *Client) NotifyRefresh(ctx context.Context, runID int, jobName string) error {
	if !c.Configured() {
		c.log.Info("notify: not configured; skipping refresh notification", "run", runID)
		return nil
	}

	label := jobName
	if label == "" {
		label = "Finance sync"
	}
	msg := ntfyMessage{
		Topic:   c.cfg.Topic,
		Title:   label + ": refresh approval needed",
		Message: "A scheduled finance sync is waiting for approval to refresh. Approve to make it claimable.",
	}
	if c.cfg.AckURL != "" {
		u, err := url.Parse(c.cfg.AckURL)
		if err != nil {
			return fmt.Errorf("notify: parse ack url: %w", err)
		}
		q := u.Query()
		q.Set("run_id", strconv.Itoa(runID))
		if c.cfg.AckToken != "" {
			q.Set("token", c.cfg.AckToken)
		}
		u.RawQuery = q.Encode()
		msg.Actions = []ntfyAction{{
			Action: "http",
			Label:  "Approve refresh",
			URL:    u.String(),
			Method: http.MethodPost,
			Clear:  true,
		}}
	}

	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("notify: marshal message: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notify: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("notify: post to ntfy: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: ntfy returned status %d", resp.StatusCode)
	}
	c.log.Info("notify: sent refresh notification", "run", runID, "topic", c.cfg.Topic)
	return nil
}
