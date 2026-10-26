package matrix

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRateLimitRetry: a 429 M_LIMIT_EXCEEDED is waited out and the request repeated with the same body.
func TestRateLimitRetry(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errcode":"M_LIMIT_EXCEEDED","error":"Too Many Requests","retry_after_ms":5}`))
			return
		}
		_, _ = w.Write([]byte(`{"event_id":"$ok"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, "t.lan", "tok")
	id, err := c.Send(context.Background(), "!r:t.lan", map[string]any{"msgtype": "m.text", "body": "hi"}, "")
	if err != nil || id != "$ok" || calls != 3 {
		t.Fatalf("id %q err %v calls %d", id, err, calls)
	}
}
