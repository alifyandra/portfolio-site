package digest

import (
	"bufio"
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
	anthropicEndpoint      = "https://api.anthropic.com/v1/messages"
	anthropicBatchEndpoint = "https://api.anthropic.com/v1/messages/batches"
	anthropicVersion       = "2023-06-01"
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
	reqBody, err := json.Marshal(a.messageParams(system, user))
	if err != nil {
		return Summary{}, fmt.Errorf("digest: marshal messages request: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost, anthropicEndpoint, reqBody)
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
	return parseSummary(out)
}

// messageParams builds the single-user-message request body shared by the
// synchronous Summarize call and each Message Batch request.
func (a *Anthropic) messageParams(system, user string) anthropicRequest {
	return anthropicRequest{
		Model:     a.model,
		MaxTokens: a.maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
	}
}

// do issues an authenticated request to the Anthropic API. A nil body means a GET
// with no content-type (used for the batch status/results reads). The version
// header is pinned; Batches is GA so no beta header is needed.
func (a *Anthropic) do(ctx context.Context, method, url string, body []byte) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, fmt.Errorf("digest: build request: %w", err)
	}
	req.Header.Set("x-api-key", a.apiKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	if body != nil {
		req.Header.Set("content-type", "application/json")
	}
	return a.http.Do(req)
}

// parseSummary turns a decoded Messages response into a Summary, applying the same
// rules whether it came from a synchronous call or a batch result: a "refusal"
// stop_reason is a hard error, an empty text set is an error, and "max_tokens" is
// usable-but-Truncated.
func parseSummary(out anthropicResponse) (Summary, error) {
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

// --- Message Batches (ADR 0013 Batch API amendment) -------------------------
//
// Prod submits the digest as a one-request Message Batch (50% cheaper than a
// synchronous call) instead of blocking on inference. The batch runs
// asynchronously on Anthropic's side; the recurring digest.collect job polls it
// with CollectBatch and persists the result once it has ended. Local dev keeps
// the synchronous Summarize path for immediate feedback.

// batchRequestItem is one entry in a batch: a caller-chosen custom_id (the digest
// keys it by date) plus the same params a synchronous Messages call would send.
type batchRequestItem struct {
	CustomID string           `json:"custom_id"`
	Params   anthropicRequest `json:"params"`
}

type batchCreateRequest struct {
	Requests []batchRequestItem `json:"requests"`
}

// batchStatusResponse is the subset of the batch object we read: the id, the
// processing_status ("in_progress" | "canceling" | "ended"), and the results_url
// (populated once ended).
type batchStatusResponse struct {
	ID               string `json:"id"`
	ProcessingStatus string `json:"processing_status"`
	ResultsURL       string `json:"results_url"`
}

// batchResultLine is one line of the JSONL results stream: the custom_id and a
// result whose type is succeeded | errored | canceled | expired, carrying the
// Messages response on success.
type batchResultLine struct {
	CustomID string `json:"custom_id"`
	Result   struct {
		Type    string             `json:"type"`
		Message *anthropicResponse `json:"message"`
	} `json:"result"`
}

// BatchState is the outcome of polling a batch with CollectBatch.
type BatchState int

const (
	// BatchProcessing: the batch has not ended yet; poll again later.
	BatchProcessing BatchState = iota
	// BatchSucceeded: the request completed; Summary carries the digest.
	BatchSucceeded
	// BatchFailed: the request errored/expired/canceled or refused; Reason says why.
	BatchFailed
)

// BatchOutcome is the result of CollectBatch. Summary is valid only when the
// state is BatchSucceeded; Reason is set only when BatchFailed.
type BatchOutcome struct {
	State   BatchState
	Summary Summary
	Reason  string
}

// SubmitBatch creates a single-request Message Batch (one user message, optional
// system prompt) tagged with customID, and returns the batch id to poll later.
// A non-2xx fails the run; the caller records a failed Result and the schedule
// retries (mirroring Summarize's contract).
func (a *Anthropic) SubmitBatch(ctx context.Context, system, user, customID string) (string, error) {
	if !a.Configured() {
		return "", ErrNotConfigured
	}
	reqBody, err := json.Marshal(batchCreateRequest{
		Requests: []batchRequestItem{{CustomID: customID, Params: a.messageParams(system, user)}},
	})
	if err != nil {
		return "", fmt.Errorf("digest: marshal batch request: %w", err)
	}
	resp, err := a.do(ctx, http.MethodPost, anthropicBatchEndpoint, reqBody)
	if err != nil {
		return "", fmt.Errorf("digest: batch create request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("digest: batch create status %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var out batchStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("digest: decode batch create response: %w", err)
	}
	if out.ID == "" {
		return "", fmt.Errorf("digest: batch create returned no id")
	}
	return out.ID, nil
}

// CollectBatch polls batchID and, once the batch has ended, fetches and parses the
// result for customID. It returns BatchProcessing while the batch is still in
// flight (leave it for the next poll), BatchSucceeded with the Summary, or
// BatchFailed with a reason for a terminal failure (errored/expired/canceled,
// refusal, a missing result, or a 404 batch). A transient error (network, 5xx)
// is returned as an error so the caller leaves the row pending and retries.
func (a *Anthropic) CollectBatch(ctx context.Context, batchID, customID string) (BatchOutcome, error) {
	if !a.Configured() {
		return BatchOutcome{}, ErrNotConfigured
	}

	// 1. Status. A 404 is terminal (the batch is gone — stop polling); other non-2xx
	//    are transient.
	resp, err := a.do(ctx, http.MethodGet, anthropicBatchEndpoint+"/"+batchID, nil)
	if err != nil {
		return BatchOutcome{}, fmt.Errorf("digest: retrieve batch %s: %w", batchID, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return BatchOutcome{State: BatchFailed, Reason: fmt.Sprintf("batch %s not found (status 404)", batchID)}, nil
	}
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		resp.Body.Close()
		return BatchOutcome{}, fmt.Errorf("digest: retrieve batch %s status %d: %s", batchID, resp.StatusCode, bytes.TrimSpace(snippet))
	}
	var status batchStatusResponse
	derr := json.NewDecoder(resp.Body).Decode(&status)
	resp.Body.Close()
	if derr != nil {
		return BatchOutcome{}, fmt.Errorf("digest: decode batch status: %w", derr)
	}
	if status.ProcessingStatus != "ended" {
		return BatchOutcome{State: BatchProcessing}, nil
	}
	if status.ResultsURL == "" {
		return BatchOutcome{State: BatchFailed, Reason: "batch ended with no results_url"}, nil
	}

	// 2. Results (JSONL, one line per request). Find the line for customID.
	rresp, err := a.do(ctx, http.MethodGet, status.ResultsURL, nil)
	if err != nil {
		return BatchOutcome{}, fmt.Errorf("digest: fetch batch results: %w", err)
	}
	defer rresp.Body.Close()
	if rresp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(rresp.Body, 2048))
		return BatchOutcome{}, fmt.Errorf("digest: batch results status %d: %s", rresp.StatusCode, bytes.TrimSpace(snippet))
	}

	scanner := bufio.NewScanner(rresp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // a result line can exceed the 64KB default
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var rl batchResultLine
		if err := json.Unmarshal(line, &rl); err != nil {
			return BatchOutcome{}, fmt.Errorf("digest: decode batch result line: %w", err)
		}
		if rl.CustomID != customID {
			continue
		}
		if rl.Result.Type != "succeeded" {
			// errored | canceled | expired — terminal.
			return BatchOutcome{State: BatchFailed, Reason: fmt.Sprintf("batch request %s", rl.Result.Type)}, nil
		}
		if rl.Result.Message == nil {
			return BatchOutcome{State: BatchFailed, Reason: "succeeded result carried no message"}, nil
		}
		summary, err := parseSummary(*rl.Result.Message)
		if err != nil {
			// A refusal or empty response is a terminal content failure.
			return BatchOutcome{State: BatchFailed, Reason: err.Error()}, nil
		}
		return BatchOutcome{State: BatchSucceeded, Summary: summary}, nil
	}
	if err := scanner.Err(); err != nil {
		return BatchOutcome{}, fmt.Errorf("digest: scan batch results: %w", err)
	}
	return BatchOutcome{State: BatchFailed, Reason: fmt.Sprintf("no result for custom_id %q in batch %s", customID, batchID)}, nil
}
