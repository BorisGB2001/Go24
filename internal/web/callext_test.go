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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCallExternal: nothing is offered until the administrators switch a service on; only they may (the key is
// kept sealed and never shown back). Telemost with the company's token makes a meeting by its API: «Видеовстреча»
// in a task posts the invitation into the task (someone outside it cannot), in a form only the link comes back.
// Telegram has no API: it is offered to whoever gave their own link, which goes into the invitation; a link not
// https is refused. A service switched off is offered to nobody.
func TestCallExternal(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, boss, outsider := newUser("Анна"), newUser("Админ"), newUser("Посторонний")
	names := map[string]string{anna: "Анна", boss: "Админ", outsider: "Посторонний"}
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "CE", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "CE")
	must(err)
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Созвон с клиентом", ResponsibleID: anna})
	must(err)
	taskID, err := ts.TaskIDByKey(ctx, key)
	must(err)

	var asked []string
	tm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		asked = append(asked, r.Header.Get("Authorization")+" "+string(b))
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"1","join_url":"https://telemost.yandex.ru/j/111"}`))
	}))
	defer tm.Close()
	ext := calls.NewExternal(pool, "secret")
	ext.TelemostAPI = tm.URL
	srv := &Server{DB: pool, Tasks: ts, External: ext, BaseURL: "http://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Language", "ru")
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: names[who], Email: names[who] + "@t", Admin: who == boss}))
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	offered := func(who string) string {
		t.Helper()
		_, body := do(who, "GET", "/call/external", nil)
		var d struct{ Services []struct{ Service string } }
		must(json.Unmarshal([]byte(body), &d))
		var list []string
		for _, s := range d.Services {
			list = append(list, s.Service)
		}
		return strings.Join(list, ",")
	}

	if o := offered(anna); o != "" {
		t.Fatalf("offered before anything is on: %q", o)
	}
	if st, _ := do(anna, "POST", "/settings/call-services/telemost", url.Values{"enabled": {"1"}, "secret": {"ytok"}}); st != http.StatusForbidden {
		t.Fatalf("not an administrator set a service: %d", st)
	}
	if st, _ := do(boss, "POST", "/settings/call-services/telemost", url.Values{"enabled": {"1"}, "secret": {"ytok"}}); st != http.StatusSeeOther {
		t.Fatalf("telemost: %d", st)
	}
	do(boss, "POST", "/settings/call-services/telegram", url.Values{"enabled": {"1"}})
	var sealed []byte
	must(pool.QueryRow(ctx, `SELECT secret FROM call_services WHERE service = 'telemost'`).Scan(&sealed))
	if strings.Contains(string(sealed), "ytok") {
		t.Fatal("the token is kept as it is")
	}
	if _, page := do(boss, "GET", "/settings/calls", nil); strings.Contains(page, "ytok") || !strings.Contains(page, "задан") {
		t.Fatalf("the settings page shows the token or not that it is set")
	}
	do(boss, "POST", "/settings/call-services/telemost", url.Values{"enabled": {"1"}}) // saved again without the token: it stays
	if o := offered(anna); o != "telemost" {
		t.Fatalf("offered %q", o)
	}

	if st, _ := do(outsider, "POST", "/call/external?task="+key, url.Values{"service": {"telemost"}}); st == http.StatusOK {
		t.Fatal("someone outside the task invited into it")
	}
	st, body := do(anna, "POST", "/call/external?task="+key, url.Values{"service": {"telemost"}})
	if st != http.StatusOK || !strings.Contains(body, "https://telemost.yandex.ru/j/111") || !strings.Contains(body, `"posted": true`) {
		t.Fatalf("telemost meeting: %d %s", st, body)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "OAuth ytok ") || !strings.Contains(asked[0], "Анна@t") {
		t.Fatalf("telemost was asked %v", asked)
	}
	var comment string
	must(pool.QueryRow(ctx, `SELECT body FROM task_comments WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, taskID).Scan(&comment))
	if comment != "Анна приглашает на видеовстречу (Яндекс Телемост): https://telemost.yandex.ru/j/111" {
		t.Fatalf("the task is told %q", comment)
	}
	if st, body := do(anna, "POST", "/call/external", url.Values{"service": {"telemost"}, "title": {"Встреча"}}); st != http.StatusOK || !strings.Contains(body, `"posted": false`) {
		t.Fatalf("a link for a form: %d %s", st, body)
	}

	if st, body := do(anna, "POST", "/call/external", url.Values{"service": {"telegram"}}); st == http.StatusOK || !strings.Contains(body, "недоступен") {
		t.Fatalf("telegram without a link: %d %s", st, body)
	}
	do(anna, "POST", "/settings/call-links", url.Values{"telegram": {"javascript:alert(1)"}})
	if links, _ := ext.Links(ctx, anna); len(links) != 0 {
		t.Fatalf("a link not https was kept: %v", links)
	}
	if st, _ := do(anna, "POST", "/settings/call-links", url.Values{"telegram": {"https://t.me/anna"}}); st != http.StatusSeeOther {
		t.Fatalf("own link: %d", st)
	}
	if _, page := do(anna, "GET", "/settings/call-links", nil); !strings.Contains(page, `value="https://t.me/anna"`) || strings.Contains(page, `name="zoom"`) {
		t.Fatal("«Мои видеосервисы» shows other services or not the link")
	}
	if o, other := offered(anna), offered(outsider); o != "telemost,telegram" || other != "telemost" {
		t.Fatalf("offered %q and %q", o, other)
	}
	if _, body := do(anna, "POST", "/call/external?task="+key, url.Values{"service": {"telegram"}}); !strings.Contains(body, "https://t.me/anna") {
		t.Fatalf("telegram: %s", body)
	}
	do(boss, "POST", "/settings/call-services/telegram", url.Values{})
	if o := offered(anna); o != "telemost" {
		t.Fatalf("a service switched off is offered: %q", o)
	}
}
