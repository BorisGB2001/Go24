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
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailMyDayHTTP drives «Мой день» of the mail against the test database: the button over the mail, the day's
// meetings (one's own, the ones one is invited to — not a colleague's own), ‹ › to another day, one's open tasks by
// their deadlines (not a colleague's, not the done ones), a task completed from the pane, and the drop targets.
func TestMailMyDayHTTP(t *testing.T) {
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
	user := func(sub, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	anna, eve := user("anna", "Анна"), user("eve", "Ева")
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Mail: mail.NewStore(pool, "x"), Calendar: calendar.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err = srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	tomorrow := today.AddDate(0, 0, 1)
	meet := func(owner, title string, at time.Time, invite ...string) {
		_, err := srv.Calendar.Save(ctx, owner, calendar.Event{Title: title, Where: "Зал", Start: at, End: at.Add(time.Hour)}, invite)
		must(err)
	}
	meet(anna, "Планёрка Анны", tomorrow.Add(10*time.Hour))
	meet(eve, "Обзор Евы", tomorrow.Add(14*time.Hour), anna)
	meet(eve, "Чужая встреча", tomorrow.Add(16*time.Hour))
	meet(anna, "Через неделю", today.AddDate(0, 0, 7).Add(10*time.Hour))

	_, err = ts.CreateProject(ctx, anna, "GO", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "GO")
	must(err)
	task := func(title, resp string, deadline *time.Time) string {
		key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: title, ResponsibleID: resp, Deadline: deadline})
		must(err)
		return key
	}
	at := func(t time.Time) *time.Time { return &t }
	late := task("Просроченный отчёт", anna, at(now.Add(-24*time.Hour)))
	task("Счёт сегодня", anna, at(today.Add(23*time.Hour+59*time.Minute)))
	task("План через три дня", anna, at(today.AddDate(0, 0, 3).Add(12*time.Hour)))
	task("Когда-нибудь", anna, nil)
	task("Задача Евы", eve, nil)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	do := func(who, method, path string, form url.Values, accept string, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, b)
		}
		return string(b)
	}

	if page := do(anna, "GET", "/mail", nil, "", 200); !strings.Contains(page, "data-myday-toggle") || !strings.Contains(page, `id="mmyday"`) || !strings.Contains(page, "js/mailday.js") {
		t.Fatal("the mail has no «Мой день»")
	}
	day := tomorrow.Format(time.DateOnly)
	pane := do(anna, "GET", "/mail/myday?day="+day, nil, "text/html", 200)
	for _, want := range []string{"Планёрка Анны", "Обзор Евы", "ждёт ответа", "10:00–11:00",
		`data-myday-day="` + today.Format(time.DateOnly) + `"`, `data-myday-day="` + tomorrow.AddDate(0, 0, 1).Format(time.DateOnly) + `"`,
		`data-myday-drop="/calendar/new?date=` + day + `&mail="`, `data-myday-drop="/tasks/new?mail="`, `href="/calendar/new?date=` + day + `"`,
		"Просроченные", "Просроченный отчёт", "На сегодня", "Счёт сегодня", "Позже", "План через три дня", "Без срока", "Когда-нибудь",
		`data-myday-done="` + late + `"`} {
		if !strings.Contains(pane, want) {
			t.Fatalf("«Мой день» of %s has no %q:\n%s", day, want, pane)
		}
	}
	for _, not := range []string{"Чужая встреча", "Через неделю", "Задача Евы", ">Сегодня, "} {
		if strings.Contains(pane, not) {
			t.Fatalf("«Мой день» of %s shows %q", day, not)
		}
	}
	if pane := do(anna, "GET", "/mail/myday", nil, "text/html", 200); !strings.Contains(pane, "Сегодня, ") || strings.Contains(pane, "Планёрка Анны") || !strings.Contains(pane, "На этот день встреч нет") {
		t.Fatalf("«Мой день» of today:\n%s", pane)
	}
	// eve: her own meeting and task, nobody else's
	if pane := do(eve, "GET", "/mail/myday?day="+day, nil, "text/html", 200); !strings.Contains(pane, "Чужая встреча") || strings.Contains(pane, "Планёрка Анны") ||
		!strings.Contains(pane, "Задача Евы") || strings.Contains(pane, "Когда-нибудь") {
		t.Fatalf("«Мой день» of eve:\n%s", pane)
	}
	// a letter dropped on the calendar: a meeting of it on the day shown; on the tasks: a task of it
	var inbox, order string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) SELECT id, 'INBOX', 'inbox', true FROM mailboxes RETURNING id::text`).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, thread)
		SELECT mailbox_id, id, 1, 'ivan@client.test', 'Иван', 'anna@go24.test', 'Заказ на 10 коробок', 'До пятницы', now(), 'order' FROM mail_folders WHERE id::text = $1
		RETURNING id::text`, inbox).Scan(&order))
	if f := do(anna, "GET", "/calendar/new?date="+day+"&mail="+order, nil, "", 200); !strings.Contains(f, `name="start" value="`+day+"T") || !strings.Contains(f, "Заказ на 10 коробок") {
		t.Fatal("a letter dropped on the calendar of «Мой день» is not a meeting on its day")
	}
	if f := do(anna, "GET", "/tasks/new?mail="+order, nil, "", 200); !strings.Contains(f, `name="mail" value="`+order) {
		t.Fatal("a letter dropped on the tasks of «Мой день» is not a task")
	}
	// the box of a task: it is done and leaves the pane
	do(anna, "POST", "/t/"+late+"/action", url.Values{"action": {"complete"}}, "application/json", 200)
	if pane := do(anna, "GET", "/mail/myday?day="+day, nil, "text/html", 200); strings.Contains(pane, "Просроченный отчёт") || strings.Contains(pane, "Просроченные") {
		t.Fatal("a completed task stays in «Мой день»")
	}
}
