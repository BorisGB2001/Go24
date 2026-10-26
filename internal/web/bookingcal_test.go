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
	"go24/internal/booking"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestBookingCalendarHTTP: an employee resource goes by the calendar — the form keeps «Учитывать занятость в
// календаре», the grid shades the employee's meeting, the client's page and REST /slots offer no slot of it; the
// booking is in the employee's calendar (not when «Онлайн-запись» is switched off); REST shows and takes calendar_busy.
func TestBookingCalendarHTTP(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Босс', 'boss@go24.test', '{admins}') RETURNING id`).Scan(&boss))
	keys := apikeys.NewStore(pool)
	key, err := keys.Create(ctx, boss, "k", []string{"booking:read", "booking:write"}, time.Hour)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), Keys: keys,
		Calendar: calendar.NewStore(pool), Company: company.NewStore(pool),
		AdminGroups: []string{"admins"}, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(signed bool, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if signed {
			req.AddCookie(cookie)
			req.Header.Set("X-Go24-Form", "1")
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	call := func(method, path string, body any, want int, v any) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, hs.URL+path, bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, res.StatusCode, out, want)
		}
		if v != nil {
			must(json.Unmarshal(out, v))
		}
	}

	mon := time.Now().AddDate(0, 0, 3)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	mon = time.Date(mon.Year(), mon.Month(), mon.Day(), 0, 0, 0, 0, time.Local)
	monDay := mon.Format("2006-01-02")
	types, err := srv.Booking.Types(ctx)
	must(err)
	page := do(true, "GET", "/booking/resources?edit=new", nil, http.StatusOK)
	if !strings.Contains(page, `name="calendar_busy" value="1" checked`) {
		t.Fatal("a new resource does not go by the calendar")
	}
	do(true, "POST", "/booking/resources", url.Values{"name": {"Мастер"}, "type": {types[0].ID}, "user": {boss}, "from": {"09:00"},
		"to": {"13:00"}, "slot": {"60"}, "days": {"1"}, "calendar_busy": {"1"}}, http.StatusOK)
	list, err := srv.Booking.Resources(ctx, "")
	must(err)
	r := list[0]
	if !r.CalendarBusy || r.UserID != boss {
		t.Fatalf("saved: %+v", r)
	}
	_, err = srv.Calendar.Save(ctx, boss, calendar.Event{Title: "Планёрка", Start: mon.Add(10 * time.Hour), End: mon.Add(11 * time.Hour)}, nil)
	must(err)
	page = do(true, "GET", "/booking?date="+monDay, nil, http.StatusOK)
	if !strings.Contains(page, `class="bk-off bk-cal" style="top:48px;height:48px"`) {
		t.Fatalf("the grid does not shade the meeting: %.400s", page[strings.Index(page, "cg-col bk-col"):])
	}
	l, err := srv.Booking.SaveLink(ctx, boss, booking.Link{Name: "Салон", DaysAhead: 30, Active: true})
	must(err)
	link, err := srv.Booking.Link(ctx, l)
	must(err)
	page = do(false, "GET", "/bk/"+link.Token+"?r="+r.ID+"&date="+monDay, nil, http.StatusOK)
	if !strings.Contains(page, ">09:00<") || strings.Contains(page, ">10:00<") || !strings.Contains(page, ">11:00<") {
		t.Fatal("the client's page offers the time of the meeting")
	}
	var slots struct {
		Free []string `json:"free"`
	}
	call("GET", "/api/v1/booking/resources/"+r.ID+"/slots?date="+monDay, nil, http.StatusOK, &slots)
	if len(slots.Free) != 3 {
		t.Fatalf("REST slots: %v", slots)
	}
	do(false, "POST", "/bk/"+link.Token, url.Values{"r": {r.ID}, "date": {monDay}, "t": {"09:00"}, "name": {"Ира"},
		"phone": {"+7 900 000-00-02"}}, http.StatusSeeOther)
	cal := do(true, "GET", "/calendar?view=week&date="+monDay, nil, http.StatusOK)
	if !strings.Contains(cal, `class="calitem cg-ev booking bk-unconfirmed" style="top:`) || !strings.Contains(cal, `data-cal="booking" href="/booking?date=`+monDay+`&amp;b=`) ||
		!strings.Contains(cal, `value="booking" checked`) {
		t.Fatal("the booking is not in the employee's calendar")
	}
	must(srv.saveSectionsOff(httptest.NewRequest("POST", "/", nil), boss, []string{"booking"}))
	if cal := do(true, "GET", "/calendar?view=week&date="+monDay, nil, http.StatusOK); strings.Contains(cal, `data-cal="booking" href`) ||
		strings.Contains(cal, `value="booking"`) {
		t.Fatal("bookings in the calendar with «Онлайн-запись» switched off")
	}
	must(srv.saveSectionsOff(httptest.NewRequest("POST", "/", nil), boss, nil))

	// REST: calendar_busy is shown and taken; without it the meeting's time is free
	var res struct {
		CalendarBusy bool `json:"calendar_busy"`
	}
	call("GET", "/api/v1/booking/resources/"+r.ID, nil, http.StatusOK, &res)
	if !res.CalendarBusy {
		t.Fatal("REST: calendar_busy")
	}
	call("PUT", "/api/v1/booking/resources/"+r.ID, map[string]any{"calendar_busy": false}, http.StatusOK, nil)
	call("GET", "/api/v1/booking/resources/"+r.ID+"/slots?date="+monDay, nil, http.StatusOK, &slots)
	if len(slots.Free) != 3 { // 9:00 is booked, 10:00 is free now
		t.Fatalf("REST slots without the calendar: %v", slots)
	}
	if page := do(true, "GET", "/booking?date="+monDay, nil, http.StatusOK); strings.Contains(page, "bk-cal") {
		t.Fatal("the grid shades the meeting of a resource that does not go by the calendar")
	}
}
