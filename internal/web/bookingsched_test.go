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
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestBookingScheduleHTTP: the schedule of a resource by days — the form keeps a weekday's own hours, the break and
// the dates off; the grid shades the break and spans the day's own hours; the client's page offers no slot of the
// break nor of a date off; REST shows and takes them.
func TestBookingScheduleHTTP(t *testing.T) {
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

	// a Monday a week ahead works 10:00–14:00 (its own hours), the Tuesday after it is off; the break 12:00–13:00
	mon := time.Now().AddDate(0, 0, 3)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	monDay, tueDay := mon.Format("2006-01-02"), mon.AddDate(0, 0, 1).Format("2006-01-02")
	types, err := srv.Booking.Types(ctx)
	must(err)
	do(true, "POST", "/booking/resources", url.Values{"name": {"Мастер"}, "type": {types[0].ID}, "from": {"09:00"}, "to": {"18:00"},
		"slot": {"60"}, "days": {"1", "2", "3"}, "day_from_1": {"10:00"}, "day_to_1": {"14:00"}, "day_from_5": {"07:00"},
		"day_to_5": {"08:00"}, "break_from": {"12:00"}, "break_to": {"13:00"}, "off": {tueDay, ""}}, http.StatusOK)
	list, err := srv.Booking.Resources(ctx, "")
	must(err)
	r := list[0]
	if len(r.DayHours) != 1 || r.DayHours[1] != [2]int{600, 840} || r.BreakFrom != 720 || len(r.OffDates) != 1 {
		t.Fatalf("saved: %+v", r)
	}
	page := do(true, "GET", "/booking/resources?edit="+r.ID, nil, http.StatusOK)
	if !strings.Contains(page, `name="day_from_1" value="10:00"`) || !strings.Contains(page, `name="day_from_2" value=""`) ||
		!strings.Contains(page, `name="break_from" value="12:00"`) || !strings.Contains(page, `name="off" value="`+tueDay+`"`) {
		t.Fatal("the form does not show the schedule")
	}
	if page := do(true, "GET", "/booking/resources", nil, http.StatusOK); !strings.Contains(page, "Перерыв 12:00–13:00") || !strings.Contains(page, "ресурсов: 1<") {
		t.Fatal("the list does not show the break")
	}
	page = do(true, "GET", "/booking?date="+monDay, nil, http.StatusOK)
	if !strings.Contains(page, `data-from="600" data-to="840" data-break-from="720" data-break-to="780"`) || !strings.Contains(page, "bk-break") {
		t.Fatalf("the grid of Monday: %.300s", page[strings.Index(page, "cg-col bk-col"):])
	}
	if page := do(true, "GET", "/booking?date="+tueDay, nil, http.StatusOK); !strings.Contains(page, "bk-col off") {
		t.Fatal("the date off works on the grid")
	}

	// the client's page: 10, 11, 13 on Monday; nothing on the date off
	l, err := srv.Booking.SaveLink(ctx, boss, booking.Link{Name: "Салон", DaysAhead: 30, Active: true})
	must(err)
	link, err := srv.Booking.Link(ctx, l)
	must(err)
	page = do(false, "GET", "/bk/"+link.Token+"?r="+r.ID+"&date="+monDay, nil, http.StatusOK)
	for _, want := range []string{">10:00<", ">11:00<", ">13:00<"} {
		if !strings.Contains(page, want) {
			t.Fatalf("no %s on Monday", want)
		}
	}
	if strings.Contains(page, ">12:00<") || strings.Contains(page, ">09:00<") {
		t.Fatal("a slot of the break or before the day's hours")
	}
	if page := do(false, "GET", "/bk/"+link.Token+"?r="+r.ID+"&date="+tueDay, nil, http.StatusOK); strings.Contains(page, ">10:00<") {
		t.Fatal("slots on the date off")
	}
	do(false, "POST", "/bk/"+link.Token, url.Values{"r": {r.ID}, "date": {monDay}, "t": {"12:00"}, "name": {"Ира"},
		"phone": {"+7 900 000-00-02"}}, http.StatusSeeOther) // the break: back to the form with the message
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM bookings`).Scan(&n))
	if n != 0 {
		t.Fatal("booked in the break")
	}

	// REST
	var got apiBkResource
	call("GET", "/api/v1/booking/resources/"+r.ID, nil, http.StatusOK, &got)
	if got.DayHours["1"] != [2]string{"10:00", "14:00"} || got.BreakFrom != "12:00" || got.BreakTo != "13:00" || len(got.OffDates) != 1 || got.OffDates[0] != tueDay {
		t.Fatalf("REST: %+v", got)
	}
	got = apiBkResource{}
	call("PUT", "/api/v1/booking/resources/"+r.ID, map[string]any{"day_hours": map[string][2]string{"3": {"08:00", "12:00"}},
		"break_from": "", "break_to": "", "off_dates": []string{}}, http.StatusOK, &got)
	if len(got.DayHours) != 1 || got.DayHours["3"][0] != "08:00" || got.BreakFrom != "" || len(got.OffDates) != 0 {
		t.Fatalf("REST changed: %+v", got)
	}
	call("PUT", "/api/v1/booking/resources/"+r.ID, map[string]any{"break_from": "13:00", "break_to": "12:00"}, http.StatusUnprocessableEntity, nil)
}
