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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMRolesHTTP: «Права доступа» of the CRM — an administrator gives a role adding, export and
// import levels and a pipeline's own ones, gives it to a department; its employee sees no «Экспорт» and «Импорт»,
// gets no CSV, creates deals where the role lets add and not in the pipeline that does not.
func TestCRMRolesHTTP(t *testing.T) {
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
	var boss, anna, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, sales)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wholesale, err := srv.CRM.AddPipeline(ctx, "Опт")
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for u, sess := range map[string]auth.Session{boss: {UserID: boss, Admin: true, AdminMode: true}, anna: {UserID: anna}} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
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
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	form := url.Values{"name": {"Менеджер"}}
	for _, e := range []string{"deal", "lead", "contact", "company"} {
		for a, l := range map[string]string{"read": "own", "add": "own", "write": "own", "delete": "none", "export": "none", "import": "none"} {
			form.Set(e+"."+a, l)
		}
	}
	form.Set("deal:"+wholesale+".add", "none")
	if code, _ := do(boss, "POST", "/crm/roles/new", form); code != http.StatusSeeOther {
		t.Fatalf("new role: %d", code)
	}
	var role string
	must(pool.QueryRow(ctx, `SELECT id FROM crm_roles WHERE name = 'Менеджер'`).Scan(&role))
	if code, _ := do(boss, "POST", "/crm/roles/"+role+"/members", url.Values{"user_id": {"dept:" + sales}}); code != http.StatusSeeOther {
		t.Fatalf("a department as a member: %d", code)
	}
	_, page := do(boss, "GET", "/crm/roles", nil)
	for _, want := range []string{"Продажи", `name="deal:` + wholesale + `.add"`, `name="deal.export"`, `name="lead.import"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the rights page has no %s", want)
		}
	}

	_, page = do(anna, "GET", "/crm/leads", nil)
	if strings.Contains(page, "/crm/export/leads") || strings.Contains(page, "/crm/import/leads") {
		t.Fatal("«Экспорт» or «Импорт» shown without the rights")
	}
	if code, body := do(anna, "GET", "/crm/export/leads", nil); code == http.StatusOK && strings.Contains(body, ";") {
		t.Fatal("a CSV exported without the right")
	}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	main := pipes[0].ID
	if main == wholesale {
		main = pipes[1].ID
	}
	do(anna, "POST", "/crm/deals/new", url.Values{"title": {"Своя"}, "pipeline": {main}})
	do(anna, "POST", "/crm/deals/new", url.Values{"title": {"Оптовая"}, "pipeline": {wholesale}})
	var made []string
	rows, err := pool.Query(ctx, `SELECT title FROM crm_deals ORDER BY title`)
	must(err)
	for rows.Next() {
		var s string
		must(rows.Scan(&s))
		made = append(made, s)
	}
	if strings.Join(made, ",") != "Своя" {
		t.Fatalf("deals made: %v", made)
	}
}
