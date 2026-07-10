package digest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// roundTripFunc stubs the outbound Messages request without touching the real API
// (mirrors the spotify package's test seam).
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func stubAnthropic(status int, body string) *Anthropic {
	rt := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{},
		}, nil
	})
	return NewAnthropic("key", "claude-haiku-4-5", 1024, WithHTTPClient(&http.Client{Transport: rt}))
}

func jsonResp(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}
}

// submitStub answers a batch create (POST /v1/messages/batches) with the given id.
func submitStub(batchID string) *Anthropic {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.Method == http.MethodPost && strings.HasSuffix(req.URL.Path, "/messages/batches") {
			return jsonResp(http.StatusOK, fmt.Sprintf(`{"id":%q,"processing_status":"in_progress"}`, batchID)), nil
		}
		return jsonResp(http.StatusInternalServerError, `{"error":"unexpected call"}`), nil
	})
	return NewAnthropic("key", "claude-haiku-4-5", 1024, WithHTTPClient(&http.Client{Transport: rt}))
}

// collectStub answers a CollectBatch poll: the batch status at
// /v1/messages/batches/{id}, then (when ended) the JSONL result at results_url.
// procStatus is "in_progress" or "ended"; resultType is the per-request result
// type on the JSONL line ("succeeded" | "errored" | "expired" | ...).
func collectStub(batchID, customID, procStatus, resultType, text string) *Anthropic {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch p := req.URL.Path; {
		case strings.HasSuffix(p, "/messages/batches/"+batchID+"/results"):
			line := fmt.Sprintf(
				`{"custom_id":%q,"result":{"type":%q,"message":{"model":"claude-haiku-4-5","stop_reason":"end_turn","content":[{"type":"text","text":%q}],"usage":{"input_tokens":10,"output_tokens":20}}}}`,
				customID, resultType, text)
			return jsonResp(http.StatusOK, line), nil
		case strings.HasSuffix(p, "/messages/batches/"+batchID):
			if procStatus == "ended" {
				return jsonResp(http.StatusOK, fmt.Sprintf(
					`{"id":%q,"processing_status":"ended","results_url":"https://api.anthropic.com/v1/messages/batches/%s/results"}`,
					batchID, batchID)), nil
			}
			return jsonResp(http.StatusOK, fmt.Sprintf(`{"id":%q,"processing_status":%q}`, batchID, procStatus)), nil
		}
		return jsonResp(http.StatusInternalServerError, `{"error":"unexpected call"}`), nil
	})
	return NewAnthropic("key", "claude-haiku-4-5", 1024, WithHTTPClient(&http.Client{Transport: rt}))
}

func TestSubmitBatch_ReturnsID(t *testing.T) {
	id, err := submitStub("msgbatch_123").SubmitBatch(context.Background(), "sys", "user", "2026-07-10")
	if err != nil {
		t.Fatalf("SubmitBatch = %v, want nil", err)
	}
	if id != "msgbatch_123" {
		t.Errorf("batch id = %q, want msgbatch_123", id)
	}
}

func TestSubmitBatch_Non2xxIsError(t *testing.T) {
	_, err := stubAnthropic(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`).
		SubmitBatch(context.Background(), "", "u", "2026-07-10")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("SubmitBatch on 401 = %v, want a status error naming 401", err)
	}
}

func TestSubmitBatch_NotConfigured(t *testing.T) {
	_, err := NewAnthropic("", "m", 10).SubmitBatch(context.Background(), "", "u", "d")
	if err != ErrNotConfigured {
		t.Fatalf("SubmitBatch without a key = %v, want ErrNotConfigured", err)
	}
}

func TestCollectBatch_Succeeded(t *testing.T) {
	out, err := collectStub("msgbatch_1", "2026-07-10", "ended", "succeeded", "the briefing").
		CollectBatch(context.Background(), "msgbatch_1", "2026-07-10")
	if err != nil {
		t.Fatalf("CollectBatch = %v, want nil", err)
	}
	if out.State != BatchSucceeded {
		t.Fatalf("state = %v, want BatchSucceeded", out.State)
	}
	if out.Summary.Text != "the briefing" {
		t.Errorf("summary text = %q, want the briefing", out.Summary.Text)
	}
}

func TestCollectBatch_Processing(t *testing.T) {
	out, err := collectStub("msgbatch_1", "2026-07-10", "in_progress", "", "").
		CollectBatch(context.Background(), "msgbatch_1", "2026-07-10")
	if err != nil {
		t.Fatalf("CollectBatch = %v, want nil", err)
	}
	if out.State != BatchProcessing {
		t.Errorf("state = %v, want BatchProcessing (batch not ended)", out.State)
	}
}

func TestCollectBatch_RequestFailed(t *testing.T) {
	out, err := collectStub("msgbatch_1", "2026-07-10", "ended", "expired", "").
		CollectBatch(context.Background(), "msgbatch_1", "2026-07-10")
	if err != nil {
		t.Fatalf("CollectBatch = %v, want nil (a per-request failure is a terminal outcome, not an error)", err)
	}
	if out.State != BatchFailed {
		t.Fatalf("state = %v, want BatchFailed", out.State)
	}
	if !strings.Contains(out.Reason, "expired") {
		t.Errorf("reason = %q, want it to name the expired failure", out.Reason)
	}
}

func TestCollectBatch_NotFoundIsTerminal(t *testing.T) {
	// A 404 on the status read is terminal (stop polling), not a transient error.
	rt := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResp(http.StatusNotFound, `{"error":{"type":"not_found_error"}}`), nil
	})
	a := NewAnthropic("key", "m", 10, WithHTTPClient(&http.Client{Transport: rt}))
	out, err := a.CollectBatch(context.Background(), "msgbatch_gone", "2026-07-10")
	if err != nil {
		t.Fatalf("CollectBatch on 404 = %v, want nil (terminal, not transient)", err)
	}
	if out.State != BatchFailed {
		t.Errorf("state = %v, want BatchFailed on 404", out.State)
	}
}

func TestSummarize_ConcatenatesTextBlocks(t *testing.T) {
	body := `{"model":"claude-haiku-4-5","stop_reason":"end_turn",
		"content":[{"type":"text","text":"Hello "},{"type":"thinking","text":"IGNORED"},{"type":"text","text":"world"}],
		"usage":{"input_tokens":12,"output_tokens":3}}`
	got, err := stubAnthropic(http.StatusOK, body).Summarize(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Text != "Hello world" {
		t.Errorf("Text = %q, want %q", got.Text, "Hello world")
	}
	if got.Model != "claude-haiku-4-5" {
		t.Errorf("Model = %q, want claude-haiku-4-5", got.Model)
	}
	if got.Truncated {
		t.Error("Truncated = true, want false for stop_reason=end_turn")
	}
	if got.InputTokens != 12 || got.OutputTokens != 3 {
		t.Errorf("tokens = %d/%d, want 12/3", got.InputTokens, got.OutputTokens)
	}
}

func TestSummarize_MaxTokensFlagsTruncated(t *testing.T) {
	body := `{"model":"m","stop_reason":"max_tokens","content":[{"type":"text","text":"cut"}]}`
	got, err := stubAnthropic(http.StatusOK, body).Summarize(context.Background(), "", "u")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated = false, want true for stop_reason=max_tokens")
	}
}

func TestSummarize_RefusalIsError(t *testing.T) {
	body := `{"model":"m","stop_reason":"refusal","content":[{"type":"text","text":"no"}]}`
	_, err := stubAnthropic(http.StatusOK, body).Summarize(context.Background(), "", "u")
	if err == nil || !strings.Contains(err.Error(), "refus") {
		t.Fatalf("Summarize on refusal = %v, want a refusal error", err)
	}
}

func TestSummarize_NoTextIsError(t *testing.T) {
	// A 200 with only non-text blocks must not be reported as a successful empty run.
	body := `{"model":"m","stop_reason":"end_turn","content":[{"type":"thinking","text":"x"}]}`
	_, err := stubAnthropic(http.StatusOK, body).Summarize(context.Background(), "", "u")
	if err == nil || !strings.Contains(err.Error(), "no text") {
		t.Fatalf("Summarize with no text blocks = %v, want a no-text error", err)
	}
}

func TestSummarize_Non2xxIsError(t *testing.T) {
	_, err := stubAnthropic(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`).
		Summarize(context.Background(), "", "u")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Summarize on 401 = %v, want a status error naming 401", err)
	}
}

func TestSummarize_NotConfigured(t *testing.T) {
	_, err := NewAnthropic("", "m", 10).Summarize(context.Background(), "", "u")
	if err != ErrNotConfigured {
		t.Fatalf("Summarize without a key = %v, want ErrNotConfigured", err)
	}
}

func TestNormalizeDate(t *testing.T) {
	// A non-UTC afternoon must collapse to that instant's UTC calendar-day midnight.
	loc := time.FixedZone("UTC+9", 9*3600)
	in := time.Date(2026, 7, 9, 15, 30, 0, 0, loc) // 2026-07-09 06:30 UTC
	got := NormalizeDate(in)
	want := time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("NormalizeDate = %s, want %s", got, want)
	}
	if got.Location() != time.UTC {
		t.Errorf("location = %s, want UTC", got.Location())
	}
}

func TestParseDate(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    time.Time
		wantErr bool
	}{
		{name: "YYYY-MM-DD", in: "2026-07-09", want: time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)},
		{name: "RFC3339 normalizes to UTC midnight", in: "2026-07-09T23:30:00+09:00", want: time.Date(2026, 7, 9, 0, 0, 0, 0, time.UTC)},
		{name: "garbage", in: "not-a-date", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseDate(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ParseDate(%q) = %s, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDate(%q) unexpected error: %v", c.in, err)
			}
			if !got.Equal(c.want) {
				t.Errorf("ParseDate(%q) = %s, want %s", c.in, got, c.want)
			}
		})
	}
}

func TestParseDate_EmptyIsTodayUTC(t *testing.T) {
	before := NormalizeDate(time.Now())
	got, err := ParseDate("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	after := NormalizeDate(time.Now())
	// Empty input means "today at UTC midnight". If the test happens to straddle a
	// UTC-midnight boundary, before and after differ by a day; accept either so the
	// test cannot flake.
	if !got.Equal(before) && !got.Equal(after) {
		t.Errorf("ParseDate(\"\") = %s, want today UTC (%s or %s)", got, before, after)
	}
}
