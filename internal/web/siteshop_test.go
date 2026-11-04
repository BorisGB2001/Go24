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

	"go24/internal/auth"
	"go24/internal/booking"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sites"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSiteShopHTTP: the block «Каталог товаров» of a site — it shows the active products of its section (a product
// with variations is chosen by them, an inactive one is not shown); the visitor's order becomes a deal «Заказ с
// сайта» in the block's pipeline with the products at the catalog's prices, of a new contact (the next order of the
// same phone — of the same contact), for the block's manager; a cart with a product not on sale or without a phone
// and an e-mail is refused with the reason on the page.
func TestSiteShopHTTP(t *testing.T) {
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
	var boss, seller string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('seller', 'Продавец', 'seller@go24.test') RETURNING id`).Scan(&seller))
	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Sites: sites.NewStore(pool), Booking: booking.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), I18n: bundle, pages: pages,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(auth bool, method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if auth {
			req.AddCookie(cookie)
		}
		req.Header.Set("Accept-Language", "ru")
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header.Get("Location")
	}

	lamps, err := srv.CRM.SaveSection(ctx, crm.Section{Name: "Лампы"})
	must(err)
	lamp, err := srv.CRM.SaveProduct(ctx, boss, crm.Product{Name: "Лампа настольная", Price: 1500, Currency: "RUB", Active: true, SectionID: lamps})
	must(err)
	shirt, err := srv.CRM.SaveProduct(ctx, boss, crm.Product{Name: "Абажур", Price: 900, Currency: "RUB", Active: true, SectionID: lamps})
	must(err)
	red, err := srv.CRM.SaveProduct(ctx, boss, crm.Product{Name: "Красный", Price: 950, Currency: "RUB", Active: true, ParentID: shirt, SectionID: lamps})
	must(err)
	off, err := srv.CRM.SaveProduct(ctx, boss, crm.Product{Name: "Снятая лампа", Price: 10, Currency: "RUB", SectionID: lamps})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE crm_products SET active = false WHERE id = $1`, off)
	must(err)
	other, err := srv.CRM.SaveProduct(ctx, boss, crm.Product{Name: "Стол", Price: 5000, Currency: "RUB", Active: true})
	must(err)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe := pipes[len(pipes)-1].ID

	_, siteURL := do(true, "POST", "/sites", url.Values{"name": {"Светлый дом"}}, 303)
	_, pageURL := do(true, "GET", siteURL, nil, 302)
	_, loc := do(true, "POST", pageURL+"/blocks", url.Values{"type": {"catalog"}}, 303)
	bid := loc[strings.Index(loc, "#b-")+3:]
	editor, _ := do(true, "GET", pageURL, nil, 200)
	if !strings.Contains(editor, `value="`+lamps+`"`) || !strings.Contains(editor, "Стол") || !strings.Contains(editor, "Оформить заказ") {
		t.Fatal("the editor of the catalog block")
	}
	do(true, "POST", pageURL+"/blocks/"+bid, url.Values{"title": {"Магазин"}, "section": {lamps}, "manager": {seller}, "pipeline": {pipe},
		"ask_name": {"on"}, "ask_phone": {"on"}, "ask_email": {"on"}, "ask_address": {"req"}}, 303)
	do(true, "POST", siteURL+"/publish", url.Values{"on": {"1"}}, 303)
	public, _ := do(false, "GET", "/site/svetlyy-dom", nil, 200)
	if !strings.Contains(public, "Лампа настольная") || !strings.Contains(public, `value="`+red+`"`) || strings.Contains(public, "Снятая") ||
		strings.Contains(public, "Стол") || !strings.Contains(public, `name="cart"`) || !strings.Contains(public, "siteshop.js") {
		t.Fatalf("the published catalog: %s", public)
	}

	hits := siteFormHits // more orders here than one address may send
	siteFormHits = &sites.Hits{Max: 100, Per: time.Minute}
	defer func() { siteFormHits = hits }()
	old := sites.FormStamp(time.Now().Add(-time.Minute))
	order := func(cart, phone, email string) string {
		t.Helper()
		_, loc := do(false, "POST", "/site/svetlyy-dom", url.Values{"block": {bid}, "t": {old}, "cart": {cart}, "name": {"Ира"},
			"phone": {phone}, "email": {email}, "address": {"Москва, Тверская 1"}}, 303)
		return loc
	}
	if loc := order(lamp+":2,"+other+":1", "+7 900 111-22-33", ""); !strings.Contains(loc, "err=err.site_cart_gone") {
		t.Fatalf("a product not of the block → %s", loc)
	}
	if loc := order(shirt+":1", "+7 900 111-22-33", ""); !strings.Contains(loc, "err=err.site_cart_gone") {
		t.Fatalf("a product with variations is ordered by them → %s", loc)
	}
	if loc := order("", "+7 900 111-22-33", ""); !strings.Contains(loc, "err=err.site_cart") {
		t.Fatalf("an empty cart → %s", loc)
	}
	if loc := order(lamp+":1", "", ""); !strings.Contains(loc, "err=err.site_cart_contact") {
		t.Fatalf("no phone, no e-mail → %s", loc)
	}
	if page, _ := do(false, "GET", "/site/svetlyy-dom?form="+bid+"&err=err.site_cart_gone", nil, 200); !strings.Contains(page, "больше нет в продаже") {
		t.Fatal("the refusal is not shown")
	}
	if loc := order(lamp+":2,"+red+":1,"+lamp+":1", "+7 900 111-22-33", ""); !strings.Contains(loc, "sent=1") {
		t.Fatalf("the order → %s", loc)
	}
	deals, err := srv.CRM.Deals(ctx, "", "", "", "", "")
	must(err)
	if len(deals) != 1 {
		t.Fatalf("deals: %d", len(deals))
	}
	d, err := srv.CRM.Deal(ctx, deals[0].ID)
	must(err)
	if d.Title != "Заказ с сайта «Светлый дом» — Ира" || d.PipelineID != pipe || d.Responsible.ID != seller || d.Origin.Code != "WEB" ||
		len(d.Contacts) != 1 || !strings.Contains(d.Comment, "Тверская") || d.Amount != 1500*3+950 {
		t.Fatalf("the deal %+v", d)
	}
	rows, err := srv.CRM.ProductRows(ctx, "deal", d.ID)
	must(err)
	if len(rows) != 2 || rows[0].ProductID != lamp || rows[0].Quantity != 3 || rows[1].Price != 950 {
		t.Fatalf("rows %+v", rows)
	}
	if page, _ := do(false, "GET", "/site/svetlyy-dom?form="+bid+"&sent=1", nil, 200); !strings.Contains(page, "Заказ принят") {
		t.Fatal("no thanks")
	}
	// the same phone again — the same contact
	order(red+":1", "+79001112233", "")
	deals, err = srv.CRM.Deals(ctx, "", "", "", "", "")
	must(err)
	if len(deals) != 2 {
		t.Fatalf("deals: %d", len(deals))
	}
	for _, x := range deals {
		dd, err := srv.CRM.Deal(ctx, x.ID)
		must(err)
		if len(dd.Contacts) != 1 || dd.Contacts[0].ID != d.Contacts[0].ID {
			t.Fatalf("contacts %+v vs %+v", dd.Contacts, d.Contacts)
		}
	}
	// the preview cannot be sent, the editor shows the goods
	if prev, _ := do(true, "GET", pageURL+"/preview", nil, 200); !strings.Contains(prev, "Лампа настольная") || !strings.Contains(prev, "disabled") {
		t.Fatal("the preview")
	}
}
