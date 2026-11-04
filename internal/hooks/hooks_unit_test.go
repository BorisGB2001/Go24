package hooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go24/internal/i18n"
)

func TestCheckURL(t *testing.T) {
	longValidPath := strings.Repeat("a", 1000-len("https://example.com/"))
	longInvalidPath := strings.Repeat("a", 1001-len("https://example.com/"))

	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "standard http", raw: "http://example.com", wantErr: false},
		{name: "standard https", raw: "https://example.com", wantErr: false},
		{name: "with path", raw: "https://example.com/webhook", wantErr: false},
		{name: "with port", raw: "http://localhost:8080/hook", wantErr: false},
		{name: "with query and fragment", raw: "https://example.com:8443/webhook?token=xyz&debug=1#frag", wantErr: false},
		{name: "with credentials", raw: "https://user:pass@example.com/webhook", wantErr: false},
		{name: "with ipv4", raw: "http://127.0.0.1:9000/webhook", wantErr: false},
		{name: "with ipv6", raw: "http://[::1]:9000/webhook", wantErr: false},
		{name: "with surrounding whitespace", raw: "   https://n8n.example.com/webhook   ", wantErr: false},
		{name: "max length 1000 bytes", raw: "https://example.com/" + longValidPath, wantErr: false},
		{name: "exceeds 1000 bytes", raw: "https://example.com/" + longInvalidPath, wantErr: true},
		{name: "empty string", raw: "", wantErr: true},
		{name: "whitespace only", raw: "   \t\n", wantErr: true},
		{name: "ftp scheme", raw: "ftp://example.com/hook", wantErr: true},
		{name: "file scheme", raw: "file:///etc/passwd", wantErr: true},
		{name: "javascript scheme", raw: "javascript:alert(1)", wantErr: true},
		{name: "data scheme", raw: "data:text/plain;base64,aGVsbG8=", wantErr: true},
		{name: "ws scheme", raw: "ws://example.com/socket", wantErr: true},
		{name: "wss scheme", raw: "wss://example.com/socket", wantErr: true},
		{name: "no scheme", raw: "example.com/webhook", wantErr: true},
		{name: "scheme relative", raw: "//example.com/webhook", wantErr: true},
		{name: "http empty host", raw: "http://", wantErr: true},
		{name: "https empty host", raw: "https://", wantErr: true},
		{name: "http root path only", raw: "http:///path", wantErr: true},
		{name: "http query only", raw: "http://?query=1", wantErr: true},
		{name: "http fragment only", raw: "http://#frag", wantErr: true},
		{name: "malformed ipv6", raw: "http://[::1", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckURL(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("CheckURL(%q) = nil; want error", tc.raw)
				}
				var i18nErr *i18n.Error
				if !errors.As(err, &i18nErr) || i18nErr.Key != "err.hook_url" {
					t.Errorf("CheckURL(%q) err = %v; want err.hook_url", tc.raw, err)
				}
			} else if err != nil {
				t.Fatalf("CheckURL(%q) unexpected err = %v", tc.raw, err)
			}
		})
	}
}

func TestSign(t *testing.T) {
	// RFC 4231 HMAC-SHA256 test vectors.
	vectors := []struct {
		name   string
		secret string
		body   []byte
		want   string
	}{
		{
			name:   "RFC 4231 Case 1",
			secret: string(bytes.Repeat([]byte{0x0b}, 20)),
			body:   []byte("Hi There"),
			want:   "sha256=b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7",
		},
		{
			name:   "RFC 4231 Case 2",
			secret: "Jefe",
			body:   []byte("what do ya want for nothing?"),
			want:   "sha256=5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843",
		},
		{
			name:   "RFC 4231 Case 3",
			secret: string(bytes.Repeat([]byte{0xaa}, 20)),
			body:   bytes.Repeat([]byte{0xdd}, 50),
			want:   "sha256=773ea91e36800e46854db8ebd09181a72959098b3ef8c122d9635514ced565fe",
		},
		{
			name:   "empty secret and empty body",
			secret: "",
			body:   []byte(""),
			want:   "sha256=b613679a0814d9ec772f95d778c35fc5ff1697c493715653c6c712144292c5ad",
		},
		{
			name:   "empty secret and nil body",
			secret: "",
			body:   nil,
			want:   "sha256=b613679a0814d9ec772f95d778c35fc5ff1697c493715653c6c712144292c5ad",
		},
	}

	for _, tc := range vectors {
		t.Run(tc.name, func(t *testing.T) {
			got := Sign(tc.secret, tc.body)
			if got != tc.want {
				t.Errorf("Sign(%q, %q) = %q; want %q", tc.secret, tc.body, got, tc.want)
			}
		})
	}

	t.Run("properties and sensitivity", func(t *testing.T) {
		sec := "test-secret"
		body := []byte(`{"event":"deal.created","data":{"id":123}}`)
		sig := Sign(sec, body)

		if !strings.HasPrefix(sig, "sha256=") {
			t.Fatalf("signature missing sha256= prefix: %q", sig)
		}
		hexPart := strings.TrimPrefix(sig, "sha256=")
		if len(hexPart) != 64 {
			t.Fatalf("hex length = %d; want 64", len(hexPart))
		}
		if _, err := hex.DecodeString(hexPart); err != nil {
			t.Fatalf("invalid hex: %v", err)
		}

		// Deterministic.
		if Sign(sec, body) != sig {
			t.Fatal("Sign is not deterministic")
		}

		// Secret sensitivity.
		if Sign("other-secret", body) == sig {
			t.Fatal("different secret must produce different signature")
		}

		// Body sensitivity.
		bodyMod := append([]byte(nil), body...)
		bodyMod[len(bodyMod)-1] = '}' + 1
		if Sign(sec, bodyMod) == sig {
			t.Fatal("modified body must produce different signature")
		}

		// Unicode UTF-8 secret and body.
		uSec := "секрет-123"
		uBody := []byte(`{"title":"Сделка"}`)
		uSig := Sign(uSec, uBody)
		mac := hmac.New(sha256.New, []byte(uSec))
		mac.Write(uBody)
		wantUSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if uSig != wantUSig {
			t.Fatalf("Unicode Sign(%q, %q) = %q; want %q", uSec, uBody, uSig, wantUSig)
		}
	})
}

func TestPost_HeadersAndDelivery(t *testing.T) {
	secret := "hook-secret-xyz"
	event := "deal.created"
	payload := []byte(`{"event":"deal.created","data":{"title":"Sale"}}`)

	var mu sync.Mutex
	var gotMethod, gotContentType, gotUA, gotEvent, gotSig string
	var gotBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotMethod = r.Method
		gotContentType = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		gotEvent = r.Header.Get("X-Go24-Event")
		gotSig = r.Header.Get("X-Go24-Signature")
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Store{client: srv.Client()}
	status, err := s.post(context.Background(), srv.URL, secret, event, payload)
	if err != nil {
		t.Fatalf("post() err = %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("post() status = %d; want %d", status, http.StatusOK)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotMethod != http.MethodPost {
		t.Errorf("Method = %q; want POST", gotMethod)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", gotContentType)
	}
	if gotUA != "go24-webhook" {
		t.Errorf("User-Agent = %q; want go24-webhook", gotUA)
	}
	if gotEvent != event {
		t.Errorf("X-Go24-Event = %q; want %q", gotEvent, event)
	}
	wantSig := Sign(secret, payload)
	if gotSig != wantSig {
		t.Errorf("X-Go24-Signature = %q; want %q", gotSig, wantSig)
	}
	if !bytes.Equal(gotBody, payload) {
		t.Errorf("Body = %s; want %s", gotBody, payload)
	}
}

func TestPost_StatusCodes(t *testing.T) {
	codes := []int{
		http.StatusOK,
		http.StatusCreated,
		http.StatusAccepted,
		http.StatusNoContent,
		http.StatusBadRequest,
		http.StatusUnauthorized,
		http.StatusForbidden,
		http.StatusNotFound,
		http.StatusConflict,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	}

	for _, code := range codes {
		code := code
		t.Run(fmt.Sprintf("HTTP_%d", code), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(code)
			}))
			defer srv.Close()

			s := &Store{client: srv.Client()}
			status, err := s.post(context.Background(), srv.URL, "sec", "ping", []byte("{}"))
			if err != nil {
				t.Fatalf("post() returned unexpected error for status %d: %v", code, err)
			}
			if status != code {
				t.Errorf("post() status = %d; want %d", status, code)
			}
		})
	}
}

func TestPost_PayloadBuilding(t *testing.T) {
	tests := []struct {
		name    string
		event   string
		payload any
	}{
		{
			name:  "deal created payload",
			event: "deal.created",
			payload: map[string]any{
				"event": "deal.created",
				"at":    "2026-10-28T12:00:00Z",
				"data": map[string]any{
					"id":     "deal-1",
					"title":  "Contract A",
					"amount": float64(50000),
				},
			},
		},
		{
			name:  "deal stage payload",
			event: "deal.stage",
			payload: map[string]any{
				"event": "deal.stage",
				"at":    "2026-10-28T12:00:00Z",
				"data": map[string]any{
					"id":             "deal-1",
					"old_stage_id":   "stage-new",
					"stage_code":     "WON",
					"stage_semantic": "success",
				},
			},
		},
		{
			name:  "lead created payload",
			event: "lead.created",
			payload: map[string]any{
				"event": "lead.created",
				"at":    "2026-10-28T12:00:00Z",
				"data": map[string]any{
					"id":    "lead-42",
					"title": "Inbound request",
				},
			},
		},
		{
			name:  "task completed payload",
			event: "task.completed",
			payload: map[string]any{
				"event": "task.completed",
				"at":    "2026-10-28T12:00:00Z",
				"data": map[string]any{
					"id":     "task-99",
					"key":    "MKT-12",
					"title":  "Publish newsletter",
					"status": "completed",
				},
			},
		},
		{
			name:  "ping payload",
			event: "ping",
			payload: map[string]any{
				"event": "ping",
				"at":    "2026-10-28T12:00:00Z",
				"data": map[string]any{
					"webhook": "n8n automation",
				},
			},
		},
		{
			name:  "unicode cyrillic content",
			event: "openline.message",
			payload: map[string]any{
				"event": "openline.message",
				"at":    "2026-10-28T12:00:00Z",
				"data": map[string]any{
					"text": "Здравствуйте, интересует стоимость тарифа",
				},
			},
		},
	}

	secret := "secret-payload-test"

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rawBody, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatalf("json.Marshal failed: %v", err)
			}

			var mu sync.Mutex
			var received map[string]any
			var receivedSig, receivedEvent string

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				receivedSig = r.Header.Get("X-Go24-Signature")
				receivedEvent = r.Header.Get("X-Go24-Event")
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &received)
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()

			s := &Store{client: srv.Client()}
			status, err := s.post(context.Background(), srv.URL, secret, tc.event, rawBody)
			if err != nil {
				t.Fatalf("post() err = %v", err)
			}
			if status != http.StatusOK {
				t.Fatalf("post() status = %d; want %d", status, http.StatusOK)
			}

			mu.Lock()
			defer mu.Unlock()
			if receivedEvent != tc.event {
				t.Errorf("X-Go24-Event = %q; want %q", receivedEvent, tc.event)
			}
			if receivedSig != Sign(secret, rawBody) {
				t.Errorf("X-Go24-Signature mismatch: got %q, want %q", receivedSig, Sign(secret, rawBody))
			}
			if received["event"] != tc.event {
				t.Errorf("deserialized event = %v; want %q", received["event"], tc.event)
			}
		})
	}
}

func TestPost_LargeResponseBody(t *testing.T) {
	// Server responds with 128 KiB to verify LimitReader drains up to 64 KiB without hanging.
	largeResp := bytes.Repeat([]byte("B"), 128<<10)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(largeResp)
	}))
	defer srv.Close()

	s := &Store{client: srv.Client()}
	status, err := s.post(context.Background(), srv.URL, "secret", "ping", []byte("{}"))
	if err != nil {
		t.Fatalf("post() failed on large response body: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d; want %d", status, http.StatusOK)
	}
}

func TestPost_ErrorPaths(t *testing.T) {
	s := &Store{client: &http.Client{Timeout: 5 * time.Second}}

	t.Run("invalid url format", func(t *testing.T) {
		_, err := s.post(context.Background(), "://invalid-url", "secret", "ping", []byte("{}"))
		if err == nil {
			t.Fatal("post() with invalid url must fail")
		}
	})

	t.Run("connection refused", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		srvURL := srv.URL
		srv.Close() // Close immediately to trigger connection failure.

		status, err := s.post(context.Background(), srvURL, "secret", "ping", []byte("{}"))
		if err == nil {
			t.Fatal("post() to closed server must fail")
		}
		if status != 0 {
			t.Errorf("status = %d; want 0", status)
		}
	})

	t.Run("canceled context", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := s.post(ctx, srv.URL, "secret", "ping", []byte("{}"))
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("post() with canceled context err = %v; want context.Canceled", err)
		}
	})
}

func TestPost_Concurrency(t *testing.T) {
	var count int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&count, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Store{client: srv.Client()}
	var wg sync.WaitGroup
	const workers = 16

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := []byte(fmt.Sprintf(`{"worker":%d}`, id))
			status, err := s.post(context.Background(), srv.URL, "secret", "ping", payload)
			if err != nil || status != http.StatusOK {
				t.Errorf("worker %d: status = %d, err = %v", id, status, err)
			}
		}(i)
	}
	wg.Wait()

	if total := atomic.LoadInt64(&count); total != workers {
		t.Fatalf("total requests = %d; want %d", total, workers)
	}
}

func TestBackoffScheduleAndRetryDecisions(t *testing.T) {
	// Verify backoff schedule definition.
	expectedBackoff := []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		30 * time.Minute,
		2 * time.Hour,
		6 * time.Hour,
	}

	if len(backoff) != len(expectedBackoff) {
		t.Fatalf("backoff length = %d; want %d", len(backoff), len(expectedBackoff))
	}
	for i, d := range expectedBackoff {
		if backoff[i] != d {
			t.Errorf("backoff[%d] = %v; want %v", i, backoff[i], d)
		}
	}

	// Decision logic from Store.Deliver:
	// - msg is empty if err == nil && status < 300.
	// - msg is "HTTP <status>" if err == nil && status >= 300.
	// - msg is err.Error() if err != nil.
	// - if msg == "": delivery succeeded, attempts = attempts + 1, done_at = now().
	// - if msg != "" && attempts >= len(backoff): given up, attempts = attempts + 1, done_at = now().
	// - if msg != "" && attempts < len(backoff): retry scheduled, attempts = attempts + 1, next_at = now + backoff[attempts].
	decide := func(status int, postErr error, attempts int, now time.Time) (msg string, st *int, done bool, nextAt time.Time, givenUp bool) {
		if postErr != nil {
			msg = postErr.Error()
		} else if status >= 300 {
			msg = fmt.Sprintf("HTTP %d", status)
		}
		if status > 0 {
			st = &status
		}
		if msg == "" {
			done = true
		} else if attempts >= len(backoff) {
			done = true
			givenUp = true
		} else {
			nextAt = now.Add(backoff[attempts])
		}
		return
	}

	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	t.Run("successful status codes mark delivery done", func(t *testing.T) {
		for _, st := range []int{200, 201, 204, 299} {
			msg, stPtr, done, nextAt, givenUp := decide(st, nil, 0, now)
			if msg != "" || !done || givenUp || !nextAt.IsZero() {
				t.Errorf("status %d: msg=%q done=%v givenUp=%v nextAt=%v", st, msg, done, givenUp, nextAt)
			}
			if stPtr == nil || *stPtr != st {
				t.Errorf("status pointer for %d mismatch: %v", st, stPtr)
			}
		}
	})

	t.Run("retry progression on failures", func(t *testing.T) {
		// Test attempt 0 through 4 (within backoff schedule).
		for attempt := 0; attempt < len(backoff); attempt++ {
			msg, stPtr, done, nextAt, givenUp := decide(500, nil, attempt, now)
			if msg != "HTTP 500" {
				t.Errorf("attempt %d: msg = %q; want 'HTTP 500'", attempt, msg)
			}
			if done || givenUp {
				t.Errorf("attempt %d: must not be done or given up yet", attempt)
			}
			wantNext := now.Add(backoff[attempt])
			if !nextAt.Equal(wantNext) {
				t.Errorf("attempt %d: nextAt = %v; want %v (pause %v)", attempt, nextAt, wantNext, backoff[attempt])
			}
			if stPtr == nil || *stPtr != 500 {
				t.Errorf("attempt %d: status pointer mismatch: %v", attempt, stPtr)
			}
		}

		// Attempt 5 (equals len(backoff)): must give up.
		msg, _, done, nextAt, givenUp := decide(500, nil, len(backoff), now)
		if msg != "HTTP 500" || !done || !givenUp || !nextAt.IsZero() {
			t.Errorf("attempt 5 (exhausted): msg=%q done=%v givenUp=%v nextAt=%v", msg, done, givenUp, nextAt)
		}

		// Beyond max attempts.
		_, _, done, nextAt, givenUp = decide(500, nil, len(backoff)+1, now)
		if !done || !givenUp || !nextAt.IsZero() {
			t.Errorf("attempt > max: done=%v givenUp=%v nextAt=%v", done, givenUp, nextAt)
		}
	})

	t.Run("network error precedence and nil status pointer", func(t *testing.T) {
		netErr := errors.New("dial tcp: connection refused")
		msg, stPtr, done, nextAt, givenUp := decide(0, netErr, 0, now)
		if msg != netErr.Error() {
			t.Errorf("msg = %q; want %q", msg, netErr.Error())
		}
		if stPtr != nil {
			t.Errorf("stPtr = %v; want nil for status 0", stPtr)
		}
		if done || givenUp || !nextAt.Equal(now.Add(1*time.Minute)) {
			t.Errorf("unexpected retry outcome for net error: done=%v givenUp=%v nextAt=%v", done, givenUp, nextAt)
		}
	})
}

func TestEvents(t *testing.T) {
	expectedEvents := []string{
		"deal.created",
		"deal.stage",
		"lead.created",
		"lead.stage",
		"task.created",
		"task.completed",
		"task.deleted",
		"openline.started",
		"openline.message",
		"openline.closed",
		"call.ended",
	}

	if len(Events) != len(expectedEvents) {
		t.Fatalf("Events length = %d; want %d", len(Events), len(expectedEvents))
	}

	seen := make(map[string]bool)
	for _, e := range Events {
		if seen[e] {
			t.Errorf("duplicate event in Events list: %q", e)
		}
		seen[e] = true
	}

	for _, exp := range expectedEvents {
		if !seen[exp] {
			t.Errorf("expected event missing from Events: %q", exp)
		}
	}
}

func TestSave_ValidationAndEventFiltering(t *testing.T) {
	s := &Store{} // Store without DB to verify pure validation and error returns.
	ctx := context.Background()

	tests := []struct {
		name    string
		hook    Webhook
		wantErr string
	}{
		{
			name:    "empty name",
			hook:    Webhook{Name: "", URL: "https://example.com/hook", Events: []string{"deal.created"}},
			wantErr: "err.hook_name",
		},
		{
			name:    "whitespace name",
			hook:    Webhook{Name: "   \t", URL: "https://example.com/hook", Events: []string{"deal.created"}},
			wantErr: "err.hook_name",
		},
		{
			name:    "name exceeding 100 runes",
			hook:    Webhook{Name: strings.Repeat("n", 101), URL: "https://example.com/hook", Events: []string{"deal.created"}},
			wantErr: "err.hook_name",
		},
		{
			name:    "cyrillic name exceeding 100 runes",
			hook:    Webhook{Name: strings.Repeat("я", 101), URL: "https://example.com/hook", Events: []string{"deal.created"}},
			wantErr: "err.hook_name",
		},
		{
			name:    "invalid url scheme",
			hook:    Webhook{Name: "Webhook", URL: "ftp://example.com", Events: []string{"deal.created"}},
			wantErr: "err.hook_url",
		},
		{
			name:    "empty url",
			hook:    Webhook{Name: "Webhook", URL: "", Events: []string{"deal.created"}},
			wantErr: "err.hook_url",
		},
		{
			name:    "nil events slice",
			hook:    Webhook{Name: "Webhook", URL: "https://example.com/hook", Events: nil},
			wantErr: "err.hook_events",
		},
		{
			name:    "empty events slice",
			hook:    Webhook{Name: "Webhook", URL: "https://example.com/hook", Events: []string{}},
			wantErr: "err.hook_events",
		},
		{
			name:    "only unknown events",
			hook:    Webhook{Name: "Webhook", URL: "https://example.com/hook", Events: []string{"unknown.event", "deal.modified"}},
			wantErr: "err.hook_events",
		},
		{
			name:    "case sensitive events",
			hook:    Webhook{Name: "Webhook", URL: "https://example.com/hook", Events: []string{"Deal.Created"}},
			wantErr: "err.hook_events",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Save(ctx, "user-1", tc.hook)
			if err == nil {
				t.Fatalf("Save() = nil; want error %s", tc.wantErr)
			}
			var i18nErr *i18n.Error
			if !errors.As(err, &i18nErr) || i18nErr.Key != tc.wantErr {
				t.Errorf("Save() error = %v; want key %q", err, tc.wantErr)
			}
		})
	}

	t.Run("event filtering retains known events", func(t *testing.T) {
		// When at least one known event is present alongside unknown events,
		// Save filters out the unknown ones, passes validation, and proceeds to DB access.
		// Since s.db is nil, it panics on s.db.QueryRow, confirming validation passed.
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic from nil s.db after successful validation")
			}
		}()

		hook := Webhook{
			Name:   "Mixed Events Hook",
			URL:    "https://example.com/hook",
			Events: []string{"fake.event", "deal.created", "another.unknown"},
		}
		_, _ = s.Save(ctx, "user-1", hook)
	})

	t.Run("update validation passes with existing id", func(t *testing.T) {
		// When w.ID is non-empty, Save validates and attempts s.db.Exec.
		// Since s.db is nil, it panics on Exec, confirming validation passed.
		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("expected panic from nil s.db on Exec after successful validation")
			}
		}()

		hook := Webhook{
			ID:     "existing-id-123",
			Name:   "Updated Hook",
			URL:    "https://example.com/updated",
			Events: []string{"task.created"},
			Active: true,
		}
		_, _ = s.Save(ctx, "user-1", hook)
	})
}

func TestNewStore(t *testing.T) {
	s := NewStore(nil)
	if s == nil {
		t.Fatal("NewStore returned nil")
	}
	if s.db != nil {
		t.Errorf("s.db = %v; want nil", s.db)
	}
	if s.client == nil {
		t.Fatal("s.client is nil")
	}
	if s.client.Timeout != 15*time.Second {
		t.Errorf("s.client.Timeout = %v; want 15s", s.client.Timeout)
	}

	// Verify s.post works with the client configured by NewStore.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	status, err := s.post(context.Background(), srv.URL, "secret", "ping", []byte("{}"))
	if err != nil || status != http.StatusOK {
		t.Fatalf("post() using NewStore client: status = %d, err = %v", status, err)
	}
}

func TestWebhookStruct(t *testing.T) {
	now := time.Now()
	code := 200
	w := Webhook{
		ID:         "hook-123",
		Name:       "CRM Sync",
		URL:        "https://n8n.internal/webhook",
		Secret:     "secret-hex",
		Events:     []string{"deal.created", "deal.stage"},
		Active:     true,
		LastAt:     &now,
		LastStatus: &code,
		LastError:  "",
		Pending:    3,
	}

	if w.ID != "hook-123" || w.Name != "CRM Sync" || !w.Active || w.Pending != 3 {
		t.Fatalf("Webhook fields mismatch: %+v", w)
	}
	if *w.LastStatus != 200 || !w.LastAt.Equal(now) {
		t.Fatalf("Webhook pointer fields mismatch: %+v", w)
	}
}
