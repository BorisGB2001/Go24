package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMTileHTTP: «Настроить вид карточки» of the kanban — the standard tile (amount, deed,
// responsible); one's own fields in their order with names (a custom field, the client, observers), an empty one not
// shown; the administrator's common view for everybody, «Вернуть общий вид», «Сбросить общий вид»; a non-administrator
// may not make the common one; an unknown field is refused; the «+» of a column renders the tile by the view; leads
// have their own view; REST GET/PUT /api/v1/crm/tile.
func TestCRMTileHTTP(t *testing.T) {
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
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe := pipes[0]

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(s auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, s))
		return rec.Result().Cookies()[0]
	}
	annaC, bobC := cookieOf(auth.Session{UserID: anna, Admin: true}), cookieOf(auth.Session{UserID: bob})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	as := func(c *http.Cookie, path string, form url.Values) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(c)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	must(srv.Tasks.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Город", Kind: "string"}))
	var city string
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_deal_fields WHERE name = 'Город'`).Scan(&city))
	contact, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Пётр Клиентов", Responsible: crm.Ref{ID: anna}})
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Альфа", PipelineID: pipe.ID, Amount: 1500, Currency: "RUB",
		Responsible: crm.Ref{ID: bob}, Contact: crm.Ref{ID: contact}})
	must(err)
	must(srv.Tasks.SetFieldValuesOf(ctx, tasks.CRMFieldSets["deal"], deal, map[string]string{city: "Москва"}))
	must(srv.CRM.SetObservers(ctx, anna, "deal", deal, []string{anna}))
	// a deal without the city: its line is not shown
	_, err = srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Бета", PipelineID: pipe.ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	tileOf := func(page, title string) string { // the tile of the deal
		t.Helper()
		i := strings.Index(page, `class="ttitle">`+title+`<`)
		if i < 0 {
			t.Fatalf("no tile %s", title)
		}
		j := strings.LastIndex(page[:i], `<div class="kcard"`)
		k := strings.Index(page[i:], `<div class="kcard"`)
		if k < 0 {
			k = strings.Index(page[i:], `<div class="empty`)
		}
		return page[j : i+k]
	}
	before := func(s, a, b string) bool {
		i, j := strings.Index(s, a), strings.Index(s, b)
		return i >= 0 && j >= 0 && i < j
	}
	kanban := "/crm/deals?pipeline=" + pipe.ID

	// the standard tile: amount, the deed, the responsible
	_, page, _ := as(annaC, kanban, nil)
	tile := tileOf(page, "Альфа")
	if !strings.Contains(tile, crm.Amount(1500)) || !strings.Contains(tile, "nextact") || !strings.Contains(tile, "Борис") ||
		strings.Contains(tile, "Москва") || !strings.Contains(page, `data-crmtile`) || !strings.Contains(page, "Сделать общим видом") {
		t.Fatalf("standard tile %s", tile)
	}

	// anna's own: the city, the client, the observers, the responsible; no amount, no deed
	code, _, loc := as(annaC, "/crm/tile", url.Values{"scope": {"deal"}, "back": {kanban}, "cols": {"f:" + city, "client", "observers", "responsible"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, kanban+"&msg=") {
		t.Fatalf("save own: %d %s", code, loc)
	}
	_, page, _ = as(annaC, kanban, nil)
	tile = tileOf(page, "Альфа")
	if !before(tile, "Город:", "Москва") || !before(tile, "Москва", "Пётр Клиентов") || !before(tile, "Пётр Клиентов", "Анна") ||
		strings.Contains(tile, crm.Amount(1500)) || strings.Contains(tile, "nextact") || !strings.Contains(tile, "Борис") {
		t.Fatalf("own tile %s", tile)
	}
	if !strings.Contains(page, "Это ваш вид карточки") || !strings.Contains(page, "Сбросить</button>") {
		t.Fatal("own view in the gear")
	}
	// the gear lists the shown ones first in their order
	if !before(page, `value="f:`+city+`" checked`, `value="client" checked`) || !before(page, `value="responsible" checked`, `value="amount" >`) {
		t.Fatal("gear order")
	}
	// bob still has the standard one and may not make the common one, nor show an unknown field
	if _, page, _ = as(bobC, kanban, nil); strings.Contains(tileOf(page, "Альфа"), "Москва") || strings.Contains(page, "Сделать общим видом") {
		t.Fatal("bob took anna's view")
	}
	if code, _, _ := as(bobC, "/crm/tile", url.Values{"scope": {"deal"}, "cols": {"amount"}, "common": {"1"}}); code < 400 {
		t.Fatalf("bob's common view: %d", code)
	}
	saved := url.QueryEscape("Вид карточки сохранён") // a refused view goes back with the error instead
	if _, _, loc := as(bobC, "/crm/tile", url.Values{"scope": {"deal"}, "cols": {"password"}}); strings.Contains(loc, saved) {
		t.Fatalf("unknown field: %s", loc)
	}
	if _, _, loc := as(bobC, "/crm/tile", url.Values{"scope": {"contact"}, "cols": {"phone"}}); strings.Contains(loc, saved) {
		t.Fatalf("contacts have no kanban: %s", loc)
	}

	// the common one by anna: her own gives way, bob sees it
	code, _, _ = as(annaC, "/crm/tile", url.Values{"scope": {"deal"}, "back": {kanban}, "cols": {"amount", "f:" + city}, "common": {"1"}})
	if code != http.StatusSeeOther {
		t.Fatalf("save common: %d", code)
	}
	_, page, _ = as(bobC, kanban, nil)
	if tile = tileOf(page, "Альфа"); !before(tile, crm.Amount(1500), "Москва") || strings.Contains(tile, "Борис") || !strings.Contains(page, "Это общий вид карточки") {
		t.Fatalf("common tile %s", tile)
	}
	if !strings.Contains(tileOf(page, "Бета"), `<span class="kval"></span>`) { // hidden by the style
		t.Fatal("an empty field's line")
	}
	// bob's own, then «Вернуть общий вид»
	as(bobC, "/crm/tile", url.Values{"scope": {"deal"}, "cols": {"responsible"}})
	if _, page, _ = as(bobC, kanban, nil); strings.Contains(tileOf(page, "Альфа"), "Москва") || !strings.Contains(page, "Вернуть общий вид") {
		t.Fatal("bob's own view")
	}
	as(bobC, "/crm/tile", url.Values{"scope": {"deal"}, "reset": {"own"}})
	if _, page, _ = as(bobC, kanban, nil); !strings.Contains(tileOf(page, "Альфа"), "Москва") {
		t.Fatal("back to the common view")
	}
	// the «+» of a column renders the new tile by the view
	req, _ := http.NewRequest("POST", hs.URL+"/crm/deals/new", strings.NewReader(url.Values{"title": {"Гамма"}, "amount": {"700"},
		"pipeline": {pipe.ID}, "stage": {pipe.Stages[0].ID}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.AddCookie(bobC)
	res, err := client.Do(req)
	must(err)
	var quick struct{ Card string }
	must(json.NewDecoder(res.Body).Decode(&quick))
	res.Body.Close()
	if !strings.Contains(quick.Card, "Гамма") || !strings.Contains(quick.Card, "700") || !strings.Contains(quick.Card, "Город:") ||
		strings.Contains(quick.Card, "nextact") {
		t.Fatalf("quick tile %q", quick.Card)
	}
	// leads have their own view
	if _, page, _ = as(bobC, "/crm/leads", nil); !strings.Contains(page, `name="scope" value="lead"`) || strings.Contains(page, "Это общий вид карточки") {
		t.Fatal("leads' view")
	}
	// «Сбросить общий вид»
	as(annaC, "/crm/tile", url.Values{"scope": {"deal"}, "reset": {"common"}})
	if _, page, _ = as(bobC, kanban, nil); strings.Contains(tileOf(page, "Альфа"), "Москва") || !strings.Contains(tileOf(page, "Альфа"), "Борис") {
		t.Fatal("common reset")
	}

	// REST
	token, err := srv.Keys.Create(ctx, bob, "t", apikeys.AllScopes, time.Hour)
	must(err)
	call := func(method, path string, body any, out any) int {
		t.Helper()
		var rd io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			rd = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, hs.URL+"/api/v1/"+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if out != nil && res.StatusCode < 300 {
			must(json.Unmarshal(raw, out))
		}
		return res.StatusCode
	}
	var view crmTileJSON
	if code := call("GET", "crm/tile?scope=deal", nil, &view); code != 200 || !reflect.DeepEqual(view.Fields, crm.DefaultTile) || view.Own || view.Common {
		t.Fatalf("GET %d %+v", code, view)
	}
	if code := call("PUT", "crm/tile", map[string]any{"scope": "deal", "fields": []string{"client", "f:" + city, "client"}}, &view); code != 200 ||
		!reflect.DeepEqual(view.Fields, []string{"client", "f:" + city}) || !view.Own {
		t.Fatalf("PUT %d %+v", code, view)
	}
	if code := call("PUT", "crm/tile", map[string]any{"scope": "deal", "fields": []string{"amount"}, "common": true}, nil); code != http.StatusForbidden {
		t.Fatalf("PUT common by bob: %d", code)
	}
	if code := call("PUT", "crm/tile", map[string]any{"scope": "deal", "reset": true}, &view); code != 200 || view.Own {
		t.Fatalf("PUT reset %d %+v", code, view)
	}
}
