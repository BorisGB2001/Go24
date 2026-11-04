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
	"go24/internal/booking"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestBookingMoveHTTP: the client moves the booking from its page — the days and free times of the same resource (its
// own time free for it, another booking's not), the move keeps the status and the length, the reminder goes by the
// new time, the one responsible is told; a time taken or out of the days is refused; a cancelled or past booking is
// not moved; «Добавить в календарь» — its .ics; the deal's trigger «Клиент перенёс запись» may be set.
func TestBookingMoveHTTP(t *testing.T) {
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
	var boss, ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@go24.test') RETURNING id`).Scan(&ann))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true}))
	bossCookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// do: the client without signing in, or the administrator (admin); it returns the page and where it goes
	do := func(admin bool, method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if admin {
			req.AddCookie(bossCookie)
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
		return string(raw), res.Header.Get("Location")
	}

	types, err := srv.Booking.Types(ctx)
	must(err)
	do(true, "POST", "/booking/resources", url.Values{"name": {"Мастер Переносов"}, "type": {types[0].ID}, "user": {ann},
		"from": {"09:00"}, "to": {"18:00"}, "slot": {"60"}, "days": {"1", "2", "3", "4", "5", "6", "7"}}, 200)
	list, err := srv.Booking.Resources(ctx, "")
	must(err)
	res := list[0]
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 2)
	at := func(h int) time.Time { return day.Add(time.Duration(h) * time.Hour) }
	id, err := srv.Booking.SaveBooking(ctx, boss, booking.Booking{ResourceID: res.ID, Start: at(10), End: at(11),
		ClientName: "Ирина", ClientEmail: "irina@client.test", Status: "confirmed"})
	must(err)
	_, err = srv.Booking.SaveBooking(ctx, boss, booking.Booking{ResourceID: res.ID, Start: at(12), End: at(13), ClientName: "Пётр", Status: "confirmed"})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE bookings SET reminded_at = now() WHERE id = $1`, id)
	must(err)
	b, err := srv.Booking.Booking(ctx, id)
	must(err)
	tok := b.ClientToken
	date := day.Format("2006-01-02")

	// the page offers «Перенести запись» and «Добавить в календарь»; ?move=1 — the free times of the day
	page, _ := do(false, "GET", "/bk/b/"+tok, nil, 200)
	if !strings.Contains(page, "Перенести запись") || !strings.Contains(page, `/bk/b/`+tok+`/ics`) {
		t.Fatal("no «Перенести запись» or «Добавить в календарь»")
	}
	page, _ = do(false, "GET", "/bk/b/"+tok+"?move=1&date="+date, nil, 200)
	if !strings.Contains(page, ">10:00<") || strings.Contains(page, ">12:00<") || !strings.Contains(page, ">14:00<") {
		t.Fatalf("the free times to move to: %.2000s", page)
	}
	page, _ = do(false, "GET", "/bk/b/"+tok+"?move=1&date="+date+"&t=14:00", nil, 200)
	if !strings.Contains(page, `action="/bk/b/`+tok+`/move"`) || !strings.Contains(page, `name="t" value="14:00"`) {
		t.Fatal("no form to move")
	}

	// a time taken, out of the days ahead, not a time: refused
	for _, f := range []url.Values{{"date": {date}, "t": {"12:00"}}, {"date": {day.AddDate(0, 0, 60).Format("2006-01-02")}, "t": {"10:00"}}, {"date": {"x"}}} {
		if _, loc := do(false, "POST", "/bk/b/"+tok+"/move", f, 303); !strings.Contains(loc, "msg=") {
			t.Fatalf("moved to %v → %s", f, loc)
		}
	}
	if b, _ := srv.Booking.Booking(ctx, id); !b.Start.Equal(at(10)) {
		t.Fatal("moved to a time not free")
	}

	// moved: the status, the length stay; the reminder goes again; the employee is told what it was
	_, loc := do(false, "POST", "/bk/b/"+tok+"/move", url.Values{"date": {date}, "t": {"14:00"}}, 303)
	if !strings.Contains(loc, "msg=") {
		t.Fatalf("moved → %s", loc)
	}
	b, err = srv.Booking.Booking(ctx, id)
	must(err)
	var reminded bool
	must(pool.QueryRow(ctx, `SELECT reminded_at IS NOT NULL FROM bookings WHERE id = $1`, id).Scan(&reminded))
	if !b.Start.Equal(at(14)) || !b.End.Equal(at(15)) || b.Status != "confirmed" || reminded {
		t.Fatalf("moved %+v, reminded %v", b, reminded)
	}
	var body string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'booking_moved'`, ann).Scan(&body))
	if !strings.Contains(body, "Ирина") || !strings.Contains(body, at(14).Format("02.01.2006 15:04")) || !strings.Contains(body, "было "+at(10).Format("02.01.2006 15:04")) {
		t.Fatalf("the employee is told %q", body)
	}

	// «Добавить в календарь»
	ics, _ := do(false, "GET", "/bk/b/"+tok+"/ics", nil, 200)
	if !strings.Contains(ics, "BEGIN:VEVENT") || !strings.Contains(ics, "Мастер Переносов") || !strings.Contains(ics, "UID:booking-"+id+"@go24") ||
		!strings.Contains(ics, "/bk/b/"+tok) {
		t.Fatalf("the .ics %s", ics)
	}

	// cancelled: no moving, no file
	do(false, "POST", "/bk/b/"+tok+"/status", url.Values{"status": {"cancelled"}}, 303)
	if page, _ := do(false, "GET", "/bk/b/"+tok+"?move=1", nil, 200); strings.Contains(page, "Перенести запись") || strings.Contains(page, "/move\"") {
		t.Fatal("a cancelled booking may be moved")
	}
	do(false, "POST", "/bk/b/"+tok+"/move", url.Values{"date": {date}, "t": {"16:00"}}, 303)
	if b, _ := srv.Booking.Booking(ctx, id); !b.Start.Equal(at(14)) {
		t.Fatal("a cancelled booking moved")
	}
	do(false, "GET", "/bk/b/"+tok+"/ics", nil, 404)
	do(false, "POST", "/bk/b/nope/move", url.Values{"date": {date}, "t": {"16:00"}}, 404)

	// the deal's trigger «Клиент перенёс запись» is one of the kinds
	var def string
	must(pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'crm_triggers_kind_check'`).Scan(&def))
	if !strings.Contains(def, "booking_moved") {
		t.Fatal("no trigger «Клиент перенёс запись»")
	}
}
