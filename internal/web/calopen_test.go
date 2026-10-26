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

// TestOpenEventsHTTP: «Открытые события» — a category, an open event in it that everyone hears of and sees, joining
// and leaving it, a muted category, a closed one, a guest from outside who has none of it, deleting a category, REST.
func TestOpenEventsHTTP(t *testing.T) {
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, eve, bob, guest := user("Анна"), user("Ева"), user("Боб"), user("Гость")
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: calendar.NewStore(pool), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve, bob, guest} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
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
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	notified := func(who, kind, ev string) bool {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2 AND event_id::text = $3`, who, kind, ev).Scan(&n))
		return n > 0
	}
	start := time.Now().Truncate(time.Hour).AddDate(0, 0, 3)
	event := func(who, title, cat string) (int, string) {
		code, _, _ := do(who, "POST", "/calendar/new", url.Values{"title": {title}, "start": {start.Format("2006-01-02T15:04")},
			"end": {start.Add(time.Hour).Format("2006-01-02T15:04")}, "category_on": {"1"}, "category": {cat}, "private": {"on"}})
		var id string
		_ = pool.QueryRow(ctx, `SELECT id::text FROM events WHERE title = $1`, title).Scan(&id)
		return code, id
	}

	// a category: the calendar links to the page, the page has the form
	if _, page, _ := do(anna, "GET", "/calendar", nil); !strings.Contains(page, `href="/calendar/open"`) {
		t.Fatal("no «Открытые события» in the calendar")
	}
	if _, page, _ := do(guest, "GET", "/calendar", nil); strings.Contains(page, `href="/calendar/open"`) {
		t.Fatal("a guest has «Открытые события»")
	}
	_, _, loc := do(anna, "POST", "/calendar/open/categories", url.Values{"name": {"Обучение"}, "description": {"Курсы и семинары"}})
	var cat string
	must(pool.QueryRow(ctx, `SELECT id::text FROM event_categories WHERE name = 'Обучение'`).Scan(&cat))
	if !strings.HasPrefix(loc, "/calendar/open?cat="+cat) {
		t.Fatalf("category saved → %s", loc)
	}
	_, page, _ := do(anna, "GET", "/calendar/new?category="+cat, nil)
	if !strings.Contains(page, `<option value="`+cat+`" selected>Обучение</option>`) {
		t.Fatal("the new event's form has no category")
	}

	// an open event: everyone but the organizer hears of it (not the guest), it is never private
	code, ev := event(anna, "Семинар по продажам", cat)
	if code != http.StatusSeeOther || ev == "" {
		t.Fatalf("open event: %d", code)
	}
	if !notified(eve, "event_open", ev) || !notified(bob, "event_open", ev) || notified(anna, "event_open", ev) || notified(guest, "event_open", ev) {
		t.Fatal("the open event's notifications")
	}
	var private bool
	must(pool.QueryRow(ctx, `SELECT private FROM events WHERE id::text = $1`, ev).Scan(&private))
	if private {
		t.Fatal("a private open event")
	}
	_, page, _ = do(eve, "GET", "/calendar/open", nil)
	if !strings.Contains(page, `href="/calendar/`+ev+`"`) || !strings.Contains(page, `action="/calendar/`+ev+`/join"`) || !strings.Contains(page, "участников: 1") {
		t.Fatal("the open events page has no event to join")
	}
	if code, page, _ := do(eve, "GET", "/calendar/"+ev, nil); code != http.StatusOK || !strings.Contains(page, "Пойду") {
		t.Fatalf("the open event's page: %d", code)
	}
	if code, _, _ := do(guest, "GET", "/calendar/"+ev, nil); code == http.StatusOK {
		t.Fatal("a guest sees an open event")
	}
	if code, _, _ := do(guest, "GET", "/calendar/open", nil); code == http.StatusOK {
		t.Fatal("a guest has the open events")
	}

	// joining: Ева comes (the organizer hears of it), the event is in her calendar; then she does not come any more
	if _, _, loc := do(eve, "POST", "/calendar/"+ev+"/join", url.Values{"join": {"1"}, "back": {"/calendar/open?cat=" + cat}}); loc != "/calendar/open?cat="+cat {
		t.Fatalf("join → %s", loc)
	}
	var status string
	must(pool.QueryRow(ctx, `SELECT status FROM event_attendees WHERE event_id::text = $1 AND user_id = $2`, ev, eve).Scan(&status))
	if status != "accepted" || !notified(anna, "event_reply", ev) {
		t.Fatalf("joined: %s", status)
	}
	if list, _ := srv.Calendar.Range(ctx, eve, start.AddDate(0, 0, -1), start.AddDate(0, 0, 1)); len(list) != 1 {
		t.Fatalf("Ева's calendar %d", len(list))
	}
	_, page, _ = do(eve, "GET", "/calendar/open?cat="+cat, nil)
	if !strings.Contains(page, "Вы идёте") || !strings.Contains(page, "участников: 2") {
		t.Fatal("the page does not show Ева going")
	}
	if list, _ := srv.Calendar.Range(ctx, bob, start.AddDate(0, 0, -1), start.AddDate(0, 0, 1)); len(list) != 0 {
		t.Fatal("an open event in the calendar of one who has not joined")
	}
	do(eve, "POST", "/calendar/"+ev+"/join", url.Values{"join": {"0"}})
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM event_attendees WHERE event_id::text = $1`, ev).Scan(&n))
	if n != 0 {
		t.Fatal("left, still there")
	}
	// the organizer's form without the field keeps the category; an ordinary meeting cannot be joined
	if err := srv.Calendar.Join(ctx, eve, ev, true); err != nil {
		t.Fatal(err)
	}
	do(anna, "POST", "/calendar/"+ev, url.Values{"title": {"Семинар по продажам"}, "start": {start.Format("2006-01-02T15:04")},
		"end": {start.Add(time.Hour).Format("2006-01-02T15:04")}, "invite": {eve}})
	if e, _ := srv.Calendar.Get(ctx, anna, ev); e.CategoryID != cat || len(e.Attendees) != 1 {
		t.Fatalf("after the edit %q %+v", e.CategoryID, e.Attendees)
	}
	plain, err := srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Своя", Start: start, End: start.Add(time.Hour)}, nil)
	must(err)
	if err := srv.Calendar.Join(ctx, eve, plain, true); err != calendar.ErrNotFound {
		t.Fatalf("joined an ordinary meeting: %v", err)
	}

	// muted: no notification of the next event; a closed category takes events of its author only
	do(bob, "POST", "/calendar/open/categories/"+cat+"/mute", url.Values{"on": {"1"}})
	_, ev2 := event(anna, "Тренинг", cat)
	if notified(bob, "event_open", ev2) || !notified(eve, "event_open", ev2) {
		t.Fatal("a muted category")
	}
	if _, page, _ := do(bob, "GET", "/calendar/open?cat="+cat, nil); !strings.Contains(page, "Уведомлять") {
		t.Fatal("the muted category has no «Уведомлять»")
	}
	do(anna, "POST", "/calendar/open/categories/"+cat, url.Values{"name": {"Обучение"}, "closed": {"on"}})
	if code, _ := event(eve, "Чужое", cat); code == http.StatusSeeOther {
		t.Fatal("an event in someone else's closed category")
	}
	if code, _, _ := do(eve, "POST", "/calendar/open/categories/"+cat+"/delete", nil); code == http.StatusSeeOther {
		t.Fatal("someone else's category deleted")
	}

	// REST: the categories, the open events, joining
	token, err := srv.Keys.Create(ctx, bob, "t", apikeys.AllScopes, time.Hour)
	must(err)
	call := func(method, path string, body any, want int) any {
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
		var out any
		_ = json.Unmarshal(raw, &out)
		return out
	}
	cats := call("GET", "/api/v1/calendar/categories", nil, http.StatusOK).([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["muted"] != true || cats[0].(map[string]any)["upcoming"].(float64) != 2 {
		t.Fatalf("REST categories %v", cats)
	}
	open := call("GET", "/api/v1/calendar/open?category="+cat, nil, http.StatusOK).([]any)
	if len(open) != 2 || open[0].(map[string]any)["category"] != cat {
		t.Fatalf("REST open %v", open)
	}
	got := call("POST", "/api/v1/calendar/events/"+ev2+"/join", map[string]bool{"join": true}, http.StatusOK).(map[string]any)
	if got["my_status"] != "accepted" {
		t.Fatalf("REST join %v", got)
	}
	c := call("POST", "/api/v1/calendar/categories", map[string]any{"name": "Спорт"}, http.StatusCreated).(map[string]any)
	call("DELETE", "/api/v1/calendar/categories/"+c["id"].(string), nil, http.StatusNoContent)

	// the category deleted: its events stay as ordinary meetings
	do(anna, "POST", "/calendar/open/categories/"+cat+"/delete", nil)
	if e, err := srv.Calendar.Get(ctx, anna, ev); err != nil || e.CategoryID != "" {
		t.Fatalf("after the category %v %v", e.CategoryID, err)
	}
}
