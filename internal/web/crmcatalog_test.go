package web

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
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
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMCatalogHTTP: an administrator makes a section and a subsection and a product in it with a purchase price
// (and a picture, with the test S3); the catalog shows the tree, the crumbs and the section's products with its
// subsections'; an employee can't make sections and doesn't see the purchase price; the deal's picker groups the
// products by sections; REST lists the sections and a section's products.
func TestCRMCatalogHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s3ep := os.Getenv("GO24_TEST_S3")
	if s3ep != "" {
		s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
	}
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
	send := func(who, method, path, ctype string, body io.Reader) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		if form == nil {
			return send(who, method, path, "", nil)
		}
		return send(who, method, path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	}

	if code, _, _ := do(eve, "POST", "/crm/product-sections/new", url.Values{"name": {"Чужой"}}); code != http.StatusForbidden {
		t.Fatalf("an employee made a section: %d", code)
	}
	code, _, loc := do(anna, "POST", "/crm/product-sections/new", url.Values{"name": {"Оборудование"}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "section=") {
		t.Fatalf("section: %d %s", code, loc)
	}
	hw := strings.TrimPrefix(loc, "/crm/products?section=")
	_, _, loc = do(anna, "POST", "/crm/product-sections/new", url.Values{"name": {"Принтеры"}, "parent": {hw}})
	pr := strings.TrimPrefix(loc, "/crm/products?section=")

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range map[string]string{"name": "Принтер HP", "price": "1 000", "purchase_price": "700,50", "section": pr, "vat_included": "1"} {
		must(mw.WriteField(k, v))
	}
	if s3ep != "" {
		fw, _ := mw.CreateFormFile("photo", "hp.png")
		must(png.Encode(fw, image.NewRGBA(image.Rect(0, 0, 4, 4))))
	}
	must(mw.Close())
	if code, page, _ := send(anna, "POST", "/crm/products/new", mw.FormDataContentType(), &body); code != http.StatusSeeOther {
		t.Fatalf("product: %d %s", code, page)
	}
	list, err := srv.CRM.Products(ctx, "", true)
	must(err)
	if len(list) != 1 || list[0].SectionID != pr || list[0].PurchasePrice == nil || *list[0].PurchasePrice != 700.5 || list[0].Price != 1000 {
		t.Fatalf("saved %+v", list)
	}
	if s3ep != "" {
		if list[0].PhotoType != "image/png" {
			t.Fatalf("photo %q", list[0].PhotoType)
		}
		if code, img, _ := do(eve, "GET", "/crm/products/"+list[0].ID+"/photo", nil); code != http.StatusOK || !strings.HasPrefix(img, "\x89PNG") {
			t.Fatalf("photo file: %d", code)
		}
	}

	_, page, _ := do(anna, "GET", "/crm/products?section="+hw, nil)
	if !strings.Contains(page, "Принтер HP") || !strings.Contains(page, "Оборудование / Принтеры") || !strings.Contains(page, `class="crumbs"`) ||
		!strings.Contains(page, "700,50") {
		t.Fatal("the section misses its subsection's product, the crumbs or the purchase price")
	}
	_, page, _ = do(eve, "GET", "/crm/products?section="+pr, nil)
	if !strings.Contains(page, "Принтер HP") || strings.Contains(page, "700,50") || strings.Contains(page, "/crm/product-sections/") {
		t.Fatal("an employee: no product, or the purchase price, or the section's forms")
	}
	_, page, _ = do(anna, "GET", "/crm/deals/"+deal, nil)
	if !strings.Contains(page, `<optgroup label="Оборудование / Принтеры">`) {
		t.Fatal("the picker isn't grouped by sections")
	}
	if code, _, _ := do(anna, "POST", "/crm/product-sections/"+hw, url.Values{"name": {"Оборудование"}, "parent": {pr}}); code != http.StatusSeeOther {
		t.Fatalf("loop: %d", code)
	}
	if secs, _ := srv.CRM.Sections(ctx); secs[0].ParentID != "" {
		t.Fatal("a section went into its own subsection")
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
	var secs []struct{ ID, ParentID, Path string }
	get("/api/v1/crm/product-sections", &secs)
	if len(secs) != 2 || secs[1].Path != "Оборудование / Принтеры" {
		t.Fatalf("REST sections %+v", secs)
	}
	var prods []map[string]any
	get("/api/v1/crm/products?section_id="+hw, &prods)
	if len(prods) != 1 || prods[0]["section"] != "Оборудование / Принтеры" || prods[0]["purchase_price"] != nil {
		t.Fatalf("REST products %+v", prods)
	}

	if code, _, _ := do(anna, "POST", "/crm/product-sections/"+pr+"/delete", url.Values{"parent": {hw}}); code != http.StatusSeeOther {
		t.Fatalf("delete: %d", code)
	}
	if p, _ := srv.CRM.Product(ctx, list[0].ID); p.SectionID != hw {
		t.Fatalf("the product after its section's deletion: %q", p.SectionID)
	}

	// «Экспорт» and «Импорт» of the catalog: everyone exports (the purchase price — administrators), administrators
	// import; the headers are known in any language
	if code, csv, _ := do(eve, "GET", "/crm/products/export", nil); code != http.StatusOK || !strings.Contains(csv, "Принтер HP;Товар;;Оборудование;;1000") ||
		strings.Contains(csv, "Закупочная цена") {
		t.Fatalf("an employee's export: %d %s", code, csv)
	}
	if _, csv, _ := do(anna, "GET", "/crm/products/export?section="+hw, nil); !strings.Contains(csv, "Закупочная цена") || !strings.Contains(csv, "700,5") {
		t.Fatalf("an administrator's export: %s", csv)
	}
	upload := func(who, table string) (int, string) {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "p.csv")
		_, _ = fw.Write([]byte("\ufeff" + table))
		must(mw.WriteField("update", "1"))
		must(mw.Close())
		code, _, loc := send(who, "POST", "/crm/products/import", mw.FormDataContentType(), &body)
		return code, loc
	}
	if code, _ := upload(eve, "Name;Price\nX;1\n"); code != http.StatusForbidden {
		t.Fatalf("an employee imported: %d", code)
	}
	code, loc = upload(anna, "Name;Price;Section\nПринтер HP;1200;\nБумага;300;Расходники\n")
	if code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("import: %d %s", code, loc)
	}
	if msg, _ := url.QueryUnescape(loc); !strings.Contains(msg, "Создано товаров: 1, обновлено: 1") {
		t.Fatalf("import message %s", msg)
	}
	if p, _ := srv.CRM.Product(ctx, list[0].ID); p.Price != 1200 || p.SectionID != "" || p.PurchasePrice == nil {
		t.Fatalf("the imported product %+v", p)
	}
}
