package matrix

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
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
	tests := []struct {
		name       string
		base       string
		serverName string
		token      string
		wantNil    bool
		wantBase   string
	}{
		{"empty base", "", "example.com", "tok", true, ""},
		{"empty serverName", "http://synapse:8008", "", "tok", true, ""},
		{"empty token", "http://synapse:8008", "example.com", "", true, ""},
		{"all empty", "", "", "", true, ""},
		{"valid no slash", "http://synapse:8008", "example.com", "tok", false, "http://synapse:8008"},
		{"valid trailing slash trimmed", "http://synapse:8008/", "example.com", "tok", false, "http://synapse:8008"},
		{"valid multiple trailing slashes trimmed", "http://synapse:8008///", "example.com", "tok", false, "http://synapse:8008"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(tt.base, tt.serverName, tt.token)
			if tt.wantNil {
				if c != nil {
					t.Fatalf("New(%q, %q, %q) = %+v, want nil", tt.base, tt.serverName, tt.token, c)
				}
				return
			}
			if c == nil {
				t.Fatalf("New(%q, %q, %q) returned nil", tt.base, tt.serverName, tt.token)
			}
			if c.Base != tt.wantBase {
				t.Errorf("c.Base = %q, want %q", c.Base, tt.wantBase)
			}
			if c.ServerName != tt.serverName {
				t.Errorf("c.ServerName = %q, want %q", c.ServerName, tt.serverName)
			}
			if c.Token != tt.token {
				t.Errorf("c.Token = %q, want %q", c.Token, tt.token)
			}
			if c.HTTP == nil || c.HTTP.Timeout != 60*time.Second {
				t.Errorf("c.HTTP.Timeout = %v, want 60s", c.HTTP.Timeout)
			}
		})
	}
}

func TestError_Error(t *testing.T) {
	e := &Error{
		Status:  http.StatusNotFound,
		Code:    "M_NOT_FOUND",
		Message: "Room was not found",
	}
	want := "matrix: HTTP 404 M_NOT_FOUND: Room was not found"
	if got := e.Error(); got != want {
		t.Errorf("e.Error() = %q, want %q", got, want)
	}
}

func TestIsCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		want bool
	}{
		{"nil error", nil, "M_NOT_FOUND", false},
		{"standard error", errors.New("something went wrong"), "M_NOT_FOUND", false},
		{"matching code", &Error{Code: "M_NOT_FOUND"}, "M_NOT_FOUND", true},
		{"different code", &Error{Code: "M_FORBIDDEN"}, "M_NOT_FOUND", false},
		{"empty code match", &Error{Code: ""}, "", true},
		{"empty code mismatch", &Error{Code: "M_FORBIDDEN"}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsCode(tt.err, tt.code); got != tt.want {
				t.Errorf("IsCode(%v, %q) = %v, want %v", tt.err, tt.code, got, tt.want)
			}
		})
	}
}

func TestUserID(t *testing.T) {
	c := New("http://localhost:8008", "chat.example.com", "token123")
	tests := []struct {
		localpart string
		want      string
	}{
		{"alice", "@alice:chat.example.com"},
		{"bot.help", "@bot.help:chat.example.com"},
		{"user_123", "@user_123:chat.example.com"},
	}
	for _, tt := range tests {
		if got := c.UserID(tt.localpart); got != tt.want {
			t.Errorf("UserID(%q) = %q, want %q", tt.localpart, got, tt.want)
		}
	}
}

func TestEsc(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"@alice:example.com", "@alice:example.com"},
		{"!room$1:server", "%21room$1:server"},
		{"hello/world", "hello%2Fworld"},
		{"hello world", "hello%20world"},
		{"a?b=c&d=e", "a%3Fb=c&d=e"},
	}
	for _, tt := range tests {
		if got := esc(tt.in); got != tt.want {
			t.Errorf("esc(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestClient_Do(t *testing.T) {
	t.Run("success with body and out and custom token", func(t *testing.T) {
		var receivedAuth, receivedContentType, receivedBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAuth = r.Header.Get("Authorization")
			receivedContentType = r.Header.Get("Content-Type")
			b, _ := io.ReadAll(r.Body)
			receivedBody = string(b)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"result":"ok","count":42}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "bot-token")
		var out struct {
			Result string `json:"result"`
			Count  int    `json:"count"`
		}

		err := c.Do(context.Background(), http.MethodPost, "/test/path", map[string]string{"foo": "bar"}, &out, "user-token")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedAuth != "Bearer user-token" {
			t.Errorf("received auth = %q, want 'Bearer user-token'", receivedAuth)
		}
		if receivedContentType != "application/json" {
			t.Errorf("received content type = %q, want 'application/json'", receivedContentType)
		}
		if receivedBody != `{"foo":"bar"}` {
			t.Errorf("received body = %q, want '{\"foo\":\"bar\"}'", receivedBody)
		}
		if out.Result != "ok" || out.Count != 42 {
			t.Errorf("out = %+v, want Result: ok, Count: 42", out)
		}
	})

	t.Run("default token when token param is empty", func(t *testing.T) {
		var receivedAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "default-bot-token")
		err := c.Do(context.Background(), http.MethodGet, "/test", nil, nil, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if receivedAuth != "Bearer default-bot-token" {
			t.Errorf("auth = %q, want default bot token", receivedAuth)
		}
	})

	t.Run("body marshal error", func(t *testing.T) {
		c := New("http://dummy", "example.com", "token")
		// Channels cannot be JSON-marshaled.
		err := c.Do(context.Background(), http.MethodPost, "/path", make(chan int), nil)
		if err == nil {
			t.Fatal("expected error on unmarshalable body, got nil")
		}
	})

	t.Run("http error response decoded into Error struct", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"You are not in this room"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "token")
		err := c.Do(context.Background(), http.MethodGet, "/room", nil, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		matErr, ok := err.(*Error)
		if !ok {
			t.Fatalf("expected *Error, got %T: %v", err, err)
		}
		if matErr.Status != http.StatusForbidden {
			t.Errorf("Status = %d, want 403", matErr.Status)
		}
		if matErr.Code != "M_FORBIDDEN" {
			t.Errorf("Code = %q, want M_FORBIDDEN", matErr.Code)
		}
		if matErr.Message != "You are not in this room" {
			t.Errorf("Message = %q, want 'You are not in this room'", matErr.Message)
		}
	})

	t.Run("http non-json error response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`<html>502 Bad Gateway</html>`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "token")
		err := c.Do(context.Background(), http.MethodGet, "/gateway", nil, nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		matErr, ok := err.(*Error)
		if !ok {
			t.Fatalf("expected *Error, got %T: %v", err, err)
		}
		if matErr.Status != http.StatusBadGateway {
			t.Errorf("Status = %d, want 502", matErr.Status)
		}
		if matErr.Code != "" {
			t.Errorf("Code = %q, want empty", matErr.Code)
		}
	})

	t.Run("context canceled during 429 retry wait", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"errcode":"M_LIMIT_EXCEEDED","retry_after_ms":5000}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "example.com", "token")
		ctx, cancel := context.WithCancel(context.Background())
		// Cancel immediately so the select in Do picks <-ctx.Done()
		cancel()

		err := c.Do(ctx, http.MethodGet, "/retry", nil, nil)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	})

	t.Run("network error returns wrapped error with clean path prefix", func(t *testing.T) {
		// Use an address that is not listening.
		c := New("http://127.0.0.1:0", "example.com", "token")
		err := c.Do(context.Background(), http.MethodGet, "/test/path?secret=123", nil, nil)
		if err == nil {
			t.Fatal("expected network error, got nil")
		}
		if !strings.HasPrefix(err.Error(), "matrix GET /test/path:") {
			t.Errorf("err message %q does not match expected prefix", err.Error())
		}
	})
}

func TestNoRatelimit(t *testing.T) {
	var receivedMethod, receivedPath string
	var receivedBody map[string]int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		receivedPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&receivedBody)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	err := c.NoRatelimit(context.Background(), "@user:example.com")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if receivedMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", receivedMethod)
	}
	wantPath := "/_synapse/admin/v1/users/@user:example.com/override_ratelimit"
	if receivedPath != wantPath {
		t.Errorf("path = %q, want %q", receivedPath, wantPath)
	}
	if receivedBody["messages_per_second"] != 0 || receivedBody["burst_count"] != 0 {
		t.Errorf("unexpected body: %v", receivedBody)
	}
}

func TestWhoAmI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_matrix/client/v3/account/whoami" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"user_id":"@bot:example.com"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "example.com", "tok")
	uid, err := c.WhoAmI(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if uid != "@bot:example.com" {
		t.Errorf("uid = %q, want '@bot:example.com'", uid)
	}
}

func TestRegisterAdmin(t *testing.T) {
	const secret = "shared-secret-key"
	const localpart = "matrixbot"
	const displayName = "Matrix Bot"

	t.Run("success flow", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				// Nonce request
				if r.Method != http.MethodGet || r.URL.Path != "/_synapse/admin/v1/register" {
					t.Errorf("call 1: unexpected %s %s", r.Method, r.URL.Path)
				}
				_, _ = w.Write([]byte(`{"nonce":"nonce_12345"}`))
				return
			}
			// Register request
			if r.Method != http.MethodPost || r.URL.Path != "/_synapse/admin/v1/register" {
				t.Errorf("call 2: unexpected %s %s", r.Method, r.URL.Path)
			}
			var body struct {
				Nonce       string `json:"nonce"`
				Username    string `json:"username"`
				DisplayName string `json:"displayname"`
				Password    string `json:"password"`
				Admin       bool   `json:"admin"`
				MAC         string `json:"mac"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode register body: %v", err)
			}
			if body.Nonce != "nonce_12345" {
				t.Errorf("body.Nonce = %q, want nonce_12345", body.Nonce)
			}
			if body.Username != localpart {
				t.Errorf("body.Username = %q, want %q", body.Username, localpart)
			}
			if body.DisplayName != displayName {
				t.Errorf("body.DisplayName = %q, want %q", body.DisplayName, displayName)
			}
			if !body.Admin {
				t.Error("body.Admin is not true")
			}
			// Verify HMAC signature
			mac := hmac.New(sha1.New, []byte(secret))
			mac.Write([]byte("nonce_12345\x00" + localpart + "\x00" + body.Password + "\x00admin"))
			expectedMAC := hex.EncodeToString(mac.Sum(nil))
			if body.MAC != expectedMAC {
				t.Errorf("body.MAC = %q, want %q", body.MAC, expectedMAC)
			}

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"access_token":"syt_registered_token"}`))
		}))
		defer srv.Close()

		tok, err := RegisterAdmin(context.Background(), srv.URL, secret, localpart, displayName)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if tok != "syt_registered_token" {
			t.Errorf("tok = %q, want 'syt_registered_token'", tok)
		}
	})

	t.Run("empty nonce error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"nonce":""}`))
		}))
		defer srv.Close()

		_, err := RegisterAdmin(context.Background(), srv.URL, secret, localpart, displayName)
		if err == nil || !strings.Contains(err.Error(), "no registration nonce") {
			t.Fatalf("expected 'no registration nonce' error, got %v", err)
		}
	})

	t.Run("registration failure status", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				_, _ = w.Write([]byte(`{"nonce":"nonce_abc"}`))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"User ID already taken"}`))
		}))
		defer srv.Close()

		_, err := RegisterAdmin(context.Background(), srv.URL, secret, localpart, displayName)
		if err == nil || !strings.Contains(err.Error(), "HTTP 400 User ID already taken") {
			t.Fatalf("expected HTTP 400 error, got %v", err)
		}
	})

	t.Run("get nonce network error", func(t *testing.T) {
		_, err := RegisterAdmin(context.Background(), "http://127.0.0.1:0", secret, localpart, displayName)
		if err == nil {
			t.Fatal("expected network error, got nil")
		}
	})
}
