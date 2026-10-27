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
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestListsHTTP: «Списки» — an administrator makes a list with fields on its settings page (a non-administrator may
// not), closes it to a department; an employee of it adds an element by the form (a required field empty — back with
// the message), the table finds and shows it, the CSV has it, the card shows its history and the templates of the list
// to start; one outside the department sees nothing; REST reads the lists and adds, changes and deletes elements.
func TestListsHTTP(t *testing.T) {
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
	user := func(name string, admin bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ($1, $1, $1 || '@go24.test', $2) RETURNING id`,
			name, admin).Scan(&id))
		return id
	}
	boss, anna, eve, oleg := user("boss", true), user("anna", false), user("eve", false), user("oleg", false)
	_ = oleg
	var root, sales string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Продажи', $1) RETURNING id`, root).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, eve, sales)
	must(err)

	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, CRM: crm.NewStore(pool), Company: company.NewStore(pool),
		I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Feed, srv.Notify = feed.NewStore(pool, srv.Tasks), notify.NewStore(pool)
	srv.BP = bp.NewStore(pool, srv.Tasks, srv.CRM, srv.Company)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true})}
	for _, u := range []string{anna, eve, oleg} {
		as[u] = cookie(auth.Session{UserID: u})
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var last *http.Response
	send := func(who, method, path, ctype string, body io.Reader, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if c := as[who]; c != nil {
			req.AddCookie(c)
		} else {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("Accept-Language", "ru")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		last = res
		return string(raw)
	}
	get := func(who, path string) string { t.Helper(); return send(who, "GET", path, "", nil, 200) }
	// post: the redirect's path and message
	post := func(who, path string, v url.Values) (string, string) {
		t.Helper()
		send(who, "POST", path, "application/x-www-form-urlencoded", strings.NewReader(v.Encode()), 303)
		u, err := url.Parse(last.Header.Get("Location"))
		must(err)
		return u.Path, u.Query().Get("msg")
	}

	// the settings: an administrator's
	send(anna, "GET", "/bp/lists/new/settings", "", nil, 403)
	p, msg := post(boss, "/bp/lists/new/settings", url.Values{"op": {"meta"}, "name": {"Заявки на оплату"}, "description": {"Из бухгалтерии"},
		"f_key": {"sum", "who", "paid", ""}, "f_label": {"Сумма", "Получатель", "Оплачено", ""}, "f_kind": {"number", "user", "bool", "string"},
		"f_req": {"1", "", "", ""}, "f_opts": {"", "", "", ""}, "f_def": {"", "", "", ""}})
	list := strings.TrimSuffix(strings.TrimPrefix(p, "/bp/lists/"), "/settings")
	if len(list) != 36 || msg != "Сохранено" {
		t.Fatalf("new list: %s %q", p, msg)
	}
	page := get(boss, p)
	for _, want := range []string{"Заявки на оплату", `name="col" value="sum" checked`, "/bp/templates/new?list=" + list} {
		if !strings.Contains(page, want) {
			t.Fatalf("settings page lacks %q", want)
		}
	}
	post(boss, p, url.Values{"op": {"read"}, "to": {"dept:" + sales}})
	tpl, err := srv.BP.SaveTemplate(ctx, boss, bp.Template{Name: "Согласование заявки", Entity: bp.EntityList, ListID: list, Active: true,
		Steps: []bp.Step{{Kind: "approve", To: "responsible", Text: "{title}: {list.sum}"}}})
	must(err)
	if page := get(boss, "/bp/templates/"+tpl); !strings.Contains(page, `value="list:`+list+`" selected`) {
		t.Fatal("the constructor does not show the template's list")
	}

	// an element by the form
	if strings.Contains(get(anna, "/bp/lists"), "Заявки на оплату") {
		t.Fatal("anna, not of «Продажи», sees the list")
	}
	send(anna, "GET", "/bp/lists/"+list, "", nil, 404)
	if !strings.Contains(get(eve, "/bp/lists"), "Заявки на оплату") {
		t.Fatal("eve does not see the list")
	}
	if p, msg := post(eve, "/bp/lists/"+list+"/add", url.Values{"name": {"Без суммы"}, "f_paid": {"N"}}); p != "/bp/lists/"+list+"/add" || msg == "" {
		t.Fatalf("an element without the required sum: %s %q", p, msg)
	}
	if p, _ := post(eve, "/bp/lists/"+list+"/add", url.Values{"name": {"Командировка"}, "f_sum": {"1500"}, "f_who": {oleg}, "f_paid": {"Y", "N"}}); p != "/bp/lists/"+list {
		t.Fatalf("added: %s", p)
	}
	table := get(eve, "/bp/lists/"+list+"?q=командир&sort=sum&desc=1")
	for _, want := range []string{"Командировка", "1500", "oleg", "Да"} {
		if !strings.Contains(table, want) {
			t.Fatalf("the table lacks %q", want)
		}
	}
	var el string
	must(pool.QueryRow(ctx, `SELECT id::text FROM list_elements WHERE name = 'Командировка'`).Scan(&el))
	csv := get(eve, "/bp/lists/"+list+"/export")
	if !strings.HasPrefix(csv, "\uFEFF") || !strings.Contains(csv, "Название;Сумма;Получатель;Оплачено") || !strings.Contains(csv, "Командировка;1500;oleg;Да;eve") {
		t.Fatalf("csv: %q", csv)
	}
	if p, msg := post(eve, "/bp/el/"+el, url.Values{"name": {"Командировка в Казань"}, "f_sum": {"2000"}, "f_who": {oleg}, "f_paid": {"Y", "N"}}); p != "/bp/el/"+el || msg != "Сохранено" {
		t.Fatalf("changed: %s %q", p, msg)
	}
	card := get(eve, "/bp/el/"+el)
	for _, want := range []string{"Командировка в Казань", "1500", "2000", "Элемент добавлен", "/bp/start/" + tpl + "?entity_id=" + el} {
		if !strings.Contains(card, want) {
			t.Fatalf("the card lacks %q", want)
		}
	}
	send(anna, "GET", "/bp/el/"+el, "", nil, 404)
	if p, _ := post(eve, "/bp/start/"+tpl, url.Values{"entity_id": {el}}); !strings.HasPrefix(p, "/bp/i/") {
		t.Fatalf("started from the card: %s", p)
	}

	// REST
	eveKey, err := keys.Create(ctx, eve, "k", []string{"lists:read", "lists:write"}, time.Hour)
	must(err)
	annaKey, err := keys.Create(ctx, anna, "k", []string{"lists:read", "lists:write"}, time.Hour)
	must(err)
	if got := get(annaKey, "/api/v1/lists"); got != "[]\n" && got != "[]" {
		t.Fatalf("anna's lists: %s", got)
	}
	var lists []struct {
		ID       string     `json:"id"`
		Fields   []bp.Field `json:"fields"`
		Elements int        `json:"elements"`
		CanWrite bool       `json:"can_write"`
	}
	must(json.Unmarshal([]byte(get(eveKey, "/api/v1/lists")), &lists))
	if len(lists) != 1 || lists[0].ID != list || len(lists[0].Fields) != 3 || lists[0].Elements != 1 || !lists[0].CanWrite {
		t.Fatalf("REST lists: %+v", lists)
	}
	var made struct {
		ID     string            `json:"id"`
		Values map[string]string `json:"values"`
	}
	send(eveKey, "POST", "/api/v1/lists/"+list+"/elements", "application/json", strings.NewReader(`{"name":"Бумага","values":{"sum":"abc"}}`), 422)
	must(json.Unmarshal([]byte(send(eveKey, "POST", "/api/v1/lists/"+list+"/elements", "application/json",
		strings.NewReader(`{"name":"Бумага","values":{"sum":"300"}}`), 201)), &made))
	must(json.Unmarshal([]byte(send(eveKey, "PATCH", "/api/v1/lists/"+list+"/elements/"+made.ID, "application/json",
		strings.NewReader(`{"values":{"paid":"Y"}}`), 200)), &made))
	if made.Values["sum"] != "300" || made.Values["paid"] != "Y" {
		t.Fatalf("patched: %v", made.Values)
	}
	if got := get(eveKey, "/api/v1/lists/"+list+"/elements?sort=sum"); !strings.Contains(got, "Бумага") || strings.Index(got, "Бумага") > strings.Index(got, "Казань") {
		t.Fatalf("elements by sum: %s", got)
	}
	send(annaKey, "DELETE", "/api/v1/lists/"+list+"/elements/"+made.ID, "", nil, 404)
	send(eveKey, "DELETE", "/api/v1/lists/"+list+"/elements/"+made.ID, "", nil, 204)
	send(eveKey, "GET", "/api/v1/lists/"+list+"/elements/"+made.ID, "", nil, 404)

	// the list deleted, with its elements
	post(boss, "/bp/lists/"+list+"/delete", nil)
	send(eve, "GET", "/bp/el/"+el, "", nil, 404)
}
