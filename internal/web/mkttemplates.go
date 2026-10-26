package web

import (
	"embed"
	"html/template"
	"net/http"
	"net/url"
	"strings"

	"go24/internal/auth"
	mailpkg "go24/internal/mail"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

// «Шаблоны писем» of «Маркетинг»: a new campaign starts from a template — a ready-made one of go24
// (by language) or one the company saved («Сохранить как шаблон» of the campaign's editor or the «Шаблоны» tab).

// mktBaseFS are the ready-made letters by language, made by tools/mkttemplates.py from one layout: inline styles and tables only,
// as mail clients and mailpkg.Sanitize keep them.
//
//go:embed mkttemplates
var mktBaseFS embed.FS

// mktBase are their kinds, in the order offered; a ready-made template's id is "base-" and its kind.
var mktBase = []string{"simple", "news", "promo", "event", "digest"}

// mktBaseTemplates are the ready-made templates in the viewer's language.
func (s *Server) mktBaseTemplates(r *http.Request) []marketing.Template {
	var out []marketing.Template
	for _, k := range mktBase {
		data, err := mktBaseFS.ReadFile("mkttemplates/" + s.lang(r) + "/" + k + ".html")
		if err != nil {
			data, err = mktBaseFS.ReadFile("mkttemplates/en-US/" + k + ".html")
		}
		if err != nil {
			continue
		}
		out = append(out, marketing.Template{ID: "base-" + k, Name: s.tr(r, "mkt.base."+k), Subject: s.tr(r, "mkt.base."+k+"_subject"),
			Body: strings.TrimSpace(string(data))})
	}
	return out
}

// mktTemplate is a template by its id: ready-made or the company's.
func (s *Server) mktTemplate(r *http.Request, id string) (marketing.Template, error) {
	if strings.HasPrefix(id, "base-") {
		for _, t := range s.mktBaseTemplates(r) {
			if t.ID == id {
				return t, nil
			}
		}
		return marketing.Template{}, marketing.ErrNotFound
	}
	return s.Marketing.Template(r.Context(), id)
}

// mktTemplatesPage: the ready-made templates and the company's, each with its letter in small; ?edit= — the editor
// of one (new or the user's).
func (s *Server) mktTemplatesPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	ctx := r.Context()
	list, err := s.Marketing.Templates(ctx)
	if err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	admin := tasks.IsAdmin(ctx)
	letters := map[string]template.HTML{}
	base := s.mktBaseTemplates(r)
	for _, t := range append(base, list...) {
		letters[t.ID] = template.HTML(mailpkg.Sanitize(t.Body))
	}
	data := map[string]any{"Tab": "templates", "Base": base, "Templates": list, "Letters": letters, "Me": sess.UserID, "Admin": admin}
	if id := r.URL.Query().Get("edit"); id == "new" {
		data["Edit"] = &marketing.Template{}
	} else if id != "" {
		for i := range list {
			if list[i].ID == id && (admin || list[i].CreatedBy == sess.UserID) {
				data["Edit"] = &list[i]
			}
		}
	}
	s.render(w, r, "marketing", page{TitleKey: "mkt.templates", Nav: "marketing", Flash: r.URL.Query().Get("msg"), Data: data})
}

// mktTemplateSave keeps a template of the «Шаблоны» tab.
func (s *Server) mktTemplateSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	t := marketing.Template{ID: r.PathValue("id"), Name: r.FormValue("name"), Subject: r.FormValue("subject"), Body: mailpkg.Sanitize(r.FormValue("html"))}
	if t.ID == "new" {
		t.ID = ""
	}
	if _, err := s.Marketing.SaveTemplate(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), t); err != nil {
		s.taskErr(w, r, err, "/marketing/templates?edit="+r.PathValue("id")+"#edit")
		return
	}
	http.Redirect(w, r, "/marketing/templates?msg="+url.QueryEscape(s.tr(r, "mkt.tpl_saved")), http.StatusSeeOther)
}

func (s *Server) mktTemplateDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	if err := s.Marketing.DeleteTemplate(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/marketing/templates")
		return
	}
	http.Redirect(w, r, "/marketing/templates", http.StatusSeeOther)
}

// mktTemplateUse makes a draft campaign of the template's letter («Создать рассылку») and opens its editor.
func (s *Server) mktTemplateUse(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.mktAllowed(r, sess); err != nil {
		s.taskErr(w, r, err, "/marketing")
		return
	}
	t, err := s.mktTemplate(r, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/marketing/templates")
		return
	}
	c := marketing.Campaign{Title: t.Name, Subject: t.Subject, Body: mailpkg.Sanitize(t.Body)}
	if s.Mail != nil { // from the user's first mailbox, as the editor offers
		if boxes, err := s.Mail.Mailboxes(r.Context(), sess.UserID); err == nil && len(boxes) > 0 {
			c.MailboxID = boxes[0].ID
		}
	}
	id, err := s.Marketing.SaveCampaign(r.Context(), sess.UserID, false, c)
	if err != nil {
		s.taskErr(w, r, err, "/marketing/templates")
		return
	}
	http.Redirect(w, r, "/marketing/campaigns/"+id, http.StatusSeeOther)
}
