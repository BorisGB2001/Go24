package web

import (
	"bytes"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/sites"
	"go24/internal/storages"
)

// ---------- «Сайты» ----------

// siteDefaults are the texts a new block of the type starts with (site.def.<type>.<field>).
func (s *Server) siteDefaults(r *http.Request, typ string) map[string]string {
	t, _ := sites.TypeOf(typ)
	lang, out := s.lang(r), map[string]string{}
	for _, f := range t.Fields {
		if key := "site.def." + typ + "." + f.Name; s.I18n.Has(lang, key) {
			out[f.Name] = s.I18n.T(lang, key)
		}
	}
	return out
}

// siteStarter is the home page of a new site, as the empty template has: a menu, a cover, advantages,
// contacts and a footer.
func (s *Server) siteStarter(r *http.Request, name string) []sites.Block {
	var out []sites.Block
	for _, typ := range []string{"menu", "cover", "features", "contacts", "footer"} {
		d := s.siteDefaults(r, typ)
		if typ == "menu" {
			d["logo"] = name
		}
		out = append(out, sites.Block{Type: typ, Data: d})
	}
	return out
}

func (s *Server) sitesHome(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.Sites.Sites(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/my")
		return
	}
	s.render(w, r, "sites", page{TitleKey: "site.title", Nav: "sites", Data: map[string]any{"Sites": list, "Base": s.BaseURL}})
}

func (s *Server) siteCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	name := r.FormValue("name")
	id, err := s.Sites.CreateSite(r.Context(), sess.UserID, name, r.FormValue("slug"), s.tr(r, "site.home_title"), s.siteStarter(r, strings.TrimSpace(name)))
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	http.Redirect(w, r, "/sites/"+id, http.StatusSeeOther)
}

// siteOpen: a site opens on its home page.
func (s *Server) siteOpen(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, err := s.Sites.Site(r.Context(), r.PathValue("site"))
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	http.Redirect(w, r, "/sites/"+x.ID+"/p/"+x.HomeID, http.StatusFound)
}

func (s *Server) siteSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("site")
	if err := s.Sites.UpdateSite(r.Context(), id, r.FormValue("name"), r.FormValue("slug"), r.FormValue("description")); err != nil {
		s.taskErr(w, r, err, "/sites/"+id)
		return
	}
	http.Redirect(w, r, "/sites/"+id, http.StatusSeeOther)
}

func (s *Server) sitePublish(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("site")
	if err := s.Sites.PublishSite(r.Context(), id, r.FormValue("on") == "1"); err != nil {
		s.taskErr(w, r, err, "/sites/"+id)
		return
	}
	http.Redirect(w, r, "/sites/"+id, http.StatusSeeOther)
}

func (s *Server) siteDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.Sites.DeleteSite(r.Context(), sess.UserID, r.PathValue("site")); err != nil {
		s.taskErr(w, r, err, "/sites/"+r.PathValue("site"))
		return
	}
	http.Redirect(w, r, "/sites", http.StatusSeeOther)
}

func (s *Server) siteNewPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site := r.PathValue("site")
	id, err := s.Sites.CreatePage(r.Context(), sess.UserID, site, r.FormValue("title"), r.FormValue("slug"), nil)
	if err != nil {
		s.taskErr(w, r, err, "/sites/"+site)
		return
	}
	http.Redirect(w, r, "/sites/"+site+"/p/"+id, http.StatusSeeOther)
}

// siteTypeOf is the library by type, for the forms of the blocks.
func siteTypeOf() map[string]sites.BlockType {
	out := map[string]sites.BlockType{}
	for _, t := range sites.BlockTypes {
		out[t.Type] = t
	}
	return out
}

// sitePage is the editor of a page: its blocks as they look on the site, each with its tools and its form.
func (s *Server) sitePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, err := s.Sites.Site(r.Context(), r.PathValue("site"))
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	p, err := s.Sites.Page(r.Context(), x.ID, r.PathValue("page"))
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	pages, err := s.Sites.Pages(r.Context(), x.ID)
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	users, err := s.Tasks.ListUsers(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	siteForms(p.Blocks, "preview", "", "")
	s.render(w, r, "sitepage", page{Title: p.Title + " · " + x.Name, Nav: "sites", Data: map[string]any{"Site": x, "Page": p, "Pages": pages,
		"Types": sites.BlockTypes, "TypeOf": siteTypeOf(), "CanDelete": x.CanDelete(r.Context(), sess.UserID), "Users": users}})
}

func (s *Server) siteSavePage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site, id := r.PathValue("site"), r.PathValue("page")
	back := "/sites/" + site + "/p/" + id
	if err := s.Sites.UpdatePage(r.Context(), sess.UserID, site, id, r.FormValue("title"), r.FormValue("slug")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// sitePageAction: publish, unpublish, home (make it the home page), delete.
func (s *Server) sitePageAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site, id := r.PathValue("site"), r.PathValue("page")
	back := "/sites/" + site + "/p/" + id
	var err error
	switch r.PathValue("act") {
	case "publish", "unpublish":
		err = s.Sites.PublishPage(r.Context(), site, id, r.PathValue("act") == "publish")
	case "home":
		err = s.Sites.SetHome(r.Context(), site, id)
	case "delete":
		if err = s.Sites.DeletePage(r.Context(), site, id); err == nil {
			back = "/sites/" + site
		}
	default:
		err = sites.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, "/sites/"+site+"/p/"+id)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) siteAddBlock(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site, id := r.PathValue("site"), r.PathValue("page")
	back := "/sites/" + site + "/p/" + id
	at, err := strconv.Atoi(r.FormValue("at"))
	if err != nil { // no place chosen: at the end
		at = math.MaxInt
	}
	typ := r.FormValue("type")
	bid, err := s.Sites.AddBlock(r.Context(), sess.UserID, site, id, typ, at, s.siteDefaults(r, typ))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#b-"+bid, http.StatusSeeOther)
}

func (s *Server) siteSaveBlock(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site, id, bid := r.PathValue("site"), r.PathValue("page"), r.PathValue("block")
	back := "/sites/" + site + "/p/" + id
	_ = r.ParseForm()
	data := map[string]string{}
	for k := range r.PostForm {
		data[k] = r.PostForm.Get(k)
	}
	if err := s.Sites.SaveBlock(r.Context(), sess.UserID, site, id, bid, data); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#b-"+bid, http.StatusSeeOther)
}

// siteBlockAction: up, down, hide (or show), delete.
func (s *Server) siteBlockAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site, id, bid, act := r.PathValue("site"), r.PathValue("page"), r.PathValue("block"), r.PathValue("act")
	back := "/sites/" + site + "/p/" + id
	if err := s.Sites.BlockAction(r.Context(), sess.UserID, site, id, bid, act); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	if act != "delete" {
		back += "#b-" + bid
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// sitePreview shows a page as a visitor will see it, published or not.
func (s *Server) sitePreview(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, err := s.Sites.Site(r.Context(), r.PathValue("site"))
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	p, err := s.Sites.Page(r.Context(), x.ID, r.PathValue("page"))
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	p.Blocks = sites.Visible(p.Blocks)
	siteForms(p.Blocks, "preview", "", "")
	w.Header().Set("Cache-Control", "no-store")
	s.renderSite(w, r, map[string]any{"Site": x, "Page": p, "Preview": true})
}

// siteUpload takes a picture for a block of a site; it is open to everyone at /sitefile/{id}.
func (s *Server) siteUpload(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, err := s.Sites.Site(r.Context(), r.PathValue("site"))
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)
	f, hdr, err := r.FormFile("file")
	if err != nil {
		s.jsonErr(w, r, i18n.E("err.file_required"))
		return
	}
	defer f.Close()
	ct := hdr.Header.Get("Content-Type")
	if !(feed.File{ContentType: ct}).Image() {
		s.jsonErr(w, r, i18n.E("err.site_image"))
		return
	}
	name := path.Base(strings.ReplaceAll(hdr.Filename, "\\", "/"))
	key := path.Join("go24", time.Now().Format("2006/01"), randomHex(16), name)
	if err := s.Storages.Put(r.Context(), sess.UserID, false, "", key, f, hdr.Size, ct); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	id, err := s.Storages.AddFile(r.Context(), storages.NewFile{Key: key, Name: name, ContentType: ct, Size: hdr.Size,
		SiteID: x.ID, UploadedBy: sess.UserID})
	if err != nil {
		s.fail(w, "save file row", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": name, "url": "/sitefile/" + id})
}

// ---------- the public side: no sign-in ----------

// renderSite renders a site's page by itself, without the go24 layout and its signed-in parts.
func (s *Server) renderSite(w http.ResponseWriter, r *http.Request, data map[string]any) {
	p := page{Lang: s.lang(r), Data: data}
	var buf bytes.Buffer
	if err := s.pages[p.Lang]["sitepublic"].Execute(&buf, p); err != nil {
		s.fail(w, "render sitepublic", err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = buf.WriteTo(w)
}

// sitePublic is a published page: /site/{slug} — the home page, /site/{slug}/{page} — another one.
func (s *Server) sitePublic(w http.ResponseWriter, r *http.Request) {
	x, p, _, err := s.Sites.Public(r.Context(), r.PathValue("slug"), r.PathValue("page"))
	if err != nil {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	state := ""
	if q.Get("sent") == "1" {
		state = "sent"
	}
	siteForms(p.Blocks, state, q.Get("form"), q.Get("err"))
	keepUTM(w, r)
	w.Header().Set("Cache-Control", "no-store")
	s.renderSite(w, r, map[string]any{"Site": x, "Page": p})
}

// siteForms prepares the form blocks of a page to be shown: each gets the stamp of the time; the one the visitor has
// just sent (id; every one when id is "") — the state and the refusal.
func siteForms(list []sites.Block, state, id, errKey string) {
	if !strings.HasPrefix(errKey, "err.site_form_") {
		errKey = ""
	}
	stamp := sites.FormStamp(time.Now())
	for i := range list {
		if b := &list[i]; b.Type == "form" {
			b.Stamp = stamp
			if id == "" || b.ID == id {
				b.State, b.Err = state, errKey
			}
		}
	}
}

// siteFormHits: one address sends at most 5 forms in 10 minutes.
var siteFormHits = &sites.Hits{Max: 5, Per: 10 * time.Minute}

// siteFormSend: a visitor sends the block «CRM-форма» of a published page — a lead in CRM (source «Веб-сайт») for
// the form's responsible, who gets a notification. The page shows the answer: ?form=<block>&sent=1 or &err=<key>.
// A robot (the trap field filled, the form sent too fast) is told it went well, and nothing is created.
func (s *Server) siteFormSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	slug, pg := r.PathValue("slug"), r.PathValue("page")
	x, p, _, err := s.Sites.Public(ctx, slug, pg)
	if err != nil {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	bid := r.FormValue("block")
	b, ok := p.FormBlock(bid)
	if !ok {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	back := "/site/" + slug
	if pg != "" {
		back += "/" + pg
	}
	back += "?form=" + url.QueryEscape(bid)
	refuse := func(key string) { http.Redirect(w, r, back+"&err="+key+"#b-"+bid, http.StatusSeeOther) }
	now := time.Now()
	if !sites.Human(r.FormValue("website"), r.FormValue("t"), now) {
		http.Redirect(w, r, back+"&sent=1#b-"+bid, http.StatusSeeOther)
		return
	}
	if !siteFormHits.Allow(clientIP(r), now) {
		refuse("err.site_form_often")
		return
	}
	ans, err := b.Answer(r.FormValue)
	if err != nil {
		var ie *i18n.Error
		if errors.As(err, &ie) {
			refuse(ie.Key)
			return
		}
		s.fail(w, "site form", err)
		return
	}
	resp, err := s.Sites.Responsible(ctx, x, b)
	if err != nil {
		s.fail(w, "site form responsible", err)
		return
	}
	who := ans["name"]
	for _, f := range []string{"phone", "email"} {
		if who == "" {
			who = ans[f]
		}
	}
	title := s.portalTr("site.lead_title", x.Name)
	if who != "" {
		title += " — " + who
	}
	comment := ans["comment"]
	if comment != "" {
		comment += "\n\n"
	}
	comment += s.portalTr("site.lead_page", p.Title, strings.TrimRight(s.BaseURL, "/")+strings.SplitN(back, "?", 2)[0])
	id, err := s.CRM.SaveLead(ctx, resp, crm.Lead{Title: clipText(title, 300), Name: ans["name"], Phone: ans["phone"], Email: ans["email"],
		Source: clipText(s.portalTr("site.lead_source", x.Name), 300), Origin: crm.Dict{Code: "WEB"}, Comment: comment, Responsible: crm.Ref{ID: resp}})
	if err != nil {
		var ie *i18n.Error
		if errors.As(err, &ie) {
			refuse("err.site_form_failed")
			return
		}
		s.fail(w, "site form lead", err)
		return
	}
	if err := s.setUTM(ctx, "lead", id, visitorUTM(r)); err != nil {
		s.Log.Error("site form utm", "lead", id, "err", err)
	}
	if err := s.Sites.NotifyLead(ctx, resp, title, "/crm/leads/"+id); err != nil {
		s.Log.Error("site form notify", "lead", id, "err", err)
	}
	http.Redirect(w, r, back+"&sent=1#b-"+bid, http.StatusSeeOther)
}

// siteFile is a picture of a site's page, for anyone.
func (s *Server) siteFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.Sites.PublicFile(r.Context(), id) {
		http.NotFound(w, r)
		return
	}
	m, err := s.Storages.FileMeta(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	obj, err := s.Storages.Open(r.Context(), m.StorageID, m.Key)
	if err != nil {
		s.fail(w, "open file", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", m.ContentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; style-src 'unsafe-inline'")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	if _, err := io.Copy(w, obj); err != nil {
		s.Log.Error("site file", "id", id, "err", err)
	}
}
