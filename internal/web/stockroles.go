package web

import (
	"net/http"
	"net/url"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// «Складской учёт» → «Права доступа» (administrators): roles, their rights on the documents, warehouses, purchase
// prices and settings, their people.
func (s *Server) stockRoles(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/store/docs")
		return
	}
	data := s.stockData(r, sess, "rights")
	roles, err := s.CRM.StockRoles(r.Context())
	if err == nil {
		data["Users"], err = s.Tasks.ListUsers(r.Context())
	}
	if err == nil {
		data["Warehouses"], err = s.CRM.Warehouses(r.Context())
	}
	if err != nil {
		s.taskErr(w, r, err, "/store/docs")
		return
	}
	data["Roles"], data["Actions"] = roles, crm.StockActions
	s.render(w, r, "stockroles", page{TitleKey: "stockroles.title", Nav: "store_docs", Data: data})
}

// stockRoleSave: the role's rights from the form (fields <kind>.<action>, warehouse, purchase_price, settings), a new
// role with id "new"; op=delete removes it, op=add / op=remove gives a person the role or takes it away.
func (s *Server) stockRoleSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/store/rights"
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	id := r.PathValue("id")
	var err error
	switch r.FormValue("op") {
	case "delete":
		err = s.CRM.DeleteStockRole(r.Context(), id)
	case "add", "remove":
		err = s.CRM.SetStockMember(r.Context(), id, r.FormValue("user_id"), r.FormValue("op") == "add")
	default:
		if err = r.ParseForm(); err != nil {
			break
		}
		if id == "new" {
			id = ""
		}
		p := crm.StockPerms{Docs: map[string][]string{}, Warehouses: r.PostForm["warehouse"],
			PurchasePrice: r.FormValue("purchase_price") != "", Settings: r.FormValue("settings") != ""}
		for _, k := range crm.StockKinds {
			for _, a := range crm.StockActions {
				if r.FormValue(k+"."+a) != "" {
					p.Docs[k] = append(p.Docs[k], a)
				}
			}
		}
		_, err = s.CRM.SaveStockRole(r.Context(), crm.StockRole{ID: id, Name: r.FormValue("name"), Perms: p})
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	s.Log.Info("stock role saved", "user_id", sess.UserID, "role", id, "op", r.FormValue("op"))
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}
