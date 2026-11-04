package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go24/internal/i18n"
)

// TestPWA: the installable app — the manifest (the portal's name, icons, quick actions in the user's language), the
// service worker from the root with the build's version, the «Нет связи» page; all three open without a session.
func TestPWA(t *testing.T) {
	bundle, err := i18n.Load("")
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer((&Server{I18n: bundle}).Routes())
	defer hs.Close()
	get := func(path, lang string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("Accept-Language", lang)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("%s: %d %s", path, res.StatusCode, b)
		}
		return res, string(b)
	}

	res, body := get("/manifest.webmanifest", "ru-RU")
	if ct := res.Header.Get("Content-Type"); ct != "application/manifest+json" {
		t.Errorf("manifest type %q", ct)
	}
	var m struct {
		Name      string    `json:"name"`
		ShortName string    `json:"short_name"`
		StartURL  string    `json:"start_url"`
		Display   string    `json:"display"`
		Scope     string    `json:"scope"`
		Lang      string    `json:"lang"`
		Icons     []pwaIcon `json:"icons"`
		Shortcuts []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"shortcuts"`
	}
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "go24" || m.ShortName != "go24" || m.Display != "standalone" || m.Scope != "/" || m.Lang != "ru-RU" || !strings.HasPrefix(m.StartURL, "/") {
		t.Errorf("manifest %+v", m)
	}
	var maskable, big bool
	for _, ic := range m.Icons {
		if !strings.HasPrefix(ic.Src, "/static/img/pwa/") {
			t.Errorf("icon %s", ic.Src)
		}
		if _, err := staticFS.ReadFile("static" + strings.TrimPrefix(strings.SplitN(ic.Src, "?", 2)[0], "/static")); err != nil {
			t.Errorf("icon %s: %v", ic.Src, err)
		}
		maskable = maskable || ic.Purpose == "maskable"
		big = big || ic.Sizes == "512x512"
	}
	if !maskable || !big {
		t.Errorf("an installable app needs a 512 px and a maskable icon: %+v", m.Icons)
	}
	if len(m.Shortcuts) != 4 || m.Shortcuts[0].Name != "Лента" || m.Shortcuts[1].URL != "/my" {
		t.Errorf("shortcuts %+v", m.Shortcuts)
	}
	if _, body := get("/manifest.webmanifest", "en-US"); !strings.Contains(body, `"name":"My tasks"`) || !strings.Contains(body, `"lang":"en-US"`) {
		t.Errorf("English manifest: %s", body)
	}

	res, body = get("/sw.js", "ru-RU")
	if res.Header.Get("Service-Worker-Allowed") != "/" || !strings.Contains(res.Header.Get("Content-Type"), "javascript") ||
		res.Header.Get("Cache-Control") != "no-cache" {
		t.Errorf("sw.js headers %v", res.Header)
	}
	if !strings.HasPrefix(body, `const GO24_VERSION = "`+assetVersion+`";`) || !strings.Contains(body, "'/offline'") {
		t.Errorf("sw.js: %.200s", body)
	}
	// push notifications (GO24DEV-743): shown with the portal's name, a tap opens the notification's page
	if !strings.Contains(body, "\nconst GO24_TITLE = \"") || !strings.Contains(body, "addEventListener('push'") ||
		!strings.Contains(body, "addEventListener('notificationclick'") {
		t.Errorf("sw.js has no push: %.300s", body)
	}

	if _, body := get("/offline", "ru-RU"); !strings.Contains(body, "Нет подключения к интернету") || !strings.Contains(body, "Повторить") {
		t.Errorf("offline page: %.300s", body)
	}
	if _, body := get("/offline", "en-US"); !strings.Contains(body, "No internet connection") {
		t.Errorf("offline page in English: %.300s", body)
	}
}
