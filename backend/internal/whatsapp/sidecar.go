package whatsapp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Event is one newline-delimited JSON object streamed by the sidecar during a
// batch. The full schema lives in docs/whatsapp-sidecar-contract.md. Only the
// fields the backend acts on are modelled; the raw line is relayed verbatim to
// the browser, so unknown fields survive.
type Event struct {
	Type string `json:"type"` // qr | ready | progress | done | error

	Value string `json:"value,omitempty"` // qr: the linking payload

	// progress: one per recipient
	Phone  string `json:"phone,omitempty"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"` // sent | skipped | failed
	Reason string `json:"reason,omitempty"` // skipped: e.g. not_registered

	// done: terminal aggregate counts
	Sent    int `json:"sent,omitempty"`
	Skipped int `json:"skipped,omitempty"`
	Failed  int `json:"failed,omitempty"`

	// error: terminal failure message (also carried on a failed progress line)
	Error string `json:"error,omitempty"`
}

// Event type constants and the progress status values.
const (
	EventQR       = "qr"
	EventReady    = "ready"
	EventProgress = "progress"
	EventDone     = "done"
	EventError    = "error"

	StatusSent    = "sent"
	StatusSkipped = "skipped"
	StatusFailed  = "failed"
)

// SessionRecipient is one already-normalized recipient sent to the sidecar.
type SessionRecipient struct {
	Phone string `json:"phone"`
	Name  string `json:"name"`
}

// SessionRequest is the POST /sessions body: a batch's template body and its
// resolved recipients. Caps are enforced by the backend before this is sent.
type SessionRequest struct {
	BatchID      int                `json:"batch_id"`
	TemplateBody string             `json:"template_body"`
	Recipients   []SessionRecipient `json:"recipients"`
}

// ErrSessionBusy is returned when the sidecar rejects a session because the user
// already has one live (HTTP 409). The backend surfaces it as "a send is already
// running".
var ErrSessionBusy = errors.New("whatsapp: a send is already running for this user")

// Client dials the private sidecar. Every connection is outbound from the
// backend; the sidecar has no public ingress. A zero/blank baseURL yields a
// client whose Configured reports false.
type Client struct {
	baseURL string
	secret  string
	http    *http.Client
}

// NewClient builds a sidecar client. baseURL blank => Configured is false and the
// send path is disabled. The HTTP client has no overall timeout because a session
// streams for the life of a batch; the sidecar enforces its own scan window and
// hard cap, and the request context carries cancellation.
func NewClient(baseURL, secret string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		secret:  secret,
		http: &http.Client{
			// No Timeout: streaming responses outlive any fixed deadline. Bound the
			// dial + TLS handshake instead so an unreachable host fails fast.
			Transport: &http.Transport{
				ResponseHeaderTimeout: 30 * time.Second,
			},
		},
	}
}

// Configured reports whether a sidecar URL is set. When false, callers must not
// attempt a send.
func (c *Client) Configured() bool { return c.baseURL != "" }

// StartSession opens a streaming POST /sessions and invokes onEvent for each
// NDJSON line the sidecar emits, passing both the raw bytes (for verbatim relay)
// and the parsed Event (for bookkeeping). It returns when the stream closes or
// onEvent returns an error (which aborts the read and tears the session down by
// closing the body). A 409 maps to ErrSessionBusy.
func (c *Client) StartSession(ctx context.Context, req SessionRequest, onEvent func(raw []byte, ev Event) error) error {
	if !c.Configured() {
		return errors.New("whatsapp: sidecar not configured")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal session request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/sessions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build session request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.secret)

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("dial sidecar: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// stream below
	case http.StatusConflict:
		return ErrSessionBusy
	default:
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("sidecar returned %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	scanner := bufio.NewScanner(resp.Body)
	// Allow long lines: a QR payload plus JSON overhead can exceed the 64KB default.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			// Skip a malformed line rather than aborting the whole batch.
			continue
		}
		// Copy the raw line: scanner reuses its buffer on the next Scan.
		raw := append([]byte(nil), line...)
		if err := onEvent(raw, ev); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read sidecar stream: %w", err)
	}
	return nil
}
