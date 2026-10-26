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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestStockReportsHTTP: «Остатки» and «Движение товаров» as pages, CSV and over REST; the purchase prices are shown
// to the storekeeper only; a product's movements link to its documents.
func TestStockReportsHTTP(t *testing.T) {
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wh, err := srv.CRM.SaveWarehouse(ctx, crm.Warehouse{Name: "Основной", Responsible: crm.Ref{ID: anna}})
	must(err)
	p, err := srv.CRM.SaveProduct(ctx, anna, crm.Product{Name: "Лампа", SKU: "L-7"})
	must(err)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local)
	doc, err := srv.CRM.SaveStockDoc(ctx, anna, crm.StockDoc{Kind: "arrival", Date: day, Rows: []crm.StockRow{{ProductID: p, Quantity: 4, Price: 250, To: wh}}})
	must(err)
	must(srv.CRM.ConductStockDoc(ctx, anna, doc))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna}))
		as[u] = rec.Result().Cookies()[0]
	}
	get := func(who, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		if who != "" {
			req.AddCookie(as[who])
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	code, page := get(anna, "/store/reports/stock?date=2026-10-20")
	if code != http.StatusOK || !strings.Contains(page, "Лампа") || !strings.Contains(page, "1 000") {
		t.Fatalf("stock report %d", code)
	}
	if _, page = get(eve, "/store/reports/stock?date=2026-10-20"); !strings.Contains(page, "Лампа") || strings.Contains(page, "1 000") {
		t.Fatal("the purchase prices are shown to one not keeping the warehouse")
	}
	if _, page = get(anna, "/store/reports/stock?date=2026-09-30"); strings.Contains(page, "Лампа") {
		t.Fatal("on a date before the arrival")
	}
	_, csv := get(anna, "/store/reports/stock?date=2026-10-20&csv=1")
	if !strings.Contains(csv, "Лампа;L-7;;4;4;250;1000") {
		t.Fatalf("csv %q", csv)
	}
	if _, csv = get(eve, "/store/reports/stock?date=2026-10-20&csv=1"); !strings.HasSuffix(strings.TrimSpace(csv), "Лампа;L-7;;4;4") {
		t.Fatalf("csv without prices %q", csv)
	}
	if _, page = get(anna, "/store/reports/moves?from=2026-10-01&to=2026-10-31"); !strings.Contains(page, "+4") {
		t.Fatal("movement report")
	}
	if _, page = get(anna, "/store/reports/moves?product="+p+"&from=2026-10-01&to=2026-10-31"); !strings.Contains(page, "/store/docs/"+doc) {
		t.Fatal("the product's movements have no document")
	}
	if code, _ = get(anna, "/store/reports/other"); code != http.StatusNotFound {
		t.Fatalf("unknown report %d", code)
	}

	// «Настройки» of the section: administrators only
	if _, page = get(anna, "/store/reports/stock"); !strings.Contains(page, `href="/store/settings"`) {
		t.Fatal("no settings tab for an administrator")
	}
	if _, page = get(eve, "/store/reports/stock"); strings.Contains(page, `href="/store/settings"`) {
		t.Fatal("settings tab for an employee")
	}
	post := func(who string, form url.Values) int {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/store/settings", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(as[who])
		res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		must(err)
		res.Body.Close()
		return res.StatusCode
	}
	post(eve, url.Values{"auto_reserve": {"1"}})
	if set, _ := srv.CRM.StockSettings(ctx); set.AutoReserve {
		t.Fatal("an employee changed the settings")
	}
	if code = post(anna, url.Values{"auto_reserve": {"1"}, "reserve_days": {"7"}, "ship_on_win": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("save settings %d", code)
	}
	if set, _ := srv.CRM.StockSettings(ctx); set != (crm.StockSettings{AutoReserve: true, ReserveDays: 7, ShipOnWin: true}) {
		t.Fatalf("settings %+v", set)
	}
	if _, page = get(anna, "/store/settings"); !strings.Contains(page, `name="reserve_days" min="0" max="3650" value="7"`) {
		t.Fatal("settings page")
	}

	token, err := srv.Keys.Create(ctx, anna, "t", apikeys.AllScopes, time.Hour)
	must(err)
	api := func(path string, out any) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+"/api/v1/store/reports/"+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d", path, res.StatusCode)
		}
		must(json.NewDecoder(res.Body).Decode(out))
	}
	var stock []struct {
		ProductID     string
		Quantity, Sum float64
		On            map[string]float64 `json:"warehouses"`
	}
	if api("stock?date=2026-10-20&warehouse_id="+wh, &stock); len(stock) != 1 || stock[0].Quantity != 4 || stock[0].Sum != 1000 || stock[0].On[wh] != 4 {
		t.Fatalf("api stock %+v", stock)
	}
	var moves []struct{ Opening, In, Closing float64 }
	if api("moves?from=2026-10-01&to=2026-10-31", &moves); len(moves) != 1 || moves[0].In != 4 || moves[0].Closing != 4 {
		t.Fatalf("api moves %+v", moves)
	}
	var one struct {
		Opening float64
		Moves   []struct {
			DocID   string `json:"doc_id"`
			Balance float64
		}
	}
	if api("moves?product_id="+p+"&from=2026-10-01&to=2026-10-31", &one); len(one.Moves) != 1 || one.Moves[0].DocID != doc || one.Moves[0].Balance != 4 {
		t.Fatalf("api product moves %+v", one)
	}

	// «Продажи»: what the conducted shipments took, with the cost and the profit to the storekeeper only
	ship, err := srv.CRM.SaveStockDoc(ctx, anna, crm.StockDoc{Kind: "shipment", Date: day.AddDate(0, 0, 9), Rows: []crm.StockRow{{ProductID: p, Quantity: 1, Price: 400, From: wh}}})
	must(err)
	must(srv.CRM.ConductStockDoc(ctx, anna, ship))
	if _, page = get(anna, "/store/reports/sales?from=2026-10-01&to=2026-10-31"); !strings.Contains(page, "Лампа") || !strings.Contains(page, ">150<") || !strings.Contains(page, ">60<") {
		t.Fatal("sales report")
	}
	if _, page = get(eve, "/store/reports/sales?from=2026-10-01&to=2026-10-31"); !strings.Contains(page, ">400<") || strings.Contains(page, ">150<") {
		t.Fatal("the profit is shown to one not keeping the warehouse")
	}
	if _, page = get(anna, "/store/reports/sales?from=2026-09-01&to=2026-09-30"); strings.Contains(page, "Лампа") {
		t.Fatal("sales of another month")
	}
	if _, csv = get(anna, "/store/reports/sales?from=2026-10-01&to=2026-10-31&csv=1"); !strings.Contains(csv, "Лампа;L-7;;1;1;400;250;150;60") {
		t.Fatalf("sales csv %q", csv)
	}
	if _, csv = get(eve, "/store/reports/sales?from=2026-10-01&to=2026-10-31&csv=1"); !strings.HasSuffix(strings.TrimSpace(csv), "Лампа;L-7;;1;1;400") {
		t.Fatalf("sales csv without the cost %q", csv)
	}
	var sales []struct {
		Quantity, Revenue float64
		Cost, Profit      *float64
	}
	if api("sales?from=2026-10-01&to=2026-10-31", &sales); len(sales) != 1 || sales[0].Revenue != 400 || sales[0].Cost == nil || *sales[0].Profit != 150 {
		t.Fatalf("api sales %+v", sales)
	}

	// the supplier of an arrival: a contact of the CRM in the form and over REST
	ivan, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Иван Поставщиков", Responsible: crm.Ref{ID: anna}})
	must(err)
	if _, page = get(anna, "/store/docs/new?kind=arrival"); !strings.Contains(page, `value="contact:`+ivan+`"`) {
		t.Fatal("no contact among the suppliers")
	}
	body := `{"kind": "arrival", "supplier_contact_id": "` + ivan + `", "rows": [{"product_id": "` + p + `", "quantity": 1, "price": 10, "to_warehouse_id": "` + wh + `"}]}`
	req, _ := http.NewRequest("POST", hs.URL+"/api/v1/store/docs", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	must(err)
	var made struct {
		ID         string
		SupplierID string `json:"supplier_id"`
		ContactID  string `json:"supplier_contact_id"`
	}
	must(json.NewDecoder(res.Body).Decode(&made))
	res.Body.Close()
	if made.ContactID != ivan || made.SupplierID != "" {
		t.Fatalf("api arrival %d %+v", res.StatusCode, made)
	}
	if _, page = get(anna, "/store/docs/"+made.ID); !strings.Contains(page, `value="contact:`+ivan+`" selected`) {
		t.Fatal("the document's contact isn't chosen")
	}
}
