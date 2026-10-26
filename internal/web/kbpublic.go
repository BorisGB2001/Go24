package web

import (
	"net/http"

	"go24/internal/auth"
	"go24/internal/feed"
	"go24/internal/kb"
)

// ---------- «Публикация» of the knowledge base: /kbp/{token} for anyone, no sign-in ----------

// kbPublish opens the base (no page) or a page by an outside link, or switches the link off (off=1).
func (s *Server) kbPublish(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	spaceID, pageID := r.PathValue("space"), r.FormValue("page")
	back := "/kb/" + spaceID + "/settings#publish"
	if pageID != "" {
		back = "/kb/" + spaceID + "/" + pageID
	}
	var err error
	if r.FormValue("off") == "1" {
		err = s.KB.Unpublish(r.Context(), sess.UserID, spaceID, pageID)
	} else {
		_, err = s.KB.Publish(r.Context(), sess.UserID, spaceID, pageID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// kbPublic is a page under an outside link: the tree of what is published on the left, the page on the right.
func (s *Server) kbPublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	pub, err := s.KB.OpenPublic(r.Context(), r.PathValue("token"))
	var tree []*kb.Page
	if err == nil {
		tree, err = s.KB.PublicTree(r.Context(), pub)
	}
	var p kb.Page
	if err == nil {
		p, err = s.KB.PublicPage(r.Context(), pub, tree, r.PathValue("page"))
	}
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "kbpublic", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return
	}
	var crumbs []*kb.Page
	if path := kb.Path(tree, p.ID); len(path) > 0 {
		crumbs = path[:len(path)-1]
	}
	p.Body = kb.PublicBody(p.Body, pub.SpaceID, pub.Token)
	s.render(w, r, "kbpublic", page{Title: p.Title, Data: map[string]any{"Public": pub, "Tree": tree, "Page": p, "Crumbs": crumbs,
		"TOC": kb.Headings(p.Body)}})
}

// kbPublicFile is a picture or a file shown on a page under the link.
func (s *Server) kbPublicFile(w http.ResponseWriter, r *http.Request) {
	pub, err := s.KB.OpenPublic(r.Context(), r.PathValue("token"))
	var tree []*kb.Page
	if err == nil {
		tree, err = s.KB.PublicTree(r.Context(), pub)
	}
	id := r.PathValue("id")
	if err == nil {
		err = s.KB.PublicFile(r.Context(), tree, pub.SpaceID, id)
	}
	m, merr := s.Storages.FileMeta(r.Context(), id)
	if err != nil || merr != nil {
		http.NotFound(w, r)
		return
	}
	disposition := "attachment"
	if r.URL.Query().Get("inline") == "1" && (feed.File{ContentType: m.ContentType}).Image() {
		disposition = "inline"
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.sendFile(w, r, m, disposition)
}
