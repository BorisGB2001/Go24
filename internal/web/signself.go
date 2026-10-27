package web

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/docgen"
	"go24/internal/i18n"
	"go24/internal/sign"
	"go24/internal/tasks"
)

// ---------- «Документы от сотрудников»: an employee makes an application by a template (GO24DEV-726) ----------

// selfValueMax is the longest value an employee types into a field of the template.
const selfValueMax = 2000

// selfField is a placeholder of the template the employee fills in; Date — a date picker.
type selfField struct {
	Name string
	Date bool
}

// selfFields are the placeholders of the template go24 does not fill itself (the profile, the number, «Моя компания»,
// the lists of products).
func selfFields(tpl []byte) ([]selfField, error) {
	names, err := docgen.Names(tpl)
	if err != nil {
		return nil, err
	}
	var out []selfField
	for _, n := range names {
		if slices.Contains(employeeFields, n) || n == "DocumentNumber" || n == "DocumentDate" || docImageField(n) ||
			strings.Contains(n, ".") {
			continue
		}
		out = append(out, selfField{Name: n, Date: strings.Contains(strings.ToLower(n), "date")})
	}
	return out, nil
}

// signSelfTemplates are the templates the user may make a document by themselves («Кому доступен» of the template).
func (s *Server) signSelfTemplates(r *http.Request, sess auth.Session) ([]sign.SelfTemplate, error) {
	list, err := s.Sign.SelfTemplates(r.Context())
	if err != nil {
		return nil, err
	}
	out := list[:0]
	for _, t := range list {
		if s.docTemplateAllowed(r, sess, t.TemplateID) == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// signSelfNew is «Создать документ»: the templates, and the fields of the one chosen (?tpl=).
func (s *Server) signSelfNew(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	tpls, err := s.signSelfTemplates(r, sess)
	if err != nil {
		s.fail(w, "sign self", err)
		return
	}
	data := map[string]any{"Templates": tpls}
	if id := r.URL.Query().Get("tpl"); id != "" {
		i := slices.IndexFunc(tpls, func(t sign.SelfTemplate) bool { return t.TemplateID == id })
		if i < 0 {
			s.taskErr(w, r, sign.ErrNotFound, "/sign/my")
			return
		}
		_, tpl, err := s.docTemplateOf(r, crm.TemplateEmployee, id)
		if err != nil {
			s.taskErr(w, r, err, "/sign/my")
			return
		}
		if data["Fields"], err = selfFields(tpl); err != nil {
			s.taskErr(w, r, i18n.E("err.doc_not_docx"), "/sign/my")
			return
		}
		data["Tpl"] = tpls[i]
	}
	s.render(w, r, "signself", page{TitleKey: "sign.self.new", Nav: "sign", Data: data})
}

// signSelfCreate makes the employee's document with their fields and those typed in; they sign it first, then the
// representative of the template.
func (s *Server) signSelfCreate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/my"
	id, err := s.signSelfMake(r, sess, r.FormValue("template"))
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/sign/"+id+"?msg="+url.QueryEscape(s.tr(r, "sign.self.created")), http.StatusSeeOther)
}

// signSelfMake makes the document of the template tplID for the user by the form's fields («v.Name»); its id.
func (s *Server) signSelfMake(r *http.Request, sess auth.Session, tplID string) (string, error) {
	st, err := s.Sign.SelfTemplate(r.Context(), tplID)
	if err == nil {
		err = s.docTemplateAllowed(r, sess, tplID)
	}
	if err != nil {
		return "", sign.ErrNotFound
	}
	t, tpl, err := s.docTemplateOf(r, crm.TemplateEmployee, tplID)
	if err != nil {
		return "", err
	}
	fields, err := selfFields(tpl)
	if err != nil {
		return "", i18n.E("err.doc_not_docx")
	}
	people, err := s.signHRRecipients(r, []string{sess.UserID})
	if err != nil {
		return "", err
	}
	if len(people) == 0 { // a guest
		return "", sign.ErrNotFound
	}
	me := people[0]
	values := map[string]string{"Date": time.Now().Format(s.tr(r, "format.date"))}
	if err := s.myCompanyValues(r, values); err != nil {
		return "", err
	}
	for k, v := range me.Values {
		values[k] = v
	}
	for _, f := range fields {
		v := strings.TrimSpace(r.FormValue("v." + f.Name))
		if len([]rune(v)) > selfValueMax {
			return "", i18n.E("sign.self.err.long", f.Name, selfValueMax)
		}
		if f.Date {
			if d, err := time.Parse("2006-01-02", v); err == nil {
				v = d.Format(s.tr(r, "format.date"))
			}
		}
		values[f.Name] = v
	}
	images, err := s.docImages(r)
	if err != nil {
		return "", err
	}
	data, err := s.hrDocument(r, t.ID, tpl, values, images)
	if err != nil {
		return "", err
	}
	title := t.Name + " — " + me.Name
	d := sign.Doc{Title: title, AuthorID: sess.UserID, Hash: sign.Hash(data), RemindDays: 1}
	if d.FileID, err = s.signKeep(r, sess.UserID, data, title+".docx"); err != nil {
		return "", err
	}
	parties := []sign.Party{{UserID: me.ID, Name: me.Name, Email: me.Email, Side: "employee"}}
	if st.SignerID != me.ID {
		var rep tasks.User
		if err := s.DB.QueryRow(r.Context(), `SELECT id::text, name, email FROM users WHERE id::text = $1 AND active AND NOT guest`,
			st.SignerID).Scan(&rep.ID, &rep.Name, &rep.Email); err != nil {
			_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, d.FileID)
			return "", i18n.E("sign.self.err.signer")
		}
		parties = append(parties, sign.Party{UserID: rep.ID, Name: rep.Name, Email: rep.Email})
	}
	if d.ID, err = s.Sign.Create(r.Context(), d, parties); err != nil {
		_, _ = s.DB.Exec(r.Context(), `DELETE FROM files WHERE id::text = $1`, d.FileID)
		return "", err
	}
	s.Log.Info("sign self", "user_id", sess.UserID, "template", t.ID, "doc", d.ID)
	return d.ID, nil
}

// signSelfSet: an HR officer or an administrator lets employees make documents by the template themselves, with the
// representative who signs after them (none — no longer).
func (s *Server) signSelfSet(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/sign/hr#self"
	if !s.signHRAllowed(r, sess) {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	id := r.PathValue("id")
	err := s.docTemplateAllowed(r, sess, id)
	if err == nil {
		err = s.Sign.SetSelf(r.Context(), id, r.FormValue("signer"), sess.UserID)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/sign/hr?msg="+url.QueryEscape(s.tr(r, "common.saved"))+"#self", http.StatusSeeOther)
}
