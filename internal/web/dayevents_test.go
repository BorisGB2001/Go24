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
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestDayEventsHTTP: «События» of the working day window — «Добавить событие» puts a meeting of today into the
// user's calendar (with a time, or for the whole day), a wrong time or no title comes back with the message; the
// window lists the user's meetings of today, whole days first, and not those of a colleague.
func TestDayEventsHTTP(t *testing.T) {
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
	user := func(sub, name string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	anna, olga := user("anna", "Анна Сотрудникова"), user("olga", "Ольга Коллегова")
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), I18n: bundle, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		Notify: notify.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, id := range []string{anna, olga} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		as[id] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
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
		resp, err := noRedirect.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, resp.StatusCode, want, b)
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			return loc
		}
		return string(b)
	}

	if loc := do(anna, "POST", "/dayplan/event", url.Values{"title": {"Планёрка"}, "start": {"09:00"}, "end": {"09:30"}, "back": {"/worktime"}}, http.StatusSeeOther); loc != "/worktime" {
		t.Fatalf("back: %q", loc)
	}
	do(anna, "POST", "/dayplan/event", url.Values{"title": {"Выездной день"}, "back": {"/worktime"}}, http.StatusSeeOther)
	do(anna, "POST", "/dayplan/event", url.Values{"title": {"Звонок клиенту"}, "start": {"15:00"}, "back": {"/worktime"}}, http.StatusSeeOther)
	if loc := do(anna, "POST", "/dayplan/event", url.Values{"title": {"Плохое"}, "start": {"25:99"}, "back": {"/worktime"}}, http.StatusSeeOther); !strings.Contains(loc, "msg=") {
		t.Fatalf("a wrong time is taken: %q", loc)
	}
	if loc := do(anna, "POST", "/dayplan/event", url.Values{"start": {"10:00"}, "back": {"//evil.example"}}, http.StatusSeeOther); !strings.HasPrefix(loc, "/worktime?msg=") {
		t.Fatalf("no title / foreign back: %q", loc)
	}

	y, m, d := time.Now().Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	list, err := srv.Calendar.Range(ctx, anna, day, day.AddDate(0, 0, 1))
	must(err)
	if len(list) != 3 {
		t.Fatalf("events: %+v", list)
	}
	for _, e := range list {
		switch e.Title {
		case "Планёрка":
			if !e.Start.Equal(day.Add(9*time.Hour)) || !e.End.Equal(day.Add(9*time.Hour+30*time.Minute)) {
				t.Fatalf("Планёрка: %v–%v", e.Start, e.End)
			}
		case "Звонок клиенту":
			if !e.End.Equal(e.Start.Add(time.Hour)) {
				t.Fatalf("no end — an hour: %v–%v", e.Start, e.End)
			}
		case "Выездной день":
			if !e.AllDay {
				t.Fatalf("no time — the whole day: %+v", e)
			}
		}
	}

	page := do(anna, "GET", "/worktime", nil, http.StatusOK)
	all, first, call := strings.Index(page, "Выездной день"), strings.Index(page, "Планёрка"), strings.Index(page, "Звонок клиенту")
	if all < 0 || first < 0 || call < 0 || !(all < first && first < call) {
		t.Fatalf("order in the window: %d %d %d", all, first, call)
	}
	if !strings.Contains(page, `action="/dayplan/event"`) {
		t.Fatal("no «Добавить событие»")
	}
	if page := do(olga, "GET", "/worktime", nil, http.StatusOK); strings.Contains(page, "Планёрка") {
		t.Fatal("a colleague sees the meetings of another")
	}
}
