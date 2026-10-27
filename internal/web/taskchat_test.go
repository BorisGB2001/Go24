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
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestTaskChatMenu: the chat's message menu (GO24DEV-771) — an answer shows the quote, the author changes the
// message («изменено») and deletes it, someone else may do neither, an administrator deletes any; «Создать
// подзадачу» opens the subtask form with the message.
func TestTaskChatMenu(t *testing.T) {
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
	boss, anna, admin := newUser("Борис"), newUser("Анна"), newUser("Админ")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "AA", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "AA")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))
	key, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Собрать отчёт", ResponsibleID: anna, Priority: 1})
	must(err)
	id, err := ts.TaskIDByKey(ctx, key)
	must(err)

	srv := &Server{DB: pool, Tasks: ts, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who string, admin bool, method, path string, form url.Values) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Admin: admin, AdminMode: admin}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	msg := func(body string) (id, html string) {
		var m map[string]any
		must(json.Unmarshal([]byte(body), &m))
		id, _ = m["id"].(string)
		html, _ = m["html"].(string)
		return
	}

	_, body := do(boss, false, "POST", "/t/"+key+"/comments", url.Values{"body": {"Когда будет отчёт?"}})
	q, _ := msg(body)
	st, body := do(anna, false, "POST", "/t/"+key+"/comments", url.Values{"body": {"Завтра"}, "reply_to": {q}})
	a, html := msg(body)
	if st != http.StatusCreated || !strings.Contains(html, `class="im-quote" href="#m-`+q+`"`) || !strings.Contains(html, "Когда будет отчёт?") ||
		!strings.Contains(html, "data-msg-menu") {
		t.Fatalf("answer: %d %s", st, body)
	}

	if st, _ := do(boss, false, "POST", "/t/"+key+"/comments/"+a+"/edit", url.Values{"body": {"Сегодня"}}); st != http.StatusForbidden {
		t.Fatalf("someone else edits: %d", st)
	}
	st, body = do(anna, false, "POST", "/t/"+key+"/comments/"+a+"/edit", url.Values{"body": {"Сегодня вечером"}})
	if _, html = msg(body); st != http.StatusOK || !strings.Contains(html, "Сегодня вечером") || !strings.Contains(html, "изменено") {
		t.Fatalf("edit: %d %s", st, body)
	}

	// the card: the quote, «изменено», the menu with «Создать подзадачу» and «Удалить» for an administrator
	_, page := do(admin, true, "GET", "/t/"+key, nil)
	for _, want := range []string{`id="m-` + a + `"`, "изменено", `data-msgpop`, "data-any"} {
		if !strings.Contains(page, want) {
			t.Fatalf("card lacks %q", want)
		}
	}
	if _, page := do(anna, false, "GET", "/t/"+key, nil); strings.Contains(page, "data-any") || !strings.Contains(page, `href="/tasks/new?parent=`+key+`&comment="`) {
		t.Fatal("a participant: may delete any message, or has no «Создать подзадачу»")
	}
	if _, form := do(anna, false, "GET", "/tasks/new?parent="+key+"&comment="+a, nil); !strings.Contains(form, `value="Сегодня вечером"`) ||
		!strings.Contains(form, "Сообщение от Анна") || !strings.Contains(form, "/t/"+key+"#m-"+a) {
		t.Fatalf("subtask form of a message: %s", form)
	}

	if st, _ := do(anna, false, "POST", "/t/"+key+"/comments/"+q+"/delete", nil); st != http.StatusForbidden {
		t.Fatalf("a participant deletes someone else's message: %d", st)
	}
	if st, _ := do(admin, true, "POST", "/t/"+key+"/comments/"+q+"/delete", nil); st != http.StatusOK {
		t.Fatalf("an administrator deletes: %d", st)
	}
	if st, _ := do(anna, false, "POST", "/t/"+key+"/comments/"+a+"/delete", nil); st != http.StatusOK {
		t.Fatalf("the author deletes: %d", st)
	}
	c, err := ts.GetCard(ctx, boss, id)
	must(err)
	if len(c.Comments) != 0 {
		t.Fatalf("left: %+v", c.Comments)
	}
}
