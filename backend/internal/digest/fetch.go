package digest

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxSourceBytes caps how much of a Source we read into the prompt. It bounds the
// token cost and guards against a huge page; the tail is dropped.
const maxSourceBytes = 200 * 1024

// Fetcher pulls a Source's raw content over plain HTTP. Public and
// unauthenticated: no credentials are ever attached (see CONTEXT.md "Source").
type Fetcher struct {
	http *http.Client
}

// NewFetcher builds a fetcher with its own 30s timeout.
func NewFetcher() *Fetcher {
	return &Fetcher{http: &http.Client{Timeout: 30 * time.Second}}
}

// Fetch GETs url and returns its body as text, capped at maxSourceBytes. Transport
// errors are wrapped; a non-2xx is a labelled status error. Callers collect
// per-source failures and continue, so one dead Source never blanks the rest.
func (f *Fetcher) Fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "aliflabs-digest/1.0 (+https://aliflabs.dev)")
	req.Header.Set("Accept", "text/html, application/xml, application/rss+xml, application/atom+xml;q=0.9, */*;q=0.8")

	resp, err := f.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceBytes))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}
