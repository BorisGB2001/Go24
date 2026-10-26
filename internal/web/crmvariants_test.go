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

// TestProductVariantsHTTP: an administrator marks a property «для вариаций» and adds a variation to a product from
// the catalog (an employee can't); the catalog shows it under the product with the product's stock its sum; the
// deal's and the stock document's pickers offer the variation, not the product; REST gives the variation with its
// parent_id and the product's variations by parent_id.
func TestProductVariantsHTTP(t *testing.T) {
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

	if code, _ := do(anna, "POST", "/crm/product-props/new", url.Values{"name": {"Размер"}, "kind": {"list"}, "options": {"M\r\nL"},
		"variant": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("property: %d", code)
	}
	props, err := srv.CRM.ProductProps(ctx)
	must(err)
	if len(props) != 1 || !props[0].Variant {
		t.Fatalf("props %+v", props)
	}
	size := props[0].ID
	if _, page := do(anna, "GET", "/crm/products/settings", nil); !strings.Contains(page, `name="variant"`) {
		t.Fatal("the property's form has no «для вариаций»")
	}
	shirt, err := srv.CRM.SaveProduct(ctx, anna, crm.Product{Name: "Футболка", Price: 900, Unit: "шт"})
	must(err)
	_, page := do(anna, "GET", "/crm/products", nil)
	if !strings.Contains(page, `name="parent" value="`+shirt+`"`) || !strings.Contains(page, "Добавить вариацию") {
		t.Fatal("no variation's form at the product")
	}
	if strings.Count(page, `name="prop.`+size+`"`) != 1 {
		t.Fatal("the variation property is in the product's form or missing in the variation's")
	}
	form := url.Values{"parent": {shirt}, "prop." + size: {"L"}, "sku": {"TS-L"}, "price": {"950"}, "barcodes": {"4600000000017"}}
	if code, _ := do(eve, "POST", "/crm/products/new", form); code != http.StatusForbidden {
		t.Fatalf("an employee added a variation: %d", code)
	}
	if code, _ := do(anna, "POST", "/crm/products/new", form); code != http.StatusSeeOther {
		t.Fatalf("variation: %d", code)
	}
	vars, err := srv.CRM.Variants(ctx, shirt)
	must(err)
	if len(vars) != 1 || vars[0].Name != "Футболка (L)" || vars[0].Unit != "шт" || vars[0].Price != 950 {
		t.Fatalf("variants %+v", vars)
	}
	large := vars[0].ID
	wh, err := srv.CRM.SaveWarehouse(ctx, crm.Warehouse{Name: "Основной"})
	must(err)
	arr, err := srv.CRM.SaveStockDoc(ctx, anna, crm.StockDoc{Kind: "arrival", Rows: []crm.StockRow{{ProductID: large, Quantity: 7, To: wh}}})
	must(err)
	must(srv.CRM.ConductStockDoc(ctx, anna, arr))

	_, page = do(eve, "GET", "/crm/products?q=TS-L", nil)
	if !strings.Contains(page, "Футболка (L)") || !strings.Contains(page, `class="crmvariant`) || !strings.Contains(page, "Вариаций: 1") ||
		strings.Count(page, `<td class="right nowrap">7</td>`) != 2 {
		t.Fatal("the catalog doesn't show the variation under its product with the stock")
	}
	for _, path := range []string{"/crm/deals/" + deal, "/store/docs/new?kind=arrival"} {
		_, page = do(anna, "GET", path, nil)
		if !strings.Contains(page, `<option value="`+large+`"`) || strings.Contains(page, `<option value="`+shirt+`"`) {
			t.Fatalf("%s: the picker offers the product instead of its variation", path)
		}
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
	var prods []struct {
		ID       string
		ParentID string `json:"parent_id"`
		Props    map[string]string
	}
	get("/api/v1/crm/products", &prods)
	if len(prods) != 1 || prods[0].ID != large || prods[0].ParentID != shirt || prods[0].Props[size] != "L" {
		t.Fatalf("REST products %+v", prods)
	}
	get("/api/v1/crm/products?parent_id="+shirt, &prods)
	if len(prods) != 1 || prods[0].ID != large {
		t.Fatalf("REST variants %+v", prods)
	}
	var rprops []struct{ Variant bool }
	get("/api/v1/crm/product-props", &rprops)
	if len(rprops) != 1 || !rprops[0].Variant {
		t.Fatalf("REST props %+v", rprops)
	}
}
