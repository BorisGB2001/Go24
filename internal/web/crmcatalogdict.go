package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Настройки каталога» (/crm/products/settings): units of measure and the products' properties ----------

// unitView is a unit of the dictionary as shown: a built-in unit's empty name and symbol come from the locale.
type unitView struct {
	crm.Unit
	ShownName, ShownSymbol string
}

// units are the dictionary's units in the request's language.
func (s *Server) units(r *http.Request) ([]unitView, error) {
	list, err := s.CRM.Units(r.Context())
	if err != nil {
		return nil, err
	}
	out := make([]unitView, len(list))
	for i, u := range list {
		v := unitView{Unit: u, ShownName: u.Name, ShownSymbol: u.Symbol}
		if k := "unit.name." + u.Code; v.ShownName == "" && u.Code != "" {
			if v.ShownName = s.tr(r, k); v.ShownName == k {
				v.ShownName = u.Code
			}
		}
		if k := "unit.sym." + u.Code; v.ShownSymbol == "" && u.Code != "" {
			if v.ShownSymbol = s.tr(r, k); v.ShownSymbol == k {
				v.ShownSymbol = u.Code
			}
		}
		out[i] = v
	}
	return out, nil
}

// catalogDicts adds the units and the properties to a page's data.
func (s *Server) catalogDicts(r *http.Request, data map[string]any) error {
	units, err := s.units(r)
	if err != nil {
		return err
	}
	props, err := s.CRM.ProductProps(r.Context())
	if err != nil {
		return err
	}
	var cols []crm.Prop
	for _, p := range props {
		if p.InList {
			cols = append(cols, p)
		}
	}
	data["Units"], data["Props"], data["PropCols"] = units, props, cols
	return nil
}

// crmCatalogSettings shows the catalog's dictionaries; administrators keep them.
func (s *Server) crmCatalogSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	data := map[string]any{"Kind": "products"}
	err := s.catalogDicts(r, data)
	if err == nil {
		err = s.crmTabs(r, sess, data)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/products")
		return
	}
	s.render(w, r, "crmcatalogsettings", page{TitleKey: "crm.catalog_settings", Nav: "crm_products", Data: data})
}

// crmUnitSave creates (id "new"), changes or deletes (op "delete") a unit of measure — administrators.
func (s *Server) crmUnitSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/products/settings"
	var err error = i18n.E("err.forbidden")
	if sess.Admin {
		id := r.PathValue("id")
		if r.FormValue("op") == "delete" {
			err = s.CRM.DeleteUnit(r.Context(), id)
		} else {
			if id == "new" {
				id = ""
			}
			sort, _ := strconv.Atoi(r.FormValue("sort"))
			_, err = s.CRM.SaveUnit(r.Context(), crm.Unit{ID: id, Code: r.FormValue("code"), Name: r.FormValue("name"),
				Symbol: r.FormValue("symbol"), Default: r.FormValue("default") != "", Sort: sort})
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// crmPropSave creates (id "new"), changes or deletes (op "delete") a property of the products — administrators.
// A list's values come one to a line.
func (s *Server) crmPropSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/products/settings"
	var err error = i18n.E("err.forbidden")
	if sess.Admin {
		id := r.PathValue("id")
		if r.FormValue("op") == "delete" {
			err = s.CRM.DeleteProp(r.Context(), id)
		} else {
			if id == "new" {
				id = ""
			}
			sort, _ := strconv.Atoi(r.FormValue("sort"))
			_, err = s.CRM.SaveProp(r.Context(), crm.Prop{ID: id, Name: r.FormValue("name"), Kind: r.FormValue("kind"),
				Options: strings.Split(r.FormValue("options"), "\n"), InList: r.FormValue("in_list") != "", Variant: r.FormValue("variant") != "", Sort: sort})
		}
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// formProps reads the properties' values of a product's form: the fields "prop.<id>".
func formProps(r *http.Request) map[string]string {
	out := map[string]string{}
	for k, v := range r.Form {
		if id, ok := strings.CutPrefix(k, "prop."); ok && len(v) > 0 {
			out[id] = v[0]
		}
	}
	return out
}

// formBarcodes reads barcodes typed one to a line (or by commas).
func formBarcodes(v string) []string {
	return strings.FieldsFunc(v, func(c rune) bool { return c == '\n' || c == '\r' || c == ',' || c == ';' })
}
