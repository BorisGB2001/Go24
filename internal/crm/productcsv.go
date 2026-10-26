package crm

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «Экспорт» and «Импорт» of the catalog: the products go out as a table (a product, then its
// variations), with their sections' paths, prices, VAT, barcodes, stock and properties; a table comes back in — a
// row finds its product by ID, SKU or name and changes it, or makes a new one, its section made by the path
// («Оборудование / Принтеры») when there is none. A column that is not in the table keeps the product's value.

// ProductFields are the catalog table's columns in their order (the properties follow, by their names); a column's
// header is the locale's "crm.pcsv."+field.
var ProductFields = []string{"id", "name", "kind", "sku", "section", "parent", "price", "currency", "purchase_price", "unit", "vat",
	"vat_included", "active", "barcodes", "stock", "description"}

// ExportProducts is the catalog as a table: the fields of its columns (without the purchase price unless purchase),
// the properties after them, and a row per product, its variations after it (tr gives the texts of a yes, "crm.pcsv.yes",
// and of the types, "crm.kind."+kind; numbers with a decimal comma, as Excel reads them in Russian).
func (s *Store) ExportProducts(ctx context.Context, q, section string, purchase bool, tr func(key string) string) ([]string, []Prop, [][]string, error) {
	fields := slices.DeleteFunc(slices.Clone(ProductFields), func(f string) bool { return f == "purchase_price" && !purchase })
	props, err := s.ProductProps(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	list, err := s.Catalog(ctx, q, section, "", true)
	if err != nil {
		return nil, nil, nil, err
	}
	stock, err := s.StockTotals(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	num := func(v float64) string { return strings.ReplaceAll(strconv.FormatFloat(v, 'f', -1, 64), ".", ",") }
	opt := func(v *float64) string {
		if v == nil {
			return ""
		}
		return num(*v)
	}
	yes := tr("crm.pcsv.yes")
	flag := func(b bool) string {
		if b {
			return yes
		}
		return ""
	}
	var rows [][]string
	add := func(p, parent Product) {
		var row []string
		for _, f := range fields {
			v := ""
			switch f {
			case "id":
				v = p.ID
			case "name":
				v = p.Name
			case "kind":
				v = tr("crm.kind." + p.Kind)
			case "sku":
				v = p.SKU
			case "section":
				v = p.SectionPath
			case "parent":
				if parent.ID != "" {
					v = parent.Name
					if parent.SKU != "" {
						v = parent.SKU
					}
				}
			case "price":
				v = num(p.Price)
			case "currency":
				v = p.Currency
			case "purchase_price":
				v = opt(p.PurchasePrice)
			case "unit":
				v = p.Unit
			case "vat":
				v = opt(p.VATRate)
			case "vat_included":
				v = flag(p.VATIncluded)
			case "active":
				v = flag(p.Active)
			case "barcodes":
				v = strings.Join(p.Barcodes, ", ")
			case "stock":
				if !p.Service() {
					v = num(stock[p.ID])
				}
			case "description":
				v = p.Description
			}
			row = append(row, v)
		}
		for _, pr := range props {
			v := p.Props[pr.ID]
			if pr.Kind == PropBool {
				v = flag(v == "1")
			}
			row = append(row, v)
		}
		rows = append(rows, row)
	}
	for _, p := range list {
		add(p, Product{})
		for _, v := range p.Variants {
			add(v, p)
		}
	}
	return fields, props, rows, nil
}

// ImportProducts changes or makes the products of the table's rows: a row is the product with its ID, else the one
// with its SKU, else the one with its name (a variation: among its product's variations); update false skips the rows
// of existing products. «Раздел» is a path of sections, made when missing; «Товар» of a variation is its product's SKU
// or name. names gives a locale key's texts in every language: a column is a field by its header in any of them (or
// by the field's code), else a property by its name; a yes is "1", "+", "true" or "crm.pcsv.yes" in any language, a type
// is its code or "crm.kind."+kind in any language (empty — as it was, a product for a new one).
func (s *Store) ImportProducts(ctx context.Context, userID string, table [][]string, update bool, names func(key string) []string) (ImportResult, error) {
	var res ImportResult
	if len(table) < 2 {
		return res, i18n.E("err.crm_csv_empty")
	}
	if len(table) > 10001 {
		return res, i18n.E("err.crm_csv_big")
	}
	props, err := s.ProductProps(ctx)
	if err != nil {
		return res, err
	}
	key := func(v string) string { return strings.ToLower(strings.Join(strings.Fields(v), " ")) }
	headers := map[string]string{} // a header → a field, or "prop:" and a property's id
	for _, f := range ProductFields {
		headers[f] = f
		for _, n := range names("crm.pcsv." + f) {
			headers[key(n)] = f
		}
	}
	for _, pr := range props {
		if _, ok := headers[key(pr.Name)]; !ok {
			headers[key(pr.Name)] = "prop:" + pr.ID
		}
	}
	cols := map[int]string{}
	for i, h := range table[0] {
		if f := headers[key(h)]; f != "" && f != "stock" {
			cols[i] = f
		}
	}
	if !slices.Contains(mapValues(cols), "name") && !slices.Contains(mapValues(cols), "sku") && !slices.Contains(mapValues(cols), "id") {
		return res, i18n.E("err.crm_csv_columns")
	}
	yes := []string{"1", "+", "true", "yes"}
	for _, n := range names("crm.pcsv.yes") {
		yes = append(yes, key(n))
	}
	kinds := map[string]string{}
	for _, k := range []string{KindProduct, KindService} {
		kinds[k] = k
		for _, n := range names("crm.kind." + k) {
			kinds[key(n)] = k
		}
	}
	sections, err := s.Sections(ctx)
	if err != nil {
		return res, err
	}
	paths := map[string]string{} // a section's path → its id
	for _, x := range sections {
		paths[key(x.Path)] = x.ID
	}
	sectionOf := func(path string) (string, error) {
		id := ""
		var parts []string
		for _, name := range strings.Split(path, "/") {
			if name = strings.TrimSpace(name); name == "" {
				continue
			}
			parts = append(parts, name)
			k := key(strings.Join(parts, " / "))
			if paths[k] == "" {
				x, err := s.SaveSection(ctx, Section{Name: name, ParentID: id})
				if err != nil {
					return "", err
				}
				paths[k] = x
			}
			id = paths[k]
		}
		return id, nil
	}
	for n, rec := range table[1:] {
		if strings.TrimSpace(strings.Join(rec, "")) == "" {
			continue
		}
		v := map[string]string{}
		for i, f := range cols {
			if i < len(rec) {
				v[f] = strings.TrimSpace(rec[i])
			}
		}
		has := func(f string) bool { _, ok := v[f]; return ok }
		p, found, err := s.importedProduct(ctx, v)
		if err == nil && found && !update {
			res.Skipped++
			continue
		}
		if err == nil && !found {
			p = Product{Active: true, VATIncluded: true}
			if v["parent"] != "" {
				p.ParentID, err = s.productByRef(ctx, v["parent"])
			}
		}
		if err == nil && has("section") && p.ParentID == "" {
			p.SectionID, err = sectionOf(v["section"])
		}
		if err == nil {
			err = fillImported(&p, v, has, func(s string) bool { return slices.Contains(yes, key(s)) })
		}
		if err == nil && v["kind"] != "" {
			if p.Kind = kinds[key(v["kind"])]; p.Kind == "" {
				err = i18n.E("err.crm_product_kind")
			}
		}
		if err == nil {
			for f, val := range v {
				if id, ok := strings.CutPrefix(f, "prop:"); ok {
					if p.Props == nil {
						p.Props = map[string]string{}
					}
					if i := slices.IndexFunc(props, func(pr Prop) bool { return pr.ID == id }); i >= 0 && props[i].Kind == PropBool {
						val = map[bool]string{true: "1", false: ""}[slices.Contains(yes, key(val))]
					}
					p.Props[id] = val
				}
			}
			_, err = s.SaveProduct(ctx, userID, p)
		}
		if err != nil {
			var ie *i18n.Error
			if errors.Is(err, ErrNotFound) {
				ie = i18n.E("err.crm_pcsv_parent")
			} else if !errors.As(err, &ie) {
				return res, err
			}
			if len(res.Errors) < 50 {
				res.Errors = append(res.Errors, ImportError{n + 2, ie})
			}
			continue
		}
		if found {
			res.Updated++
		} else {
			res.Created++
		}
	}
	return res, nil
}

// importedProduct finds the product of an imported row: by its ID, SKU or name (a variation's — among its product's).
func (s *Store) importedProduct(ctx context.Context, v map[string]string) (Product, bool, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM crm_products WHERE ($1 <> '' AND id::text = $1)
			OR ($2 <> '' AND lower(sku) = lower($2))
			OR ($2 = '' AND $3 <> '' AND lower(name) = lower($3) AND CASE WHEN $4 = '' THEN parent_id IS NULL
				ELSE parent_id = (SELECT o.id FROM crm_products o WHERE o.parent_id IS NULL AND (lower(o.sku) = lower($4) OR lower(o.name) = lower($4))
					ORDER BY lower(o.sku) = lower($4) DESC LIMIT 1) END)
		ORDER BY id::text = $1 DESC, lower(sku) = lower($2) DESC LIMIT 1`, v["id"], v["sku"], v["name"], v["parent"]).Scan(&id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = nil
		}
		return Product{}, false, err
	}
	p, err := s.Product(ctx, id)
	return p, err == nil, err
}

// productByRef is the product (not a variation) with the SKU or, else, the name.
func (s *Store) productByRef(ctx context.Context, ref string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id::text FROM crm_products WHERE parent_id IS NULL AND (lower(sku) = lower($1) OR lower(name) = lower($1))
		ORDER BY lower(sku) = lower($1) DESC LIMIT 1`, ref).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	return id, err
}

// fillImported puts a row's values into the product: the columns the table has (has), an empty one clearing it.
func fillImported(p *Product, v map[string]string, has func(string) bool, yes func(string) bool) error {
	number := func(f string) (*float64, error) {
		t := strings.TrimSuffix(strings.NewReplacer(" ", "", " ", "", ",", ".").Replace(v[f]), "%")
		if t == "" {
			return nil, nil
		}
		n, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return nil, i18n.E("err.crm_amount")
		}
		return &n, nil
	}
	for f, to := range map[string]*string{"name": &p.Name, "sku": &p.SKU, "currency": &p.Currency, "unit": &p.Unit, "description": &p.Description} {
		if has(f) {
			*to = v[f]
		}
	}
	var err error
	if has("price") {
		var n *float64
		if n, err = number("price"); n != nil {
			p.Price = *n
		} else if err == nil {
			p.Price = 0
		}
	}
	if err == nil && has("purchase_price") {
		p.PurchasePrice, err = number("purchase_price")
	}
	if err == nil && has("vat") {
		p.VATRate, err = number("vat")
	}
	if has("vat_included") {
		p.VATIncluded = yes(v["vat_included"])
	}
	if has("active") {
		p.Active = yes(v["active"])
	}
	if has("barcodes") {
		p.Barcodes = strings.FieldsFunc(v["barcodes"], func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' })
	}
	return err
}

func mapValues(m map[int]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
