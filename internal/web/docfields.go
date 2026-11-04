package web

import (
	"net/http"
	"net/url"
	"strings"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/docgen"
	"go24/internal/i18n"
)

// «Поля» of a template of the document generator, as b_documentgenerator_field (GO24DEV-1007): each field of
// the template's file with its title, value by default, required and «Скрывать строку, если пусто».

// docSettable tells the fields of a template that have settings: not the number and date given when a document is
// made, the products (the card's) and the pictures («Моя компания»'s).
func docSettable(n string) bool {
	return n != "DocumentNumber" && n != "DocumentDate" && !strings.HasPrefix(n, "Product.") && !docImageField(n)
}

// docTemplateFieldList is the template's fields from its file with their settings (those without — none set).
func (s *Server) docTemplateFieldList(r *http.Request, id string) (crm.DocTemplate, []crm.DocField, error) {
	t, err := s.CRM.DocTemplate(r.Context(), id)
	if err != nil {
		return t, nil, err
	}
	_, tpl, err := s.docTemplateOf(r, t.Kind(), id)
	if err != nil {
		return t, nil, err
	}
	names, err := docgen.Names(tpl)
	if err != nil {
		return t, nil, i18n.E("err.doc_not_docx")
	}
	set, err := s.CRM.DocTemplateFields(r.Context(), id)
	if err != nil {
		return t, nil, err
	}
	var out []crm.DocField
	for _, n := range names {
		if docSettable(n) {
			f := set[n]
			f.Name = n
			out = append(out, f)
		}
	}
	return t, out, nil
}

// docTemplateFields is the page «Поля» of a template (those who change the templates).
func (s *Server) docTemplateFields(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.docRightOr403(w, r, sess, "templates") {
		return
	}
	t, fields, err := s.docTemplateFieldList(r, r.PathValue("id"))
	if err != nil {
		s.taskErr(w, r, err, "/crm/documents")
		return
	}
	s.render(w, r, "docfields", page{Title: t.Name, Nav: "crm_deals", Data: map[string]any{"Template": t, "Fields": fields}})
}

// docTemplateFieldsSave saves the settings of the fields the page sent: f.N.name, f.N.title, f.N.value,
// f.N.required, f.N.hide_row for each N.
func (s *Server) docTemplateFieldsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.docRightOr403(w, r, sess, "templates") {
		return
	}
	id := r.PathValue("id")
	_ = r.ParseForm()
	var fields []crm.DocField
	for k := range r.PostForm {
		p, ok := strings.CutSuffix(k, ".name")
		if !ok || !strings.HasPrefix(p, "f.") {
			continue
		}
		fields = append(fields, crm.DocField{Name: r.PostForm.Get(k), Title: r.PostForm.Get(p + ".title"),
			Value: r.PostForm.Get(p + ".value"), Required: r.PostForm.Get(p+".required") != "", HideRow: r.PostForm.Get(p+".hide_row") != ""})
	}
	back := "/crm/documents/" + id + "/fields"
	if err := s.CRM.SetDocTemplateFields(r.Context(), id, sess.UserID, fields); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "common.saved")), http.StatusSeeOther)
}

// apiDocTemplateFields: GET /api/v1/document-templates/{id}/fields — the template's fields with their settings (who
// may use the template).
func (s *Server) apiDocTemplateFields(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	id := r.PathValue("id")
	if err := s.docTemplateAllowed(r, s.apiSess(r, p), id); err != nil {
		return err
	}
	_, fields, err := s.docTemplateFieldList(r, id)
	if err != nil {
		return err
	}
	if fields == nil {
		fields = []crm.DocField{}
	}
	writeJSON(w, http.StatusOK, fields)
	return nil
}

// apiDocTemplateFieldsSet: PUT /api/v1/document-templates/{id}/fields — [{name, title, value, required, hide_row}],
// all at once (the right «templates»); the fields as GET gives them.
func (s *Server) apiDocTemplateFieldsSet(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	sess := s.apiSess(r, p)
	if err := s.docRight(r.Context(), sess, "templates"); err != nil {
		return err
	}
	var in []crm.DocField
	if err := readJSON(r, &in); err != nil {
		return err
	}
	id := r.PathValue("id")
	if err := s.CRM.SetDocTemplateFields(r.Context(), id, sess.UserID, in); err != nil {
		return err
	}
	return s.apiDocTemplateFields(w, r, p)
}
