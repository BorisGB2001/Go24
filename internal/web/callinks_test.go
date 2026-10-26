package web

import (
	"context"
	"fmt"
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
	"go24/internal/tasks"
)

// TestCalendarLinksHTTP: a calendar connected by its iCal link — loaded at once into a calendar of its own, the
// meetings read-only; the next round of go24-worker brings the changes (a new, a changed, a gone one); a link that
// fails keeps the meetings and tells why; the same link twice is refused; disconnecting removes the meetings.
func TestCalendarLinksHTTP(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))

	// the outside calendar: two meetings, then one changed, one gone, one new; then it fails
	day := time.Now().AddDate(0, 0, 2).UTC().Format("20060102")
	vevent := func(uid, title, hour string) string {
		return "BEGIN:VEVENT\r\nUID:" + uid + "\r\nSUMMARY:" + title + "\r\nDTSTART:" + day + "T" + hour + "0000Z\r\nDTEND:" + day + "T" + hour + "3000Z\r\nEND:VEVENT\r\n"
	}
	feed := vevent("a@google", "Стендап", "08") + vevent("b@google", "Обед", "11")
	status := http.StatusOK
	outside := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n"+feed+"END:VCALENDAR\r\n")
	}))
	defer outside.Close()
	defer func(c *http.Client) { linkClient = c }(linkClient)
	linkClient = outside.Client()

	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: calendar.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	titles := func(cal string) string {
		var s string
		must(pool.QueryRow(ctx, `SELECT coalesce(string_agg(title, ',' ORDER BY starts_at), '') FROM events WHERE calendar_id::text = $1`, cal).Scan(&s))
		return s
	}

	link := strings.Replace(outside.URL, "https://", "webcal://", 1) + "/basic.ics"
	if _, page, _ := do("GET", "/calendar/sync", nil); !strings.Contains(page, `action="/calendar/links"`) {
		t.Fatal("no «Подключить календарь по ссылке»")
	}
	_, _, loc := do("POST", "/calendar/links", url.Values{"url": {link}, "name": {"Google"}})
	if msg, _ := url.QueryUnescape(loc); !strings.Contains(msg, "Календарь «Google» загружен, встреч: 2") {
		t.Fatalf("connected → %s", msg)
	}
	var cal, kept string
	must(pool.QueryRow(ctx, `SELECT id::text, source_url FROM user_calendars WHERE name = 'Google'`).Scan(&cal, &kept))
	if kept != strings.Replace(link, "webcal://", "https://", 1) || titles(cal) != "Стендап,Обед" {
		t.Fatalf("the calendar %q: %s", kept, titles(cal))
	}
	if _, _, loc := do("POST", "/calendar/links", url.Values{"url": {link}}); !strings.Contains(loc, "msg=") {
		t.Fatal("the same link twice")
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM user_calendars`).Scan(&n))
	if n != 1 {
		t.Fatalf("calendars %d", n)
	}

	// read-only: no form on the page, a change and a move are refused, a meeting cannot be put there
	var ev string
	must(pool.QueryRow(ctx, `SELECT id::text FROM events WHERE title = 'Стендап'`).Scan(&ev))
	_, page, _ := do("GET", "/calendar/"+ev, nil)
	if strings.Contains(page, `name="title"`) || !strings.Contains(page, "подключённого по ссылке") {
		t.Fatal("a connected meeting can be changed on its page")
	}
	e, err := srv.Calendar.Get(ctx, anna, ev)
	must(err)
	e.Title = "Своё"
	if _, err := srv.Calendar.Save(ctx, anna, e, nil); err != calendar.ErrForbidden {
		t.Fatalf("changed: %v", err)
	}
	start := time.Now().AddDate(0, 0, 3)
	if _, err := srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Моё", Start: start, End: start.Add(time.Hour), CalendarID: cal}, nil); err == nil {
		t.Fatal("a meeting put into a connected calendar")
	}

	// the worker's round: due after half an hour
	feed = vevent("a@google", "Стендап в 9", "09") + vevent("c@google", "Ретро", "15")
	must(srv.SyncCalendarLinks(ctx, time.Now()))
	if titles(cal) != "Стендап,Обед" {
		t.Fatal("loaded again before its time")
	}
	must(srv.SyncCalendarLinks(ctx, time.Now().Add(calendar.LinkEvery+time.Minute)))
	if titles(cal) != "Стендап в 9,Ретро" {
		t.Fatalf("after the round: %s", titles(cal))
	}
	var same int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id::text = $1`, ev).Scan(&same))
	if same != 1 {
		t.Fatal("the changed meeting is another one")
	}

	// it fails: the meetings stay, the page tells why
	status = http.StatusNotFound
	do("POST", "/calendar/links/"+cal+"/sync", nil)
	if titles(cal) != "Стендап в 9,Ретро" {
		t.Fatal("a failure removed the meetings")
	}
	if _, page, _ := do("GET", "/calendar/sync", nil); !strings.Contains(page, "Не удалось загрузить календарь (HTTP 404)") {
		t.Fatal("the failure is not shown")
	}
	if _, err := srv.Calendar.AddLink(ctx, anna, "", "ftp://x/y.ics"); err == nil {
		t.Fatal("an ftp link")
	}

	// disconnected: its meetings go
	if _, _, loc := do("POST", "/calendar/sections/"+cal+"/delete", url.Values{"back": {"/calendar/sync"}}); loc != "/calendar/sync" {
		t.Fatalf("disconnected → %s", loc)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events`).Scan(&n))
	if n != 0 {
		t.Fatal("the meetings stayed")
	}
}
