package web

import (
	"net/http"
	"net/url"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Валюты» of the CRM (/crm/currencies): everyone sees the rates, administrators keep the list ----------

func (s *Server) crmCurrencies(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "currencies", "CBR": crm.CBRURL}
	if err := s.crmTabs(r, sess, data); err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	list, _ := data["Currencies"].([]crm.Currency)
	for i, c := range list { // a currency without its own name has the locale's one, if it knows it
		if key := "currency." + c.Code; c.Name == "" && s.tr(r, key) != key {
			list[i].Name = s.tr(r, key)
		}
	}
	s.render(w, r, "crmcurrencies", page{TitleKey: "crm.currencies", Nav: "crm_currencies", Data: data})
}

// crmCurrencySave: the administrator adds or changes a currency, makes it the base one, deletes it or takes the rates
// of the Central Bank at once.
func (s *Server) crmCurrencySave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/crm/currencies")
		return
	}
	var err error
	code := r.FormValue("code")
	switch r.FormValue("op") {
	case "save":
		err = s.CRM.SaveCurrency(r.Context(), crm.Currency{Code: code, Name: r.FormValue("name"), Rate: formNumber(r.FormValue("rate")),
			Auto: r.FormValue("auto") != ""})
	case "base":
		err = s.CRM.SetBaseCurrency(r.Context(), code)
	case "delete":
		err = s.CRM.DeleteCurrency(r.Context(), code)
	case "fetch":
		_, err = s.CRM.FetchRates(r.Context(), true)
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/currencies")
		return
	}
	http.Redirect(w, r, "/crm/currencies?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}
