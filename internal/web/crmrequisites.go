package web

import (
	"net/http"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Реквизиты» of a company or a contact: made, changed, made the default and removed on its card ----------

// crmRequisite: "save" a requisite (a new one without req), make it the "default", or "delete" it — who may change the card.
func (s *Server) crmRequisite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	entity, back := crmEntities[kind], "/crm/"+kind+"/"+id
	if entity != "company" && entity != "contact" {
		http.NotFound(w, r)
		return
	}
	err := s.crmCheck(r, sess, kind, id, "write")
	req := r.FormValue("req")
	if err == nil && req != "" { // the requisite must be this card's
		var q crm.Requisite
		if q, err = s.CRM.Requisite(r.Context(), req); err == nil && (q.Entity != entity || q.EntityID != id) {
			err = crm.ErrNotFound
		}
	}
	if err == nil {
		switch r.FormValue("op") {
		case "save":
			_, err = s.CRM.SaveRequisite(r.Context(), crm.Requisite{ID: req, Entity: entity, EntityID: id,
				Preset: r.FormValue("preset"), Name: r.FormValue("name"), INN: r.FormValue("inn"), KPP: r.FormValue("kpp"),
				OGRN: r.FormValue("ogrn"), Director: r.FormValue("director"), Address: r.FormValue("address"),
				Bank: r.FormValue("bank"), BIK: r.FormValue("bik"), Account: r.FormValue("account"),
				CorrAccount: r.FormValue("corr_account"), Default: r.FormValue("default") == "1"})
		case "default":
			err = s.CRM.SetDefaultRequisite(r.Context(), req)
		case "delete":
			err = s.CRM.DeleteRequisite(r.Context(), req)
		default:
			err = i18n.E("err.bad_form")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#requisites", http.StatusSeeOther)
}
