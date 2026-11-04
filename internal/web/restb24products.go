package web

import (
	"cmp"
	"errors"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"

	"go24/internal/crm"
	"go24/internal/i18n"
)

// REST compatible with the portal, the products of the CRM (GO24DEV-928): the catalog — crm.product.*,
// crm.productsection.*, crm.catalog.*, crm.measure.*, crm.vat.* — and the product rows of deals and leads —
// crm.deal.productrows.*, crm.lead.productrows.*, crm.productrow.* and crm.item.productrow.* (the same rows with
// camelCase names). Products, sections, units and warehouses have numbers (migration 851); a row's number is its id
// and stays while the row does. The catalog is kept by administrators, as on its pages; the rows — by those who may
// change the card. The rows' prices are those: PRICE is the price of one with the discount and the tax,
// PRICE_EXCLUSIVE — with the discount and without the tax, PRICE_NETTO — without both, PRICE_BRUTTO — with the tax and
// without the discount; go24 keeps the price as entered (with the tax or without it, TAX_INCLUDED) and the discount
// in percent, so an absolute discount (DISCOUNT_TYPE_ID 1) is kept as its percent.

func init() {
	product := func(run func(*Server, portalCall, portalEntity) (portalOut, error)) func(*Server, portalCall) (portalOut, error) {
		return func(s *Server, c portalCall) (portalOut, error) { return run(s, c, portalProduct) }
	}
	section := func(run func(*Server, portalCall, portalEntity) (portalOut, error)) func(*Server, portalCall) (portalOut, error) {
		return func(s *Server, c portalCall) (portalOut, error) { return run(s, c, portalProductSection) }
	}
	for name, m := range map[string]portalMethod{
		"crm.product.fields":         {Scope: "crm:read", Run: product((*Server).portalCRMFields)},
		"crm.product.list":           {Scope: "crm:read", Run: product((*Server).portalProductList)},
		"crm.product.get":            {Scope: "crm:read", Run: product((*Server).portalProductGet)},
		"crm.product.add":            {Scope: "crm:write", Run: (*Server).portalProductAdd},
		"crm.product.update":         {Scope: "crm:write", Run: (*Server).portalProductUpdate},
		"crm.product.delete":         {Scope: "crm:write", Run: (*Server).portalProductDelete},
		"crm.productsection.fields":  {Scope: "crm:read", Run: section((*Server).portalCRMFields)},
		"crm.productsection.list":    {Scope: "crm:read", Run: section((*Server).portalCRMList)},
		"crm.productsection.get":     {Scope: "crm:read", Run: section((*Server).portalProductGet)},
		"crm.productsection.add":     {Scope: "crm:write", Run: (*Server).portalSectionAdd},
		"crm.productsection.update":  {Scope: "crm:write", Run: (*Server).portalSectionUpdate},
		"crm.productsection.delete":  {Scope: "crm:write", Run: (*Server).portalSectionDelete},
		"crm.catalog.fields":         {Scope: "crm:read", Run: (*Server).portalCatalogFields},
		"crm.catalog.list":           {Scope: "crm:read", Run: (*Server).portalCatalogList},
		"crm.catalog.get":            {Scope: "crm:read", Run: (*Server).portalCatalogGet},
		"crm.measure.list":           {Scope: "crm:read", Run: (*Server).portalMeasureList},
		"crm.measure.get":            {Scope: "crm:read", Run: (*Server).portalMeasureGet},
		"crm.vat.list":               {Scope: "crm:read", Run: (*Server).portalVATList},
		"crm.vat.get":                {Scope: "crm:read", Run: (*Server).portalVATGet},
		"crm.productrow.fields":      {Scope: "crm:read", Run: (*Server).portalRowFields},
		"crm.productrow.list":        {Scope: "crm:read", Run: (*Server).portalRowList},
		"crm.item.productrow.fields": {Scope: "crm:read", Run: (*Server).portalItemRowFields},
		"crm.item.productrow.list":   {Scope: "crm:read", Run: (*Server).portalItemRowList},
		"crm.item.productrow.get":    {Scope: "crm:read", Run: (*Server).portalItemRowGet},
		"crm.item.productrow.add":    {Scope: "crm:write", Run: (*Server).portalItemRowAdd},
		"crm.item.productrow.update": {Scope: "crm:write", Run: (*Server).portalItemRowUpdate},
		"crm.item.productrow.delete": {Scope: "crm:write", Run: (*Server).portalItemRowDelete},
		"crm.item.productrow.set":    {Scope: "crm:write", Run: (*Server).portalItemRowSet},
	} {
		portalMethods[name] = m
	}
	for _, entity := range []string{"deal", "lead"} {
		portalMethods["crm."+entity+".productrows.get"] = portalMethod{Scope: "crm:read",
			Run: func(s *Server, c portalCall) (portalOut, error) { return s.portalCardRowsGet(c, entity) }}
		portalMethods["crm."+entity+".productrows.set"] = portalMethod{Scope: "crm:write",
			Run: func(s *Server, c portalCall) (portalOut, error) { return s.portalCardRowsSet(c, entity) }}
	}
}

// portalCatalogID: go24 has one catalog of products, the CATALOG_ID.
const portalCatalogID = "1"

// portalVATRates are the VAT rates of crm.vat.list, the rates the product's form offers; VAT_ID is the place from 1.
var portalVATRates = []float64{0, 5, 7, 10, 20}

// portalVATSQL is the SQL of VAT_ID of a VAT rate column; another rate has none.
func portalVATSQL(col string) string {
	q := "CASE " + col
	for i, r := range portalVATRates {
		q += " WHEN " + strconv.FormatFloat(r, 'f', -1, 64) + " THEN " + strconv.Itoa(i+1)
	}
	return "(" + q + " END)::bigint"
}

var portalProduct = portalEntity{Entity: "product", Kind: "products",
	From: `crm_products x LEFT JOIN crm_product_sections xs ON xs.id = x.section_id JOIN users uc ON uc.id = x.created_by`,
	Fields: []portalField{
		{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
		{Name: "CATALOG_ID", Type: "integer", Col: portalCatalogID + "::bigint", ReadOnly: true},
		{Name: "NAME", Type: "string", Col: "x.name", Required: true},
		{Name: "XML_ID", Type: "string", Col: "x.sku"},
		{Name: "ACTIVE", Type: "char", Col: "CASE WHEN x.active THEN 'Y' ELSE 'N' END"},
		{Name: "SECTION_ID", Type: "integer", Col: "xs.num"},
		{Name: "SORT", Type: "integer", Col: "500::bigint", ReadOnly: true},
		{Name: "TYPE", Type: "integer", Col: "CASE WHEN x.kind = 'service' THEN 7 WHEN x.parent_id IS NOT NULL THEN 4 ELSE 1 END::bigint"},
		{Name: "PRICE", Type: "double", Col: "x.price::float8"},
		{Name: "CURRENCY_ID", Type: "crm_currency", Col: "x.currency"},
		{Name: "VAT_ID", Type: "integer", Col: portalVATSQL("x.vat_rate")},
		{Name: "VAT_INCLUDED", Type: "char", Col: "CASE WHEN x.vat_included THEN 'Y' ELSE 'N' END"},
		{Name: "MEASURE", Type: "integer"}, // the unit is kept as its symbol: given apart (portalMeasures)
		{Name: "DESCRIPTION", Type: "string", Col: "x.description"},
		{Name: "DESCRIPTION_TYPE", Type: "string", Col: "'text'", ReadOnly: true},
		{Name: "DATE_CREATE", Type: "datetime", Col: "x.created_at", ReadOnly: true},
		{Name: "TIMESTAMP_X", Type: "datetime", Col: "x.created_at", ReadOnly: true},
		{Name: "CREATED_BY", Type: "user", Col: "uc.num", ReadOnly: true},
		{Name: "MODIFIED_BY", Type: "user", Col: "uc.num", ReadOnly: true},
	},
}

var portalProductSection = portalEntity{Entity: "productsection", Kind: "product-sections",
	From: `crm_product_sections x LEFT JOIN crm_product_sections xp ON xp.id = x.parent_id`,
	Fields: []portalField{
		{Name: "ID", Type: "integer", Col: "x.num", ReadOnly: true},
		{Name: "CATALOG_ID", Type: "integer", Col: portalCatalogID + "::bigint", ReadOnly: true},
		{Name: "SECTION_ID", Type: "integer", Col: "xp.num"},
		{Name: "NAME", Type: "string", Col: "x.name", Required: true},
		{Name: "SORT", Type: "integer", Col: "x.sort::bigint"},
		{Name: "XML_ID", Type: "string", Col: "''", ReadOnly: true},
		{Name: "CODE", Type: "string", Col: "''", ReadOnly: true},
	},
}

// ---------- units and VAT ----------

// portalUnit is a unit of measure with its number.
type portalUnit struct {
	unitView
	Num int64
}

func (s *Server) portalUnits(r *http.Request) ([]portalUnit, error) {
	list, err := s.units(r)
	if err != nil {
		return nil, err
	}
	nums := map[string]int64{}
	rows, err := s.DB.Query(r.Context(), `SELECT id::text, num FROM crm_units`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		nums[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]portalUnit, len(list))
	for i, u := range list {
		out[i] = portalUnit{u, nums[u.ID]}
	}
	return out, nil
}

// portalUnitOf is the unit of a product's or a row's unit (its symbol; "" — the default one).
func portalUnitOf(units []portalUnit, unit string) (portalUnit, bool) {
	for _, u := range units {
		if (unit == "" && u.Default) || (unit != "" && (strings.EqualFold(u.ShownSymbol, unit) || strings.EqualFold(u.Symbol, unit))) {
			return u, true
		}
	}
	return portalUnit{}, false
}

func portalMeasureOut(u portalUnit) map[string]any {
	code, _ := strconv.Atoi(u.Code)
	return map[string]any{"ID": strconv.FormatInt(u.Num, 10), "CODE": strconv.Itoa(code), "MEASURE_TITLE": u.ShownName,
		"SYMBOL_RUS": u.ShownSymbol, "SYMBOL_INTL": u.ShownSymbol, "SYMBOL_LETTER_INTL": u.Code, "IS_DEFAULT": portalYN(u.Default)}
}

// crm.measure.list: the units of «Единицы измерения».
func (s *Server) portalMeasureList(c portalCall) (portalOut, error) {
	units, err := s.portalUnits(c.r)
	if err != nil {
		return portalOut{}, err
	}
	out := []map[string]any{}
	for _, u := range units {
		out = append(out, portalMeasureOut(u))
	}
	return portalPaged(out, 0, len(out), len(out)), nil
}

func (s *Server) portalMeasureGet(c portalCall) (portalOut, error) {
	units, err := s.portalUnits(c.r)
	if err != nil {
		return portalOut{}, err
	}
	for _, u := range units {
		if strconv.FormatInt(u.Num, 10) == portalStr(portalParam(c.in, "id")) {
			return portalOut{Result: portalMeasureOut(u)}, nil
		}
	}
	return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
}

func portalVATOut(i int) map[string]any {
	r := strconv.FormatFloat(portalVATRates[i], 'f', -1, 64)
	return map[string]any{"ID": strconv.Itoa(i + 1), "ACTIVE": "Y", "C_SORT": strconv.Itoa((i + 1) * 100), "NAME": r + "%",
		"RATE": strconv.FormatFloat(portalVATRates[i], 'f', 2, 64)}
}

// crm.vat.list: the VAT rates.
func (s *Server) portalVATList(c portalCall) (portalOut, error) {
	out := []map[string]any{}
	for i := range portalVATRates {
		out = append(out, portalVATOut(i))
	}
	return portalPaged(out, 0, len(out), len(out)), nil
}

func (s *Server) portalVATGet(c portalCall) (portalOut, error) {
	n, err := strconv.Atoi(portalStr(portalParam(c.in, "id")))
	if err != nil || n < 1 || n > len(portalVATRates) {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return portalOut{Result: portalVATOut(n - 1)}, nil
}

// ---------- the catalog ----------

func (s *Server) portalCatalogOut(c portalCall) map[string]any {
	return map[string]any{"ID": portalCatalogID, "NAME": s.tr(c.r, "crm.products"), "ORIGINATOR_ID": nil, "ORIGIN_ID": nil, "XML_ID": ""}
}

func (s *Server) portalCatalogFields(c portalCall) (portalOut, error) {
	return portalOut{Result: s.portalFields(c, "portal.field.", portalField{Name: "ID", Type: "integer", ReadOnly: true},
		portalField{Name: "NAME", Type: "string", ReadOnly: true}, portalField{Name: "ORIGINATOR_ID", Type: "string", ReadOnly: true},
		portalField{Name: "ORIGIN_ID", Type: "string", ReadOnly: true}, portalField{Name: "XML_ID", Type: "string", ReadOnly: true})}, nil
}

// crm.catalog.list: go24 has one catalog.
func (s *Server) portalCatalogList(c portalCall) (portalOut, error) {
	return portalPaged([]map[string]any{s.portalCatalogOut(c)}, 0, 1, 1), nil
}

func (s *Server) portalCatalogGet(c portalCall) (portalOut, error) {
	if portalStr(portalParam(c.in, "id")) != portalCatalogID {
		return portalOut{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return portalOut{Result: s.portalCatalogOut(c)}, nil
}

// portalMeasures adds MEASURE (the unit's number) to the products, when it is selected.
func (s *Server) portalMeasures(c portalCall, sel []portalField, list []map[string]any) error {
	if !slices.ContainsFunc(sel, func(f portalField) bool { return f.Name == "MEASURE" }) || len(list) == 0 {
		return nil
	}
	var nums []string
	for _, x := range list {
		nums = append(nums, portalStr(x["ID"]))
	}
	rows, err := s.DB.Query(c.r.Context(), `SELECT num::text, unit FROM crm_products WHERE num::text = ANY($1)`, nums)
	if err != nil {
		return err
	}
	unitOf, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct{ Num, Unit string }])
	if err != nil {
		return err
	}
	units, err := s.portalUnits(c.r)
	if err != nil {
		return err
	}
	for _, x := range list {
		x["MEASURE"] = nil
		for _, p := range unitOf {
			if p.Num == portalStr(x["ID"]) {
				if u, ok := portalUnitOf(units, p.Unit); ok {
					x["MEASURE"] = strconv.FormatInt(u.Num, 10)
				}
			}
		}
	}
	return nil
}

// crm.product.list: filter, order, select, start — the products, the services and the variations of the catalog.
func (s *Server) portalProductList(c portalCall, e portalEntity) (portalOut, error) {
	out, err := s.portalCRMList(c, e)
	if err != nil {
		return out, err
	}
	list, _ := out.Result.([]map[string]any)
	return out, s.portalMeasures(c, portalSelect(e, portalList(portalParam(c.in, "select"))), list)
}

// crm.product.get, crm.productsection.get: the record by id with all its fields.
func (s *Server) portalProductGet(c portalCall, e portalEntity) (portalOut, error) {
	id, err := s.portalCardID(c, e, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	list, err := s.portalRows(c.r.Context(), e, "x.id = $1", "x.num", []any{id}, 0, 0, e.Fields)
	if err != nil || len(list) == 0 {
		return portalOut{}, err
	}
	if e.Entity == "product" {
		err = s.portalMeasures(c, e.Fields, list)
	}
	return portalOut{Result: list[0]}, err
}

// portalFieldsIn are the fields of add or update; none — the error.
func (s *Server) portalFieldsIn(c portalCall) (map[string]any, error) {
	fields := portalMap(portalParam(c.in, "fields"))
	if fields == nil {
		return nil, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_fields")
	}
	return fields, nil
}

// portalNumber reads a number ("1 200,50" too).
func portalNumber(v any) (float64, error) {
	x := strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(portalStr(v)), " ", ""), ",", ".")
	if x == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(x, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, i18n.E("err.bad_number", portalStr(v))
	}
	return f, nil
}

// portalFlag: Y, true, 1.
func portalFlag(v any) bool {
	return slices.Contains([]string{"1", "y", "true"}, strings.ToLower(strings.TrimSpace(portalStr(v))))
}

// portalProductIn applies the fields to the product: only those given.
func (s *Server) portalProductIn(c portalCall, p *crm.Product, fields map[string]any) error {
	ctx := c.r.Context()
	for name, v := range fields {
		var err error
		switch strings.ToUpper(name) {
		case "NAME":
			p.Name = portalStr(v)
		case "XML_ID":
			p.SKU = portalStr(v)
		case "ACTIVE":
			p.Active = portalFlag(v)
		case "PRICE":
			p.Price, err = portalNumber(v)
		case "CURRENCY_ID":
			p.Currency = portalStr(v)
		case "DESCRIPTION":
			p.Description = portalStr(v)
		case "VAT_INCLUDED":
			p.VATIncluded = portalFlag(v)
		case "VAT_ID":
			p.VATRate = nil
			if n, _ := strconv.Atoi(portalStr(v)); n >= 1 && n <= len(portalVATRates) {
				p.VATRate = &portalVATRates[n-1]
			} else if portalStr(v) != "" && portalStr(v) != "0" {
				err = i18n.E("err.portal_field", "VAT_ID")
			}
		case "TYPE":
			if p.ParentID == "" {
				p.Kind = crm.KindProduct
				if portalStr(v) == "7" {
					p.Kind = crm.KindService
				}
			}
		case "SECTION_ID":
			p.SectionID, err = s.portalNum(ctx, "crm_product_sections", v)
		case "MEASURE":
			var units []portalUnit
			if units, err = s.portalUnits(c.r); err == nil {
				i := slices.IndexFunc(units, func(u portalUnit) bool { return strconv.FormatInt(u.Num, 10) == portalStr(v) })
				if i < 0 && portalStr(v) != "" {
					err = i18n.E("err.portal_field", "MEASURE")
				} else if i >= 0 {
					p.Unit = units[i].ShownSymbol
				}
			}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// crm.product.add: fields → the new product's number.
func (s *Server) portalProductAdd(c portalCall) (portalOut, error) {
	if err := s.apiCatalogAdmin(c.r.Context(), c.p); err != nil {
		return portalOut{}, err
	}
	fields, err := s.portalFieldsIn(c)
	if err != nil {
		return portalOut{}, err
	}
	p := crm.Product{Active: true, VATIncluded: true}
	if err := s.portalProductIn(c, &p, fields); err != nil {
		return portalOut{}, err
	}
	id, err := s.CRM.SaveProduct(c.r.Context(), c.p.UserID, p)
	if err != nil {
		return portalOut{}, err
	}
	var num int64
	err = s.DB.QueryRow(c.r.Context(), `SELECT num FROM crm_products WHERE id = $1`, id).Scan(&num)
	return portalOut{Result: num}, err
}

// crm.product.update: id, fields — only the fields given change.
func (s *Server) portalProductUpdate(c portalCall) (portalOut, error) {
	if err := s.apiCatalogAdmin(c.r.Context(), c.p); err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCardID(c, portalProduct, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	fields, err := s.portalFieldsIn(c)
	if err != nil {
		return portalOut{}, err
	}
	p, err := s.CRM.Product(c.r.Context(), id)
	if err != nil {
		return portalOut{}, err
	}
	if err := s.portalProductIn(c, &p, fields); err != nil {
		return portalOut{}, err
	}
	if _, err := s.CRM.SaveProduct(c.r.Context(), c.p.UserID, p); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// crm.product.delete: id — the product leaves the catalog with its variations; the cards keep their rows.
func (s *Server) portalProductDelete(c portalCall) (portalOut, error) {
	if err := s.apiCatalogAdmin(c.r.Context(), c.p); err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCardID(c, portalProduct, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.CRM.DeleteProduct(c.r.Context(), id)
}

// portalSectionSave makes a section (id "") or changes one by the fields given; answers its id.
func (s *Server) portalSectionSave(c portalCall, id string) (string, error) {
	ctx := c.r.Context()
	if err := s.apiCatalogAdmin(ctx, c.p); err != nil {
		return "", err
	}
	fields, err := s.portalFieldsIn(c)
	if err != nil {
		return "", err
	}
	x := crm.Section{ID: id}
	if id != "" {
		list, err := s.CRM.Sections(ctx)
		if err != nil {
			return "", err
		}
		i := slices.IndexFunc(list, func(y crm.Section) bool { return y.ID == id })
		if i < 0 {
			return "", crm.ErrNotFound
		}
		x = list[i]
	}
	for name, v := range fields {
		switch strings.ToUpper(name) {
		case "NAME":
			x.Name = portalStr(v)
		case "SORT":
			x.Sort, _ = strconv.Atoi(portalStr(v))
		case "SECTION_ID":
			if x.ParentID, err = s.portalNum(ctx, "crm_product_sections", v); err != nil {
				return "", err
			}
		}
	}
	return s.CRM.SaveSection(ctx, x)
}

// crm.productsection.add: fields (NAME, SECTION_ID — the parent, SORT) → the new section's number.
func (s *Server) portalSectionAdd(c portalCall) (portalOut, error) {
	id, err := s.portalSectionSave(c, "")
	if err != nil {
		return portalOut{}, err
	}
	var num int64
	err = s.DB.QueryRow(c.r.Context(), `SELECT num FROM crm_product_sections WHERE id = $1`, id).Scan(&num)
	return portalOut{Result: num}, err
}

func (s *Server) portalSectionUpdate(c portalCall) (portalOut, error) {
	id, err := s.portalCardID(c, portalProductSection, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	if _, err := s.portalSectionSave(c, id); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, nil
}

// crm.productsection.delete: its products and subsections go to its parent.
func (s *Server) portalSectionDelete(c portalCall) (portalOut, error) {
	if err := s.apiCatalogAdmin(c.r.Context(), c.p); err != nil {
		return portalOut{}, err
	}
	id, err := s.portalCardID(c, portalProductSection, portalParam(c.in, "id"))
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.CRM.DeleteSection(c.r.Context(), id)
}

// ---------- product rows ----------

// portalRowFields are the fields of a product row: its name in crm.productrow.* (crm.item.productrow.* — in camelCase).
var portalRowFields = []portalField{
	{Name: "ID", Type: "integer", ReadOnly: true},
	{Name: "OWNER_ID", Type: "integer", Required: true},
	{Name: "OWNER_TYPE", Type: "char", Required: true},
	{Name: "PRODUCT_ID", Type: "integer"},
	{Name: "PRODUCT_NAME", Type: "string"},
	{Name: "ORIGINAL_PRODUCT_NAME", Type: "string", ReadOnly: true},
	{Name: "PRODUCT_DESCRIPTION", Type: "string", ReadOnly: true},
	{Name: "PRICE", Type: "double"},
	{Name: "PRICE_EXCLUSIVE", Type: "double"},
	{Name: "PRICE_NETTO", Type: "double"},
	{Name: "PRICE_BRUTTO", Type: "double"},
	{Name: "PRICE_ACCOUNT", Type: "double", ReadOnly: true},
	{Name: "QUANTITY", Type: "double"},
	{Name: "DISCOUNT_TYPE_ID", Type: "integer"},
	{Name: "DISCOUNT_RATE", Type: "double"},
	{Name: "DISCOUNT_SUM", Type: "double"},
	{Name: "TAX_RATE", Type: "double"},
	{Name: "TAX_INCLUDED", Type: "char"},
	{Name: "CUSTOMIZED", Type: "char", ReadOnly: true},
	{Name: "MEASURE_CODE", Type: "integer"},
	{Name: "MEASURE_NAME", Type: "string"},
	{Name: "SORT", Type: "integer"},
	{Name: "TYPE", Type: "integer", ReadOnly: true},
	{Name: "STORE_ID", Type: "integer"},
	{Name: "RESERVE_QUANTITY", Type: "double"},
	{Name: "DATE_RESERVE_END", Type: "date"},
}

// portalCamel is the camelCase name of a field: PRODUCT_NAME → productName.
func portalCamel(name string) string {
	parts := strings.Split(strings.ToLower(name), "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// portalKey is a parameter's name to compare: productName, PRODUCT_NAME and product_name are one.
func portalKey(name string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' {
			return -1
		}
		return unicode.ToLower(r)
	}, name)
}

// portalRowParam is a field of a row in any of its spellings.
func portalRowParam(in map[string]any, name string) (any, bool) {
	for k, v := range in {
		if portalKey(k) == portalKey(name) {
			return v, true
		}
	}
	return nil, false
}

// portalRowOwners: OWNER_TYPE of a row → the go24 entity; ownerType of crm.item.productrow is the same letter, or
// entityTypeId.
var portalRowOwners = map[string]string{"D": "deal", "L": "lead", "2": "deal", "1": "lead"}

func portalOwnerLetter(entity string) string {
	if entity == "lead" {
		return "L"
	}
	return "D"
}

// portalRowCard is a card with its rows, as the caller may act on it.
type portalRowCard struct {
	Entity, ID string
	Num        int64
	Rows       []crm.ProductRow
}

// portalRowOwner is the card of OWNER_TYPE (D or L) and OWNER_ID (its number) the caller may read or change.
func (s *Server) portalRowOwner(c portalCall, typ, num any, action string) (portalRowCard, error) {
	entity := portalRowOwners[strings.ToUpper(strings.TrimSpace(portalStr(typ)))]
	if entity == "" {
		return portalRowCard{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_row_owner")
	}
	e := portalEntities[entity]
	id, err := s.portalCardID(c, e, num)
	if err != nil {
		return portalRowCard{}, err
	}
	if _, err := s.apiCRMItem(cardReq(c.r, e.Kind, id), c.p, action); err != nil {
		return portalRowCard{}, err
	}
	card := portalRowCard{Entity: entity, ID: id}
	card.Num, _ = strconv.ParseInt(portalStr(num), 10, 64)
	card.Rows, err = s.CRM.ProductRows(c.r.Context(), entity, id)
	return card, err
}

// portalRowRefs are the numbers of the products and the warehouses of rows, and the units, read once for a call.
type portalRowRefs struct {
	products, stores map[string]int64 // go24 id → number
	units            []portalUnit
}

func (s *Server) portalRefs(c portalCall) (portalRowRefs, error) {
	refs := portalRowRefs{products: map[string]int64{}, stores: map[string]int64{}}
	for table, m := range map[string]map[string]int64{"crm_products": refs.products, "crm_warehouses": refs.stores} {
		rows, err := s.DB.Query(c.r.Context(), `SELECT id::text, num FROM `+table)
		if err != nil {
			return refs, err
		}
		for rows.Next() {
			var id string
			var n int64
			if err := rows.Scan(&id, &n); err != nil {
				return refs, err
			}
			m[id] = n
		}
		if err := rows.Err(); err != nil {
			return refs, err
		}
	}
	var err error
	refs.units, err = s.portalUnits(c.r)
	return refs, err
}

// portalPrices are the prices of one of a row gives them.
type portalPrices struct{ Price, Exclusive, Netto, Brutto, DiscountSum float64 }

func portalRowPrices(r crm.ProductRow) portalPrices {
	t := 0.0
	if r.VATRate != nil {
		t = *r.VATRate / 100
	}
	netto := r.Price
	if r.VATIncluded {
		netto = r.Price / (1 + t)
	}
	excl := netto * (1 - r.Discount/100)
	return portalPrices{Price: round2(excl * (1 + t)), Exclusive: round2(excl), Netto: round2(netto), Brutto: round2(netto * (1 + t)),
		DiscountSum: round2(netto - excl)}
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// portalRowOut is a row as crm.productrow.* gives it (camel — as crm.item.productrow.*: camelCase, numbers as numbers).
func portalRowOut(card portalRowCard, i int, refs portalRowRefs, camel bool) map[string]any {
	r := card.Rows[i]
	p := portalRowPrices(r)
	var tax any
	if r.VATRate != nil {
		tax = *r.VATRate
	}
	code := 0
	if u, ok := portalUnitOf(refs.units, r.Unit); ok {
		code, _ = strconv.Atoi(u.Code)
	}
	typ := 1
	if r.Service {
		typ = 7
	}
	var store, until any
	if n, ok := refs.stores[r.WarehouseID]; ok {
		store = n
	}
	if r.ReserveUntil != nil {
		until = r.ReserveUntil.Format("2006-01-02")
	}
	out := map[string]any{"ID": r.ID, "OWNER_ID": card.Num, "OWNER_TYPE": portalOwnerLetter(card.Entity), "PRODUCT_ID": refs.products[r.ProductID],
		"PRODUCT_NAME": r.Name, "ORIGINAL_PRODUCT_NAME": r.Name, "PRODUCT_DESCRIPTION": "", "PRICE": p.Price, "PRICE_EXCLUSIVE": p.Exclusive,
		"PRICE_NETTO": p.Netto, "PRICE_BRUTTO": p.Brutto, "PRICE_ACCOUNT": p.Price, "QUANTITY": r.Quantity, "DISCOUNT_TYPE_ID": 2,
		"DISCOUNT_RATE": r.Discount, "DISCOUNT_SUM": p.DiscountSum, "TAX_RATE": tax, "TAX_INCLUDED": portalYN(r.VATIncluded),
		"CUSTOMIZED": "Y", "MEASURE_CODE": code, "MEASURE_NAME": r.Unit, "SORT": (i + 1) * 10, "TYPE": typ, "STORE_ID": store,
		"RESERVE_QUANTITY": r.Reserve, "DATE_RESERVE_END": until}
	if !camel {
		out["ID"], out["OWNER_ID"] = strconv.FormatInt(r.ID, 10), strconv.FormatInt(card.Num, 10)
		return out
	}
	cc := map[string]any{}
	for k, v := range out {
		cc[portalCamel(k)] = v
	}
	return cc
}

func portalRowsOut(card portalRowCard, refs portalRowRefs, camel bool) []map[string]any {
	out := []map[string]any{}
	for i := range card.Rows {
		out = append(out, portalRowOut(card, i, refs, camel))
	}
	return out
}

// portalRowIn makes a row of the fields; base is the row changed (nil — a new one). A field not given keeps
// the base's value; a new row of a catalog product takes the product's name, price, unit and VAT that are not given.
// The price is the first given of PRICE, PRICE_EXCLUSIVE, PRICE_NETTO, PRICE_BRUTTO; none — the base's (the
// product's) price without the discount.
func (s *Server) portalRowIn(c portalCall, refs portalRowRefs, in map[string]any, base *crm.ProductRow) (crm.ProductRow, error) {
	ctx := c.r.Context()
	get := func(name string) (any, bool) { return portalRowParam(in, name) }
	num := func(name string) (float64, bool, error) {
		v, ok := get(name)
		if !ok || v == nil {
			return 0, false, nil
		}
		f, err := portalNumber(v)
		if err != nil {
			return 0, true, i18n.E("err.portal_field", name)
		}
		return f, true, nil
	}
	r := crm.ProductRow{Quantity: 1, Reserve: -1}
	if base != nil {
		r = *base
	}
	netto := 0.0 // the price of one without the discount and the tax
	if base != nil {
		netto = portalRowPrices(*base).Netto
	}
	if v, ok := get("PRODUCT_ID"); ok {
		id, err := s.portalNum(ctx, "crm_products", v)
		if err != nil {
			return r, err
		}
		if id != r.ProductID && id != "" {
			p, err := s.CRM.Product(ctx, id)
			if err != nil {
				return r, err
			}
			r.Name, r.Unit, r.VATRate, r.VATIncluded = p.Name, p.Unit, p.VATRate, p.VATIncluded
			netto = p.Price
			if p.VATIncluded && p.VATRate != nil {
				netto = p.Price / (1 + *p.VATRate/100)
			}
		}
		r.ProductID = id
	}
	if v, ok := get("PRODUCT_NAME"); ok && strings.TrimSpace(portalStr(v)) != "" {
		r.Name = portalStr(v)
	}
	if v, ok := get("TAX_RATE"); ok {
		r.VATRate = nil
		if x := strings.TrimSpace(portalStr(v)); x != "" && !strings.EqualFold(x, "false") {
			f, err := portalNumber(v)
			if err != nil {
				return r, i18n.E("err.portal_field", "TAX_RATE")
			}
			r.VATRate = &f
		}
	}
	if v, ok := get("TAX_INCLUDED"); ok {
		r.VATIncluded = portalFlag(v)
	}
	t := 0.0
	if r.VATRate != nil {
		t = *r.VATRate / 100
	}
	q, ok, err := num("QUANTITY")
	if err != nil {
		return r, err
	}
	if ok {
		r.Quantity = q
	}
	// the discount: in percent (DISCOUNT_TYPE_ID 2, by default) or a sum off the price of one without the tax (1)
	rate, rateGiven, err := num("DISCOUNT_RATE")
	if err != nil {
		return r, err
	}
	sum, sumGiven, err := num("DISCOUNT_SUM")
	if err != nil {
		return r, err
	}
	absolute := false
	if v, ok := get("DISCOUNT_TYPE_ID"); ok {
		absolute = portalStr(v) == "1"
	} else {
		absolute = sumGiven && !rateGiven
	}
	excl, exclGiven := 0.0, false
	if f, ok, err := num("PRICE"); err != nil {
		return r, err
	} else if ok {
		excl, exclGiven = f/(1+t), true
	} else if f, ok, err := num("PRICE_EXCLUSIVE"); err != nil {
		return r, err
	} else if ok {
		excl, exclGiven = f, true
	} else if f, ok, err := num("PRICE_NETTO"); err != nil {
		return r, err
	} else if ok {
		netto = f
	} else if f, ok, err := num("PRICE_BRUTTO"); err != nil {
		return r, err
	} else if ok {
		netto = f / (1 + t)
	}
	d := r.Discount
	switch {
	case exclGiven && absolute:
		netto = excl + sum
	case exclGiven:
		if rateGiven {
			d = rate
		}
		if d < 100 {
			netto = excl / (1 - d/100)
		}
	}
	if absolute {
		d = 0
		if netto > 0 {
			d = sum / netto * 100
		}
	} else if rateGiven {
		d = rate
	}
	if d < 0 || d > 100 {
		return r, i18n.E("err.portal_field", "DISCOUNT_RATE")
	}
	r.Discount = round2(d)
	r.Price = netto
	if r.VATIncluded {
		r.Price = netto * (1 + t)
	}
	r.Price = round2(r.Price)
	if v, ok := get("MEASURE_NAME"); ok && strings.TrimSpace(portalStr(v)) != "" {
		r.Unit = portalStr(v)
	} else if v, ok := get("MEASURE_CODE"); ok && portalStr(v) != "" {
		i := slices.IndexFunc(refs.units, func(u portalUnit) bool { a, _ := strconv.Atoi(u.Code); return a > 0 && strconv.Itoa(a) == portalStr(v) })
		if i < 0 {
			return r, i18n.E("err.portal_field", "MEASURE_CODE")
		}
		r.Unit = refs.units[i].ShownSymbol
	}
	if v, ok := get("STORE_ID"); ok {
		if r.WarehouseID, err = s.portalNum(ctx, "crm_warehouses", v); err != nil {
			return r, err
		}
	}
	if f, ok, err := num("RESERVE_QUANTITY"); err != nil {
		return r, err
	} else if ok {
		r.Reserve = f
	} else if r.Reserve > r.Quantity {
		r.Reserve = r.Quantity
	}
	if v, ok := get("DATE_RESERVE_END"); ok {
		if r.ReserveUntil, err = portalDate(portalStr(v)); err != nil {
			return r, err
		}
	}
	return r, nil
}

// portalSaveRows saves the card's rows and reads them back with their numbers.
func (s *Server) portalSaveRows(c portalCall, card *portalRowCard) error {
	if _, err := s.CRM.SetProductRows(c.r.Context(), c.p.UserID, card.Entity, card.ID, card.Rows); err != nil {
		return err
	}
	var err error
	card.Rows, err = s.CRM.ProductRows(c.r.Context(), card.Entity, card.ID)
	return err
}

// portalRowsIn are the rows of set: each a new row (one with the ID of the card's row keeps its number), in the
// order of SORT.
func (s *Server) portalRowsIn(c portalCall, refs portalRowRefs, v any) ([]crm.ProductRow, error) {
	var items []map[string]any
	for _, x := range anyList(v) {
		if y := portalMap(x); y != nil {
			items = append(items, y)
		}
	}
	type sorted struct {
		row  crm.ProductRow
		sort float64
	}
	var list []sorted
	for i, it := range items {
		row, err := s.portalRowIn(c, refs, it, nil)
		if err != nil {
			return nil, err
		}
		if v, ok := portalRowParam(it, "ID"); ok {
			row.ID, _ = strconv.ParseInt(portalStr(v), 10, 64)
		}
		srt := float64(i+1) * 10
		if v, ok := portalRowParam(it, "SORT"); ok && portalStr(v) != "" {
			srt, _ = portalNumber(v)
		}
		list = append(list, sorted{row, srt})
	}
	slices.SortStableFunc(list, func(a, b sorted) int { return cmp.Compare(a.sort, b.sort) })
	out := make([]crm.ProductRow, len(list))
	for i, x := range list {
		out[i] = x.row
	}
	return out, nil
}

// anyList is a JSON list, or the values of a PHP array ({"0": …, "1": …}) in the order of their keys.
func anyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.SortFunc(keys, func(a, b string) int {
			na, ea := strconv.Atoi(a)
			nb, eb := strconv.Atoi(b)
			if ea == nil && eb == nil {
				return na - nb
			}
			return strings.Compare(a, b)
		})
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = x[k]
		}
		return out
	}
	return nil
}

// crm.deal.productrows.get, crm.lead.productrows.get: id → the card's rows.
func (s *Server) portalCardRowsGet(c portalCall, entity string) (portalOut, error) {
	card, err := s.portalRowOwner(c, portalOwnerLetter(entity), portalParam(c.in, "id"), "read")
	if err != nil {
		return portalOut{}, err
	}
	refs, err := s.portalRefs(c)
	if err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: portalRowsOut(card, refs, false)}, nil
}

// crm.deal.productrows.set, crm.lead.productrows.set: id, rows — the card's rows replaced (none — all removed); the
// card's amount is counted from them.
func (s *Server) portalCardRowsSet(c portalCall, entity string) (portalOut, error) {
	card, err := s.portalRowOwner(c, portalOwnerLetter(entity), portalParam(c.in, "id"), "write")
	if err != nil {
		return portalOut{}, err
	}
	refs, err := s.portalRefs(c)
	if err != nil {
		return portalOut{}, err
	}
	if card.Rows, err = s.portalRowsIn(c, refs, portalParam(c.in, "rows")); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: true}, s.portalSaveRows(c, &card)
}

func (s *Server) portalRowFields(c portalCall) (portalOut, error) {
	return portalOut{Result: s.portalFields(c, "portal.field.", portalRowFields...)}, nil
}

// portalFilterOwner is the owner of the rows a filter names: OWNER_TYPE and OWNER_ID (=ownerType, =ownerId).
func (s *Server) portalFilterOwner(c portalCall) (portalRowCard, error) {
	filter := portalMap(portalParam(c.in, "filter"))
	var typ, num any
	for k, v := range filter {
		switch portalKey(strings.TrimPrefix(k, "=")) {
		case "ownertype":
			typ = v
		case "ownerid":
			num = v
		}
	}
	if typ == nil || num == nil {
		return portalRowCard{}, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_row_owner")
	}
	return s.portalRowOwner(c, typ, num, "read")
}

// crm.productrow.list: filter[OWNER_TYPE], filter[OWNER_ID] — the rows of the card.
func (s *Server) portalRowList(c portalCall) (portalOut, error) {
	card, err := s.portalFilterOwner(c)
	if err != nil {
		return portalOut{}, err
	}
	refs, err := s.portalRefs(c)
	if err != nil {
		return portalOut{}, err
	}
	out := portalRowsOut(card, refs, false)
	return portalPaged(out, 0, len(out), len(out)), nil
}

func (s *Server) portalItemRowFields(c portalCall) (portalOut, error) {
	fields := s.portalFields(c, "portal.field.", portalRowFields...)
	out := map[string]any{}
	for k, v := range fields {
		out[portalCamel(k)] = v
	}
	return portalOut{Result: map[string]any{"fields": out}}, nil
}

// crm.item.productrow.list: filter {=ownerType, =ownerId}, start → {"productRows": [...]} of 50.
func (s *Server) portalItemRowList(c portalCall) (portalOut, error) {
	card, err := s.portalFilterOwner(c)
	if err != nil {
		return portalOut{}, err
	}
	refs, err := s.portalRefs(c)
	if err != nil {
		return portalOut{}, err
	}
	all := portalRowsOut(card, refs, true)
	start := min(portalStart(c.in), len(all))
	page := all[start:min(start+portalPage, len(all))]
	out := portalPaged(map[string]any{"productRows": page}, start, len(page), len(all))
	return out, nil
}

// portalRowByID is the card of the row numbered id, which the caller may act on, and the row's place in it.
func (s *Server) portalRowByID(c portalCall, action string) (portalRowCard, int, error) {
	n, err := strconv.ParseInt(portalStr(portalParam(c.in, "id")), 10, 64)
	if err != nil {
		return portalRowCard{}, 0, s.portalErr(c.r, http.StatusBadRequest, "ERROR_CORE", "err.portal_id")
	}
	var entity string
	var num int64
	err = s.DB.QueryRow(c.r.Context(), `SELECT r.entity, coalesce(d.num, l.num, 0) FROM crm_product_rows r
		LEFT JOIN crm_deals d ON r.entity = 'deal' AND d.id = r.entity_id LEFT JOIN crm_leads l ON r.entity = 'lead' AND l.id = r.entity_id
		WHERE r.id = $1 AND r.entity IN ('deal', 'lead')`, n).Scan(&entity, &num)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && num == 0) {
		return portalRowCard{}, 0, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	if err != nil {
		return portalRowCard{}, 0, err
	}
	card, err := s.portalRowOwner(c, portalOwnerLetter(entity), num, action)
	if err != nil {
		return card, 0, err
	}
	i := slices.IndexFunc(card.Rows, func(r crm.ProductRow) bool { return r.ID == n })
	if i < 0 {
		return card, 0, s.portalErr(c.r, http.StatusBadRequest, "ERROR_NOT_FOUND", "err.not_found")
	}
	return card, i, nil
}

// portalItemRowAnswer answers {"productRow": …} of the row numbered id.
func (s *Server) portalItemRowAnswer(c portalCall, card portalRowCard, id int64) (portalOut, error) {
	refs, err := s.portalRefs(c)
	if err != nil {
		return portalOut{}, err
	}
	i := slices.IndexFunc(card.Rows, func(r crm.ProductRow) bool { return r.ID == id })
	if i < 0 {
		return portalOut{}, crm.ErrNotFound
	}
	return portalOut{Result: map[string]any{"productRow": portalRowOut(card, i, refs, true)}}, nil
}

func (s *Server) portalItemRowGet(c portalCall) (portalOut, error) {
	card, i, err := s.portalRowByID(c, "read")
	if err != nil {
		return portalOut{}, err
	}
	return s.portalItemRowAnswer(c, card, card.Rows[i].ID)
}

// crm.item.productrow.add: fields {ownerType, ownerId, productId, productName, price, quantity, …, sort} — a row
// added to the card (at its place by sort, else the last).
func (s *Server) portalItemRowAdd(c portalCall) (portalOut, error) {
	fields, err := s.portalFieldsIn(c)
	if err != nil {
		return portalOut{}, err
	}
	typ, _ := portalRowParam(fields, "OWNER_TYPE")
	num, _ := portalRowParam(fields, "OWNER_ID")
	card, err := s.portalRowOwner(c, typ, num, "write")
	if err != nil {
		return portalOut{}, err
	}
	refs, err := s.portalRefs(c)
	if err != nil {
		return portalOut{}, err
	}
	row, err := s.portalRowIn(c, refs, fields, nil)
	if err != nil {
		return portalOut{}, err
	}
	at := len(card.Rows)
	if v, ok := portalRowParam(fields, "SORT"); ok && portalStr(v) != "" {
		srt, _ := portalNumber(v)
		at = min(max(int(math.Ceil(srt/10))-1, 0), len(card.Rows)) // the rows are 10, 20, 30…
		if srt > float64(len(card.Rows))*10 {
			at = len(card.Rows)
		}
	}
	card.Rows = slices.Insert(card.Rows, at, row)
	if _, err := s.CRM.SetProductRows(c.r.Context(), c.p.UserID, card.Entity, card.ID, card.Rows); err != nil {
		return portalOut{}, err
	}
	id := card.Rows[at].ID
	if card.Rows, err = s.CRM.ProductRows(c.r.Context(), card.Entity, card.ID); err != nil {
		return portalOut{}, err
	}
	return s.portalItemRowAnswer(c, card, id)
}

// crm.item.productrow.update: id, fields — only the fields given change.
func (s *Server) portalItemRowUpdate(c portalCall) (portalOut, error) {
	card, i, err := s.portalRowByID(c, "write")
	if err != nil {
		return portalOut{}, err
	}
	fields, err := s.portalFieldsIn(c)
	if err != nil {
		return portalOut{}, err
	}
	refs, err := s.portalRefs(c)
	if err != nil {
		return portalOut{}, err
	}
	id := card.Rows[i].ID
	if card.Rows[i], err = s.portalRowIn(c, refs, fields, &card.Rows[i]); err != nil {
		return portalOut{}, err
	}
	if err := s.portalSaveRows(c, &card); err != nil {
		return portalOut{}, err
	}
	return s.portalItemRowAnswer(c, card, id)
}

func (s *Server) portalItemRowDelete(c portalCall) (portalOut, error) {
	card, i, err := s.portalRowByID(c, "write")
	if err != nil {
		return portalOut{}, err
	}
	card.Rows = slices.Delete(card.Rows, i, i+1)
	return portalOut{}, s.portalSaveRows(c, &card)
}

// crm.item.productrow.set: ownerType, ownerId, productRows — the card's rows replaced → {"productRows": [...]}.
func (s *Server) portalItemRowSet(c portalCall) (portalOut, error) {
	card, err := s.portalRowOwner(c, portalParam(c.in, "ownerType"), portalParam(c.in, "ownerId"), "write")
	if err != nil {
		return portalOut{}, err
	}
	refs, err := s.portalRefs(c)
	if err != nil {
		return portalOut{}, err
	}
	if card.Rows, err = s.portalRowsIn(c, refs, portalParam(c.in, "productRows")); err != nil {
		return portalOut{}, err
	}
	if err := s.portalSaveRows(c, &card); err != nil {
		return portalOut{}, err
	}
	return portalOut{Result: map[string]any{"productRows": portalRowsOut(card, refs, true)}}, nil
}
