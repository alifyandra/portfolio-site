package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNotifyRefresh_UnconfiguredNoOp: with no base URL/topic the client is a logged
// no-op — it returns nil without panicking and makes no HTTP call. This is the
// graceful-degradation contract that keeps local/dev unaffected without ntfy.
func TestNotifyRefresh_UnconfiguredNoOp(t *testing.T) {
	c := New(Config{}, nil)
	if c.Configured() {
		t.Fatal("Configured() = true for empty config, want false")
	}
	if err := c.NotifyRefresh(context.Background(), 42, "Finance sync"); err != nil {
		t.Errorf("NotifyRefresh(unconfigured) = %v, want nil (no-op)", err)
	}
}

// TestNotifyRefresh_PostsWithAckAction: a configured client POSTs a JSON ntfy
// message to the base URL carrying an action button whose URL targets the ack
// endpoint with the run id and token.
func TestNotifyRefresh_PostsWithAckAction(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(Config{
		BaseURL:  srv.URL,
		Topic:    "financesync",
		AckURL:   "https://api.example.dev/api/finance/sync/ack",
		AckToken: "s3cr3t",
	}, nil)
	if !c.Configured() {
		t.Fatal("Configured() = false, want true")
	}
	if err := c.NotifyRefresh(context.Background(), 99, "Finance sync"); err != nil {
		t.Fatalf("NotifyRefresh = %v, want nil", err)
	}

	var msg struct {
		Topic   string `json:"topic"`
		Actions []struct {
			Action string `json:"action"`
			URL    string `json:"url"`
			Method string `json:"method"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(gotBody, &msg); err != nil {
		t.Fatalf("decode posted body: %v; body=%s", err, string(gotBody))
	}
	if msg.Topic != "financesync" {
		t.Errorf("topic = %q, want financesync", msg.Topic)
	}
	if len(msg.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(msg.Actions))
	}
	a := msg.Actions[0]
	if a.Action != "http" || a.Method != http.MethodPost {
		t.Errorf("action = %+v, want http/POST", a)
	}
	if !strings.Contains(a.URL, "run_id=99") || !strings.Contains(a.URL, "token=s3cr3t") {
		t.Errorf("action url = %q, want it to carry run_id=99 and token=s3cr3t", a.URL)
	}
}
