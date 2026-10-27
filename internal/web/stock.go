package web

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Складской учёт»: the stock documents (/store/docs), the warehouses and the stock ----------

// stockRights are the person's rights in the section (none when they can't be read).
func (s *Server) stockRights(r *http.Request, sess auth.Session) crm.StockRights {
	rights, err := s.CRM.StockRightsOf(r.Context(), sess.UserID, sess.Admin)
	if err != nil {
		s.Log.Error("stock rights", "err", err)
	}
	return rights
}

// stockData is what every page of the section has: its tab and the person's rights.
func (s *Server) stockData(r *http.Request, sess auth.Session, tab string) map[string]any {
	return map[string]any{"Tab": tab, "Rights": s.stockRights(r, sess), "Admin": sess.Admin, "Kinds": crm.StockKinds, "Statuses": crm.StockStatuses}
}

// stockWarehousesOf are the warehouses a person's documents may use.
func stockWarehousesOf(rights crm.StockRights, list []crm.Warehouse) []crm.Warehouse {
	if !rights.Limited() {
		return list
	}
	var out []crm.Warehouse
	for _, w := range list {
		if rights.Warehouse(w.ID) {
			out = append(out, w)
		}
	}
	return out
}

// stockDocs is the list of documents, by kind and status, found by number, title, incoming number or supplier.
func (s *Server) stockDocs(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	data := s.stockData(r, sess, "docs")
	rights := data["Rights"].(crm.StockRights)
	f := crm.StockFilter{Kind: q.Get("kind"), Status: q.Get("status"), Q: q.Get("q"), ProductID: q.Get("product"),
		Kinds: rights.Kinds("read"), Warehouses: rights.WarehouseIDs()}
	list, err := s.CRM.StockDocs(r.Context(), f)
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	data["Items"], data["F"] = list, f
	s.render(w, r, "stock", page{TitleKey: "stock.docs", Nav: "store_docs", Data: data})
}

// stockDocPage is a document (id "new" with ?kind= — a new one): its details and rows; a conducted one is read only.
func (s *Server) stockDocPage(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	data := s.stockData(r, sess, "docs")
	rights := data["Rights"].(crm.StockRights)
	var d crm.StockDoc
	var err error
	if id == "new" {
		d = crm.StockDoc{Kind: r.URL.Query().Get("kind"), Status: "draft", Date: time.Now(), Responsible: crm.Ref{ID: sess.UserID}}
		if src := r.URL.Query().Get("copy"); src != "" { // «Копировать»: the document's details and rows in a new draft
			var c crm.StockDoc
			if c, err = s.CRM.StockDoc(r.Context(), src); err == nil && !rights.Doc(c, "read") {
				err = crm.ErrNotFound
			}
			d.Kind, d.Title, d.Supplier, d.SupplierKind, d.Reason = c.Kind, c.Title, c.Supplier, c.SupplierKind, c.Reason
			d.Comment, d.Currency, d.Rows = c.Comment, c.Currency, c.Rows
		}
		if err == nil && !crm.ValidStockKind(d.Kind) {
			err = crm.ErrNotFound
		} else if err == nil && !rights.Can(d.Kind, "write") {
			err = i18n.E("err.forbidden")
		}
	} else if d, err = s.CRM.StockDoc(r.Context(), id); err == nil && !rights.Doc(d, "read") {
		err = crm.ErrNotFound
	}
	if err == nil {
		var whs []crm.Warehouse
		whs, err = s.CRM.Warehouses(r.Context())
		data["Warehouses"] = stockWarehousesOf(rights, whs)
	}
	if err == nil {
		data["Catalog"], err = s.CRM.Goods(r.Context(), "", "", false)
	}
	if err == nil {
		data["Users"], err = s.Tasks.ListUsers(r.Context())
	}
	if err == nil {
		data["Currencies"], err = s.CRM.Currencies(r.Context())
	}
	if err == nil && d.Kind == "arrival" {
		data["Suppliers"], err = s.CRM.Companies(r.Context(), sess.UserID, "")
		if err == nil {
			data["SupplierContacts"], err = s.CRM.Contacts(r.Context(), sess.UserID, "", "")
		}
	}
	if err != nil {
		s.taskErr(w, r, err, "/store/docs")
		return
	}
	data["Doc"], data["ID"] = d, id
	write := rights.Doc(d, "write")
	if id == "new" {
		write = rights.Can(d.Kind, "write")
	}
	data["CanWrite"] = write && d.Editable()
	data["CanConduct"] = d.Status != "conducted" && rights.Doc(d, "conduct")
	data["CanCancel"] = d.Status == "conducted" && rights.Doc(d, "cancel")
	data["CanDelete"] = id != "new" && d.Editable() && rights.Doc(d, "delete")
	data["CanCopy"] = id != "new" && rights.Can(d.Kind, "write")
	title := s.tr(r, "stock.kind."+d.Kind)
	if d.Number > 0 {
		title += " № " + strconv.Itoa(d.Number)
	}
	s.render(w, r, "stockdoc", page{Title: title, Nav: "store_docs", Data: data})
}

// stockDocAction: "save" the document (id "new" — makes it), "conduct" it, "cancel" the conducting, "delete" it.
func (s *Server) stockDocAction(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/store/docs/" + id
	if id == "new" {
		back = "/store/docs/new?kind=" + url.QueryEscape(r.FormValue("kind"))
	}
	rights := s.stockRights(r, sess)
	stored := id
	if id == "new" {
		stored = ""
	}
	var err error
	switch op := r.FormValue("op"); op {
	case "save", "save_conduct":
		if err = r.ParseForm(); err != nil {
			break
		}
		rows := formStockRows(r)
		if err = s.CRM.CheckStockDoc(r.Context(), rights, stored, r.FormValue("kind"), "write", rows); err == nil && op == "save_conduct" {
			err = s.CRM.CheckStockDoc(r.Context(), rights, stored, r.FormValue("kind"), "conduct", nil)
		}
		if err != nil {
			break
		}
		d := crm.StockDoc{Kind: r.FormValue("kind"), Title: r.FormValue("title"), Supplier: crm.Ref{ID: r.FormValue("supplier")},
			IncomingNumber: r.FormValue("incoming_number"), IncomingDate: formDay(r.FormValue("incoming_date")), Reason: r.FormValue("reason"),
			Comment: r.FormValue("comment"), Currency: r.FormValue("currency"), Responsible: crm.Ref{ID: r.FormValue("responsible")},
			Rows: rows}
		if id != "new" {
			d.ID = id
		}
		if c, ok := strings.CutPrefix(d.Supplier.ID, "contact:"); ok { // the supplier is a contact
			d.Supplier.ID, d.SupplierKind = c, "contact"
		}
		if t := formDay(r.FormValue("date")); t != nil {
			d.Date = *t
		}
		if id, err = s.CRM.SaveStockDoc(r.Context(), sess.UserID, d); err == nil {
			back = "/store/docs/" + id
			if r.FormValue("op") == "save_conduct" {
				err = s.CRM.ConductStockDoc(r.Context(), sess.UserID, id)
			}
		}
	case "conduct":
		if err = s.CRM.CheckStockDoc(r.Context(), rights, stored, "", "conduct", nil); err == nil {
			err = s.CRM.ConductStockDoc(r.Context(), sess.UserID, id)
		}
	case "cancel":
		if err = s.CRM.CheckStockDoc(r.Context(), rights, stored, "", "cancel", nil); err == nil {
			err = s.CRM.CancelStockDoc(r.Context(), sess.UserID, id)
		}
	case "delete":
		if err = s.CRM.CheckStockDoc(r.Context(), rights, stored, "", "delete", nil); err != nil {
			break
		}
		if err = s.CRM.DeleteStockDoc(r.Context(), id); err == nil {
			http.Redirect(w, r, "/store/docs", http.StatusSeeOther)
			return
		}
	default:
		err = i18n.E("err.bad_form")
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// stockDocsBulk: the group action on the checked documents of the list — "conduct" them (the earliest first, so an
// arrival comes before what takes from it), "cancel" the conducting (the latest first) or "delete" them; each by the
// person's rights on it, the ones that can't be done are skipped.
func (s *Server) stockDocsBulk(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/store/docs"
	if b := r.FormValue("back"); strings.HasPrefix(b, "/store/docs") {
		back = b
	}
	if err := r.ParseForm(); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	rights, op := s.stockRights(r, sess), r.FormValue("op")
	if op != "conduct" && op != "cancel" && op != "delete" {
		s.taskErr(w, r, i18n.E("err.bad_form"), back)
		return
	}
	var docs []crm.StockDoc
	skipped := 0
	for _, id := range r.PostForm["id"] {
		if d, err := s.CRM.StockDoc(r.Context(), id); err == nil && rights.Doc(d, "read") {
			docs = append(docs, d)
		} else {
			skipped++
		}
	}
	sort.SliceStable(docs, func(i, j int) bool {
		a, b := docs[i], docs[j]
		if op == "cancel" {
			a, b = b, a
		}
		if !a.Date.Equal(b.Date) {
			return a.Date.Before(b.Date)
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	done := 0
	for _, d := range docs {
		err := s.CRM.CheckStockDoc(r.Context(), rights, d.ID, "", op, nil)
		if err == nil {
			switch op {
			case "conduct":
				err = s.CRM.ConductStockDoc(r.Context(), sess.UserID, d.ID)
			case "cancel":
				err = s.CRM.CancelStockDoc(r.Context(), sess.UserID, d.ID)
			default:
				err = s.CRM.DeleteStockDoc(r.Context(), d.ID)
			}
		}
		if err != nil {
			skipped++
			continue
		}
		done++
	}
	msg := s.tr(r, "crm.bulk_done", done)
	if skipped > 0 {
		msg += " " + s.tr(r, "stock.bulk_skipped", skipped)
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// formDay reads a date of a form; empty or wrong is none.
func formDay(v string) *time.Time {
	if t, err := time.ParseInLocation("2006-01-02", v, time.Local); err == nil {
		return &t
	}
	return nil
}

// formStockRows reads the rows of a document's table: parallel fields; a row without a product is skipped.
func formStockRows(r *http.Request) []crm.StockRow {
	f := r.PostForm
	at := func(k string, i int) string {
		if i < len(f[k]) {
			return f[k][i]
		}
		return ""
	}
	var rows []crm.StockRow
	for i := range f["product"] {
		if at("product", i) == "" {
			continue
		}
		rows = append(rows, crm.StockRow{ProductID: at("product", i), Quantity: formNumber(at("quantity", i)), Price: formNumber(at("price", i)),
			From: at("from", i), To: at("to", i)})
	}
	return rows
}

// stockWarehouses is the list of warehouses with what is on them.
func (s *Server) stockWarehouses(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	list, err := s.CRM.Warehouses(r.Context())
	data := s.stockData(r, sess, "warehouses")
	if err == nil && sess.Admin {
		data["Users"], err = s.Tasks.ListUsers(r.Context())
	}
	if err != nil {
		s.taskErr(w, r, err, "/store/docs")
		return
	}
	data["Items"], data["Edit"] = list, r.URL.Query().Get("edit")
	s.render(w, r, "stockwarehouses", page{TitleKey: "stock.warehouses", Nav: "store_warehouses", Data: data})
}

// stockWarehouseSave creates (id "new"), changes or deletes ("delete") a warehouse — administrators.
func (s *Server) stockWarehouseSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/store/warehouses"
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	id := r.PathValue("id")
	var err error
	if r.FormValue("op") == "delete" {
		err = s.CRM.DeleteWarehouse(r.Context(), id)
	} else {
		if id == "new" {
			id = ""
		}
		_, err = s.CRM.SaveWarehouse(r.Context(), crm.Warehouse{ID: id, Name: r.FormValue("name"), Address: r.FormValue("address"),
			Responsible: crm.Ref{ID: r.FormValue("responsible")}, Default: r.FormValue("default") != "", Active: id == "" || r.FormValue("active") != "",
			Sort: int(formNumber(r.FormValue("sort")))})
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// stockList is «Остатки»: the products of a section (with subsections) by warehouses; ?all=1 — also those that are nowhere.
func (s *Server) stockList(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	sections, err := s.CRM.Sections(r.Context())
	var list []crm.StockLine
	var whs []crm.Warehouse
	if err == nil {
		list, err = s.CRM.Stock(r.Context(), q.Get("q"), q.Get("section"), false, q.Get("all") != "1")
	}
	if err == nil {
		whs, err = s.CRM.Warehouses(r.Context())
	}
	if err != nil {
		s.taskErr(w, r, err, "/store/docs")
		return
	}
	data := s.stockData(r, sess, "stock")
	data["Items"], data["Warehouses"], data["Sections"] = list, whs, sections
	data["Q"], data["Section"], data["All"] = q.Get("q"), q.Get("section"), q.Get("all") == "1"
	s.render(w, r, "stocklist", page{TitleKey: "stock.stock", Nav: "store_stock", Data: data})
}

// crmDealStock: «Складской учёт» of a deal's card, once there are warehouses — the rows' warehouses and reserves
// with what is available there, the deal's shipments, and a won deal with products not shipped yet.
func (s *Server) crmDealStock(r *http.Request, sess auth.Session, data map[string]any, id string, rows []crm.ProductRow) error {
	whs, err := s.CRM.Warehouses(r.Context())
	if err != nil || len(whs) == 0 {
		return err
	}
	var ids []string
	for _, x := range rows {
		if x.ProductID != "" {
			ids = append(ids, x.ProductID)
		}
	}
	st := map[string]any{"Warehouses": whs}
	if len(ids) > 0 {
		free, err := s.CRM.Available(r.Context(), ids, "")
		if err != nil {
			return err
		}
		for i, x := range rows {
			if x.ProductID != "" && x.WarehouseID != "" {
				n := free[x.ProductID][x.WarehouseID]
				rows[i].Free = &n
			}
		}
	}
	data["Stock"] = st
	data["Shipments"], err = s.CRM.StockDocs(r.Context(), crm.StockFilter{DealID: id})
	if err != nil {
		return err
	}
	data["Keeper"] = s.stockRights(r, sess).Can("shipment", "write")
	if d, ok := data["Item"].(crm.Deal); ok && d.Stage.Semantic == "success" && len(ids) > 0 {
		var left float64
		if left, err = s.CRM.Unshipped(r.Context(), id); left > 0 {
			data["Unshipped"] = left
		}
	}
	return err
}

// crmDealShipment makes a «Реализация» of the deal's products not shipped yet and opens it — for those who keep
// the stock documents.
func (s *Server) crmDealShipment(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id := r.PathValue("id")
	back := "/crm/deals/" + id + "#shipments"
	err := s.crmCheck(r, sess, "deals", id, "read")
	if err == nil && !s.stockRights(r, sess).Can("shipment", "write") {
		err = i18n.E("err.forbidden")
	}
	var doc string
	if err == nil {
		doc, err = s.CRM.NewShipment(r.Context(), sess.UserID, id)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, "/store/docs/"+doc, http.StatusSeeOther)
}

// stockSettings is «Настройки» of the section (administrators): the reserves of deals' products and the shipping
// of a won deal.
func (s *Server) stockSettings(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !s.stockRights(r, sess).Settings {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/store/docs")
		return
	}
	set, err := s.CRM.StockSettings(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/store/docs")
		return
	}
	data := s.stockData(r, sess, "settings")
	data["Set"] = set
	s.render(w, r, "stocksettings", page{TitleKey: "stock.settings", Nav: "store_settings", Data: data})
}

func (s *Server) stockSettingsSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/store/settings"
	var err error = i18n.E("err.forbidden")
	if s.stockRights(r, sess).Settings {
		err = s.CRM.SetStockSettings(r.Context(), sess.UserID, crm.StockSettings{AutoReserve: r.FormValue("auto_reserve") != "",
			ReserveDays: int(formNumber(r.FormValue("reserve_days"))), ShipOnWin: r.FormValue("ship_on_win") != ""})
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}
