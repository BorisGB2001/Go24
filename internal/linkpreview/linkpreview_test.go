package linkpreview

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	base, _ := url.Parse("https://www.example.org/news/1")
	p := Parse(strings.NewReader(`<html><head><title>Fallback</title>
<meta property="og:title" content="  Новость   дня ">
<meta name="description" content="plain">
<meta property="og:description" content="Описание">
<meta property="og:image" content="/img/a.png">
</head><body><meta property="og:title" content="from the body"></body></html>`), base)
	want := Preview{URL: base.String(), Site: "example.org", Title: "Новость дня", Text: "Описание", Image: "https://www.example.org/img/a.png"}
	if p != want {
		t.Fatalf("got %+v, want %+v", p, want)
	}
	// no Open Graph: <title> and the site name; a picture of another scheme is dropped
	p = Parse(strings.NewReader(`<head><title>Only `+strings.Repeat("x", 300)+`</title><meta property="og:site_name" content="Site"><meta property="og:image" content="javascript:alert(1)"></head>`), base)
	if p.Site != "Site" || !strings.HasPrefix(p.Title, "Only x") || !strings.HasSuffix(p.Title, "…") || len([]rune(p.Title)) != 200 || p.Image != "" {
		t.Fatalf("fallback: %+v", p)
	}
}

func TestCheck(t *testing.T) {
	for _, bad := range []string{"", "ftp://x.org/", "javascript:alert(1)", "/im", "https://", "http://u:p@x.org/"} {
		if _, err := Check(bad); !errors.Is(err, ErrBadURL) {
			t.Errorf("%q: want ErrBadURL, got %v", bad, err)
		}
	}
	if u, err := Check(" https://x.org/a#b "); err != nil || u.String() != "https://x.org/a" {
		t.Fatalf("good link: %v %v", u, err)
	}
}

// The real fetcher never reaches go24's own network: localhost, the LAN, Docker.
func TestPublicOnly(t *testing.T) {
	for _, a := range []string{"127.0.0.1:80", "10.0.0.5:443", "192.168.1.15:8080", "172.18.0.2:80", "169.254.169.254:80", "100.64.1.1:80", "[::1]:80", "0.0.0.0:80"} {
		if PublicOnly("tcp", a, nil) == nil {
			t.Errorf("%s must be refused", a)
		}
	}
	if err := PublicOnly("tcp", "93.184.216.34:443", nil); err != nil {
		t.Fatalf("an address of the internet: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<title>inside</title>")) }))
	defer srv.Close()
	if _, err := New().Get(context.Background(), srv.URL); err == nil {
		t.Fatal("a page on localhost was fetched")
	}
}

func TestGetAndImage(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			hits++
			w.Header().Set("Content-Type", "text/html; charset=windows-1251")
			_, _ = w.Write([]byte("<head><meta property=\"og:title\" content=\"\xcf\xf0\xe8\xe2\xe5\xf2\"><meta property=\"og:image\" content=\"/p.png\"></head>"))
		case "/p.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("\x89PNG"))
		case "/other.png", "/s.svg":
			w.Header().Set("Content-Type", map[bool]string{true: "image/svg+xml", false: "image/png"}[r.URL.Path == "/s.svg"])
			_, _ = w.Write([]byte("x"))
		case "/file":
			w.Header().Set("Content-Type", "application/pdf")
		}
	}))
	defer srv.Close()
	f := New()
	f.Client = srv.Client() // the test server is on localhost
	ctx := context.Background()
	p, err := f.Get(ctx, srv.URL+"/page")
	if err != nil || p.Title != "Привет" || p.Image != srv.URL+"/p.png" {
		t.Fatalf("page: %+v %v", p, err)
	}
	if _, err := f.Get(ctx, srv.URL+"/page"); err != nil || hits != 1 {
		t.Fatalf("the second card must come from the cache: hits %d, %v", hits, err)
	}
	if _, err := f.Get(ctx, srv.URL+"/file"); err == nil {
		t.Fatal("a file is not a page")
	}
	if b, ct, err := f.Image(ctx, p.Image); err != nil || ct != "image/png" || string(b) != "\x89PNG" {
		t.Fatalf("picture: %q %s %v", b, ct, err)
	}
	// only the pictures of the cards: go24 is no open proxy
	if _, _, err := f.Image(ctx, srv.URL+"/other.png"); !errors.Is(err, ErrNotCard) {
		t.Fatalf("a picture of no card: %v", err)
	}
	f.images[srv.URL+"/s.svg"] = f.images[p.Image]
	if _, _, err := f.Image(ctx, srv.URL+"/s.svg"); err == nil {
		t.Fatal("SVG must not go through")
	}
}
