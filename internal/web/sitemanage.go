package web

import (
	"net/http"
	"net/url"
	"strconv"
	"time"

	"go24/internal/auth"
	"go24/internal/sites"
	"go24/internal/tasks"
)

// «Сайты»: the history of a page with rolling back, the trash of sites and pages, the rights of a site.

// siteHistory is «История изменений» of a page or of the header or the footer of its site (page "header",
// "footer"; from — the page of the editor it was opened from).
func (s *Server) siteHistory(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site, pg := r.PathValue("site"), r.PathValue("page")
	x, err := s.Sites.Site(r.Context(), site)
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	from := pg
	title := ""
	if sites.IsArea(pg) {
		from, title = r.FormValue("from"), s.tr(r, "site.area."+pg)
	}
	if from == "" {
		from = x.HomeID
	}
	if !sites.IsArea(pg) {
		p, err := s.Sites.Page(r.Context(), x.ID, pg)
		if err != nil {
			s.taskErr(w, r, err, "/sites/"+x.ID)
			return
		}
		title = p.Title
	}
	list, err := s.Sites.Revisions(r.Context(), x.ID, pg)
	if err != nil {
		s.taskErr(w, r, err, "/sites/"+x.ID+"/p/"+from)
		return
	}
	s.render(w, r, "sitehistory", page{Title: s.tr(r, "site.history") + " · " + title, Nav: "sites", Data: map[string]any{
		"Site": x, "Page": pg, "Title": title, "From": from, "Revisions": list}})
}

// siteRevision shows a state of the history of a page as a visitor would have seen it.
func (s *Server) siteRevision(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, err := s.Sites.Site(r.Context(), r.PathValue("site"))
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	id, _ := strconv.ParseInt(r.PathValue("rev"), 10, 64)
	pg := r.PathValue("page")
	v, err := s.Sites.Revision(r.Context(), x.ID, pg, id)
	if err != nil {
		s.taskErr(w, r, err, "/sites/"+x.ID)
		return
	}
	p := sites.Page{ID: pg, SiteID: x.ID, Title: x.Name}
	switch pg {
	case sites.AreaHeader:
		x.Header = v.Blocks
	case sites.AreaFooter:
		x.Footer = v.Blocks
	default:
		if p, err = s.Sites.Page(r.Context(), x.ID, pg); err != nil {
			s.taskErr(w, r, err, "/sites/"+x.ID)
			return
		}
		p.Blocks = v.Blocks
	}
	p.Blocks = x.Compose(p.Blocks)
	siteForms(p.Blocks, "preview", "", "")
	w.Header().Set("Cache-Control", "no-store")
	s.renderSite(w, r, http.StatusOK, map[string]any{"Site": x, "Page": p, "Preview": true})
}

// siteRevert brings a page back to a state of its history.
func (s *Server) siteRevert(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site, pg := r.PathValue("site"), r.PathValue("page")
	id, _ := strconv.ParseInt(r.PathValue("rev"), 10, 64)
	back := s.siteBack(r, site, pg)
	if err := s.Sites.Revert(r.Context(), sess.UserID, site, pg, id); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "site.reverted")), http.StatusSeeOther)
}

// siteAccess saves «Права доступа» of a site: the role of every employee and the people and departments of each role
// (fields role<n>).
func (s *Server) siteAccess(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site := r.PathValue("site")
	back := s.siteBack(r, site, "")
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	everyone, err := strconv.Atoi(r.PostForm.Get("everyone"))
	if err != nil {
		everyone = -1
	}
	given := map[sites.Role][]string{}
	for _, role := range sites.Roles[1:] {
		given[role] = r.PostForm["role"+strconv.Itoa(int(role))]
	}
	if err := s.Sites.SetAccess(r.Context(), sess.UserID, site, sites.Role(everyone), given); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// siteAccessData: the people and departments of each role of a site for its form (template sitepage).
func (s *Server) siteAccessData(r *http.Request, x sites.Site) (map[string][]tasks.User, error) {
	_, list, err := s.Sites.Access(r.Context(), x.ID)
	if err != nil {
		return nil, err
	}
	out := map[string][]tasks.User{}
	for _, g := range list {
		k := strconv.Itoa(int(g.Role))
		out[k] = append(out[k], tasks.User{ID: g.ID, Name: g.Name})
	}
	return out, nil
}

// sitesTrash: «Корзина» of «Сайты» — the deleted sites and pages the user may bring back, with who and when deleted
// them and when they go for good.
func (s *Server) sitesTrash(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	days, err := s.Tasks.TrashDays(r.Context())
	if err != nil {
		s.fail(w, "sites", err)
		return
	}
	list, err := s.Sites.Trash(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/sites")
		return
	}
	type item struct {
		sites.TrashItem
		Expires time.Time
	}
	items := make([]item, 0, len(list))
	for _, t := range list {
		it := item{TrashItem: t}
		if days > 0 {
			it.Expires = t.Deleted.AddDate(0, 0, days)
		}
		items = append(items, it)
	}
	s.render(w, r, "sitetrash", page{TitleKey: "trash.title", Nav: "sites", Data: map[string]any{"Items": items, "Days": days}})
}

// siteTrashAct restores (act=restore) or deletes for good (act=purge) a site in the trash or, with page, a page.
func (s *Server) siteTrashAct(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	site, pg, ctx := r.PathValue("site"), r.FormValue("page"), r.Context()
	var err error
	to, msg := "/sites/trash", ""
	switch {
	case r.FormValue("act") == "restore" && pg != "":
		if site, err = s.Sites.RestorePage(ctx, sess.UserID, pg); err == nil {
			to, msg = "/sites/"+site+"/p/"+pg, "site.restored_page"
		}
	case r.FormValue("act") == "restore":
		if _, err = s.Sites.RestoreSite(ctx, sess.UserID, site); err == nil {
			to, msg = "/sites/"+site, "site.restored"
		}
	case r.FormValue("act") == "purge" && pg != "":
		err = s.Sites.PurgePage(ctx, sess.UserID, pg)
	case r.FormValue("act") == "purge":
		err = s.Sites.PurgeSite(ctx, sess.UserID, site)
	default:
		err = sites.ErrNotFound
	}
	if err != nil {
		s.taskErr(w, r, err, "/sites/trash")
		return
	}
	if msg != "" {
		to += "?msg=" + url.QueryEscape(s.tr(r, msg))
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}
