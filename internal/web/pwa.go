package web

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// The installable app (PWA), as the portal mobile app: the manifest names the portal and its icons, the service
// worker (static/js/sw.js, served from the root so that its scope is the whole portal) keeps the static files and the
// «Нет связи» page, so a phone without the network shows that page instead of the browser's error.

// pwaShortcuts are the quick actions of the app's icon (a long press on the phone), as in the portal app.
var pwaShortcuts = []struct{ Key, URL string }{
	{"nav.feed", "/feed"},
	{"nav.my_tasks", "/my"},
	{"nav.messenger", "/im"},
	{"calendar.title", "/calendar"},
}

type pwaIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose,omitempty"`
}

// pwaManifest: GET /manifest.webmanifest — the portal's name in its language; open to all (the browser fetches it
// without the session cookie).
func (s *Server) pwaManifest(w http.ResponseWriter, r *http.Request) {
	lang := s.lang(r)
	name := "go24"
	if s.Portal != nil {
		name = s.Portal.Current().Title()
	}
	short := []rune(name)
	if len(short) > 12 {
		short = short[:12]
	}
	icon := func(file, sizes, typ, purpose string) pwaIcon {
		return pwaIcon{Src: "/static/img/pwa/" + file + "?v=" + assetVersion, Sizes: sizes, Type: typ, Purpose: purpose}
	}
	type shortcut struct {
		Name  string    `json:"name"`
		URL   string    `json:"url"`
		Icons []pwaIcon `json:"icons"`
	}
	var shortcuts []shortcut
	for _, sc := range pwaShortcuts {
		shortcuts = append(shortcuts, shortcut{s.I18n.T(lang, sc.Key), sc.URL, []pwaIcon{icon("icon-96.png", "96x96", "image/png", "")}})
	}
	m := map[string]any{
		"id":               "/",
		"name":             name,
		"short_name":       string(short),
		"description":      s.I18n.T(lang, "app.tagline"),
		"lang":             lang,
		"start_url":        "/?source=pwa",
		"scope":            "/",
		"display":          "standalone",
		"orientation":      "any",
		"background_color": "#eef2f6",
		"theme_color":      "#1b2838",
		"icons": []pwaIcon{
			icon("icon-192.png", "192x192", "image/png", "any"),
			icon("icon-512.png", "512x512", "image/png", "any"),
			icon("icon-maskable-512.png", "512x512", "image/png", "maskable"),
			icon("icon.svg", "any", "image/svg+xml", "any"),
		},
		"shortcuts": shortcuts,
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	json.NewEncoder(w).Encode(m)
}

// pwaWorker: GET /sw.js — the service worker; never cached by HTTP, so a new build reaches the phones at once (the
// build's version in it renames its cache, and the old one is dropped).
func (s *Server) pwaWorker(w http.ResponseWriter, r *http.Request) {
	b, err := staticFS.ReadFile("static/js/sw.js")
	if err != nil {
		s.fail(w, "sw.js", err)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	title := "go24"
	if s.Portal != nil {
		title = s.Portal.Current().Title()
	}
	jt, _ := json.Marshal(title)
	w.Write([]byte("const GO24_VERSION = " + strconv.Quote(assetVersion) + ";\nconst GO24_TITLE = " + string(jt) + ";\n"))
	w.Write(b)
}

// pwaOffline: GET /offline — the page the service worker shows when the phone has no network.
func (s *Server) pwaOffline(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, "offline", page{})
}
