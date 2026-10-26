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

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCalendarDavSync: a calendar of a CalDAV server connected two ways — go24's own server here: Anna connects
// Ivan's calendar «Проекты» by its address with his API key. The account's calendars are found from the server's
// address; the meetings (a series with a changed occurrence) come at once; what Anna adds, changes and deletes goes
// to the server, what Ivan does there comes back; when both changed a meeting, the server's version wins; the round of
// go24-worker runs after a change and not again; a wrong password is told; the password is kept sealed.
func TestCalendarDavSync(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@t') RETURNING id`).Scan(&ivan))
	cal := calendar.NewStore(pool)
	cal.Key = calendar.DavKey("secret")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: cal, Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	defer func(c *http.Client) { davHTTP = c }(davHTTP)
	davHTTP = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// Ivan's calendar on «the server»: a weekly series with a changed occurrence and a meeting
	projects, err := cal.SaveSection(ctx, ivan, calendar.Section{Name: "Проекты"})
	must(err)
	at := time.Date(2030, 3, 4, 9, 0, 0, 0, time.UTC)
	series, err := cal.Save(ctx, ivan, calendar.Event{Title: "Планёрка", Start: at, End: at.Add(time.Hour), Repeat: "weekly", Every: 1,
		CalendarID: projects}, nil)
	must(err)
	_, err = cal.SaveOccurrence(ctx, ivan, series, at.AddDate(0, 0, 7), calendar.ScopeOne,
		calendar.Event{Title: "Планёрка (перенесена)", Start: at.AddDate(0, 0, 7).Add(2 * time.Hour), End: at.AddDate(0, 0, 7).Add(3 * time.Hour), CalendarID: projects}, nil)
	must(err)
	_, err = cal.Save(ctx, ivan, calendar.Event{Title: "Обзор", Start: at.AddDate(0, 0, 1), End: at.AddDate(0, 0, 1).Add(time.Hour), CalendarID: projects}, nil)
	must(err)
	key, err := srv.Keys.Create(ctx, ivan, "caldav", []string{"calendar:read", "calendar:write"}, 0)
	must(err)
	titles := func(owner, calID string) string {
		var s string
		must(pool.QueryRow(ctx, `SELECT coalesce(string_agg(title, ',' ORDER BY title), '') FROM events WHERE owner_id::text = $1 AND calendar_id::text = $2`,
			owner, calID).Scan(&s))
		return s
	}

	// the server's address: its calendars are found (Ivan's main one and «Проекты»); a wrong password is told
	if _, err := srv.connectDav(ctx, anna, hs.URL+"/caldav/", "ivan", "wrong"); err == nil || !strings.Contains(err.Error(), "calendar_dav_auth") {
		t.Fatalf("a wrong password: %v", err)
	}
	c := &davClient{login: "ivan", password: key, hc: davHTTP}
	cals, err := c.discover(ctx, hs.URL)
	must(err)
	if len(cals) != 2 || cals[0].Name == cals[1].Name {
		t.Fatalf("found %+v", cals)
	}
	list, err := srv.connectDav(ctx, anna, hs.URL+"/caldav/"+projects+"/", "ivan", key)
	must(err)
	if len(list) != 1 || list[0].Name != "Проекты" || list[0].SyncError != "" || list[0].SyncedAt == nil {
		t.Fatalf("connected %+v", list)
	}
	mine := list[0].ID
	if got := titles(anna, mine); got != "Обзор,Планёрка,Планёрка (перенесена)" {
		t.Fatalf("loaded: %s", got)
	}
	var child int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE owner_id = $1 AND series_id IS NOT NULL`, anna).Scan(&child))
	if child != 1 {
		t.Fatal("the changed occurrence is not of the series")
	}
	var sealed []byte
	must(pool.QueryRow(ctx, `SELECT dav_secret FROM user_calendars WHERE id::text = $1`, mine).Scan(&sealed))
	if strings.Contains(string(sealed), key) {
		t.Fatal("the password kept as it is")
	}
	if _, err := srv.connectDav(ctx, anna, hs.URL+"/caldav/"+projects+"/", "ivan", key); err == nil {
		t.Fatal("the same calendar twice")
	}
	sync := func() {
		t.Helper()
		x, err := cal.Dav(ctx, anna, mine)
		must(err)
		if _, err := srv.syncDav(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	sync() // nothing changed: nothing goes to the server
	if got := titles(ivan, projects); got != "Обзор,Планёрка,Планёрка (перенесена)" {
		t.Fatalf("the server after a sync: %s", got)
	}

	// Anna: a new meeting, a changed one, a deleted occurrence of the series — on the server after the sync
	find := func(owner, calID, title string) calendar.Event {
		t.Helper()
		var id string
		must(pool.QueryRow(ctx, `SELECT id::text FROM events WHERE owner_id::text = $1 AND calendar_id::text = $2 AND title = $3`, owner, calID, title).Scan(&id))
		e, err := cal.Get(ctx, owner, id)
		must(err)
		return e
	}
	_, err = cal.Save(ctx, anna, calendar.Event{Title: "Звонок", Start: at.AddDate(0, 0, 2), End: at.AddDate(0, 0, 2).Add(time.Hour), CalendarID: mine}, nil)
	must(err)
	e := find(anna, mine, "Обзор")
	e.Title = "Обзор квартала"
	_, err = cal.Save(ctx, anna, e, nil)
	must(err)
	due, err := cal.DavDue(ctx, time.Now())
	must(err)
	if len(due) != 1 {
		t.Fatal("a change does not bring the round of go24-worker")
	}
	must(srv.SyncCalendarDav(ctx, time.Now()))
	if got := titles(ivan, projects); got != "Звонок,Обзор квартала,Планёрка,Планёрка (перенесена)" {
		t.Fatalf("the server after Anna's changes: %s", got)
	}
	must(srv.SyncCalendarDav(ctx, time.Now())) // the server's answer comes back once
	if due, _ = cal.DavDue(ctx, time.Now()); len(due) != 0 {
		t.Fatal("the round runs again and again")
	}
	if got := titles(anna, mine); got != "Звонок,Обзор квартала,Планёрка,Планёрка (перенесена)" {
		t.Fatalf("Anna after the round: %s", got)
	}

	// a changed occurrence in go24 goes with its series
	s := find(anna, mine, "Планёрка")
	_, err = cal.SaveOccurrence(ctx, anna, s.ID, at.AddDate(0, 0, 14), calendar.ScopeOne,
		calendar.Event{Title: "Планёрка у Анны", Start: at.AddDate(0, 0, 14).Add(time.Hour), End: at.AddDate(0, 0, 14).Add(2 * time.Hour), CalendarID: mine}, nil)
	must(err)
	sync()
	if got := titles(ivan, projects); got != "Звонок,Обзор квартала,Планёрка,Планёрка (перенесена),Планёрка у Анны" {
		t.Fatalf("the server after the occurrence: %s", got)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE owner_id = $1 AND series_id::text = $2`, ivan, series).Scan(&child))
	if child != 2 {
		t.Fatalf("the server's series has %d changed occurrences", child)
	}

	// Ivan on the server: a change and a deleted meeting come to Anna; both changed one — the server's wins
	z := find(ivan, projects, "Звонок")
	z.Title = "Звонок клиенту"
	_, err = cal.Save(ctx, ivan, z, nil)
	must(err)
	must(cal.Delete(ctx, ivan, find(ivan, projects, "Обзор квартала").ID))
	a := find(anna, mine, "Звонок")
	a.Title = "Звонок Анны"
	_, err = cal.Save(ctx, anna, a, nil)
	must(err)
	sync()
	if got := titles(anna, mine); got != "Звонок клиенту,Планёрка,Планёрка (перенесена),Планёрка у Анны" {
		t.Fatalf("Anna after Ivan's changes: %s", got)
	}
	if got := titles(ivan, projects); got != "Звонок клиенту,Планёрка,Планёрка (перенесена),Планёрка у Анны" {
		t.Fatalf("the server after the conflict: %s", got)
	}

	// Anna deletes the series: gone from the server; moved out of the calendar — gone too
	must(cal.Delete(ctx, anna, find(anna, mine, "Планёрка").ID))
	m := find(anna, mine, "Звонок клиенту")
	m.CalendarID = ""
	_, err = cal.Save(ctx, anna, m, nil)
	must(err)
	sync()
	if got := titles(ivan, projects); got != "" {
		t.Fatalf("the server after the deletes: %s", got)
	}
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM calendar_dav_items WHERE calendar_id::text = $1`, mine).Scan(&left))
	if left != 0 {
		t.Fatalf("%d resources remembered", left)
	}

	// the page shows it; «Синхронизировать»; the server stops taking the password — the error is seen, a new one mends it
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(p string, form url.Values) string {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+p, strings.NewReader(form.Encode()))
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := client.Do(req)
		must(err)
		res.Body.Close()
		msg, _ := url.QueryUnescape(res.Header.Get("Location"))
		return msg
	}
	if msg := post("/calendar/dav/"+mine+"/sync", url.Values{}); !strings.Contains(msg, "синхронизирован, встреч: 0") {
		t.Fatalf("sync now → %s", msg)
	}
	must(srv.Keys.Revoke(ctx, ivan, keyID(t, pool, ivan)))
	if msg := post("/calendar/dav/"+mine+"/sync", url.Values{}); !strings.Contains(msg, "не принял логин и пароль") {
		t.Fatalf("a revoked key → %s", msg)
	}
	req, _ := http.NewRequest("GET", hs.URL+"/calendar/sync", nil)
	req.AddCookie(cookie)
	res, err := client.Do(req)
	must(err)
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(page), `action="/calendar/dav/`+mine+`/password"`) || !strings.Contains(string(page), "не принял логин и пароль") {
		t.Fatal("the page does not tell the error")
	}
	key2, err := srv.Keys.Create(ctx, ivan, "caldav", []string{"calendar:read", "calendar:write"}, 0)
	must(err)
	if msg := post("/calendar/dav/"+mine+"/password", url.Values{"login": {"ivan"}, "password": {key2}}); !strings.Contains(msg, "синхронизирован") {
		t.Fatalf("a new password → %s", msg)
	}

	// disconnecting removes the calendar and its meetings, the server keeps them
	_, err = cal.Save(ctx, anna, calendar.Event{Title: "Последняя", Start: at, End: at.Add(time.Hour), CalendarID: mine}, nil)
	must(err)
	sync()
	must(cal.DeleteSection(ctx, anna, mine))
	if got := titles(ivan, projects); got != "Последняя" {
		t.Fatalf("the server after disconnecting: %s", got)
	}
}

func keyID(t *testing.T, pool *pgxpool.Pool, owner string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `SELECT id::text FROM api_keys WHERE user_id = $1 ORDER BY created_at LIMIT 1`, owner).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestDavDiscoverFallback: a server without the well-known address (Yandex) is asked at its top; a 403 there is a
// login the server does not take.
func TestDavDiscoverFallback(t *testing.T) {
	var asked []string
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path == "/.well-known/caldav" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer hs.Close()
	c := &davClient{login: "a", password: "b", hc: hs.Client()}
	_, err := c.discover(context.Background(), hs.URL)
	if !isKey(err, "err.calendar_dav_auth") || strings.Join(asked, " ") != "/.well-known/caldav /" {
		t.Fatalf("%v, asked %v", err, asked)
	}
}
