// Package notify sends the finance sync's ntfy messages (ADR 0016). A scheduled
// finance sync pauses in awaiting_ack until a human approves the refresh; this
// package delivers that prompt as a message carrying an action button that calls the
// ack endpoint, and then reports the run's start and its terminal outcome.
//
// The approval prompt was once the only message the system ever sent, which meant
// every way a run could go wrong after approval was silent: a run nobody claimed, a
// runner that died mid-scrape, a sync that reported failure. Since the broker became
// unattended nobody is watching the logs either, so start/finish reporting is what
// makes a broken night visible rather than showing up as a stale dashboard weeks on.
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

// AckURL derives the absolute finance-sync ack endpoint from the backend's public
// base. The base is taken from the OAuth redirect URL (the one config value that
// already carries this backend's own scheme+host, e.g.
// https://api.example.dev/api/auth/google/callback -> https://api.example.dev). An
// empty or unparseable redirect yields an empty ack URL, which NotifyRefresh treats as
// "send the notification without an action button" (ack by hand). Hoisted here (from
// cmd/worker) so both the worker's scheduler and the API's admin force-start build the
// same ack URL. This keeps the ADR 0016 env surface to the ntfy + token vars, with no
// separate public-URL var.
func AckURL(oauthRedirect string) string {
	if oauthRedirect == "" {
		return ""
	}
	u, err := url.Parse(oauthRedirect)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/api/finance/sync/ack"
}

// Configured reports whether the client can actually send: an ntfy base URL and a
// topic are both required. When false, NotifyRefresh is a logged no-op.
func (c *Client) Configured() bool {
	return c.cfg.BaseURL != "" && c.cfg.Topic != ""
}

// ntfyMessage is the JSON publish body ntfy accepts when POSTed to the server base.
// Priority is ntfy's 1..5 (3 = default); omitted when zero. Tags render as emoji.
type ntfyMessage struct {
	Topic    string       `json:"topic"`
	Title    string       `json:"title"`
	Message  string       `json:"message"`
	Priority int          `json:"priority,omitempty"`
	Tags     []string     `json:"tags,omitempty"`
	Actions  []ntfyAction `json:"actions,omitempty"`
}

// ntfy priorities used here. A failed sync is the only one worth interrupting for;
// a start is deliberately quiet so the daily pair does not become noise to ignore.
const (
	prioLow     = 2
	prioDefault = 3
	prioHigh    = 4
)

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

	if err := c.post(ctx, msg); err != nil {
		return err
	}
	c.log.Info("notify: sent refresh notification", "run", runID, "topic", c.cfg.Topic)
	return nil
}

// NotifyRunStarted announces that a runner claimed an ack-gated run and is now
// working. Together with NotifyRunFinished this closes the gap left by the approval
// prompt being the only message the system ever sent: an approved run that never
// started, or started and never came back, used to be indistinguishable from a
// healthy one. Low priority on purpose — it is the finish that carries the news.
func (c *Client) NotifyRunStarted(ctx context.Context, runID int, jobName, runner string) error {
	if !c.Configured() {
		c.log.Info("notify: not configured; skipping run-started notification", "run", runID)
		return nil
	}
	label := jobName
	if label == "" {
		label = "Finance sync"
	}
	msg := "Run " + strconv.Itoa(runID) + " claimed and started."
	if runner != "" {
		msg += " Runner: " + runner + "."
	}
	if err := c.post(ctx, ntfyMessage{
		Topic:    c.cfg.Topic,
		Title:    label + ": started",
		Message:  msg,
		Priority: prioLow,
		Tags:     []string{"hourglass_flowing_sand"},
	}); err != nil {
		return err
	}
	c.log.Info("notify: sent run-started notification", "run", runID, "topic", c.cfg.Topic)
	return nil
}

// NotifyRunFinished announces a terminal outcome for an ack-gated run. status is the
// JobRun status ("succeeded", "failed", "cancelled"); detail is the run's error text
// where there is one, and is included verbatim so a failure says WHY without a trip
// to the admin console. Only a failure raises the priority.
func (c *Client) NotifyRunFinished(ctx context.Context, runID int, jobName, status, detail string) error {
	if !c.Configured() {
		c.log.Info("notify: not configured; skipping run-finished notification", "run", runID, "status", status)
		return nil
	}
	label := jobName
	if label == "" {
		label = "Finance sync"
	}

	title, tag, prio := label+": "+status, "white_check_mark", prioDefault
	switch status {
	case "failed":
		tag, prio = "rotating_light", prioHigh
	case "cancelled":
		tag, prio = "no_bell", prioDefault
	}

	msg := "Run " + strconv.Itoa(runID) + " finished: " + status + "."
	if detail != "" {
		msg += "\n" + detail
	}
	if err := c.post(ctx, ntfyMessage{
		Topic:    c.cfg.Topic,
		Title:    title,
		Message:  msg,
		Priority: prio,
		Tags:     []string{tag},
	}); err != nil {
		return err
	}
	c.log.Info("notify: sent run-finished notification", "run", runID, "status", status, "topic", c.cfg.Topic)
	return nil
}

// post marshals and publishes one message to ntfy. Shared by every Notify* method so
// they cannot drift on transport, headers or error shape.
func (c *Client) post(ctx context.Context, msg ntfyMessage) error {
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
	return nil
}
