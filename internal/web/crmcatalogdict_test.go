package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCatalogDictsHTTP: «Настройки каталога» list the built-in units by the locale; an administrator adds a unit and a
// property, an employee can't; the product's form has the unit's choice, the property and the barcodes, the list —
// the property's column and the barcode; the catalog's search finds by the barcode; a stock document's and a deal's
// pickers carry the barcodes for the scanner; REST gives the units, the properties and a product by its barcode.
func TestCatalogDictsHTTP(t *testing.T) {
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
	bundle, err := i18n.Load("")
	must(err)
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool), I18n: bundle,
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("Accept-Language", "ru")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	if code, page := do(anna, "GET", "/crm/products/settings", nil); code != http.StatusOK || !strings.Contains(page, "Килограмм") ||
		!strings.Contains(page, "/crm/product-props/new") {
		t.Fatalf("settings: %d", code)
	}
	if code, _ := do(eve, "POST", "/crm/product-props/new", url.Values{"name": {"Чужое"}, "kind": {"string"}}); code != http.StatusForbidden {
		t.Fatalf("an employee made a property: %d", code)
	}
	if code, _ := do(anna, "POST", "/crm/product-units/new", url.Values{"name": {"Рулон"}, "symbol": {"рул."}}); code != http.StatusSeeOther {
		t.Fatalf("unit: %d", code)
	}
	if code, _ := do(anna, "POST", "/crm/product-props/new", url.Values{"name": {"Цвет"}, "kind": {"list"}, "options": {"красный\r\nсиний"},
		"in_list": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("property: %d", code)
	}
	props, err := srv.CRM.ProductProps(ctx)
	must(err)
	if len(props) != 1 || len(props[0].Options) != 2 || props[0].Options[0] != "красный" {
		t.Fatalf("props %+v", props)
	}
	color := props[0].ID

	_, page := do(anna, "GET", "/crm/products", nil)
	if !strings.Contains(page, `<option value="рул."`) || !strings.Contains(page, `name="prop.`+color+`"`) || !strings.Contains(page, `name="barcodes"`) {
		t.Fatal("the product's form misses the units, the property or the barcodes")
	}
	if code, _ := do(anna, "POST", "/crm/products/new", url.Values{"name": {"Кружка"}, "price": {"300"}, "unit": {"рул."},
		"prop." + color: {"синий"}, "barcodes": {"4600000000017\r\n4600000000024"}}); code != http.StatusSeeOther {
		t.Fatalf("product: %d", code)
	}
	do(anna, "POST", "/crm/products/new", url.Values{"name": {"Тарелка"}, "barcodes": {"4600000000024"}}) // another product's barcode: refused
	list, err := srv.CRM.Products(ctx, "", true)
	must(err)
	if len(list) != 1 || list[0].Unit != "рул." || list[0].Props[color] != "синий" || len(list[0].Barcodes) != 2 {
		t.Fatalf("saved %+v", list)
	}
	mug := list[0].ID

	_, page = do(eve, "GET", "/crm/products?q=4600000000024", nil)
	if !strings.Contains(page, "Кружка") || !strings.Contains(page, "<th>Цвет</th>") || !strings.Contains(page, "синий") ||
		!strings.Contains(page, "4600000000017") {
		t.Fatal("the search by barcode or the property's column")
	}
	_, page = do(anna, "GET", "/crm/deals/"+deal, nil)
	if !strings.Contains(page, `data-barcodes="4600000000017 4600000000024"`) || !strings.Contains(page, "data-barcode ") {
		t.Fatal("the deal's picker has no barcodes or no scanner")
	}
	_, page = do(anna, "GET", "/store/docs/new?kind=arrival", nil)
	if !strings.Contains(page, `data-barcodes="4600000000017 4600000000024"`) || !strings.Contains(page, "data-barcode ") {
		t.Fatal("the stock document's picker has no barcodes or no scanner")
	}

	token, err := srv.Keys.Create(ctx, anna, "t", apikeys.AllScopes, time.Hour)
	must(err)
	get := func(path string, out any) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
		must(json.NewDecoder(res.Body).Decode(out))
	}
	var units []struct {
		Code, Name, Symbol string
		Default            bool
	}
	get("/api/v1/crm/product-units", &units)
	if len(units) != 11 || units[0].Code != "796" || units[0].Symbol == "" || !units[0].Default {
		t.Fatalf("REST units %+v", units)
	}
	var rprops []struct{ ID, Kind string }
	get("/api/v1/crm/product-props", &rprops)
	if len(rprops) != 1 || rprops[0].Kind != "list" {
		t.Fatalf("REST props %+v", rprops)
	}
	var prods []struct {
		ID       string
		Props    map[string]string
		Barcodes []string
	}
	get("/api/v1/crm/products?barcode=4600000000017", &prods)
	if len(prods) != 1 || prods[0].ID != mug || prods[0].Props[color] != "синий" || len(prods[0].Barcodes) != 2 {
		t.Fatalf("REST by barcode %+v", prods)
	}
	get("/api/v1/crm/products?barcode=1", &prods)
	if len(prods) != 0 {
		t.Fatalf("REST unknown barcode %+v", prods)
	}

	// «Услуга»: in the catalog by its type and in a deal's picker, not in a warehouse document's one
	if code, _ := do(anna, "POST", "/crm/products/new", url.Values{"name": {"Доставка"}, "kind": {"service"}, "price": {"500"}}); code != http.StatusSeeOther {
		t.Fatalf("service: %d", code)
	}
	if _, page = do(eve, "GET", "/crm/products?type=service", nil); !strings.Contains(page, "Доставка") || strings.Contains(page, "Кружка") {
		t.Fatal("the services of the catalog")
	}
	if _, page = do(anna, "GET", "/crm/deals/"+deal, nil); !strings.Contains(page, `data-service="1">Доставка`) {
		t.Fatal("the deal's picker has no service")
	}
	if _, page = do(anna, "GET", "/store/docs/new?kind=arrival", nil); strings.Contains(page, "Доставка") {
		t.Fatal("a service in the stock document's picker")
	}
	var typed []struct{ Name, Type string }
	get("/api/v1/crm/products?type=service", &typed)
	if len(typed) != 1 || typed[0].Name != "Доставка" || typed[0].Type != "service" {
		t.Fatalf("REST services %+v", typed)
	}
}
