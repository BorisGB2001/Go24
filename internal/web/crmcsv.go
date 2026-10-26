package web

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- CSV of the CRM: export of a list (as filtered), import of contacts, companies and leads; duplicates ----------

// crmExport gives the list as a CSV file Excel opens: the deals or leads as the list's filter has them, all contacts
// or companies the person may read.
func (s *Server) crmExport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind := r.PathValue("kind")
	q := r.URL.Query()
	f := crmFilter(q, sess.UserID)
	var head []string
	var rows [][]string
	tr := func(k string) string { return s.tr(r, k) }
	stage := func(st crm.Stage) string {
		if st.Name != "" {
			return st.Name
		}
		return tr("crm.stage." + st.Code)
	}
	num := func(v float64) string { return strings.ReplaceAll(strconv.FormatFloat(v, 'f', -1, 64), ".", ",") }
	when := func(t time.Time) string { return t.Local().Format(tr("format.datetime")) }
	var err error
	switch kind {
	case "deals":
		var list []crm.Deal
		if list, err = s.CRM.DealList(r.Context(), sess.UserID, q.Get("pipeline"), f); err == nil {
			head = []string{"crm.f.title", "crm.f.stage", "crm.f.amount", "crm.f.currency", "crm.f.company", "crm.f.contact", "crm.f.deal_type", "crm.f.origin", "crm.f.responsible", "crm.f.created", "crm.f.comment"}
			for _, d := range list {
				rows = append(rows, []string{d.Title, stage(d.Stage), num(d.Amount), d.Currency, d.Company.Name, d.Contact.Name, d.DealType.Text(tr), d.Origin.Text(tr), d.Responsible.Name, when(d.CreatedAt), d.Comment})
			}
		}
	case "leads":
		var list []crm.Lead
		if list, err = s.CRM.LeadList(r.Context(), sess.UserID, f); err == nil {
			head = []string{"crm.f.title", "crm.f.stage", "crm.f.name", "crm.f.company_title", "crm.f.phone", "crm.f.email", "crm.f.origin", "crm.f.source_info", "crm.f.amount", "crm.f.currency", "crm.f.responsible", "crm.f.created", "crm.f.comment"}
			for _, l := range list {
				rows = append(rows, []string{l.Title, stage(l.Stage), l.Name, l.CompanyTitle, l.Phone, l.Email, l.Origin.Text(tr), l.Source, num(l.Amount), l.Currency, l.Responsible.Name, when(l.CreatedAt), l.Comment})
			}
		}
	case "contacts":
		var list []crm.Contact
		if list, err = s.CRM.Contacts(r.Context(), sess.UserID, q.Get("q"), ""); err == nil {
			head = []string{"crm.f.name", "crm.f.position", "crm.f.company", "crm.f.phone", "crm.f.email", "crm.f.contact_type", "crm.f.origin", "crm.f.responsible", "crm.f.created", "crm.f.comment"}
			for _, x := range list {
				rows = append(rows, []string{x.Name, x.Position, x.Company.Name, x.Phone, x.Email, x.ContactType.Text(tr), x.Origin.Text(tr), x.Responsible.Name, when(x.CreatedAt), x.Comment})
			}
		}
	case "companies":
		var list []crm.Company
		if list, err = s.CRM.Companies(r.Context(), sess.UserID, q.Get("q")); err == nil {
			head = []string{"crm.f.title", "crm.f.phone", "crm.f.email", "crm.f.website", "crm.f.address", "crm.f.company_type", "crm.f.industry", "crm.f.responsible", "crm.f.created", "crm.f.comment"}
			for _, c := range list {
				rows = append(rows, []string{c.Title, c.Phone, c.Email, c.Website, c.Address, c.CompanyType.Text(tr), c.Industry.Text(tr), c.Responsible.Name, when(c.CreatedAt), c.Comment})
			}
		}
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/"+kind)
		return
	}
	for i, k := range head {
		head[i] = tr(k)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+crm.ExportName(kind, time.Now()))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	_ = cw.WriteAll(rows)
}

// crmImport makes contacts, companies or leads from an uploaded CSV (who may create them).
func (s *Server) crmImport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind := r.PathValue("kind")
	entity := crmEntities[kind]
	back := "/crm/" + kind
	if entity == "" || entity == "deal" {
		http.NotFound(w, r)
		return
	}
	err := s.CRM.Check(r.Context(), sess.UserID, entity, "write", sess.UserID)
	var table [][]string
	if err == nil {
		file, _, ferr := r.FormFile("file")
		if ferr != nil {
			err = i18n.E("err.crm_csv_empty")
		} else {
			defer file.Close()
			table, err = crm.ReadCSV(file)
		}
	}
	var res crm.ImportResult
	if err == nil {
		res, err = s.CRM.Import(r.Context(), sess.UserID, entity, table, r.FormValue("skip_dup") != "", func(k string) string { return s.tr(r, k) })
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	msg := s.tr(r, "crm.import_done", res.Created, res.Skipped)
	for i, e := range res.Errors {
		if i == 5 {
			msg += " …"
			break
		}
		msg += " · " + s.tr(r, "crm.import_row", e.Row) + ": " + s.I18n.Err(s.lang(r), e.Err)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// crmMerge joins another contact or company into this one (who may change this one and delete the other).
func (s *Server) crmMerge(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id, other := r.PathValue("kind"), r.PathValue("id"), r.FormValue("other")
	back := "/crm/" + kind + "/" + id
	err := s.crmCheck(r, sess, kind, id, "write")
	if err == nil {
		err = s.crmCheck(r, sess, kind, other, "delete")
	}
	if err == nil {
		err = s.CRM.Merge(r.Context(), sess.UserID, crmEntities[kind], id, other)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.merged")), http.StatusSeeOther)
}
