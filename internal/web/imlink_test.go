package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"go24/internal/auth"
	"go24/internal/linkpreview"
)

// The card of a link: its picture goes through go24; no card, no fetcher or a bad link — 204.
func TestIMLink(t *testing.T) {
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/a":
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`<head><meta property="og:title" content="Статья"><meta property="og:image" content="/p.jpg"></head>`))
		case "/p.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpg"))
		default:
			w.Header().Set("Content-Type", "text/html")
		}
	}))
	defer site.Close()
	f := linkpreview.New()
	f.Client = site.Client()
	s := &Server{Links: f}
	get := func(h func(http.ResponseWriter, *http.Request, auth.Session), path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest("GET", path, nil), auth.Session{UserID: "u"})
		return w
	}
	w := get(s.imLink, "/im/link?url="+url.QueryEscape(site.URL+"/a"))
	var p linkpreview.Preview
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Title != "Статья" || p.Image != "/im/link/image?url="+url.QueryEscape(site.URL+"/p.jpg") {
		t.Fatalf("card: %d %s", w.Code, w.Body)
	}
	img, _ := url.Parse(p.Image)
	if w := get(s.imLinkImage, img.String()); w.Code != 200 || w.Body.String() != "jpg" || w.Header().Get("Content-Type") != "image/jpeg" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("picture: %d %v", w.Code, w.Header())
	}
	if w := get(s.imLinkImage, "/im/link/image?url="+url.QueryEscape(site.URL+"/x.jpg")); w.Code != 404 {
		t.Fatalf("a picture of no card: %d", w.Code)
	}
	for _, path := range []string{"/im/link?url=" + url.QueryEscape(site.URL+"/empty"), "/im/link?url=javascript:1", "/im/link"} {
		if w := get(s.imLink, path); w.Code != 204 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	if w := get((&Server{}).imLink, "/im/link?url="+url.QueryEscape(site.URL+"/a")); w.Code != 204 {
		t.Fatalf("no fetcher: %d", w.Code)
	}
}
