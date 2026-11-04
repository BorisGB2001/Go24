package web

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRemembersPage(t *testing.T) {
	cases := []struct {
		name, method, target, dest, accept string
		want                               bool
	}{
		{"page", "GET", "/tasks/GO24DEV-1", "document", "text/html", true},
		{"old browser page", "GET", "/tasks", "", "text/html", true},
		{"unread stream", "GET", "/im/unread", "empty", "text/event-stream", false},
		{"stream without fetch metadata", "GET", "/im/unread", "", "text/event-stream", false},
		{"fetch", "GET", "/api/v1/me", "empty", "*/*", false},
		{"image", "GET", "/drive/f/1/thumb", "image", "image/*", false},
		{"post", "POST", "/tasks", "document", "text/html", false},
		{"long address", "GET", "/x?q=" + strings.Repeat("a", 2001), "document", "text/html", false},
	}
	for _, c := range cases {
		r := httptest.NewRequest(c.method, c.target, nil)
		if c.dest != "" {
			r.Header.Set("Sec-Fetch-Dest", c.dest)
		}
		r.Header.Set("Accept", c.accept)
		if got := remembersPage(r); got != c.want {
			t.Errorf("%s: remembersPage = %v, want %v", c.name, got, c.want)
		}
	}
}
