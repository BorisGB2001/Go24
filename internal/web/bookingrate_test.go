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

// TestBookingFeedbackHTTP: «Обратная связь» — an administrator turns on the rating requests of a resource, the worker
// marks the request of a visit the client came to, the client rates it on the booking's page (the employee of the
// resource is told once, a change is not told), the rating is on the grid, in the list and in the statistics.
func TestBookingFeedbackHTTP(t *testing.T) {
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
	cookie := func(s auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, s))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), ann: cookie(auth.Session{UserID: ann})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// do: who "" is a client without signing in
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if who != "" {
			req.AddCookie(as[who])
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

	types, err := srv.Booking.Types(ctx)
	must(err)
	do(boss, "POST", "/booking/resources", url.Values{"name": {"Мастер Оценок"}, "type": {types[0].ID}, "user": {ann},
		"from": {"00:00"}, "to": {"24:00"}, "slot": {"30"}, "days": {"1", "2", "3", "4", "5", "6", "7"},
		"notify_mail": {"1"}, "feedback": {"1"}}, 200)
	list, err := srv.Booking.Resources(ctx, "")
	must(err)
	if len(list) != 1 || !list[0].Feedback || !list[0].NotifyMail {
		t.Fatalf("resources: %+v", list)
	}
	if page := do(boss, "GET", "/booking/resources?edit="+list[0].ID, nil, 200); !strings.Contains(page, `name="feedback" value="1" checked`) {
		t.Fatal("the form has no feedback checked")
	}
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Minute)
	id, err := srv.Booking.SaveBooking(ctx, boss, booking.Booking{ResourceID: list[0].ID, Start: start, End: start.Add(time.Hour),
		ClientName: "Ирина", ClientEmail: "irina@client.test", Status: "came"})
	must(err)
	must(srv.BookingDue(ctx))
	var asked bool
	must(pool.QueryRow(ctx, `SELECT feedback_at IS NOT NULL FROM bookings WHERE id = $1`, id).Scan(&asked))
	if !asked {
		t.Fatal("the rating request is not marked")
	}
	b, err := srv.Booking.Booking(ctx, id)
	must(err)

	// the client's page asks to rate; no stars — a message
	tok := b.ClientToken
	if page := do("", "GET", "/bk/b/"+tok, nil, 200); !strings.Contains(page, "Оцените визит") || !strings.Contains(page, `name="stars"`) {
		t.Fatal("the page does not ask to rate")
	}
	do("", "POST", "/bk/b/"+tok+"/rate", url.Values{"stars": {"0"}}, 303)
	do("", "POST", "/bk/b/"+tok+"/rate", url.Values{"stars": {"4"}, "note": {"Всё понравилось"}}, 303)
	do("", "POST", "/bk/b/"+tok+"/rate", url.Values{"stars": {"5"}, "note": {"Всё понравилось"}}, 303) // a change: not told again
	var n int
	var body string
	must(pool.QueryRow(ctx, `SELECT count(*), max(body) FROM notifications WHERE user_id = $1 AND kind = 'booking_rated'`, ann).Scan(&n, &body))
	if n != 1 || !strings.HasPrefix(body, "★★★★ Ирина") {
		t.Fatalf("notifications: %d %q", n, body)
	}
	if page := do("", "GET", "/bk/b/"+tok, nil, 200); !strings.Contains(page, "Спасибо за оценку") || !strings.Contains(page, "★★★★★") {
		t.Fatal("the page does not show the rating")
	}
	day := start.Local().Format("2006-01-02")
	if page := do(ann, "GET", "/booking?date="+day, nil, 200); !strings.Contains(page, `data-rating="5"`) || !strings.Contains(page, `data-rating-note="Всё понравилось"`) {
		t.Fatal("the grid has no rating")
	}
	if page := do(ann, "GET", "/booking/list?from="+day+"&to="+day, nil, 200); !strings.Contains(page, "★★★★★") {
		t.Fatal("the list has no rating")
	}
	if page := do(ann, "GET", "/booking/stats?from="+day+"&to="+day, nil, 200); !strings.Contains(page, "★ 5,0") {
		t.Fatal("the statistics have no rating")
	}
	// a visit not come to is not rated
	other, err := srv.Booking.SaveBooking(ctx, boss, booking.Booking{ResourceID: list[0].ID, Start: start.Add(-2 * time.Hour),
		End: start.Add(-time.Hour), ClientName: "Пётр", Status: "no_show"})
	must(err)
	o, err := srv.Booking.Booking(ctx, other)
	must(err)
	if page := do("", "GET", "/bk/b/"+o.ClientToken, nil, 200); strings.Contains(page, `name="stars"`) {
		t.Fatal("a visit not come to asks to rate")
	}
	do("", "POST", "/bk/b/"+o.ClientToken+"/rate", url.Values{"stars": {"1"}}, 303)
	if o, _ = srv.Booking.Booking(ctx, other); o.Rating != 0 {
		t.Fatal("a visit not come to rated")
	}
}
