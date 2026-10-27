package web

import (
	"net/http"
	"net/url"
	"slices"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Поиск дубликатов» of the CRM (/crm/duplicates): the groups of alike cards, merged or «Не дубликаты» ----------

func (s *Server) crmDuplicates(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	entity, by := r.URL.Query().Get("entity"), r.URL.Query().Get("by")
	if crm.DupCriteria[entity] == nil {
		entity = "contact"
	}
	if !slices.Contains(crm.DupCriteria[entity], by) {
		by = "phone"
	}
	data := map[string]any{"Kind": "duplicates", "Entity": entity, "By": by, "Criteria": crm.DupCriteria[entity],
		"Merge": entity != "lead", "Limit": crm.DupLimit}
	err := s.crmTabs(r, sess, data)
	if err == nil {
		data["Groups"], data["Total"], err = s.CRM.DuplicateGroups(r.Context(), sess.UserID, entity, by)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmdups", page{TitleKey: "crm.dups", Nav: "crm", Data: data})
}

// crmDuplicatesAction: «Объединить» the chosen cards of a group into the main one (op=merge, keep — who may change it
// and delete the others) or mark them «Не дубликаты» (op=skip — who may change them all).
func (s *Server) crmDuplicatesAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, "/crm/duplicates")
		return
	}
	entity, keep, ids := r.FormValue("entity"), r.FormValue("keep"), r.Form["ids"]
	back := "/crm/duplicates?entity=" + url.QueryEscape(entity) + "&by=" + url.QueryEscape(r.FormValue("by"))
	kind := crmPaths[entity]
	var err error
	if kind == "" || crm.DupCriteria[entity] == nil {
		err = i18n.E("err.bad_form")
	}
	msg := ""
	switch op := r.FormValue("op"); {
	case err != nil:
	case op == "merge" && entity != "lead" && keep != "":
		others := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == keep })
		if len(others) == 0 {
			err = i18n.E("err.bad_form")
		}
		if err == nil {
			err = s.crmCheck(r, sess, kind, keep, "write")
		}
		for _, o := range others {
			if err == nil {
				err = s.crmCheck(r, sess, kind, o, "delete")
			}
		}
		for _, o := range others {
			if err == nil {
				err = s.CRM.Merge(r.Context(), sess.UserID, entity, keep, o)
			}
		}
		msg = s.tr(r, "crm.merged")
	case op == "skip":
		for _, id := range ids {
			if err == nil {
				err = s.crmCheck(r, sess, kind, id, "write")
			}
		}
		if err == nil {
			err = s.CRM.NotDuplicates(r.Context(), sess.UserID, entity, ids)
		}
		msg = s.tr(r, "crm.dups_skipped")
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"&msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
