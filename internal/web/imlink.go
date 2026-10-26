package web

import (
	"net/http"
	"net/url"

	"go24/internal/auth"
)

// imLink gives the card of a link of a chat message, as the portal messenger shows it under the message; its
// picture goes through imLinkImage. No card (a page without a title, a site that does not answer) — 204.
func (s *Server) imLink(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if s.Links == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	p, err := s.Links.Get(r.Context(), r.URL.Query().Get("url"))
	if err != nil || p.Title == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if p.Image != "" {
		p.Image = "/im/link/image?url=" + url.QueryEscape(p.Image)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	writeJSON(w, http.StatusOK, p)
}

// imLinkImage: the picture of a card, fetched by go24, so the browser never goes to the site itself.
func (s *Server) imLinkImage(w http.ResponseWriter, r *http.Request, _ auth.Session) {
	if s.Links == nil {
		http.NotFound(w, r)
		return
	}
	b, mime, err := s.Links.Image(r.Context(), r.URL.Query().Get("url"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	_, _ = w.Write(b)
}
