package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestPortalRowPrices: the prices from a go24 row and back — VAT inside or on top, a discount in percent or
// as a sum.
func TestPortalRowPrices(t *testing.T) {
	vat := 20.0
	for _, tc := range []struct {
		name string
		row  crm.ProductRow
		want portalPrices
	}{
		{"no VAT", crm.ProductRow{Price: 1000, Quantity: 1}, portalPrices{1000, 1000, 1000, 1000, 0}},
		{"VAT inside", crm.ProductRow{Price: 1200, VATRate: &vat, VATIncluded: true}, portalPrices{1200, 1000, 1000, 1200, 0}},
		{"VAT on top", crm.ProductRow{Price: 1000, VATRate: &vat}, portalPrices{1200, 1000, 1000, 1200, 0}},
		{"discount 10%", crm.ProductRow{Price: 1200, VATRate: &vat, VATIncluded: true, Discount: 10}, portalPrices{1080, 900, 1000, 1200, 100}},
	} {
		if got := portalRowPrices(tc.row); got != tc.want {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
	srv := &Server{}
	c := portalCall{r: httptest.NewRequest("GET", "/", nil)}
	for _, tc := range []struct {
		name  string
		in    map[string]any
		price float64 // as go24 keeps it
		disc  float64
		incl  bool
	}{
		{"PRICE with the tax, not included", map[string]any{"PRICE": 1200, "TAX_RATE": 20}, 1000, 0, false},
		{"PRICE with the tax, included", map[string]any{"PRICE": 1200, "TAX_RATE": 20, "TAX_INCLUDED": "Y"}, 1200, 0, true},
		{"percent off", map[string]any{"PRICE": 900, "DISCOUNT_TYPE_ID": 2, "DISCOUNT_RATE": 10}, 1000, 10, false},
		{"a sum off", map[string]any{"PRICE": 900, "DISCOUNT_TYPE_ID": 1, "DISCOUNT_SUM": 100}, 1000, 10, false},
		{"camelCase, netto", map[string]any{"priceNetto": 1000, "discountRate": 25, "taxRate": 20, "taxIncluded": "Y"}, 1200, 25, true},
	} {
		r, err := srv.portalRowIn(c, portalRowRefs{}, tc.in, nil)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if r.Price != tc.price || r.Discount != tc.disc || r.VATIncluded != tc.incl {
			t.Errorf("%s: price %v discount %v included %v, want %v %v %v", tc.name, r.Price, r.Discount, r.VATIncluded, tc.price, tc.disc, tc.incl)
		}
	}
	if portalCamel("DATE_RESERVE_END") != "dateReserveEnd" || portalKey("PRODUCT_ID") != portalKey("productId") {
		t.Error("portalCamel / portalKey")
	}
}

// TestRestPortalProducts: crm.product.*, crm.productsection.*, crm.measure.list, crm.vat.list kept by administrators;
// crm.deal.productrows.*, crm.productrow.list and crm.item.productrow.* with the rights of the card, the rows keeping
// their numbers.
func TestRestPortalProducts(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var anna, ivan string
	var annaNum, ivanNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна Петрова', 'anna@go24.test', true)
		RETURNING id, num`).Scan(&anna, &annaNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван Сидоров', 'ivan@go24.test')
		RETURNING id, num`).Scan(&ivan, &ivanNum))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), I18n: bundle,
		CRM: crm.NewStore(pool), Audit: audit.NewStore(pool), BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Свои", Perms: crm.Perms{"deal": {"read": "own", "write": "own"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, ivan, true))
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hookOf := func(user string, userNum int64) string {
		token, err := srv.Keys.Create(ctx, user, "n8n", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(userNum) + "/" + token + "/"
	}
	annaHook, ivanHook := hookOf(anna, annaNum), hookOf(ivan, ivanNum)
	call := func(hook, method, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+hook+method+".json", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Language", "ru")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	js := func(method, body string) any {
		t.Helper()
		return call(annaHook, method, body, 200)["result"]
	}
	str := func(v any) string { return portalStr(v) }
	items := func(v any) []map[string]any {
		var out []map[string]any
		for _, x := range v.([]any) {
			out = append(out, x.(map[string]any))
		}
		return out
	}

	// the dictionaries
	if vats := items(js("crm.vat.list", `{}`)); len(vats) != 5 || vats[4]["RATE"] != "20.00" || vats[4]["ID"] != "5" {
		t.Errorf("crm.vat.list: %v", vats)
	}
	if cats := items(js("crm.catalog.list", `{}`)); len(cats) != 1 || cats[0]["ID"] != portalCatalogID {
		t.Errorf("crm.catalog.list: %v", cats)
	}
	measures := items(js("crm.measure.list", `{}`))
	var piece, kg map[string]any
	for _, m := range measures {
		switch m["CODE"] {
		case "796":
			piece = m
		case "166":
			kg = m
		}
	}
	if piece == nil || kg == nil {
		t.Fatalf("crm.measure.list: no 796 or 166 in %v", measures)
	}

	// the catalog: sections and products, kept by administrators
	sec := str(js("crm.productsection.add", `{"fields": {"NAME": "Оборудование"}}`))
	sub := str(js("crm.productsection.add", `{"fields": {"NAME": "Принтеры", "SECTION_ID": `+sec+`}}`))
	if got := js("crm.productsection.get", `{"id": `+sub+`}`).(map[string]any); got["SECTION_ID"] != sec || got["NAME"] != "Принтеры" {
		t.Errorf("crm.productsection.get: %v", got)
	}
	if list := items(js("crm.productsection.list", `{"filter": {"SECTION_ID": `+sec+`}}`)); len(list) != 1 || list[0]["ID"] != sub {
		t.Errorf("crm.productsection.list: %v", list)
	}
	call(ivanHook, "crm.productsection.add", `{"fields": {"NAME": "Своё"}}`, http.StatusForbidden)
	printer := str(js("crm.product.add", `{"fields": {"NAME": "Принтер А4", "PRICE": "1200", "CURRENCY_ID": "RUB", "VAT_ID": 5,
		"VAT_INCLUDED": "Y", "XML_ID": "PR-4", "SECTION_ID": `+sub+`, "MEASURE": `+str(piece["ID"])+`}}`))
	paper := str(js("crm.product.add", `{"fields": {"NAME": "Бумага", "PRICE": 300, "MEASURE": `+str(kg["ID"])+`}}`))
	setup := str(js("crm.product.add", `{"fields": {"NAME": "Настройка", "PRICE": 500, "TYPE": 7}}`))
	call(ivanHook, "crm.product.add", `{"fields": {"NAME": "Своё"}}`, http.StatusForbidden)
	got := js("crm.product.get", `{"id": `+printer+`}`).(map[string]any)
	for k, want := range map[string]string{"NAME": "Принтер А4", "PRICE": "1200.00", "VAT_ID": "5", "VAT_INCLUDED": "Y", "XML_ID": "PR-4",
		"SECTION_ID": sub, "MEASURE": str(piece["ID"]), "TYPE": "1", "ACTIVE": "Y", "CATALOG_ID": portalCatalogID} {
		if str(got[k]) != want {
			t.Errorf("crm.product.get %s: %v, want %s", k, got[k], want)
		}
	}
	if got := js("crm.product.get", `{"id": `+setup+`}`).(map[string]any); got["TYPE"] != "7" {
		t.Errorf("a service: TYPE %v", got["TYPE"])
	}
	list := items(js("crm.product.list", `{"filter": {"%NAME": "принтер"}, "select": ["ID", "NAME", "MEASURE"]}`))
	if len(list) != 1 || list[0]["ID"] != printer || list[0]["MEASURE"] != str(piece["ID"]) || list[0]["PRICE"] != nil {
		t.Errorf("crm.product.list: %v", list)
	}
	if list := items(js("crm.product.list", `{"filter": {">PRICE": 400}, "order": {"PRICE": "DESC"}}`)); len(list) != 2 || list[0]["ID"] != printer {
		t.Errorf("crm.product.list >PRICE: %v", list)
	}
	js("crm.product.update", `{"id": `+paper+`, "fields": {"PRICE": 350, "ACTIVE": "N"}}`)
	if got := js("crm.product.get", `{"id": `+paper+`}`).(map[string]any); got["PRICE"] != "350.00" || got["ACTIVE"] != "N" ||
		got["MEASURE"] != str(kg["ID"]) || got["NAME"] != "Бумага" {
		t.Errorf("crm.product.update: %v", got)
	}

	// the rows of a deal: the catalog's product takes its price and VAT; the deal's amount is counted from the rows
	deal := str(js("crm.deal.add", `{"fields": {"TITLE": "Офис"}}`))
	js("crm.deal.productrows.set", `{"id": `+deal+`, "rows": [{"PRODUCT_ID": `+printer+`, "QUANTITY": 2},
		{"PRODUCT_NAME": "Доставка", "PRICE": 900, "DISCOUNT_TYPE_ID": 1, "DISCOUNT_SUM": 100, "QUANTITY": 1}]}`)
	rows := items(js("crm.deal.productrows.get", `{"id": `+deal+`}`))
	if len(rows) != 2 {
		t.Fatalf("crm.deal.productrows.get: %v", rows)
	}
	r0, r1 := rows[0], rows[1]
	if r0["PRODUCT_ID"] != json.Number(printer) && str(r0["PRODUCT_ID"]) != printer || r0["PRODUCT_NAME"] != "Принтер А4" || r0["PRICE"] != 1200.0 ||
		r0["PRICE_EXCLUSIVE"] != 1000.0 || r0["TAX_RATE"] != 20.0 || r0["TAX_INCLUDED"] != "Y" || r0["MEASURE_CODE"] != 796.0 ||
		r0["OWNER_TYPE"] != "D" || r0["OWNER_ID"] != deal || r0["SORT"] != 10.0 {
		t.Errorf("row 1: %v", r0)
	}
	if r1["PRICE"] != 900.0 || r1["PRICE_NETTO"] != 1000.0 || r1["DISCOUNT_RATE"] != 10.0 || r1["DISCOUNT_SUM"] != 100.0 || r1["TAX_RATE"] != nil ||
		str(r1["PRODUCT_ID"]) != "0" {
		t.Errorf("row 2: %v", r1)
	}
	if got := js("crm.deal.get", `{"id": `+deal+`}`).(map[string]any); got["OPPORTUNITY"] != "3300.00" {
		t.Errorf("the deal's amount: %v", got["OPPORTUNITY"])
	}
	if list := items(js("crm.productrow.list", `{"filter": {"OWNER_TYPE": "D", "OWNER_ID": `+deal+`}}`)); len(list) != 2 || list[1]["ID"] != r1["ID"] {
		t.Errorf("crm.productrow.list: %v", list)
	}
	call(annaHook, "crm.productrow.list", `{"filter": {"OWNER_ID": `+deal+`}}`, http.StatusBadRequest)

	// crm.item.productrow.*: camelCase, a row keeps its number
	added := js("crm.item.productrow.add", `{"fields": {"ownerType": "D", "ownerId": `+deal+`, "productId": `+setup+`, "quantity": 1, "sort": 15}}`).(map[string]any)["productRow"].(map[string]any)
	if added["productName"] != "Настройка" || added["price"] != 500.0 || added["type"] != 7.0 || added["sort"] != 20.0 {
		t.Errorf("crm.item.productrow.add: %v", added)
	}
	rid := str(added["id"])
	upd := js("crm.item.productrow.update", `{"id": `+rid+`, "fields": {"quantity": 3}}`).(map[string]any)["productRow"].(map[string]any)
	if str(upd["id"]) != rid || upd["quantity"] != 3.0 || upd["price"] != 500.0 {
		t.Errorf("crm.item.productrow.update: %v", upd)
	}
	page := js("crm.item.productrow.list", `{"filter": {"=ownerType": "D", "=ownerId": `+deal+`}}`).(map[string]any)["productRows"].([]any)
	if len(page) != 3 || str(page[0].(map[string]any)["id"]) != str(r0["ID"]) || str(page[2].(map[string]any)["id"]) != str(r1["ID"]) {
		t.Errorf("crm.item.productrow.list: the rows renumbered: %v", page)
	}
	if got := js("crm.item.productrow.get", `{"id": `+rid+`}`).(map[string]any)["productRow"].(map[string]any); got["ownerId"] != json.Number(deal) && str(got["ownerId"]) != deal {
		t.Errorf("crm.item.productrow.get: %v", got)
	}
	js("crm.item.productrow.delete", `{"id": `+rid+`}`)
	call(annaHook, "crm.item.productrow.get", `{"id": `+rid+`}`, http.StatusBadRequest)
	set := js("crm.item.productrow.set", `{"ownerType": "D", "ownerId": `+deal+`, "productRows": [{"productName": "Картридж", "price": 250,
		"quantity": 4, "measureCode": 796}]}`).(map[string]any)["productRows"].([]any)
	if len(set) != 1 || set[0].(map[string]any)["productName"] != "Картридж" {
		t.Errorf("crm.item.productrow.set: %v", set)
	}
	if got := js("crm.deal.get", `{"id": `+deal+`}`).(map[string]any); got["OPPORTUNITY"] != "1000.00" {
		t.Errorf("the deal's amount after set: %v", got["OPPORTUNITY"])
	}

	// a lead's rows; the rights of the card
	lead := str(js("crm.lead.add", `{"fields": {"TITLE": "Заявка"}}`))
	js("crm.lead.productrows.set", `{"id": `+lead+`, "rows": {"0": {"PRODUCT_NAME": "Консультация", "PRICE": 100}}}`)
	if rows := items(js("crm.lead.productrows.get", `{"id": `+lead+`}`)); len(rows) != 1 || rows[0]["OWNER_TYPE"] != "L" || rows[0]["QUANTITY"] != 1.0 {
		t.Errorf("crm.lead.productrows.get: %v", rows)
	}
	call(ivanHook, "crm.deal.productrows.get", `{"id": `+deal+`}`, http.StatusForbidden)
	call(ivanHook, "crm.item.productrow.get", `{"id": `+str(set[0].(map[string]any)["id"])+`}`, http.StatusForbidden)

	// a product removed: the rows keep their lines
	js("crm.product.delete", `{"id": `+printer+`}`)
	call(annaHook, "crm.product.get", `{"id": `+printer+`}`, http.StatusBadRequest)
	js("crm.productsection.delete", `{"id": `+sec+`}`)
	if list := items(js("crm.productsection.list", `{}`)); len(list) != 1 || list[0]["SECTION_ID"] != nil {
		t.Errorf("crm.productsection.delete: the subsection goes to the root: %v", list)
	}
}
