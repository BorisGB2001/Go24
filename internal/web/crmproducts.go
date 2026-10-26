package web

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/i18n"
)

// ---------- «Товары» of the CRM: the catalog (/crm/products) and the product rows of a deal or a lead ----------

// formNumber reads a number as people type it: "1 500,50".
func formNumber(v string) float64 {
	n, _ := strconv.ParseFloat(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(v), " ", ""), ",", "."), 64)
	return n
}

// formVAT reads a VAT rate: empty is "no VAT".
func formVAT(v string) *float64 {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	n := formNumber(v)
	return &n
}

// crmProducts is the catalog: everyone sees it, administrators keep it. ?section= shows a section with its
// subsections, as the catalog does, ?type= — only the products or the services; a product's variations
// are lines under it, its stock is theirs.
func (s *Server) crmProducts(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q, section, kind := r.URL.Query().Get("q"), r.URL.Query().Get("section"), r.URL.Query().Get("type")
	if kind != crm.KindProduct && kind != crm.KindService {
		kind = ""
	}
	sections, err := s.CRM.Sections(r.Context())
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	var cur *crm.Section
	for i := range sections {
		if sections[i].ID == section {
			cur = &sections[i]
		}
	}
	if cur == nil {
		section = ""
	}
	list, err := s.CRM.Catalog(r.Context(), q, section, kind, sess.Admin)
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	data := map[string]any{"Kind": "products", "Items": list, "Q": q, "Edit": r.URL.Query().Get("edit"), "Sections": sections,
		"Section": cur, "Crumbs": sectionCrumbs(sections, section), "PType": kind,
		"New": &crm.Product{SectionID: section, VATIncluded: true, Kind: kind}}
	stock, err := s.CRM.StockTotals(r.Context()) // «Остаток» of «Складской учёт»
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	for _, p := range list {
		for _, v := range p.Variants {
			stock[p.ID] += stock[v.ID]
		}
	}
	data["Stock"] = stock
	data["Purchase"] = s.stockRights(r, sess).PurchasePrice // the purchase prices: administrators and the roles of «Складской учёт» that see them
	if err = s.catalogDicts(r, data); err == nil {
		err = s.crmTabs(r, sess, data)
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm")
		return
	}
	s.render(w, r, "crmproducts", page{TitleKey: "crm.products", Nav: "crm_products", Data: data})
}

// crmProductSave creates (id "new") or changes a product of the catalog (administrators).
func (s *Server) crmProductSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), "/crm/products")
		return
	}
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPhotoSize+1<<20)
	back := "/crm/products"
	if sec := r.FormValue("section"); sec != "" {
		back += "?section=" + url.QueryEscape(sec)
	}
	id, err := s.CRM.SaveProduct(r.Context(), sess.UserID, crm.Product{ID: id, Name: r.FormValue("name"), SKU: r.FormValue("sku"),
		Currency: r.FormValue("currency"), Unit: r.FormValue("unit"), Description: r.FormValue("description"), Price: formNumber(r.FormValue("price")),
		PurchasePrice: formVAT(r.FormValue("purchase_price")), VATRate: formVAT(r.FormValue("vat")), VATIncluded: r.FormValue("vat_included") != "",
		Active: id == "" || r.FormValue("active") != "", SectionID: r.FormValue("section"), Props: formProps(r),
		Barcodes: formBarcodes(r.FormValue("barcodes")), ParentID: r.FormValue("parent"), Kind: r.FormValue("kind")})
	if err == nil && r.FormValue("photo_delete") != "" {
		_, err = s.CRM.SetProductPhoto(r.Context(), id, "", "")
	}
	if err == nil {
		err = s.crmProductPhoto(r, sess, id)
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	sep := "?"
	if strings.Contains(back, "?") {
		sep = "&"
	}
	http.Redirect(w, r, back+sep+"msg="+url.QueryEscape(s.tr(r, "crm.saved")), http.StatusSeeOther)
}

// crmProductPhoto stores the product's picture sent with its form, if any (the formats of employee photos).
func (s *Server) crmProductPhoto(r *http.Request, sess auth.Session, id string) error {
	f, hdr, err := r.FormFile("photo")
	if err == http.ErrMissingFile || (err == nil && hdr.Size == 0) {
		return nil
	}
	if err != nil || hdr.Size > maxPhotoSize {
		return i18n.E("err.photo")
	}
	defer f.Close()
	return s.storeProductPhoto(r.Context(), sess.UserID, id, f, hdr.Size)
}

// storeProductPhoto puts a picture of the given size into the storage as the product's one.
func (s *Server) storeProductPhoto(ctx context.Context, userID, id string, f io.Reader, size int64) error {
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ct := http.DetectContentType(head[:n])
	ext, ok := photoTypes[ct]
	if !ok {
		return i18n.E("err.photo")
	}
	key := "go24/crm/products/" + id + "/" + randomHex(8) + ext
	if err := s.Storages.Put(ctx, userID, false, "", key, io.MultiReader(bytes.NewReader(head[:n]), f), size, ct); err != nil {
		return err
	}
	_, err := s.CRM.SetProductPhoto(ctx, id, key, ct)
	return err
}

// crmProductPhotoFile serves a product's picture to signed-in users.
func (s *Server) crmProductPhotoFile(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	p, err := s.CRM.Product(r.Context(), r.PathValue("id"))
	if err != nil || p.PhotoKey == "" {
		http.NotFound(w, r)
		return
	}
	obj, err := s.Storages.Open(r.Context(), nil, p.PhotoKey)
	if err != nil {
		s.fail(w, "open product photo", err)
		return
	}
	defer obj.Close()
	w.Header().Set("Content-Type", p.PhotoType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400") // the URL changes with every new picture
	_, _ = io.Copy(w, obj)
}

// sectionCrumbs are the sections from the top down to the given one (none for the root).
func sectionCrumbs(list []crm.Section, id string) []crm.Section {
	byID := make(map[string]crm.Section, len(list))
	for _, x := range list {
		byID[x.ID] = x
	}
	var out []crm.Section
	for x, ok := byID[id]; ok && len(out) < 100; x, ok = byID[x.ParentID] {
		out = append([]crm.Section{x}, out...)
	}
	return out
}

// crmSectionSave creates (id "new") or changes a section of the catalog (administrators).
func (s *Server) crmSectionSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/products"
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	id := r.PathValue("id")
	if id == "new" {
		id = ""
	}
	sort, _ := strconv.Atoi(r.FormValue("sort"))
	id, err := s.CRM.SaveSection(r.Context(), crm.Section{ID: id, ParentID: r.FormValue("parent"), Name: r.FormValue("name"), Sort: sort})
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"?section="+url.QueryEscape(id), http.StatusSeeOther)
}

// crmSectionDelete removes a section; its products and subsections go to its parent.
func (s *Server) crmSectionDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error = i18n.E("err.forbidden")
	if sess.Admin {
		err = s.CRM.DeleteSection(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/products")
		return
	}
	back := "/crm/products"
	if p := r.FormValue("parent"); p != "" {
		back += "?section=" + url.QueryEscape(p)
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) crmProductDelete(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var err error = i18n.E("err.forbidden")
	if sess.Admin {
		err = s.CRM.DeleteProduct(r.Context(), r.PathValue("id"))
	}
	if err != nil {
		s.taskErr(w, r, err, "/crm/products")
		return
	}
	http.Redirect(w, r, "/crm/products", http.StatusSeeOther)
}

// crmProductRows saves the product rows of a deal or a lead from the card's table: the rows come as parallel
// fields; a row without a name and product is skipped (the empty one for adding).
func (s *Server) crmProductRows(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	kind, id := r.PathValue("kind"), r.PathValue("id")
	back := "/crm/" + kind + "/" + id
	err := s.crmCheck(r, sess, kind, id, "write")
	if err == nil {
		err = r.ParseForm()
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	rows := formRows(r)
	if _, err := s.CRM.SetProductRows(r.Context(), sess.UserID, crmEntities[kind], id, rows); err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	http.Redirect(w, r, back+"#products", http.StatusSeeOther)
}

// formReserve: a row's reserve; empty — the whole quantity.
func formReserve(v string) float64 {
	if strings.TrimSpace(v) == "" {
		return -1
	}
	return formNumber(v)
}

// formRows reads the product rows of a card's table: parallel fields; a row without a name and product is skipped
// (the empty one for adding).
func formRows(r *http.Request) []crm.ProductRow {
	f := r.PostForm
	var rows []crm.ProductRow
	at := func(k string, i int) string {
		if i < len(f[k]) {
			return f[k][i]
		}
		return ""
	}
	for i := range f["name"] {
		name, product := strings.TrimSpace(at("name", i)), at("product", i)
		if name == "" && product == "" {
			continue
		}
		rows = append(rows, crm.ProductRow{ProductID: product, Name: name, Unit: at("unit", i), Price: formNumber(at("price", i)),
			Quantity: formNumber(at("quantity", i)), Discount: formNumber(at("discount", i)), VATRate: formVAT(at("vat", i)),
			VATIncluded: at("vat_included", i) != "0", WarehouseID: at("warehouse", i), Reserve: formReserve(at("reserve", i)),
			ReserveUntil: formDay(at("reserve_until", i))})
	}
	return rows
}

// crmProductsExport gives the catalog (a section, the search as on the page) as a CSV file Excel opens, as «Экспорт»
// of the catalog: the products with their variations, the purchase price for those who see it (administrators, the roles of «Складской учёт»).
func (s *Server) crmProductsExport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	fields, props, rows, err := s.CRM.ExportProducts(r.Context(), q.Get("q"), q.Get("section"), s.stockRights(r, sess).PurchasePrice, func(key string) string { return s.tr(r, key) })
	if err != nil {
		s.taskErr(w, r, err, "/crm/products")
		return
	}
	head := make([]string, 0, len(fields)+len(props))
	for _, f := range fields {
		head = append(head, s.tr(r, "crm.pcsv."+f))
	}
	for _, p := range props {
		head = append(head, p.Name)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename="+crm.ExportName("products", time.Now()))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF}) // the BOM: Excel reads the file as UTF-8
	cw := csv.NewWriter(w)
	cw.Comma = ';'
	_ = cw.Write(head)
	_ = cw.WriteAll(rows)
}

// crmProductsImport changes and makes the catalog's products from an uploaded CSV (administrators), as «Импорт»
// of the catalog; the columns are known by their headers in any language of go24.
func (s *Server) crmProductsImport(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	back := "/crm/products"
	if !sess.Admin {
		s.taskErr(w, r, i18n.E("err.forbidden"), back)
		return
	}
	var table [][]string
	file, _, err := r.FormFile("file")
	if err != nil {
		err = i18n.E("err.crm_csv_empty")
	} else {
		defer file.Close()
		table, err = crm.ReadCSV(file)
	}
	var res crm.ImportResult
	if err == nil {
		res, err = s.CRM.ImportProducts(r.Context(), sess.UserID, table, r.FormValue("update") != "", func(key string) []string {
			var out []string
			for _, l := range s.I18n.Langs() {
				out = append(out, s.I18n.T(l.Code, key))
			}
			return out
		})
	}
	if err != nil {
		s.taskErr(w, r, err, back)
		return
	}
	msg := s.tr(r, "crm.pcsv.done", res.Created, res.Updated, res.Skipped)
	for i, e := range res.Errors {
		if i == 5 {
			msg += " …"
			break
		}
		msg += " · " + s.tr(r, "crm.import_row", e.Row) + ": " + s.I18n.Err(s.lang(r), e.Err)
	}
	http.Redirect(w, r, back+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}
