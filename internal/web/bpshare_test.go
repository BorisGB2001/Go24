package web

import (
	"bytes"
	"context"
	"encoding/json"
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
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestBPShareHTTP: «Кто может запускать» a template — a department (with its sub-departments) and a person chosen,
// the others neither see it on «Бизнес-процессы», in the feed's composer and in REST nor start it, an administrator
// always does; «Экспорт» gives the file and «Импорт» makes a new template of it or replaces one, finding the
// employees by e-mail (those not found — «кто запустил» or left out, counted) and the stages by code.
func TestBPShareHTTP(t *testing.T) {
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
	upload := func(path string, data []byte) (string, string) {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", "t.bp.json")
		_, _ = fw.Write(data)
		must(mw.Close())
		send(boss, "POST", path, mw.FormDataContentType(), &b, 303)
		u, err := url.Parse(last.Header.Get("Location"))
		must(err)
		return u.Path, u.Query().Get("msg")
	}

	vacation, err := srv.BP.SaveTemplate(ctx, boss, bp.Template{Name: "Отпуск", Entity: "none", Active: true,
		Fields: []bp.Field{{Key: "from", Label: "С", Kind: "date"}},
		Steps: []bp.Step{{Kind: "approve", To: anna, Users: []string{oleg, "manager"}, Text: "Согласуйте"},
			{Kind: "notify", To: oleg, Text: "Отпуск {from}"}, {Kind: "notify", To: "starter", Text: "Готово"}}})
	must(err)

	// everybody starts it while nobody is chosen
	if !strings.Contains(get(anna, "/bp"), "Отпуск") {
		t.Fatal("anna does not see the template")
	}
	// the company (with its sub-departments) and Oleg
	if p, msg := post(boss, "/bp/templates/"+vacation, url.Values{"op": {"starters"}, "to": {"dept:" + root, oleg}}); p != "/bp/templates/"+vacation || msg != "Сохранено" {
		t.Fatalf("starters: %s %q", p, msg)
	}
	page := get(boss, "/bp/templates/"+vacation)
	if !strings.Contains(page, "Компания") || !strings.Contains(page, "oleg") || !strings.Contains(page, "/bp/templates/"+vacation+"/export") {
		t.Fatal("the constructor misses «Кто может запускать» or «Экспорт»")
	}
	for who, sees := range map[string]bool{anna: false, eve: true, oleg: true, boss: true} {
		if strings.Contains(get(who, "/bp"), "/bp/start/"+vacation) != sees || strings.Contains(get(who, "/feed"), "/bp/start/"+vacation) != sees {
			t.Fatalf("%s sees the template: %v", who, !sees)
		}
	}
	if p, msg := post(anna, "/bp/start/"+vacation, url.Values{"f_from": {"2026-11-02"}}); !strings.HasPrefix(p, "/bp/start/") || msg != "Запускать этот процесс вам нельзя" {
		t.Fatalf("anna started it: %s %q", p, msg)
	}
	send(anna, "GET", "/bp/start/"+vacation, "", nil, 303)
	for _, who := range []string{eve, boss} {
		if p, msg := post(who, "/bp/start/"+vacation, url.Values{"f_from": {"2026-11-02"}}); !strings.HasPrefix(p, "/bp/i/") {
			t.Fatalf("%s does not start it: %s %q", who, p, msg)
		}
	}
	// REST: the same
	annaKey, err := keys.Create(ctx, anna, "k", []string{"bp:read", "bp:write"}, time.Hour)
	must(err)
	if strings.Contains(get(annaKey, "/api/v1/bp/templates"), vacation) {
		t.Fatal("REST lists the template to anna")
	}
	send(annaKey, "POST", "/api/v1/bp/templates/"+vacation+"/start", "application/json", strings.NewReader(`{"values":{}}`), 403)
	// nobody chosen again — everybody
	post(boss, "/bp/templates/"+vacation, url.Values{"op": {"starters"}})
	if !strings.Contains(get(anna, "/bp"), "/bp/start/"+vacation) {
		t.Fatal("the starters are not cleared")
	}

	// «Экспорт»: the employees by e-mail; administrators only
	send(anna, "GET", "/bp/templates/"+vacation+"/export", "", nil, 403)
	raw := get(boss, "/bp/templates/"+vacation+"/export")
	if !strings.Contains(last.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal("the file is not an attachment")
	}
	var f bp.File
	must(json.Unmarshal([]byte(raw), &f))
	if f.Format != "go24-bp" || f.Name != "Отпуск" || len(f.Steps) != 3 || f.People[anna] != "anna@go24.test" || f.People[oleg] != "oleg@go24.test" {
		t.Fatalf("export: %+v", f)
	}
	// as from another portal: other ids; Anna is found by her e-mail, Oleg is not there
	other := func(id string) string { return strings.Replace(id, id[:8], "00000000", 1) }
	f.People = map[string]string{other(anna): "ANNA@go24.test", other(oleg): "oleg@elsewhere.test"}
	f.Steps[0].To, f.Steps[0].Users, f.Steps[1].To = other(anna), []string{other(oleg), "manager"}, other(oleg)
	f.Name = "Отпуск (копия)"
	data, _ := json.Marshal(f)
	p, msg := upload("/bp/templates/new/import", data)
	if !strings.HasPrefix(p, "/bp/templates/") || p == "/bp/templates/"+vacation || !strings.Contains(msg, "не найдено сотрудников: 2") {
		t.Fatalf("import: %s %q", p, msg)
	}
	copied, err := srv.BP.Template(ctx, strings.TrimPrefix(p, "/bp/templates/"))
	must(err)
	if copied.Name != "Отпуск (копия)" || !copied.Active || copied.Steps[0].To != anna || len(copied.Steps[0].Users) != 1 ||
		copied.Steps[0].Users[0] != "manager" || copied.Steps[1].To != "starter" || copied.Fields[0].Key != "from" {
		t.Fatalf("imported: %+v", copied)
	}
	// over a template: its activity stays
	copied.Active = false
	_, err = srv.BP.SaveTemplate(ctx, boss, copied)
	must(err)
	f.Name = "Отпуск 2"
	data, _ = json.Marshal(f)
	if p, _ := upload("/bp/templates/"+copied.ID+"/import", data); p != "/bp/templates/"+copied.ID {
		t.Fatalf("import over: %s", p)
	}
	if again, _ := srv.BP.Template(ctx, copied.ID); again.Name != "Отпуск 2" || again.Active {
		t.Fatalf("import over: %+v", again)
	}
	if _, msg := upload("/bp/templates/new/import", []byte(`{"name":"x"}`)); msg != "Это не файл шаблона бизнес-процесса go24" {
		t.Fatalf("a wrong file: %q", msg)
	}

	// a stage is found by its code, a smart process by its name
	stages, err := srv.CRM.LeadStages(ctx)
	must(err)
	lead, err := srv.BP.SaveTemplate(ctx, boss, bp.Template{Name: "Лид", Entity: "lead", Active: true,
		Steps: []bp.Step{{Kind: "stage", Stage: stages[1].ID}}})
	must(err)
	must(json.Unmarshal([]byte(get(boss, "/bp/templates/"+lead+"/export")), &f))
	f.Steps[0].Stage = other(stages[1].ID)
	f.Stages = map[string]string{f.Steps[0].Stage: "code:" + stages[1].Code}
	data, _ = json.Marshal(f)
	p, msg = upload("/bp/templates/new/import", data)
	got, err := srv.BP.Template(ctx, strings.TrimPrefix(p, "/bp/templates/"))
	if err != nil || got.Steps[0].Stage != stages[1].ID {
		t.Fatalf("the stage: %s %q %+v", p, msg, got)
	}
	f.Stages = map[string]string{f.Steps[0].Stage: "name:Нет такой"}
	data, _ = json.Marshal(f)
	if _, msg := upload("/bp/templates/new/import", data); msg != "Шаг 1: на портале нет такой стадии" {
		t.Fatalf("a missing stage: %q", msg)
	}
	f.Entity, f.Type, f.Steps = "deal", "Ремонт", []bp.Step{{Kind: "notify", To: "starter", Text: "x"}}
	data, _ = json.Marshal(f)
	if _, msg := upload("/bp/templates/new/import", data); msg != "Нет смарт-процесса «Ремонт» — создайте его и повторите импорт" {
		t.Fatalf("a missing smart process: %q", msg)
	}
}
