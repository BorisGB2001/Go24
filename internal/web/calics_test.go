package web

import (
	"bytes"
	"context"
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

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestEventICSHTTP: «Скачать .ics» of a repeated meeting (its rule, the deleted occurrence, the reminders), not of
// someone else's private one; «Копировать» — the form of a new meeting as that one; «Импорт» of the file into one's
// calendar, and the same file again changes the meeting instead of making it twice.
func TestEventICSHTTP(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@go24.test') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna, olga := user("Босс"), user("Анна"), user("Ольга")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: calendar.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.Local)
	ev, err := srv.Calendar.Save(ctx, boss, calendar.Event{Title: "Планёрка, отдел", Where: "3 этаж", Start: start, End: start.Add(time.Hour),
		Repeat: "weekly", Reminds: []int{15, 60}, Color: "#de2b24"}, []string{anna})
	must(err)
	must(srv.Calendar.DeleteOccurrence(ctx, boss, ev, start.AddDate(0, 0, 7), calendar.ScopeOne))
	secret, err := srv.Calendar.Save(ctx, boss, calendar.Event{Title: "Тайна", Start: start, End: start.Add(time.Hour), Private: true}, nil)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss), anna: cookie(anna), olga: cookie(olga)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who string, req *http.Request, want int) *http.Response {
		t.Helper()
		req.AddCookie(as[who])
		res, err := client.Do(req)
		must(err)
		if res.StatusCode != want {
			raw, _ := io.ReadAll(res.Body)
			t.Fatalf("%s %s %s: %d, want %d: %.300s", who, req.Method, req.URL.Path, res.StatusCode, want, raw)
		}
		return res
	}
	get := func(who, path string, want int) (string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("X-Go24-Form", "1")
		res := send(who, req, want)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return string(raw), res.Header
	}

	ics, h := get(anna, "/calendar/ics/"+ev, http.StatusOK)
	if !strings.HasPrefix(h.Get("Content-Type"), "text/calendar") || !strings.Contains(h.Get("Content-Disposition"), "attachment") {
		t.Fatalf("headers %v", h)
	}
	for _, want := range []string{"BEGIN:VEVENT", "UID:" + ev + "@go24", `SUMMARY:Планёрка\, отдел`, "RRULE:FREQ=WEEKLY;INTERVAL=1",
		"EXDATE:" + start.AddDate(0, 0, 7).UTC().Format("20060102T150405Z"), "TRIGGER:-PT15M", "TRIGGER:-PT60M", "LOCATION:3 этаж"} {
		if !strings.Contains(ics, want) {
			t.Fatalf("the file has no %q:\n%s", want, ics)
		}
	}
	get(olga, "/calendar/ics/"+secret, http.StatusNotFound) // someone else's private meeting
	get(olga, "/calendar/new?copy="+secret, http.StatusNotFound)

	form, _ := get(anna, "/calendar/new?copy="+ev, http.StatusOK)
	for _, want := range []string{`value="Планёрка, отдел"`, `value="3 этаж"`, `action="/calendar/new"`} {
		if !strings.Contains(form, want) {
			t.Fatalf("the copy's form has no %q", want)
		}
	}
	if c := copyOf(mustGet(t, srv, ctx, anna, ev), anna); c.ID != "" || c.OwnerID != anna || len(c.Attendees) != 1 || c.Attendees[0].ID != boss ||
		c.Repeat != "weekly" || c.Color != "#de2b24" || len(c.Reminds) != 2 {
		t.Fatalf("copy = %+v", c)
	}

	importFile := func() string {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", "boss.ics")
		_, _ = fw.Write([]byte(ics))
		must(mw.Close())
		req, _ := http.NewRequest("POST", hs.URL+"/calendar/import", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		res := send(anna, req, http.StatusSeeOther)
		res.Body.Close()
		loc, _ := url.Parse(res.Header.Get("Location"))
		if loc.Path != "/calendar" || loc.Query().Get("month") != "2026-11" {
			t.Fatalf("redirect %s", loc)
		}
		return loc.Query().Get("msg")
	}
	if msg := importFile(); !strings.Contains(msg, "1") {
		t.Fatalf("msg %q", msg)
	}
	var mine []string
	must(pool.QueryRow(ctx, `SELECT array_agg(id::text) FROM events WHERE owner_id = $1 AND import_uid = $2`, anna, ev+"@go24").Scan(&mine))
	if len(mine) != 1 {
		t.Fatalf("imported %v", mine)
	}
	got := mustGet(t, srv, ctx, anna, mine[0])
	if got.Title != "Планёрка, отдел" || got.Repeat != "weekly" || !got.Start.Equal(start) || len(got.Skip) != 1 || len(got.Attendees) != 0 ||
		len(got.Reminds) != 2 || got.Where != "3 этаж" {
		t.Fatalf("imported %+v", got)
	}
	if msg := importFile(); !strings.Contains(msg, "0") { // again: changed, not added
		t.Fatalf("msg %q", msg)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE owner_id = $1`, anna).Scan(&n))
	if n != 1 {
		t.Fatalf("anna has %d meetings after the second import", n)
	}
	if list := mail.ParseCalendar(ics); len(list) != 1 || len(list[0].Skip) != 1 {
		t.Fatalf("parsed %+v", list)
	}
}

func mustGet(t *testing.T, srv *Server, ctx context.Context, viewer, id string) calendar.Event {
	t.Helper()
	e, err := srv.Calendar.Get(ctx, viewer, id)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
