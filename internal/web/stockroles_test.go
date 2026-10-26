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

// TestStockRolesHTTP: «Права доступа» of «Складской учёт» — administrators only; an employee without roles reads
// the documents but makes none; a role of arrivals on one warehouse lets them make an arrival there only, with only
// that warehouse to choose, see only that warehouse's documents (on the page and over REST) and not delete them.
func TestStockRolesHTTP(t *testing.T) {
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
	main, err := srv.CRM.SaveWarehouse(ctx, crm.Warehouse{Name: "Основной"})
	must(err)
	north, err := srv.CRM.SaveWarehouse(ctx, crm.Warehouse{Name: "Северный"})
	must(err)
	p, err := srv.CRM.SaveProduct(ctx, anna, crm.Product{Name: "Лампа"})
	must(err)
	other, err := srv.CRM.SaveStockDoc(ctx, anna, crm.StockDoc{Kind: "arrival", Title: "Чужой", Rows: []crm.StockRow{{ProductID: p, Quantity: 1, To: main}}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna}))
		as[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(who, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(as[who])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	post := func(who, path string, form url.Values) string { // where it sends back
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(as[who])
		res, err := noRedirect.Do(req)
		must(err)
		res.Body.Close()
		return res.Header.Get("Location")
	}

	// without roles: reading, no new documents, no rights page
	if _, page := get(eve, "/store/docs"); !strings.Contains(page, "Чужой") || strings.Contains(page, "/store/docs/new?kind=") || strings.Contains(page, `href="/store/rights"`) {
		t.Fatal("eve's documents page")
	}
	if code, _ := get(eve, "/store/docs/new?kind=arrival"); code == http.StatusOK {
		t.Fatal("eve opens a new arrival")
	}
	post(eve, "/store/docs/new", url.Values{"op": {"save"}, "kind": {"arrival"}, "product": {p}, "quantity": {"1"}, "to": {north}})
	if list, _ := srv.CRM.StockDocs(ctx, crm.StockFilter{}); len(list) != 1 {
		t.Fatal("eve made a document")
	}
	if code, page := get(eve, "/store/rights"); code == http.StatusOK && strings.Contains(page, "stockroleform") {
		t.Fatal("eve sees the rights")
	}

	// the administrator makes a role of arrivals on the north warehouse and gives it to eve
	if code, page := get(anna, "/store/rights"); code != http.StatusOK || !strings.Contains(page, "Кладовщик") || !strings.Contains(page, "Все сотрудники") {
		t.Fatalf("rights page %d", code)
	}
	if loc := post(anna, "/store/rights/new", url.Values{"name": {"Север"}, "arrival.write": {"1"}, "arrival.conduct": {"1"}, "warehouse": {north}}); !strings.HasPrefix(loc, "/store/rights?msg=") {
		t.Fatalf("new role → %s", loc)
	}
	roles, err := srv.CRM.StockRoles(ctx)
	must(err)
	if len(roles) != 3 || roles[2].Name != "Север" || !roles[2].Perms.Has("arrival", "read") || len(roles[2].Perms.Warehouses) != 1 {
		t.Fatalf("roles %+v", roles)
	}
	post(anna, "/store/rights/"+roles[2].ID, url.Values{"op": {"add"}, "user_id": {eve}})
	post(eve, "/store/rights/"+roles[2].ID, url.Values{"op": {"remove"}, "user_id": {eve}}) // not hers to take
	if roles, _ = srv.CRM.StockRoles(ctx); len(roles[2].Members) != 1 {
		t.Fatalf("members %+v", roles[2].Members)
	}

	code, page := get(eve, "/store/docs/new?kind=arrival")
	if code != http.StatusOK || !strings.Contains(page, "Северный") || strings.Contains(page, ">Основной<") {
		t.Fatalf("eve's new arrival %d", code)
	}
	if loc := post(eve, "/store/docs/new", url.Values{"op": {"save"}, "kind": {"arrival"}, "product": {p}, "quantity": {"1"}, "to": {main}}); strings.HasPrefix(loc, "/store/docs/") && !strings.Contains(loc, "new") {
		t.Fatalf("eve made an arrival on the main warehouse → %s", loc)
	}
	loc := post(eve, "/store/docs/new", url.Values{"op": {"save_conduct"}, "kind": {"arrival"}, "title": {"Свой"}, "product": {p}, "quantity": {"2"}, "to": {north}})
	mine := strings.TrimPrefix(strings.Split(loc, "?")[0], "/store/docs/")
	d, err := srv.CRM.StockDoc(ctx, mine)
	if err != nil || d.Status != "conducted" {
		t.Fatalf("eve's arrival %q %+v %v", loc, d, err)
	}
	if _, page = get(eve, "/store/docs"); !strings.Contains(page, "Свой") || strings.Contains(page, "Чужой") {
		t.Fatal("eve sees the main warehouse's documents")
	}
	if code, _ = get(eve, "/store/docs/"+other); code == http.StatusOK {
		t.Fatal("eve opens the main warehouse's document")
	}
	if _, page = get(eve, "/store/docs/"+mine); strings.Contains(page, `value="cancel"`) || strings.Contains(page, `value="delete"`) {
		t.Fatal("eve may cancel or delete")
	}
	post(eve, "/store/docs/"+mine, url.Values{"op": {"cancel"}})
	if d, _ = srv.CRM.StockDoc(ctx, mine); d.Status != "conducted" {
		t.Fatal("eve cancelled the conducting")
	}

	// over REST: the same documents
	token, err := srv.Keys.Create(ctx, eve, "t", apikeys.AllScopes, time.Hour)
	must(err)
	api := func(method, path string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+"/api/v1/store/"+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}
	_, raw := api("GET", "docs")
	var docs []struct{ ID string }
	must(json.Unmarshal(raw, &docs))
	if len(docs) != 1 || docs[0].ID != mine {
		t.Fatalf("api docs %s", raw)
	}
	if code, _ := api("GET", "docs/"+other); code != http.StatusNotFound {
		t.Fatalf("api other %d", code)
	}
	if code, _ := api("POST", "docs/"+mine+"/cancel"); code != http.StatusForbidden {
		t.Fatalf("api cancel %d", code)
	}
}
