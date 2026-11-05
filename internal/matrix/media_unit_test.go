package matrix

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpload(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		const payload = "hello media content"
		var receivedFilename, receivedAuth, receivedContentType string
		var receivedLength int64
		var receivedBody string

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedFilename = r.URL.Query().Get("filename")
			receivedAuth = r.Header.Get("Authorization")
			receivedContentType = r.Header.Get("Content-Type")
			receivedLength = r.ContentLength
			b, _ := io.ReadAll(r.Body)
			receivedBody = string(b)

			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"content_uri":"mxc://chat.example.com/media_xyz"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "chat.example.com", "bot-tok")
		uri, err := c.Upload(context.Background(), "user-tok", "doc test.pdf", "application/pdf", strings.NewReader(payload), int64(len(payload)))
		if err != nil {
			t.Fatalf("unexpected upload error: %v", err)
		}
		if uri != "mxc://chat.example.com/media_xyz" {
			t.Errorf("uri = %q, want 'mxc://chat.example.com/media_xyz'", uri)
		}
		if receivedFilename != "doc test.pdf" {
			t.Errorf("filename = %q, want 'doc test.pdf'", receivedFilename)
		}
		if receivedAuth != "Bearer user-tok" {
			t.Errorf("auth = %q, want 'Bearer user-tok'", receivedAuth)
		}
		if receivedContentType != "application/pdf" {
			t.Errorf("content-type = %q, want 'application/pdf'", receivedContentType)
		}
		if receivedLength != int64(len(payload)) {
			t.Errorf("content-length = %d, want %d", receivedLength, len(payload))
		}
		if receivedBody != payload {
			t.Errorf("body = %q, want %q", receivedBody, payload)
		}
	})

	t.Run("server error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN","error":"Upload quota exceeded"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "chat.example.com", "bot-tok")
		_, err := c.Upload(context.Background(), "user-tok", "file.txt", "text/plain", strings.NewReader("hi"), 2)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		matErr, ok := err.(*Error)
		if !ok || matErr.Status != http.StatusForbidden || matErr.Code != "M_FORBIDDEN" {
			t.Errorf("err = %v, want *Error with status 403 and M_FORBIDDEN", err)
		}
	})

	t.Run("missing or non-mxc URI", func(t *testing.T) {
		for _, resp := range []string{
			`{"content_uri":""}`,
			`{"content_uri":"http://evil.com/media"}`,
			`{"other":"data"}`,
			`not a json`,
		} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(resp))
			}))
			c := New(srv.URL, "chat.example.com", "bot-tok")
			_, err := c.Upload(context.Background(), "tok", "f.txt", "text/plain", strings.NewReader("x"), 1)
			srv.Close()
			if err == nil || !strings.Contains(err.Error(), "no content_uri") {
				t.Errorf("for response %q, expected 'no content_uri' error, got %v", resp, err)
			}
		}
	})

	t.Run("network error", func(t *testing.T) {
		c := New("http://127.0.0.1:0", "chat.example.com", "bot-tok")
		_, err := c.Upload(context.Background(), "tok", "f.txt", "text/plain", strings.NewReader("x"), 1)
		if err == nil || !strings.Contains(err.Error(), "matrix upload:") {
			t.Fatalf("expected matrix upload error, got %v", err)
		}
	})
}

func TestDownload(t *testing.T) {
	t.Run("invalid mxc URI validation", func(t *testing.T) {
		tests := []struct {
			mxc string
		}{
			{""},
			{"mxc://"},
			{"http://example.com/media"},
			{"mxc://server-only-no-slash"},
			{"mxc:///only-id-no-server"},
			{"mxc://server/empty-id/"},
			{"mxc://server/id/extra/slash"},
		}

		c := New("http://dummy", "chat.example.com", "bot-tok")
		for _, tt := range tests {
			res, err := c.Download(context.Background(), "user-tok", tt.mxc)
			if res != nil {
				res.Body.Close()
				t.Errorf("Download(%q) returned non-nil response", tt.mxc)
			}
			if !IsCode(err, "M_NOT_FOUND") {
				t.Errorf("Download(%q) err = %v, want M_NOT_FOUND", tt.mxc, err)
			}
		}
	})

	t.Run("success", func(t *testing.T) {
		const content = "media binary content"
		var receivedPath, receivedAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedPath = r.URL.Path
			receivedAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte(content))
		}))
		defer srv.Close()

		c := New(srv.URL, "chat.example.com", "bot-tok")
		res, err := c.Download(context.Background(), "tok-xyz", "mxc://synapse:8008/media123")
		if err != nil {
			t.Fatalf("unexpected download error: %v", err)
		}
		defer res.Body.Close()

		if receivedPath != "/_matrix/client/v1/media/download/synapse:8008/media123" {
			t.Errorf("path = %q, want '/_matrix/client/v1/media/download/synapse:8008/media123'", receivedPath)
		}
		if receivedAuth != "Bearer tok-xyz" {
			t.Errorf("auth = %q, want 'Bearer tok-xyz'", receivedAuth)
		}
		b, _ := io.ReadAll(res.Body)
		if string(b) != content {
			t.Errorf("content = %q, want %q", string(b), content)
		}
	})

	t.Run("server error status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"errcode":"M_NOT_FOUND","error":"Media not found"}`))
		}))
		defer srv.Close()

		c := New(srv.URL, "chat.example.com", "bot-tok")
		res, err := c.Download(context.Background(), "tok", "mxc://server/missing123")
		if res != nil {
			res.Body.Close()
			t.Fatal("expected nil response on error")
		}
		if !IsCode(err, "M_NOT_FOUND") {
			t.Errorf("err = %v, want M_NOT_FOUND", err)
		}
	})

	t.Run("network error", func(t *testing.T) {
		c := New("http://127.0.0.1:0", "chat.example.com", "bot-tok")
		res, err := c.Download(context.Background(), "tok", "mxc://server/media123")
		if res != nil {
			res.Body.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "matrix download:") {
			t.Fatalf("expected matrix download error, got %v", err)
		}
	})
}
