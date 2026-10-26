package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/sites"
)

// ---------- «CRM-формы» (/crm/forms): administrators make the forms, clients fill them at /cf/{token} ----------

func (s *Server) crmForms(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "forms", "Mode": "list", "Root": strings.TrimRight(s.BaseURL, "/")}
	err := s.crmTabs(r, sess, data)
	if err == nil {
		data["Forms"], err = s.CRM.Forms(r.Context())
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmforms", page{TitleKey: "crm.forms", Nav: "crm", Data: data})
}

// formChoice is a field the editor offers: the form's own (On, with its label and «обязательное») or not in it yet.
type formChoice struct {
	crm.FormField
	Scheme string // "" — a built-in field, either scheme's; lead | deal — a custom field of that card
	On     bool
}

// crmFormEdit is the editor of a form (/crm/forms/new, /crm/forms/{id}): the fields of the form first in its order,
// then those it may ask; the code to put it on a site.
func (s *Server) crmFormEdit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/crm/forms")
		return
	}
	ctx := r.Context()
	f := crm.Form{Scheme: "lead", Active: true}
	for _, k := range []string{"name", "phone", "email"} {
		f.Fields = append(f.Fields, crm.FormField{Key: k, Required: k == "name"})
	}
	if id := r.PathValue("id"); id != "new" {
		var err error
		if f, err = s.CRM.Form(ctx, id); err != nil {
			s.taskErr(w, r, err, "/crm/forms")
			return
		}
	}
	var choice []formChoice
	for _, x := range f.Fields {
		choice = append(choice, formChoice{FormField: x, On: true})
	}
	has := func(key string) bool {
		for _, c := range choice {
			if c.Key == key {
				return true
			}
		}
		return false
	}
	for _, k := range crm.FormBuiltin {
		if !has(k) {
			choice = append(choice, formChoice{FormField: crm.FormField{Key: k}})
		}
	}
	for _, scheme := range crm.FormSchemes {
		defs, err := s.Tasks.FieldsOf(ctx, crm.FormFieldSet(scheme))
		if err != nil {
			s.fail(w, "crm form fields", err)
			return
		}
		for _, d := range defs {
			key := "f:" + d.ID
			for i := range choice {
				if choice[i].Key == key {
					choice[i].Name, choice[i].Kind, choice[i].Scheme = d.Name, d.Kind, scheme
				}
			}
			if !has(key) {
				choice = append(choice, formChoice{FormField: crm.FormField{Key: key, Name: d.Name, Kind: d.Kind}, Scheme: scheme})
			}
		}
	}
	keep := choice[:0]
	for _, c := range choice { // a custom field deleted since
		if c.Custom() == "" || c.Scheme != "" {
			keep = append(keep, c)
		}
	}
	data := map[string]any{"Kind": "forms", "Mode": "edit", "Form": f, "Choice": keep, "Root": strings.TrimRight(s.BaseURL, "/")}
	err := s.crmTabs(r, sess, data)
	if err == nil {
		data["Pipelines"], err = s.CRM.Pipelines(ctx)
	}
	if err == nil {
		data["Users"], err = s.Tasks.ListUsers(ctx)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/forms")
		return
	}
	s.render(w, r, "crmforms", page{TitleKey: "crm.forms", Nav: "crm", Data: data})
}

// crmFormSave creates or changes a form (the fields in the order of the editor's lines, those ticked), or deletes it
// (op=delete).
func (s *Server) crmFormSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/crm/forms/" + id
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/crm/forms")
		return
	}
	if r.FormValue("op") == "delete" {
		if err := s.CRM.DeleteForm(r.Context(), id); err != nil {
			s.taskErr(w, r, err, back)
			return
		}
		http.Redirect(w, r, "/crm/forms?msg="+url.QueryEscape(s.tr(r, "crm.form_deleted")), http.StatusSeeOther)
		return
	}
	f := crm.Form{Name: r.FormValue("name"), Active: r.FormValue("active") != "", Scheme: r.FormValue("scheme"),
		PipelineID: r.FormValue("pipeline"), Responsible: crm.Ref{ID: r.FormValue("responsible")}, Title: r.FormValue("title"),
		Text: r.FormValue("text"), Button: r.FormValue("button"), Success: r.FormValue("success"), Redirect: r.FormValue("redirect"),
		Consent: r.FormValue("consent")}
	if id != "new" {
		f.ID = id
	}
	for _, k := range r.Form["key"] {
		if r.FormValue("on_"+k) != "" {
			f.Fields = append(f.Fields, crm.FormField{Key: k, Label: clipText(strings.TrimSpace(r.FormValue("label_"+k)), 200),
				Required: r.FormValue("req_"+k) != ""})
		}
	}
	id, err := s.CRM.SaveForm(r.Context(), sess.UserID, f)
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/crm/forms/"+id+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// crmFormResults are the fillings of a form with the cards they made: for administrators and the form's responsible.
func (s *Server) crmFormResults(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	ctx := r.Context()
	f, err := s.CRM.Form(ctx, r.PathValue("id"))
	if err == nil && !sess.Admin && f.Responsible.ID != sess.UserID {
		err = i18n.E("err.forbidden")
	}
	data := map[string]any{"Kind": "forms", "Mode": "results", "Form": f}
	if err == nil {
		err = s.crmTabs(r, sess, data)
	}
	if err == nil {
		data["Results"], err = s.CRM.FormResults(ctx, f.ID, 500)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/forms")
		return
	}
	s.render(w, r, "crmforms", page{TitleKey: "crm.forms", Nav: "crm", Data: data})
}

// ---------- the form as the client sees it: /cf/{token}, ?embed=1 — inside a site (static/js/crmform.js) ----------

// crmFormHits: one address sends at most 5 forms in 10 minutes.
var crmFormHits = &sites.Hits{Max: 5, Per: 10 * time.Minute}

// publicForm is the form of the link with its fields ready to be shown in the visitor's language.
func (s *Server) publicForm(r *http.Request) (crm.Form, error) {
	f, err := s.CRM.FormByToken(r.Context(), r.PathValue("token"))
	if err == nil {
		err = s.CRM.FormFields(r.Context(), &f)
	}
	f.Labeled(func(key string) string { return s.tr(r, key) })
	return f, err
}

func (s *Server) renderForm(w http.ResponseWriter, r *http.Request, status int, f crm.Form, data map[string]any) {
	data["Form"], data["Stamp"] = f, sites.FormStamp(time.Now())
	if t := r.PostFormValue("t"); t != "" { // shown again after a refusal: the time the client began, not now
		data["Stamp"] = t
	}
	data["Embed"] = r.FormValue("embed") == "1"
	data["Page"] = r.FormValue("page")
	data["OL"] = r.FormValue("ol") // the form an operator sent in a dialogue
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	s.render(w, r, "crmformpub", page{Data: data})
}

func (s *Server) crmFormPublic(w http.ResponseWriter, r *http.Request) {
	f, err := s.publicForm(r)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		s.render(w, r, "share", page{TitleKey: "share.gone", Data: map[string]any{"Gone": true}})
		return
	}
	s.renderForm(w, r, http.StatusOK, f, map[string]any{"Values": map[string]string{}})
}

// crmFormSend: the client sends the form — the cards (crm.SubmitForm), then the form's text or its address. A robot
// (the trap field filled, the form sent too fast) is told it went well, and nothing is made; a refusal shows the
// form again with what the client typed.
func (s *Server) crmFormSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := s.publicForm(r)
	if err != nil {
		http.Error(w, s.tr(r, "err.not_found"), http.StatusNotFound)
		return
	}
	values := map[string]string{}
	for _, x := range f.Fields {
		values[x.Key] = r.FormValue(x.Key)
	}
	refuse := func(err error) {
		var ie *i18n.Error
		if !errors.As(err, &ie) {
			s.fail(w, "crm form", err)
			return
		}
		s.renderForm(w, r, http.StatusUnprocessableEntity, f, map[string]any{"Values": values, "Err": s.tr(r, ie.Key, ie.Args...)})
	}
	done := func() {
		if f.Redirect != "" && r.FormValue("embed") != "1" {
			http.Redirect(w, r, f.Redirect, http.StatusSeeOther)
			return
		}
		s.renderForm(w, r, http.StatusOK, f, map[string]any{"Sent": true})
	}
	now := time.Now()
	if !sites.Human(r.FormValue("website"), r.FormValue("t"), now) {
		done()
		return
	}
	if !crmFormHits.Allow(clientIP(r), now) {
		refuse(i18n.E("err.crm_form_often"))
		return
	}
	ans, err := f.Answer(r.FormValue, r.FormValue("consent") != "")
	if err != nil {
		refuse(err)
		return
	}
	got := map[string]string{}
	for _, a := range ans {
		got[a.Key] = a.Value
	}
	who := strings.TrimSpace(got["name"] + " " + got["last_name"])
	for _, k := range []string{"phone", "email"} {
		if who == "" {
			who = got[k]
		}
	}
	title := s.portalTr("crm.form_lead_title", f.Name)
	if who != "" {
		title += " — " + who
	}
	x := crm.FormFilling{Title: clipText(title, 300), Source: clipText(s.portalTr("crm.form_lead_source", f.Name), 300),
		IP: clientIP(r), Page: r.FormValue("page")}
	if x.Page != "" {
		x.Comment = s.portalTr("crm.form_lead_page", x.Page)
	}
	ol := r.FormValue("ol")
	if ol != "" && s.OpenLines != nil { // sent in a dialogue: its cards are the dialogue's (an unknown key — a site's filling)
		if l, err := s.OpenLines.FormFor(ctx, f.ID, ol); err == nil {
			x.Session, x.LeadID = l.Session, l.LeadID
		}
	}
	res, err := s.CRM.SubmitForm(ctx, f, ans, x)
	if err != nil {
		var ie *i18n.Error
		if errors.As(err, &ie) {
			refuse(err)
			return
		}
		s.fail(w, "crm form submit", err)
		return
	}
	if x.Session != "" {
		if err := s.OpenLines.FormFilled(ctx, ol, res); err != nil {
			s.fail(w, "crm form dialogue", err)
			return
		}
	}
	done()
}

// ---------- REST ----------

type apiForm struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Active      bool            `json:"active"`
	Scheme      string          `json:"scheme"`
	PipelineID  string          `json:"pipeline_id,omitempty"`
	Responsible string          `json:"responsible_id,omitempty"`
	URL         string          `json:"url"`
	Fields      []crm.FormField `json:"fields"`
	Results     int             `json:"results"`
	CreatedAt   time.Time       `json:"created_at"`
}

// GET /api/v1/crm/forms — the forms with their public addresses and fields.
func (s *Server) apiCRMForms(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	list, err := s.CRM.Forms(r.Context())
	if err != nil {
		return err
	}
	out := make([]apiForm, 0, len(list))
	for _, f := range list {
		out = append(out, apiForm{f.ID, f.Name, f.Active, f.Scheme, f.PipelineID, f.Responsible.ID,
			strings.TrimRight(s.BaseURL, "/") + "/cf/" + f.Token, f.Fields, f.Results, f.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

// GET /api/v1/crm/forms/{id}/results — the fillings, newest first, with the cards they made.
func (s *Server) apiCRMFormResults(w http.ResponseWriter, r *http.Request, p apikeys.Principal) error {
	if _, err := s.CRM.Form(r.Context(), r.PathValue("id")); err != nil {
		return err
	}
	list, err := s.CRM.FormResults(r.Context(), r.PathValue("id"), 500)
	if err != nil {
		return err
	}
	type item struct {
		ID        string           `json:"id"`
		Answers   []crm.FormAnswer `json:"answers"`
		LeadID    string           `json:"lead_id,omitempty"`
		ContactID string           `json:"contact_id,omitempty"`
		DealID    string           `json:"deal_id,omitempty"`
		Page      string           `json:"page,omitempty"`
		CreatedAt time.Time        `json:"created_at"`
	}
	out := make([]item, 0, len(list))
	for _, x := range list {
		out = append(out, item{ID: x.ID, Answers: x.Answers, LeadID: x.LeadID, ContactID: x.ContactID, DealID: x.DealID, Page: x.Page,
			CreatedAt: x.CreatedAt})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}
