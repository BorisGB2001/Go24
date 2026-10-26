package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// «Пользовательские поля задач»: the administrator's list of fields; the task card fills them in.

// fieldSets are the administrator's pages of custom fields: /settings/task-fields and /settings/user-fields.
var fieldSets = map[string]struct {
	Set   tasks.FieldSet
	Title string // locale key of the page
	Sub   string
	CRM   string // the CRM entity: its fields may be required («Обязательное поле»), at stages for deals and leads
}{
	"task-fields":        {tasks.TaskFieldSet, "title.task_fields", "fields.subtitle", ""},
	"user-fields":        {tasks.UserFieldSet, "title.user_fields", "fields.user_subtitle", ""},
	"crm-deal-fields":    {tasks.CRMFieldSets["deal"], "title.crm_deal_fields", "fields.crm_subtitle", "deal"},
	"crm-lead-fields":    {tasks.CRMFieldSets["lead"], "title.crm_lead_fields", "fields.crm_subtitle", "lead"},
	"crm-contact-fields": {tasks.CRMFieldSets["contact"], "title.crm_contact_fields", "fields.crm_subtitle", "contact"},
	"crm-company-fields": {tasks.CRMFieldSets["company"], "title.crm_company_fields", "fields.crm_subtitle", "company"},
}

// fieldStages are the stages a field of the entity may be required at, by pipeline (one unnamed group for leads).
func (s *Server) fieldStages(r *http.Request, entity string) ([]crm.Pipeline, error) {
	switch entity {
	case "deal":
		return s.CRM.Pipelines(r.Context())
	case "lead":
		stages, err := s.CRM.LeadStages(r.Context())
		return []crm.Pipeline{{Stages: stages}}, err
	}
	return nil, nil
}

// fieldRequiredForm: the form's «Обязательное поле» — always, or only at the checked stages.
func fieldRequiredForm(r *http.Request) (bool, []string) {
	if r.FormValue("required") != "1" {
		return false, nil
	}
	if r.FormValue("required_at") != "stages" {
		return true, nil
	}
	return true, r.Form["stages"]
}

func (s *Server) fieldsPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	fs, ok := fieldSets[r.PathValue("set")]
	if !sess.Admin || !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	list, err := s.Tasks.FieldsOf(r.Context(), fs.Set)
	var pipes []crm.Pipeline
	if err == nil && fs.CRM != "" {
		pipes, err = s.fieldStages(r, fs.CRM)
	}
	if err != nil {
		s.fail(w, "fields", err)
		return
	}
	s.render(w, r, "fields", page{Title: s.tr(r, fs.Title), Nav: strings.ReplaceAll(r.PathValue("set"), "-", "_"),
		Data: map[string]any{"Fields": list, "Kinds": tasks.FieldKinds, "Base": "/settings/" + r.PathValue("set"), "Title": fs.Title, "Sub": fs.Sub,
			"CRM": fs.CRM, "Pipelines": pipes}})
}

func (s *Server) fieldAdd(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	fs, ok := fieldSets[r.PathValue("set")]
	if !sess.Admin || !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := "/settings/" + r.PathValue("set")
	sort, _ := strconv.Atoi(r.FormValue("sort"))
	f := tasks.Field{Name: r.FormValue("name"), Kind: r.FormValue("kind"), Sort: sort}
	if fs.CRM != "" {
		f.Required, f.Stages = fieldRequiredForm(r)
	}
	if f.Kind == "list" {
		f.Options = strings.Split(r.FormValue("options"), "\n")
	}
	if err := s.Tasks.AddFieldTo(r.Context(), fs.Set, f); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "fields.saved")), http.StatusSeeOther)
}

func (s *Server) fieldDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	fs, ok := fieldSets[r.PathValue("set")]
	if !sess.Admin || !ok {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := "/settings/" + r.PathValue("set")
	if err := s.Tasks.DeleteFieldOf(r.Context(), fs.Set, r.PathValue("id")); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// fieldRequired changes whether a CRM field is required, always or at the checked stages.
func (s *Server) fieldRequired(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	fs, ok := fieldSets[r.PathValue("set")]
	if !sess.Admin || !ok || fs.CRM == "" {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	back := "/settings/" + r.PathValue("set")
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	required, stages := fieldRequiredForm(r)
	if err := s.Tasks.SetFieldRequired(r.Context(), fs.Set, r.PathValue("id"), required, stages); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "fields.required_saved")), http.StatusSeeOther)
}

// userFields saves an employee's profile fields: the employee or an administrator.
func (s *Server) userFields(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/u/" + id
	if sess.UserID != id && !sess.Admin {
		http.Error(w, s.tr(r, "err.forbidden"), http.StatusForbidden)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	list, err := s.Tasks.FieldsOf(r.Context(), tasks.UserFieldSet)
	if err == nil {
		values := map[string]string{}
		for _, f := range list {
			values[f.ID] = r.FormValue("f_" + f.ID)
		}
		err = s.Tasks.SetUserFieldValues(r.Context(), id, values)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// taskFields saves the card's field form: it carries every field (an unchecked bool is "").
func (s *Server) taskFields(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	s.taskPost(w, r, "", func(id string) error {
		if err := r.ParseForm(); err != nil {
			return i18n.E("err.bad_form")
		}
		list, err := s.Tasks.Fields(r.Context())
		if err != nil {
			return err
		}
		values := map[string]string{}
		for _, f := range list {
			values[f.ID] = r.FormValue("f_" + f.ID)
		}
		return s.Tasks.SetFieldValues(r.Context(), sess.UserID, id, values)
	})
}
