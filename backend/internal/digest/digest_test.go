package digest

import (
	"context"
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
