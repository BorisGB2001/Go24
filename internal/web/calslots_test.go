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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCalendarSlotsHTTP: «Поделиться слотами» — the personal link set up to 9–12 by the hour, a joint one with a
// colleague offers only the time both are free; a guest books a free time (not a taken one, not without a contact,
// not as a robot): the meeting is the owner's with the colleague, both are told; the guest's page, its calendar file
// and cancelling it remove the meeting; a switched off link is gone; REST lists and changes the links.
func TestCalendarSlotsHTTP(t *testing.T) {
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
	boss, anna := user("Борис"), user("Анна")
	keys := apikeys.NewStore(pool)
	bossKey, err := keys.Create(ctx, boss, "k", []string{"calendar:read", "calendar:write"}, time.Hour)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, Calendar: calendar.NewStore(pool), BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	now := time.Now()
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 2)
	day := d.Format("2006-01-02")
	_, err = srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Планёрка", Start: d.Add(10 * time.Hour), End: d.Add(11 * time.Hour)}, nil)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(signed bool, method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if signed {
			req.AddCookie(cookie)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header.Get("Location")
	}
	has := func(page string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Fatalf("the page has no %q", w)
			}
		}
	}
	hasnt := func(page string, not ...string) {
		t.Helper()
		for _, w := range not {
			if strings.Contains(page, w) {
				t.Fatalf("the page has %q", w)
			}
		}
	}
	every := url.Values{"length": {"60"}, "days_ahead": {"30"}, "active": {"on"}, "from_0": {"540"}, "to_0": {"720"},
		"days_0": {"1", "2", "3", "4", "5", "6", "7"}, "from_1": {"540"}, "to_1": {"1080"}}

	// the personal link is made on the first visit, from the calendar settings
	page, _ := do(true, "GET", "/calendar/slots", nil, http.StatusOK)
	var personal, ptok string
	must(pool.QueryRow(ctx, `SELECT id::text, token FROM calendar_slot_links WHERE owner_id = $1`, boss).Scan(&personal, &ptok))
	has(page, "https://go24.test/cs/"+ptok, "60 мин")
	do(true, "POST", "/calendar/slots/"+personal+"/save", every, http.StatusSeeOther)
	bad := url.Values{"length": {"60"}, "days_ahead": {"30"}, "from_0": {"600"}, "to_0": {"630"}, "days_0": {"1"}}
	if _, loc := do(true, "POST", "/calendar/slots/"+personal+"/save", bad, http.StatusSeeOther); !strings.Contains(loc, "edit="+personal) {
		t.Fatalf("a span shorter than a meeting is taken: %s", loc)
	}
	// a joint link needs a colleague
	if _, loc := do(true, "POST", "/calendar/slots", every, http.StatusSeeOther); !strings.Contains(loc, "edit=new") {
		t.Fatalf("a joint link without colleagues: %s", loc)
	}
	joint := url.Values{"members": {anna}}
	for k, v := range every {
		joint[k] = v
	}
	do(true, "POST", "/calendar/slots", joint, http.StatusSeeOther)
	var jtok string
	must(pool.QueryRow(ctx, `SELECT token FROM calendar_slot_links WHERE owner_id = $1 AND members <> '{}'`, boss).Scan(&jtok))
	has(func() string { p, _ := do(true, "GET", "/calendar/slots", nil, http.StatusOK); return p }(), "Анна", "https://go24.test/cs/"+jtok)

	// the personal link: every hour of 9–12; the joint one — not 10, when Anna is busy
	page, _ = do(false, "GET", "/cs/"+ptok+"?date="+day, nil, http.StatusOK)
	has(page, "Встреча с Борис", ">09:00<", ">10:00<", ">11:00<")
	hasnt(page, ">12:00<", "Планёрка")
	page, _ = do(false, "GET", "/cs/"+jtok+"?date="+day, nil, http.StatusOK)
	has(page, ">09:00<", ">11:00<", "Анна")
	hasnt(page, ">10:00<")
	page, _ = do(false, "GET", "/cs/"+jtok+"?date="+day+"&t=11:00", nil, http.StatusOK)
	has(page, `name="contact"`)

	guest := url.Values{"date": {day}, "t": {"10:00"}, "name": {"Иван"}, "contact": {"ivan@client.test"}, "note": {"Обсудить договор"}}
	if _, loc := do(false, "POST", "/cs/"+jtok, guest, http.StatusSeeOther); !strings.HasPrefix(loc, "/cs/"+jtok+"?") || strings.Contains(loc, "t=10") {
		t.Fatalf("a busy time is taken: %s", loc)
	}
	guest.Set("t", "09:00")
	guest.Set("contact", " ")
	if _, loc := do(false, "POST", "/cs/"+jtok, guest, http.StatusSeeOther); !strings.Contains(loc, "t=09%3A00") || !strings.Contains(loc, "name=") {
		t.Fatalf("no contact: %s", loc)
	}
	guest.Set("contact", "ivan@client.test")
	robot := url.Values{"website": {"x"}}
	for k, v := range guest {
		robot[k] = v
	}
	if _, loc := do(false, "POST", "/cs/"+jtok, robot, http.StatusSeeOther); strings.HasPrefix(loc, "/cs/m/") {
		t.Fatal("a robot booked")
	}
	_, loc := do(false, "POST", "/cs/"+jtok, guest, http.StatusSeeOther)
	if !strings.HasPrefix(loc, "/cs/m/") {
		t.Fatalf("not booked: %s", loc)
	}
	var ev, title, desc, status string
	must(pool.QueryRow(ctx, `SELECT e.id::text, e.title, e.description, a.status FROM events e JOIN event_attendees a ON a.event_id = e.id
		WHERE e.owner_id = $1 AND a.user_id = $2`, boss, anna).Scan(&ev, &title, &desc, &status))
	if title != "Встреча с Иван" || status != "accepted" || !strings.Contains(desc, "ivan@client.test") || !strings.Contains(desc, "Обсудить договор") {
		t.Fatalf("the meeting: %q %q %q", title, desc, status)
	}
	var told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'event_slot' AND event_id::text = $1`, ev).Scan(&told))
	if told != 2 {
		t.Fatalf("told %d", told)
	}
	page, _ = do(false, "GET", loc, nil, http.StatusOK)
	has(page, "Вы записаны на встречу", "Борис", "Анна", "Иван", "Отменить встречу", loc+"/ics")
	page, _ = do(false, "GET", "/cs/"+ptok+"?date="+day, nil, http.StatusOK) // the boss is busy at 9 now
	hasnt(page, ">09:00<")
	has(page, ">10:00<")
	ics, _ := do(false, "GET", loc+"/ics", nil, http.StatusOK)
	has(ics, "BEGIN:VEVENT", "Встреча с Иван")
	if _, l := do(false, "POST", loc+"/call", nil, http.StatusSeeOther); !strings.Contains(l, "msg=") {
		t.Fatalf("joined long before the start: %s", l)
	}

	// the guest cancels: the meeting is gone, its people are told
	do(false, "POST", loc+"/cancel", nil, http.StatusSeeOther)
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id::text = $1`, ev).Scan(&left))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'event_slot_cancel'`).Scan(&told))
	if left != 0 || told != 2 {
		t.Fatalf("cancelled: events %d, told %d", left, told)
	}
	page, _ = do(false, "GET", loc, nil, http.StatusOK)
	has(page, "Встреча отменена")
	hasnt(page, "Отменить встречу")

	// switched off — gone
	do(true, "POST", "/calendar/slots/"+personal+"/toggle", nil, http.StatusSeeOther)
	do(false, "GET", "/cs/"+ptok, nil, http.StatusNotFound)
	do(false, "GET", "/cs/nope", nil, http.StatusNotFound)

	// REST
	call := func(method, path string, body any, want int, out any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(string(b)))
		req.Header.Set("Authorization", "Bearer "+bossKey)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		if out != nil {
			must(json.Unmarshal(raw, out))
		}
	}
	var list []apiSlotLink
	call("GET", "/api/v1/calendar/slots", nil, http.StatusOK, &list)
	if len(list) != 2 || list[0].ID != personal || list[0].Active || list[1].Meetings != 0 || list[0].URL != "https://go24.test/cs/"+ptok {
		t.Fatalf("REST list: %+v", list)
	}
	var one apiSlotLink
	call("PATCH", "/api/v1/calendar/slots/"+personal, map[string]any{"active": true, "length": 30}, http.StatusOK, &one)
	if !one.Active || one.Length != 30 || len(one.Rules) != 1 {
		t.Fatalf("REST patch: %+v", one)
	}
	call("PATCH", "/api/v1/calendar/slots/"+personal, map[string]any{"length": 25}, http.StatusUnprocessableEntity, nil)
	call("DELETE", "/api/v1/calendar/slots/"+personal, nil, http.StatusNotFound, nil)
	call("DELETE", "/api/v1/calendar/slots/"+list[1].ID, nil, http.StatusNoContent, nil)
}
