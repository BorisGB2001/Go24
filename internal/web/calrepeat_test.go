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

// TestEventRepeatHTTP: a weekly meeting on chosen days that ends after N meetings (GO24DEV-682) — the form takes the
// days and the end and shows them again, the meeting's page tells them, the file of the meeting carries BYDAY and
// COUNT; REST shows and changes repeat_days and repeat_count, until drops the count.
func TestEventRepeatHTTP(t *testing.T) {
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

	// 02.11.2026 is a Monday: its day is checked in a new form
	if page := do("GET", "/calendar/new?start=2026-11-02T10:00", nil); !strings.Contains(page, `name="days" value="1" checked`) ||
		!strings.Contains(page, `name="ends"`) || !strings.Contains(page, `name="count"`) {
		t.Fatal("the form has no days or end")
	}
	do("POST", "/calendar/new", url.Values{"title": {"Йога"}, "start": {"2026-11-02T18:00"}, "end": {"2026-11-02T19:00"},
		"repeat": {"weekly"}, "every": {"1"}, "days": {"3", "5"}, "ends": {"count"}, "count": {"5"}, "until": {"2027-01-01"}, "invite": {eve}})
	var id string
	var days []int
	var count int
	var until time.Time
	must(pool.QueryRow(ctx, `SELECT id::text, repeat_days, repeat_count, repeat_until FROM events WHERE title = 'Йога'`).Scan(&id, &days, &count, &until))
	if len(days) != 3 || days[0] != 1 || count != 5 || until.Format("2006-01-02") != "2026-11-11" {
		t.Fatalf("saved %v %d %v", days, count, until)
	}
	page := do("GET", "/calendar/"+id, nil)
	for _, want := range []string{`name="days" value="3" checked`, `<option value="count" selected>`, `name="count" min="1" max="999" value="5"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the meeting's form has no %q", want)
		}
	}
	cookie, eveCookie = eveCookie, cookie // the invited read it
	page = do("GET", "/calendar/"+id, nil)
	cookie, eveCookie = eveCookie, cookie
	if !strings.Contains(page, "Еженедельно, пн, ср, пт — 5 повторений") {
		t.Fatal("the meeting's page does not tell the repetition")
	}
	if ics := do("GET", "/calendar/ics/"+id, nil); !strings.Contains(ics, "RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,WE,FR;COUNT=5") {
		t.Fatalf("the file: %s", ics)
	}
	// «Не заканчивать»: no count, no end
	do("POST", "/calendar/"+id, url.Values{"title": {"Йога"}, "start": {"2026-11-02T18:00"}, "end": {"2026-11-02T19:00"},
		"repeat": {"weekly"}, "every": {"1"}, "days": {"1", "3"}, "ends": {""}, "count": {"5"}, "until": {"2027-01-01"}})
	var noEnd bool
	must(pool.QueryRow(ctx, `SELECT repeat_days, repeat_count, repeat_until IS NULL FROM events WHERE id::text = $1`, id).Scan(&days, &count, &noEnd))
	if len(days) != 2 || count != 0 || !noEnd {
		t.Fatalf("never ends: %v %d %v", days, count, noEnd)
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
	call("PATCH", "/api/v1/calendar/events/"+id, map[string]any{"repeat_days": []int{2, 4}, "repeat_count": 4}, http.StatusOK)
	got := call("GET", "/api/v1/calendar/events/"+id, nil, http.StatusOK)
	if d, _ := got["repeat_days"].([]any); len(d) != 3 || got["repeat_count"] != 4.0 || !strings.HasPrefix(got["until"].(string), "2026-11-09") {
		t.Fatalf("REST %v %v %v", got["repeat_days"], got["repeat_count"], got["until"])
	}
	call("PATCH", "/api/v1/calendar/events/"+id, map[string]any{"until": "2026-12-31"}, http.StatusOK)
	if got := call("GET", "/api/v1/calendar/events/"+id, nil, http.StatusOK); got["repeat_count"] != nil {
		t.Fatalf("until keeps the count %v", got["repeat_count"])
	}
	call("PATCH", "/api/v1/calendar/events/"+id, map[string]any{"repeat_days": []int{9}}, http.StatusUnprocessableEntity)
}
