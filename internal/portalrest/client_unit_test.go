package portalrest

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	for _, tc := range []struct {
		name    string
		webhook string
		want    string
		wantErr bool
	}{
		{
			name:    "valid standard webhook",
			webhook: "https://myportal.portal.com/rest/1/abc123xyz/",
			want:    "https://myportal.portal.com/rest/1/abc123xyz/",
		},
		{
			name:    "valid webhook without trailing slash",
			webhook: "https://myportal.portal.com/rest/1/abc123xyz",
			want:    "https://myportal.portal.com/rest/1/abc123xyz/",
		},
		{
			name:    "valid webhook with surrounding whitespace",
			webhook: "  https://myportal.portal.ru/rest/42/token/  ",
			want:    "https://myportal.portal.ru/rest/42/token/",
		},
		{
			name:    "valid custom domain with port",
			webhook: "https://portal.company.org:8443/custom/rest/1/key",
			want:    "https://portal.company.org:8443/custom/rest/1/key/",
		},
		{
			name:    "empty webhook",
			webhook: "",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			webhook: "   \t\n  ",
			wantErr: true,
		},
		{
			name:    "http scheme rejected",
			webhook: "http://myportal.portal.com/rest/1/abc123xyz/",
			wantErr: true,
		},
		{
			name:    "missing /rest/ path segment",
			webhook: "https://myportal.portal.com/api/1/abc123xyz/",
			wantErr: true,
		},
		{
			name:    "ftp scheme rejected",
			webhook: "ftp://myportal.portal.com/rest/1/abc123xyz/",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(tc.webhook)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("New(%q) expected error, got nil", tc.webhook)
				}
				if !strings.Contains(err.Error(), "webhook: expected https://<portal>/rest/<user>/<token>/") {
					t.Errorf("New(%q) error = %q, want expected format message", tc.webhook, err.Error())
				}
				if c != nil {
					t.Errorf("New(%q) client = %v, want nil", tc.webhook, c)
				}
				return
			}
			if err != nil {
				t.Fatalf("New(%q) unexpected error: %v", tc.webhook, err)
			}
			if c == nil {
				t.Fatalf("New(%q) returned nil client", tc.webhook)
			}
			if c.base != tc.want {
				t.Errorf("c.base = %q, want %q", c.base, tc.want)
			}
			if c.Gap != 550*time.Millisecond {
				t.Errorf("c.Gap = %v, want 550ms", c.Gap)
			}
			if c.http == nil || c.http.Timeout != 60*time.Second {
				t.Errorf("c.http timeout = %v, want 60s", c.http)
			}
		})
	}
}

func TestHost(t *testing.T) {
	for _, tc := range []struct {
		base string
		want string
	}{
		{"https://myportal.portal.com/rest/1/token/", "myportal.portal.com"},
		{"https://portal.company.ru:8443/rest/42/token/", "portal.company.ru:8443"},
		{"https://portal.domain.org/sub/path/rest/1/token/", "portal.domain.org"},
		{"https://simple.com/", "simple.com"},
	} {
		c := &Client{base: tc.base}
		if got := c.Host(); got != tc.want {
			t.Errorf("Host() for %q = %q, want %q", tc.base, got, tc.want)
		}
	}
}

func TestCall(t *testing.T) {
	t.Run("success response with next and total", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			if r.URL.Path != "/rest/1/token/user.get.json" {
				t.Errorf("path = %s, want /rest/1/token/user.get.json", r.URL.Path)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %s, want application/json", ct)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			if body["FILTER"].(map[string]any)["ACTIVE"] != true {
				t.Errorf("unexpected body: %v", body)
			}
			next := 50
			_ = json.NewEncoder(w).Encode(response{
				Result: json.RawMessage(`[{"ID":"1","NAME":"Boris"}]`),
				Next:   &next,
				Total:  120,
			})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		res, err := c.Call(context.Background(), "user.get", map[string]any{"FILTER": map[string]any{"ACTIVE": true}})
		if err != nil {
			t.Fatalf("Call() unexpected error: %v", err)
		}
		if res.Total != 120 {
			t.Errorf("res.Total = %d, want 120", res.Total)
		}
		if res.Next == nil || *res.Next != 50 {
			t.Errorf("res.Next = %v, want 50", res.Next)
		}
		if string(res.Result) != `[{"ID":"1","NAME":"Boris"}]` {
			t.Errorf("res.Result = %s", string(res.Result))
		}
	})

	t.Run("portal error response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(response{
				Error:     "ERROR_METHOD_NOT_FOUND",
				ErrorDesc: "Method not found!",
			})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		res, err := c.Call(context.Background(), "unknown.method", nil)
		if err == nil {
			t.Fatalf("Call() expected error, got res = %v", res)
		}
		want := "unknown.method: ERROR_METHOD_NOT_FOUND: Method not found!"
		if err.Error() != want {
			t.Errorf("Call() error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("non-json error response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "<html>502 Bad Gateway</html>")
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		_, err := c.Call(context.Background(), "crm.deal.list", nil)
		if err == nil {
			t.Fatal("Call() expected error for non-JSON response")
		}
		want := "crm.deal.list: HTTP 502, not JSON"
		if err.Error() != want {
			t.Errorf("Call() error = %q, want %q", err.Error(), want)
		}
	})

	t.Run("portal unreachable error hides secret url", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		secretURL := srv.URL + "/rest/user_secret/token_secret/"
		srv.Close() // immediately close so connection fails

		c := &Client{base: secretURL, http: srv.Client(), Gap: 0}
		_, err := c.Call(context.Background(), "department.get", nil)
		if err == nil {
			t.Fatal("Call() expected error for unreachable portal")
		}
		if err.Error() != "department.get: portal unreachable" {
			t.Errorf("Call() error = %q, want 'department.get: portal unreachable'", err.Error())
		}
		if strings.Contains(err.Error(), "token_secret") || strings.Contains(err.Error(), srv.URL) {
			t.Errorf("error %q leaked secret URL or token", err.Error())
		}
	})

	t.Run("unmarshalable params", func(t *testing.T) {
		c := &Client{base: "https://example.com/rest/1/token/", http: http.DefaultClient, Gap: 0}
		_, err := c.Call(context.Background(), "user.get", make(chan int))
		if err == nil {
			t.Fatal("Call() expected json marshal error")
		}
	})

	t.Run("invalid method produces bad request error", func(t *testing.T) {
		c := &Client{base: "https://example.com/rest/1/token/", http: http.DefaultClient, Gap: 0}
		_, err := c.Call(context.Background(), "bad\x7fmethod", nil)
		if err == nil {
			t.Fatal("Call() expected bad request error")
		}
		if !strings.Contains(err.Error(), "bad request") {
			t.Errorf("Call() error = %q, want 'bad request'", err.Error())
		}
	})

	t.Run("rate limiter gap wait", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(response{Result: json.RawMessage(`{}`)})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 2 * time.Millisecond, last: time.Now()}
		res, err := c.Call(context.Background(), "user.get", nil)
		if err != nil {
			t.Fatalf("Call() unexpected error: %v", err)
		}
		if res == nil {
			t.Fatal("Call() returned nil response")
		}
	})

	t.Run("context cancelled before call", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		c := &Client{base: "https://example.com/rest/1/token/", http: http.DefaultClient, Gap: time.Hour, last: time.Now()}
		_, err := c.Call(ctx, "user.get", nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Call() error = %v, want context.Canceled", err)
		}
	})
}

func TestList(t *testing.T) {
	t.Run("multi-page pagination", func(t *testing.T) {
		type item struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		requests := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			start := int(body["start"].(float64))
			if body["custom_filter"] != "active" {
				t.Errorf("custom_filter param lost in request: %v", body)
			}

			switch start {
			case 0:
				next := 2
				_ = json.NewEncoder(w).Encode(response{
					Result: json.RawMessage(`[{"id":"1","name":"A"},{"id":"2","name":"B"}]`),
					Next:   &next,
					Total:  3,
				})
			case 2:
				_ = json.NewEncoder(w).Encode(response{
					Result: json.RawMessage(`[{"id":"3","name":"C"}]`),
					Next:   nil,
					Total:  3,
				})
			default:
				t.Errorf("unexpected start: %d", start)
			}
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		var collected []item
		err := List(context.Background(), c, "custom.list", map[string]any{"custom_filter": "active"}, func(it item) error {
			collected = append(collected, it)
			return nil
		})
		if err != nil {
			t.Fatalf("List() unexpected error: %v", err)
		}
		if requests != 2 {
			t.Errorf("expected 2 page requests, got %d", requests)
		}
		if len(collected) != 3 {
			t.Fatalf("collected %d items, want 3", len(collected))
		}
		if collected[0].Name != "A" || collected[1].Name != "B" || collected[2].Name != "C" {
			t.Errorf("collected items = %+v", collected)
		}
	})

	t.Run("callback error aborts pagination", func(t *testing.T) {
		type item struct {
			ID string `json:"id"`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next := 2
			_ = json.NewEncoder(w).Encode(response{
				Result: json.RawMessage(`[{"id":"1"},{"id":"2"}]`),
				Next:   &next,
			})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		callCount := 0
		errAbort := errors.New("abort")
		err := List(context.Background(), c, "test.list", nil, func(it item) error {
			callCount++
			if callCount == 1 {
				return errAbort
			}
			return nil
		})
		if !errors.Is(err, errAbort) {
			t.Errorf("List() error = %v, want errAbort", err)
		}
		if callCount != 1 {
			t.Errorf("callback called %d times, want 1", callCount)
		}
	})

	t.Run("unexpected result format", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(response{
				Result: json.RawMessage(`{"error_object":"not an array"}`),
			})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		err := List(context.Background(), c, "bad.list", nil, func(it string) error {
			return nil
		})
		if err == nil {
			t.Fatal("List() expected error for non-array result")
		}
		if !strings.Contains(err.Error(), "bad.list: unexpected result:") {
			t.Errorf("List() error = %q, want 'bad.list: unexpected result:' prefix", err.Error())
		}
	})

	t.Run("call error propagates", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(response{
				Error:     "ACCESS_DENIED",
				ErrorDesc: "No access",
			})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		err := List(context.Background(), c, "sec.list", nil, func(it string) error {
			return nil
		})
		if err == nil {
			t.Fatal("List() expected error")
		}
		if !strings.Contains(err.Error(), "ACCESS_DENIED") {
			t.Errorf("List() error = %q, want ACCESS_DENIED", err.Error())
		}
	})
}

func TestBatch(t *testing.T) {
	t.Run("standard batch response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/rest/1/token/batch.json" {
				t.Errorf("path = %s, want /rest/1/token/batch.json", r.URL.Path)
			}
			var p map[string]any
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				t.Errorf("decode batch body: %v", err)
			}
			if p["halt"].(float64) != 0 {
				t.Errorf("halt = %v, want 0", p["halt"])
			}
			cmds := p["cmd"].(map[string]any)
			if cmds["g1"] != "sonet_group.user.get?ID=1" || cmds["c2"] != "task.commentitem.getlist?TASKID=2" {
				t.Errorf("unexpected cmds: %v", cmds)
			}

			_ = json.NewEncoder(w).Encode(response{
				Result: json.RawMessage(`{"result":{"g1":[{"USER_ID":"10","ROLE":"A"}],"c2":[{"ID":"90","POST_MESSAGE":"ok"}]}}`),
			})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		res, err := c.Batch(context.Background(), map[string]string{
			"g1": "sonet_group.user.get?ID=1",
			"c2": "task.commentitem.getlist?TASKID=2",
		})
		if err != nil {
			t.Fatalf("Batch() unexpected error: %v", err)
		}
		if len(res) != 2 {
			t.Fatalf("Batch() returned %d items, want 2", len(res))
		}
		if string(res["g1"]) != `[{"USER_ID":"10","ROLE":"A"}]` {
			t.Errorf("res[g1] = %s", string(res["g1"]))
		}
		if string(res["c2"]) != `[{"ID":"90","POST_MESSAGE":"ok"}]` {
			t.Errorf("res[c2] = %s", string(res["c2"]))
		}
	})

	t.Run("empty result as array returns empty map", func(t *testing.T) {
		// the portal returns [] instead of {} when batch has no results
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(response{
				Result: json.RawMessage(`[]`),
			})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		res, err := c.Batch(context.Background(), map[string]string{})
		if err != nil {
			t.Fatalf("Batch() unexpected error: %v", err)
		}
		if res == nil {
			t.Fatal("Batch() returned nil map")
		}
		if len(res) != 0 {
			t.Errorf("Batch() map len = %d, want 0", len(res))
		}
	})

	t.Run("call error propagates", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(response{
				Error:     "NO_AUTH",
				ErrorDesc: "Unauthorized",
			})
		}))
		defer srv.Close()

		c := &Client{base: srv.URL + "/rest/1/token/", http: srv.Client(), Gap: 0}
		_, err := c.Batch(context.Background(), map[string]string{"foo": "bar"})
		if err == nil {
			t.Fatal("Batch() expected error")
		}
		if !strings.Contains(err.Error(), "NO_AUTH") {
			t.Errorf("Batch() error = %q, want NO_AUTH", err.Error())
		}
	})
}
