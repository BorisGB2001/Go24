package web

import (
	"context"
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

// TestStockBulkHTTP: the group actions of the documents list — conducting the checked ones goes from the earliest,
// so a deduct checked with the arrival it takes from is conducted after it; cancelling goes from the latest; deleting
// skips the conducted; «Копировать» opens a new draft with the document's details and rows.
func TestStockBulkHTTP(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wh, err := srv.CRM.SaveWarehouse(ctx, crm.Warehouse{Name: "Основной"})
	must(err)
	p, err := srv.CRM.SaveProduct(ctx, anna, crm.Product{Name: "Лампа"})
	must(err)
	day := time.Now().AddDate(0, 0, -2)
	// the deduct is made first but dated later than the arrival
	deduct, err := srv.CRM.SaveStockDoc(ctx, anna, crm.StockDoc{Kind: "deduct", Date: day.AddDate(0, 0, 1), Rows: []crm.StockRow{{ProductID: p, Quantity: 3, From: wh}}})
	must(err)
	arrival, err := srv.CRM.SaveStockDoc(ctx, anna, crm.StockDoc{Kind: "arrival", Date: day, Title: "Партия ламп", Rows: []crm.StockRow{{ProductID: p, Quantity: 5, Price: 100, To: wh}}})
	must(err)
	extra, err := srv.CRM.SaveStockDoc(ctx, anna, crm.StockDoc{Kind: "arrival", Date: day, Rows: []crm.StockRow{{ProductID: p, Quantity: 1, To: wh}}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(cookie)
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	post := func(path string, form url.Values) string {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		res, err := noRedirect.Do(req)
		must(err)
		res.Body.Close()
		return res.Header.Get("Location")
	}
	status := func(id string) string {
		t.Helper()
		d, err := srv.CRM.StockDoc(ctx, id)
		must(err)
		return d.Status
	}
	stock := func() float64 {
		t.Helper()
		m, err := srv.CRM.ProductStock(ctx, p)
		must(err)
		return m[wh]
	}

	if _, page := get("/store/docs"); !strings.Contains(page, `action="/store/docs/bulk"`) || !strings.Contains(page, `name="id" value="`+deduct+`"`) {
		t.Fatal("no group actions on the list")
	}
	loc := post("/store/docs/bulk", url.Values{"op": {"conduct"}, "id": {deduct, arrival}, "back": {"/store/docs?kind=deduct"}})
	if !strings.HasPrefix(loc, "/store/docs?kind=deduct&msg=") || status(deduct) != "conducted" || status(arrival) != "conducted" || stock() != 2 {
		t.Fatalf("conduct → %s, %s %s, stock %v", loc, status(deduct), status(arrival), stock())
	}
	// deleting skips the conducted ones, deletes the draft
	loc = post("/store/docs/bulk", url.Values{"op": {"delete"}, "id": {arrival, extra}})
	if u, _ := url.Parse(loc); !strings.Contains(u.Query().Get("msg"), "1") || status(arrival) != "conducted" {
		t.Fatalf("delete → %s", loc)
	}
	if _, err := srv.CRM.StockDoc(ctx, extra); err == nil {
		t.Fatal("the draft is not deleted")
	}
	// cancelling from the latest: the deduct goes back first, so the arrival can be cancelled too
	post("/store/docs/bulk", url.Values{"op": {"cancel"}, "id": {arrival, deduct}})
	if status(deduct) != "cancelled" || status(arrival) != "cancelled" || stock() != 0 {
		t.Fatalf("cancel: %s %s, stock %v", status(deduct), status(arrival), stock())
	}
	if loc := post("/store/docs/bulk", url.Values{"op": {"drop"}, "id": {arrival}}); !strings.Contains(loc, "err=") && !strings.Contains(loc, "msg=") {
		t.Fatalf("wrong op → %s", loc)
	}

	// «Копировать»
	if _, page := get("/store/docs/" + arrival); !strings.Contains(page, "copy="+arrival) {
		t.Fatal("no copy button")
	}
	code, page := get("/store/docs/new?kind=arrival&copy=" + arrival)
	if code != http.StatusOK || !strings.Contains(page, "Партия ламп") || !strings.Contains(page, `value="`+p+`"`) {
		t.Fatalf("copy page %d", code)
	}
	if code, _ := get("/store/docs/new?kind=arrival&copy=00000000-0000-0000-0000-000000000000"); code == http.StatusOK {
		t.Fatal("copy of nothing")
	}
}
