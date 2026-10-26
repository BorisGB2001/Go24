package web

import (
	"net/http"
	"net/url"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Справочники» of the CRM (/crm/dicts): everyone sees the lists, administrators keep them ----------

func (s *Server) crmDicts(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "dicts", "DictKinds": crm.DictKinds}
	err := s.crmTabs(r, sess, data)
	if err == nil {
		data["Dicts"], err = s.CRM.Dicts(r.Context())
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmdicts", page{TitleKey: "crm.dicts", Nav: "crm_dicts", Data: data})
}

// crmDictSave: the administrator adds, renames, moves or deletes an item of a dictionary.
func (s *Server) crmDictSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/dicts"
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	var err error
	id := r.FormValue("id")
	switch r.FormValue("op") {
	case "add":
		_, err = s.CRM.AddDictItem(r.Context(), r.FormValue("kind"), r.FormValue("name"))
	case "rename":
		err = s.CRM.RenameDictItem(r.Context(), id, r.FormValue("name"))
	case "up", "down":
		err = s.CRM.MoveDictItem(r.Context(), id, r.FormValue("op") == "up")
	case "delete":
		err = s.CRM.DeleteDictItem(r.Context(), id)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/crm/dicts?msg="+url.QueryEscape(s.tr(r, "crm.saved"))+"#"+url.QueryEscape(r.FormValue("kind")), http.StatusSeeOther)
}
