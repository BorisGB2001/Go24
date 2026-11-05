package openlines

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStoreSealUnseal(t *testing.T) {
	cases := []struct {
		name  string
		key   []byte
		plain string
	}{
		{"empty key and plain", nil, "hello world"},
		{"with secret key", []byte("my-super-secret-key-12345"), "telegram-bot-token-secret:ABCDEF"},
		{"empty plain text", []byte("key"), ""},
		{"unicode and symbols", []byte("key"), "Привет, мир! 🤖 123 <>&'\""},
		{"long text", []byte("key"), strings.Repeat("a1b2c3d4", 500)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Store{Key: tc.key}
			sealed, err := s.seal(tc.plain)
			if err != nil {
				t.Fatalf("seal failed: %v", err)
			}
			unsealed, err := s.unseal(sealed)
			if err != nil {
				t.Fatalf("unseal failed: %v", err)
			}
			if unsealed != tc.plain {
				t.Errorf("unseal = %q; want %q", unsealed, tc.plain)
			}
		})
	}

	t.Run("unseal corrupted data", func(t *testing.T) {
		s := &Store{Key: []byte("secret")}
		// shorter than NonceSize
		if _, err := s.unseal([]byte("short")); err == nil {
			t.Error("expected error for data shorter than NonceSize")
		}

		// corrupted ciphertext
		sealed, err := s.seal("valid token")
		if err != nil {
			t.Fatal(err)
		}
		sealed[len(sealed)-1] ^= 0xFF // flip bit
		if _, err := s.unseal(sealed); err == nil {
			t.Error("expected error for tampered ciphertext")
		}

		// unseal with different store key
		s2 := &Store{Key: []byte("different-secret")}
		sealedOrig, _ := s.seal("valid token")
		if _, err := s2.unseal(sealedOrig); err == nil {
			t.Error("expected error when unsealing with different store key")
		}
	})
}

func TestStoreTGToken(t *testing.T) {
	s1 := &Store{Key: []byte("k1")}
	s2 := &Store{Key: []byte("k2")}

	t1 := s1.tgToken("w1", 100)
	t2 := s1.tgToken("w1", 100)
	if t1 != t2 {
		t.Error("tgToken must be deterministic")
	}
	if !strings.HasPrefix(t1, "tg") {
		t.Errorf("tgToken %q must start with 'tg'", t1)
	}
	if len(t1) != 2+64 {
		t.Errorf("tgToken length = %d; want 66", len(t1))
	}

	// different chat
	if s1.tgToken("w1", 101) == t1 {
		t.Error("different chat must produce different token")
	}
	// different widget
	if s1.tgToken("w2", 100) == t1 {
		t.Error("different widget must produce different token")
	}
	// different store key
	if s2.tgToken("w1", 100) == t1 {
		t.Error("different store key must produce different token")
	}
}

func TestTGError(t *testing.T) {
	err := &tgError{Code: 401, Description: "Unauthorized"}
	want := "telegram: 401 Unauthorized"
	if got := err.Error(); got != want {
		t.Errorf("tgError.Error() = %q; want %q", got, want)
	}
}

func TestTGMessageFile(t *testing.T) {
	t.Run("photo priority and properties", func(t *testing.T) {
		m := tgMessage{
			Photo: []tgFile{
				{FileID: "p1", FileSize: 100},
				{FileID: "p2", FileSize: 200},
			},
			Document: &tgFile{FileID: "d1"},
		}
		f := m.file()
		if f == nil {
			t.Fatal("expected non-nil file")
		}
		if f.FileID != "p2" || f.FileName != "photo.jpg" || f.MimeType != "image/jpeg" {
			t.Errorf("photo file mismatch: %+v", f)
		}
	})

	t.Run("document when no photo", func(t *testing.T) {
		m := tgMessage{Document: &tgFile{FileID: "doc1", FileName: "test.pdf"}}
		f := m.file()
		if f == nil || f.FileID != "doc1" {
			t.Errorf("expected document doc1, got %+v", f)
		}
	})

	t.Run("video when no photo or document", func(t *testing.T) {
		m := tgMessage{Video: &tgFile{FileID: "vid1"}}
		f := m.file()
		if f == nil || f.FileID != "vid1" {
			t.Errorf("expected video vid1, got %+v", f)
		}
	})

	t.Run("audio when no higher priority", func(t *testing.T) {
		m := tgMessage{Audio: &tgFile{FileID: "aud1"}}
		f := m.file()
		if f == nil || f.FileID != "aud1" {
			t.Errorf("expected audio aud1, got %+v", f)
		}
	})

	t.Run("voice message", func(t *testing.T) {
		m := tgMessage{Voice: &tgFile{FileID: "voc1"}}
		f := m.file()
		if f == nil || f.FileID != "voc1" {
			t.Errorf("expected voice voc1, got %+v", f)
		}
	})

	t.Run("no files present", func(t *testing.T) {
		m := tgMessage{Text: "just text"}
		if f := m.file(); f != nil {
			t.Errorf("expected nil file, got %+v", f)
		}
	})
}

func TestTGFileName(t *testing.T) {
	cases := []struct {
		f    tgFile
		want string
	}{
		{tgFile{FileName: "invoice.pdf"}, "invoice.pdf"},
		{tgFile{MimeType: "application/pdf"}, "file.pdf"},
		{tgFile{MimeType: "image/png"}, "file.png"},
		{tgFile{MimeType: "custom/unknown"}, "file.bin"},
		{tgFile{}, "file.bin"},
	}
	for _, tc := range cases {
		if got := tc.f.name(); got != tc.want {
			t.Errorf("tgFile%+v.name() = %q; want %q", tc.f, got, tc.want)
		}
	}
}

func TestStoreTGCall(t *testing.T) {
	ctx := context.Background()

	t.Run("successful api call", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s; want POST", r.Method)
			}
			if r.URL.Path != "/botsecret-tok/getMe" {
				t.Errorf("path = %s; want /botsecret-tok/getMe", r.URL.Path)
			}
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("content-type = %s", ct)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]string{"username": "my_bot"},
			})
		}))
		defer srv.Close()

		s := &Store{TelegramAPI: srv.URL}
		var out struct {
			Username string `json:"username"`
		}
		err := s.tgCall(ctx, srv.Client(), "secret-tok", "getMe", map[string]any{}, &out)
		if err != nil {
			t.Fatalf("tgCall failed: %v", err)
		}
		if out.Username != "my_bot" {
			t.Errorf("username = %q; want 'my_bot'", out.Username)
		}
	})

	t.Run("api error returned as tgError", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":          false,
				"error_code":  401,
				"description": "Unauthorized",
			})
		}))
		defer srv.Close()

		s := &Store{TelegramAPI: srv.URL}
		err := s.tgCall(ctx, srv.Client(), "bad-tok", "getMe", nil, nil)
		var te *tgError
		if !errors.As(err, &te) {
			t.Fatalf("expected *tgError, got: %v", err)
		}
		if te.Code != 401 || te.Description != "Unauthorized" {
			t.Errorf("tgError mismatch: %+v", te)
		}
	})

	t.Run("bad json response", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("502 Bad Gateway"))
		}))
		defer srv.Close()

		s := &Store{TelegramAPI: srv.URL}
		err := s.tgCall(ctx, srv.Client(), "tok", "getMe", nil, nil)
		if err == nil || !strings.Contains(err.Error(), "502") {
			t.Errorf("expected 502 error, got: %v", err)
		}
	})

	t.Run("token masking in network error", func(t *testing.T) {
		// pointing to a closed port / nonexistent server
		s := &Store{TelegramAPI: "http://127.0.0.1:0"}
		err := s.tgCall(ctx, http.DefaultClient, "secret123456", "getMe", nil, nil)
		if err == nil {
			t.Fatal("expected network error")
		}
		if strings.Contains(err.Error(), "secret123456") {
			t.Errorf("token leaked in error message: %v", err)
		}
		if !strings.Contains(err.Error(), "***") {
			t.Errorf("token should be replaced with '***': %v", err)
		}
	})
}

func TestStoreTGSend(t *testing.T) {
	ctx := context.Background()

	t.Run("text message with html escaping and operator", func(t *testing.T) {
		var received struct {
			ChatID    int64  `json:"chat_id"`
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/bottoken/sendMessage" {
				t.Errorf("unexpected path: %s", r.URL.Path)
			}
			_ = json.NewDecoder(r.Body).Decode(&received)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
		}))
		defer srv.Close()

		s := &Store{TelegramAPI: srv.URL}
		err := s.tgSend(ctx, "token", 12345, "Support & Co <Team>", "Hello <World> & 'You'", "", "", "", nil)
		if err != nil {
			t.Fatalf("tgSend failed: %v", err)
		}
		if received.ChatID != 12345 {
			t.Errorf("ChatID = %d; want 12345", received.ChatID)
		}
		if received.ParseMode != "HTML" {
			t.Errorf("ParseMode = %s; want HTML", received.ParseMode)
		}
		wantText := "<b>Support &amp; Co &lt;Team&gt;</b>\nHello &lt;World&gt; &amp; &#39;You&#39;"
		if received.Text != wantText {
			t.Errorf("Text = %q; want %q", received.Text, wantText)
		}
	})

	t.Run("text message with buttons keyboard", func(t *testing.T) {
		var received struct {
			ReplyMarkup struct {
				Keyboard       [][]map[string]string `json:"keyboard"`
				ResizeKeyboard bool                  `json:"resize_keyboard"`
				OneTime        bool                  `json:"one_time_keyboard"`
			} `json:"reply_markup"`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&received)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
		}))
		defer srv.Close()

		s := &Store{TelegramAPI: srv.URL}
		err := s.tgSend(ctx, "token", 123, "", "Pick one:", "", "", "", []string{"Option 1", "Option 2"})
		if err != nil {
			t.Fatal(err)
		}
		kb := received.ReplyMarkup.Keyboard
		if len(kb) != 2 || kb[0][0]["text"] != "Option 1" || kb[1][0]["text"] != "Option 2" {
			t.Errorf("keyboard mismatch: %+v", kb)
		}
		if !received.ReplyMarkup.ResizeKeyboard || !received.ReplyMarkup.OneTime {
			t.Errorf("reply markup flags mismatch: %+v", received.ReplyMarkup)
		}
	})

	t.Run("file with openfile nil appends filename to text", func(t *testing.T) {
		var received struct {
			Text string `json:"text"`
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&received)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
		}))
		defer srv.Close()

		s := &Store{TelegramAPI: srv.URL, OpenFile: nil}
		err := s.tgSend(ctx, "token", 123, "Alice", "Check attachment", "file_key_1", "doc.pdf", "application/pdf", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(received.Text, "📎 doc.pdf") {
			t.Errorf("expected attachment indicator in text: %q", received.Text)
		}
	})

	t.Run("file sending photo vs document", func(t *testing.T) {
		var calledMethod string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parts := strings.Split(r.URL.Path, "/")
			calledMethod = parts[len(parts)-1]
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{}})
		}))
		defer srv.Close()

		s := &Store{
			TelegramAPI: srv.URL,
			OpenFile: func(ctx context.Context, key string) (io.ReadCloser, error) {
				return io.NopCloser(strings.NewReader("fake-file-data")), nil
			},
		}

		// image/png -> sendPhoto
		if err := s.tgSend(ctx, "tok", 123, "", "a photo", "k1", "pic.png", "image/png", nil); err != nil {
			t.Fatal(err)
		}
		if calledMethod != "sendPhoto" {
			t.Errorf("calledMethod = %q; want sendPhoto", calledMethod)
		}

		// image/gif -> sendDocument
		if err := s.tgSend(ctx, "tok", 123, "", "a gif", "k2", "anim.gif", "image/gif", nil); err != nil {
			t.Fatal(err)
		}
		if calledMethod != "sendDocument" {
			t.Errorf("calledMethod = %q; want sendDocument for gif", calledMethod)
		}

		// application/pdf -> sendDocument
		if err := s.tgSend(ctx, "tok", 123, "", "a doc", "k3", "file.pdf", "application/pdf", nil); err != nil {
			t.Fatal(err)
		}
		if calledMethod != "sendDocument" {
			t.Errorf("calledMethod = %q; want sendDocument for pdf", calledMethod)
		}
	})

	t.Run("openfile error returned", func(t *testing.T) {
		s := &Store{
			TelegramAPI: "http://localhost",
			OpenFile: func(ctx context.Context, key string) (io.ReadCloser, error) {
				return nil, errors.New("file storage failure")
			},
		}
		err := s.tgSend(ctx, "tok", 123, "", "text", "bad-key", "f.png", "image/png", nil)
		if err == nil || !strings.Contains(err.Error(), "file storage failure") {
			t.Errorf("expected storage error, got: %v", err)
		}
	})
}
