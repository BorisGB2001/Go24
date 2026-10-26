package web

import (
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// ---------- «Письмо» of a CRM card and «Шаблоны писем CRM» ----------

// As: a letter to the client right from the card's timeline — from one of the user's mailboxes, to the
// client's addresses, with files and a template whose {fields} take the card's values; the sent letter goes into
// the card's timeline at once and the client's answers join it (maillinks.go). The templates are the user's own or
// shared with the company (/crm/mail-templates).

func (s *Server) crmMailTemplatesPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.CRM.MailTemplates(r.Context(), sess.UserID, "")
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	q := r.URL.Query()
	data := map[string]any{"Templates": list, "Fields": docFields, "Me": sess.UserID, "Admin": tasks.IsAdmin(r.Context()),
		"Entities": []string{"deal", "lead", "contact", "company"}}
	if id := q.Get("edit"); id == "new" {
		ent := q.Get("entity")
		if !crm.Entities[ent] {
			ent = "deal"
		}
		data["Edit"] = &crm.MailTemplate{Entity: ent, Shared: true}
	} else if id != "" {
		for i := range list {
			if list[i].ID == id {
				data["Edit"] = &list[i]
			}
		}
	}
	s.render(w, r, "crmmailtpl", page{TitleKey: "crm.mailtpl.title", Nav: "crm_deals", Flash: q.Get("msg"), Data: data})
}

func (s *Server) crmMailTemplateSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	t := crm.MailTemplate{ID: r.PathValue("id"), Name: r.FormValue("name"), Entity: r.FormValue("entity"), Subject: r.FormValue("subject"),
		Body: mail.Sanitize(r.FormValue("html")), Shared: r.FormValue("shared") == "1"}
	if t.ID == "new" {
		t.ID = ""
	}
	if _, err := s.CRM.SaveMailTemplate(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), t); err != nil {
		s.taskErr(w, r, err, "/crm/mail-templates")
		return
	}
	http.Redirect(w, r, "/crm/mail-templates?msg="+url.QueryEscape(s.tr(r, "crm.mailtpl.saved")), http.StatusSeeOther)
}

func (s *Server) crmMailTemplateDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := s.CRM.DeleteMailTemplate(r.Context(), sess.UserID, tasks.IsAdmin(r.Context()), r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, "/crm/mail-templates")
		return
	}
	http.Redirect(w, r, "/crm/mail-templates", http.StatusSeeOther)
}

// crmMailFill is a template filled with the card's values, for the card's «Письмо»: {"subject", "html"}.
func (s *Server) crmMailFill(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	if err := s.crmCheck(r, sess, kind, id, "read"); err != nil {
		s.jsonErr(w, r, err)
		return
	}
	t, err := s.CRM.MailTemplateOf(r.Context(), sess.UserID, r.PathValue("tpl"))
	if err == nil && t.Entity != crmEntities[kind] {
		err = crm.ErrNotFound
	}
	var values map[string]string
	var known map[string]bool
	if err == nil {
		values, _, known, err = s.cardValues(r, kind, id)
	}
	if err != nil {
		s.jsonErr(w, r, err)
		return
	}
	subject, body := crm.FillMailTemplate(t, values, func(k string) bool { return known[k] })
	writeJSON(w, http.StatusOK, map[string]string{"subject": subject, "html": body})
}

// crmMailSend sends the card's «Письмо» (whoever may change the card) and puts it into the card's timeline.
func (s *Server) crmMailSend(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	if s.Mail == nil {
		http.NotFound(w, r)
		return
	}
	if err := s.crmCheck(r, sess, kind, id, "write"); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mail.MaxSendSize+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil && err != http.ErrNotMultipart {
		s.taskErr(w, r, i18n.E("err.mail_files", mail.MaxSendFiles), back)
		return
	}
	l := mail.Letter{To: r.FormValue("to"), CC: r.FormValue("cc"), Subject: r.FormValue("subject"), HTML: r.FormValue("html"),
		CRM: []mail.CRMRef{{Entity: crmEntities[kind], EntityID: id}}}
	if strings.TrimSpace(mail.HTMLText(l.HTML)) == "" && !strings.Contains(l.HTML, "<img") {
		l.HTML = ""
	}
	if r.MultipartForm != nil {
		for _, h := range r.MultipartForm.File["files"] {
			f, err := h.Open()
			if err != nil {
				s.taskErr(w, r, err, back)
				return
			}
			data, err := io.ReadAll(f)
			f.Close()
			if err != nil {
				s.taskErr(w, r, err, back)
				return
			}
			ct := h.Header.Get("Content-Type")
			if ct == "" {
				ct = "application/octet-stream"
			}
			l.Files = append(l.Files, mail.Attachment{Name: path.Base(strings.ReplaceAll(h.Filename, "\\", "/")), ContentType: ct, Data: data})
		}
	}
	docs, err := s.documentAttachments(r, crmEntities[kind], id)
	if err != nil {
		s.taskErr(w, r, err, back+"#documents")
		return
	}
	l.Files = append(l.Files, docs...)
	var inv crm.Invoice
	if v := r.FormValue("inv"); v != "" { // «Отправить» of a quote or an invoice of the deal
		if inv, err = s.mailInvoice(r, kind, id, v, &l); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
	}
	if err := s.Mail.SendLetter(r.Context(), sess.UserID, r.FormValue("box"), l); err != nil {
		s.taskErr(w, r, err, back+"#timeline")
		return
	}
	s.Log.Info("crm mail sent", "user_id", sess.UserID, "entity", crmEntities[kind], "id", id)
	if inv.ID != "" { // sent to the client: a new invoice or a draft quote becomes «Отправлен»
		if inv.Status == "new" || inv.Status == "draft" {
			if err := s.CRM.SetInvoiceStatus(r.Context(), sess.UserID, inv.ID, "sent"); err != nil {
				s.Log.Error("crm invoice sent", "invoice", inv.ID, "err", err)
			}
		}
		http.Redirect(w, r, "/crm/invoices/"+inv.ID+"?msg="+url.QueryEscape(s.tr(r, "crm.mailsend.sent")), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.mailsend.sent"))+"#timeline", http.StatusSeeOther)
}

// crmMailForm is what the card's «Письмо» offers: the user's mailboxes, the client's addresses, the templates.
func (s *Server) crmMailForm(r *http.Request, sess auth.Session, entity, id string) (map[string]any, error) {
	if s.Mail == nil {
		return nil, nil
	}
	boxes, err := s.Mail.Mailboxes(r.Context(), sess.UserID)
	if err != nil || len(boxes) == 0 {
		return nil, err
	}
	to, err := s.CRM.ClientEmails(r.Context(), entity, id)
	if err != nil {
		return nil, err
	}
	tpls, err := s.CRM.MailTemplates(r.Context(), sess.UserID, entity)
	if err != nil {
		return nil, err
	}
	return map[string]any{"Boxes": boxes, "To": to, "Templates": tpls}, nil
}
