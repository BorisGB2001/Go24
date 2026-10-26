package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestCardDAVHTTP: the employees over CardDAV — only with an API key that may «company:read»; the top is the person
// and their address book home with the book «Сотрудники go24»; the book has a card of each working employee (not
// the dismissed, nor guests), read by PROPFIND, multiget, query and GET with their versions; a changed card changes
// its version and the book's; nothing is written. «Экспорт в vCard» of the list and «Визитка» of a profile.
func TestCardDAVHTTP(t *testing.T) {
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
	var anna, ivan, fired, gost string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, last_name, first_name, position, mobile_phone, inner_phone)
		VALUES ('anna', 'Анна Иванова', 'anna@go24.test', 'Иванова', 'Анна', 'Бухгалтер', '+79000000001', '101') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван Петров', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, active) VALUES ('fired', 'Уволенный', 'fired@go24.test', false) RETURNING id`).Scan(&fired))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ('gost', 'Гость Коллаба', 'gost@go24.test', true) RETURNING id`).Scan(&gost))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	key := func(who string, scopes ...string) string {
		k, err := srv.Keys.Create(ctx, who, "carddav", scopes, 0)
		must(err)
		return k
	}
	readKey, calKey := key(anna, "company:read"), key(anna, "calendar:read")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: ivan}))
	cookie := rec.Result().Cookies()[0]
	do := func(token, method, p string, hdr map[string]string, body string, want int) (string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+p, strings.NewReader(body))
		if token == "cookie" {
			req.AddCookie(cookie)
		} else if token != "" {
			req.SetBasicAuth("anyone", token)
		}
		req.Header.Set("Accept-Language", "ru")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.500s", method, p, res.StatusCode, want, raw)
		}
		return string(raw), res.Header
	}
	has := func(what, text string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(text, w) {
				t.Errorf("%s has no %q: %.2500s", what, w, text)
			}
		}
	}
	hasNot := func(what, text string, not ...string) {
		t.Helper()
		for _, w := range not {
			if strings.Contains(text, w) {
				t.Errorf("%s has %q: %.2500s", what, w, text)
			}
		}
	}
	depth0, depth1 := map[string]string{"Depth": "0"}, map[string]string{"Depth": "1"}

	if _, h := do("", "PROPFIND", "/.well-known/carddav", nil, "", 301); h.Get("Location") != "/carddav/" {
		t.Errorf("well-known leads to %q", h.Get("Location"))
	}
	do("", "PROPFIND", "/carddav/", depth0, "", 401)
	do(calKey, "PROPFIND", "/carddav/", depth0, "", 401)
	do(readKey, "PROPFIND", "/carddav/other/", depth0, "", 404)

	top, _ := do(readKey, "PROPFIND", "/carddav/", depth1, `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav" xmlns:cs="http://calendarserver.org/ns/">
		<d:prop><d:resourcetype/><d:displayname/><d:current-user-principal/><card:addressbook-home-set/><cs:getctag/></d:prop></d:propfind>`, 207)
	has("top", top, "<card:addressbook-home-set><d:href>/carddav/</d:href>", "<d:href>/carddav/employees/</d:href>",
		"<card:addressbook/>", "Сотрудники go24", "<cs:getctag>")

	book, _ := do(readKey, "PROPFIND", "/carddav/employees/", depth1, `<?xml version="1.0"?><d:propfind xmlns:d="DAV:" xmlns:cs="http://calendarserver.org/ns/">
		<d:prop><d:getetag/><d:getcontenttype/><cs:getctag/></d:prop></d:propfind>`, 207)
	has("book", book, "/carddav/employees/"+anna+".vcf", "/carddav/employees/"+ivan+".vcf", "text/vcard")
	hasNot("book", book, fired, gost)
	ctag := book[strings.Index(book, "<cs:getctag>"):]
	ctag = ctag[:strings.Index(ctag, "</cs:getctag>")]

	card, h := do(readKey, "GET", "/carddav/employees/"+anna+".vcf", nil, "", 200)
	has("card", card, "BEGIN:VCARD", "N:Иванова;Анна;;;", "TITLE:Бухгалтер", "TEL;TYPE=CELL,VOICE:+79000000001",
		"item1.TEL;TYPE=WORK:101\r\nitem1.X-ABLabel:Внутренний телефон", "EMAIL;TYPE=INTERNET,WORK:anna@go24.test")
	etag := h.Get("ETag")
	has("book etag", book, strings.Trim(etag, `"`))
	do(readKey, "GET", "/carddav/employees/"+fired+".vcf", nil, "", 404)
	do(readKey, "PUT", "/carddav/employees/"+anna+".vcf", nil, card, 403)
	do(readKey, "DELETE", "/carddav/employees/"+anna+".vcf", nil, "", 403)

	multi, _ := do(readKey, "REPORT", "/carddav/employees/", depth1, `<?xml version="1.0"?>
		<card:addressbook-multiget xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/><card:address-data/></d:prop>
		<d:href>/carddav/employees/`+ivan+`.vcf</d:href><d:href>/carddav/employees/nobody.vcf</d:href></card:addressbook-multiget>`, 207)
	has("multiget", multi, "FN:Иван Петров", "HTTP/1.1 404")
	hasNot("multiget", multi, "Иванова")
	query, _ := do(readKey, "REPORT", "/carddav/employees/", depth1, `<?xml version="1.0"?>
		<card:addressbook-query xmlns:d="DAV:" xmlns:card="urn:ietf:params:xml:ns:carddav"><d:prop><d:getetag/></d:prop></card:addressbook-query>`, 207)
	has("query", query, anna+".vcf", ivan+".vcf")
	do(readKey, "REPORT", "/carddav/employees/", depth1, `<?xml version="1.0"?><d:sync-collection xmlns:d="DAV:"/>`, 403)

	// a changed card: its version and the book's change
	_, err = pool.Exec(ctx, `UPDATE users SET position = 'Главный бухгалтер' WHERE id = $1`, anna)
	must(err)
	_, h = do(readKey, "GET", "/carddav/employees/"+anna+".vcf", nil, "", 200)
	book2, _ := do(readKey, "PROPFIND", "/carddav/employees/", depth0, "", 207)
	if h.Get("ETag") == etag || strings.Contains(book2, ctag+"<") {
		t.Errorf("versions did not change: %s %s", h.Get("ETag"), book2)
	}

	// the files: the list (by the filter) and one card
	vcf, h := do("cookie", "GET", "/company?format=vcf", nil, "", 200)
	if !strings.HasPrefix(h.Get("Content-Type"), "text/vcard") || strings.Count(vcf, "BEGIN:VCARD") != 2 {
		t.Errorf("export: %s %s", h.Get("Content-Type"), vcf)
	}
	vcf, _ = do("cookie", "GET", "/company?format=vcf&q=Иванова", nil, "", 200)
	if strings.Count(vcf, "BEGIN:VCARD") != 1 || !strings.Contains(vcf, "Иванова") {
		t.Errorf("filtered export: %s", vcf)
	}
	vcf, h = do("cookie", "GET", "/u/"+anna+"/vcard", nil, "", 200)
	has("visit card", vcf+h.Get("Content-Disposition"), "FN:Иванова Анна", "attachment", ".vcf")
}
