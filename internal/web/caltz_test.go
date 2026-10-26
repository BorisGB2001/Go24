package web

import (
	"bytes"
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
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestEventTZHTTP: the meeting's time zone — the form takes the time by the clock of the zone chosen, shows it so
// again and, on the meeting's page, beside the portal's time; an unknown zone is not taken; REST shows and changes it.
func TestEventTZHTTP(t *testing.T) {
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
	ams, _ := time.LoadLocation("Europe/Amsterdam")
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = ams
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: calendar.NewStore(pool), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	rec = httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: eve}))
	eveCookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values) string {
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
		return string(raw)
	}

	page := do("GET", "/calendar/new", nil)
	if !strings.Contains(page, `name="tz"`) || !strings.Contains(page, `<option value="">Часовой пояс портала (Europe/Amsterdam)</option>`) ||
		!strings.Contains(page, `>(UTC&#43;09:00) Asia/Tokyo</option>`) {
		t.Fatal("the form has no time zone")
	}
	do("POST", "/calendar/new", url.Values{"title": {"Созвон с Токио"}, "start": {"2026-11-10T10:00"}, "end": {"2026-11-10T11:00"}, "tz": {"Asia/Tokyo"}, "invite": {eve}})
	var id, tz string
	var start time.Time
	must(pool.QueryRow(ctx, `SELECT id::text, timezone, starts_at FROM events WHERE title = 'Созвон с Токио'`).Scan(&id, &tz, &start))
	if tz != "Asia/Tokyo" || !start.Equal(time.Date(2026, 11, 10, 10, 0, 0, 0, tokyo)) {
		t.Fatalf("saved %s %v", tz, start)
	}
	page = do("GET", "/calendar/"+id, nil)
	for _, want := range []string{`name="start" value="2026-11-10T10:00"`, `<option value="Asia/Tokyo" selected>`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the meeting's form has no %q", want)
		}
	}
	cookie, eveCookie = eveCookie, cookie // the invited see both times
	page = do("GET", "/calendar/"+id, nil)
	cookie, eveCookie = eveCookie, cookie
	if !strings.Contains(page, "02:00 — 03:00") || !strings.Contains(page, "· 10.11 10:00 — 11:00 Asia/Tokyo") {
		t.Fatal("the meeting's page has no time of its zone")
	}
	do("POST", "/calendar/new", url.Values{"title": {"Марс"}, "start": {"2026-11-10T10:00"}, "end": {"2026-11-10T11:00"}, "tz": {"Mars/Base"}})
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE title = 'Марс'`).Scan(&n))
	if n != 0 {
		t.Fatal("an unknown zone taken")
	}
	// the portal's own zone is kept as none
	do("POST", "/calendar/"+id, url.Values{"title": {"Созвон с Токио"}, "start": {"2026-11-10T02:00"}, "end": {"2026-11-10T03:00"}, "tz": {"Europe/Amsterdam"}})
	must(pool.QueryRow(ctx, `SELECT timezone, starts_at FROM events WHERE id::text = $1`, id).Scan(&tz, &start))
	if tz != "" || !start.Equal(time.Date(2026, 11, 10, 2, 0, 0, 0, ams)) {
		t.Fatalf("portal's zone %q %v", tz, start)
	}

	// REST
	token, err := srv.Keys.Create(ctx, anna, "t", apikeys.AllScopes, time.Hour)
	must(err)
	call := func(method, path string, body any, want int) map[string]any {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, hs.URL+path, bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	call("PATCH", "/api/v1/calendar/events/"+id, map[string]any{"timezone": "America/New_York"}, http.StatusOK)
	if got := call("GET", "/api/v1/calendar/events/"+id, nil, http.StatusOK); got["timezone"] != "America/New_York" {
		t.Fatalf("REST timezone %v", got["timezone"])
	}
	call("PATCH", "/api/v1/calendar/events/"+id, map[string]any{"timezone": "Mars/Base"}, http.StatusUnprocessableEntity)
}
