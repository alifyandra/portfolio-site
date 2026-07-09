package digest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// The Messages API, called directly over net/http (there is no Anthropic Go SDK
// in this repo). The version header is pinned per the API contract.
const (
	anthropicEndpoint = "https://api.anthropic.com/v1/messages"
	anthropicVersion  = "2023-06-01"
)

// Anthropic is a minimal client for the Anthropic Messages API: one non-streaming
// call per digest run. The API key never leaves the backend/task (injected from
// SSM in prod, see ADR 0013). A blank key makes Configured() false so callers can
// degrade to a no-op, mirroring the Spotify/SES pattern.
type Anthropic struct {
	apiKey    string
	model     string
	maxTokens int
	http      *http.Client
}

// AnthropicOption customizes an Anthropic client at construction.
type AnthropicOption func(*Anthropic)

// WithHTTPClient overrides the underlying *http.Client. Mainly a test seam for
// injecting a transport that stubs the Messages API (mirrors spotify.WithHTTPClient).
func WithHTTPClient(hc *http.Client) AnthropicOption {
	return func(a *Anthropic) { a.http = hc }
}

// NewAnthropic constructs a client. The key may be empty in local dev, in which
// case Configured() is false and Summarize returns ErrNotConfigured. The timeout
// is generous: this is one non-streaming generation call.
func NewAnthropic(apiKey, model string, maxTokens int, opts ...AnthropicOption) *Anthropic {
	a := &Anthropic{
		apiKey:    apiKey,
		model:     model,
		maxTokens: maxTokens,
		http:      &http.Client{Timeout: 120 * time.Second},
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Configured reports whether an API key is present.
func (a *Anthropic) Configured() bool { return a.apiKey != "" }

// Summary is the parsed result of a Messages call.
type Summary struct {
	Text         string // concatenated text blocks
	Model        string // the model that answered
	StopReason   string
	Truncated    bool // stop_reason == "max_tokens": usable but cut short
	InputTokens  int
	OutputTokens int
}

// request/response shapes: only the fields we send/read.
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	Content    []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Summarize sends one user message (with an optional system prompt) and returns
// the concatenated text blocks. A "refusal" stop_reason is a hard error, and any
// non-2xx fails the run: retrying a 4xx will not help, but the run failing is
// intended — the schedule retries fresh (ADR 0013). "max_tokens" is usable and
// flagged Truncated.
func (a *Anthropic) Summarize(ctx context.Context, system, user string) (Summary, error) {
	if !a.Configured() {
		return Summary{}, ErrNotConfigured
	}
	reqBody, err := json.Marshal(anthropicRequest{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
	})
	if err != nil {
		return Summary{}, fmt.Errorf("digest: marshal messages request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return Summary{}, fmt.Errorf("digest: build messages request: %w", err)
	}
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return Summary{}, fmt.Errorf("digest: messages request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// The API returns a JSON error object; include a bounded slice for context.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Summary{}, fmt.Errorf("digest: anthropic status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}

	var out anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Summary{}, fmt.Errorf("digest: decode messages response: %w", err)
	}
	if out.StopReason == "refusal" {
		return Summary{}, fmt.Errorf("digest: anthropic refused to answer (stop_reason=refusal)")
	}

	var text bytes.Buffer
	for _, c := range out.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	if text.Len() == 0 {
		return Summary{}, fmt.Errorf("digest: anthropic returned no text (stop_reason=%q)", out.StopReason)
	}
	return Summary{
		Text:         text.String(),
		Model:        out.Model,
		StopReason:   out.StopReason,
		Truncated:    out.StopReason == "max_tokens",
		InputTokens:  out.Usage.InputTokens,
		OutputTokens: out.Usage.OutputTokens,
	}, nil
}
