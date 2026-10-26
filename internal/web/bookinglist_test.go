package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// TestBookingListHTTP: «Список записей» with its filter, the counter of «Онлайн-запись» in the menu (the user's bookings
// not confirmed or with a late client, not those of others) and the same filter in REST.
func TestBookingListHTTP(t *testing.T) {
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
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), Keys: keys,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(s auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, s))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), ann: cookie(auth.Session{UserID: ann})}
	get := func(who, path string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(as[who])
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("GET %s: %d %s", path, res.StatusCode, raw)
		}
		return string(raw)
	}

	types, err := srv.Booking.Types(ctx)
	must(err)
	res, err := srv.Booking.SaveResource(ctx, boss, booking.Resource{TypeID: types[0].ID, Name: "Мастер Ногтей", Slot: 30,
		Days: []int{1, 2, 3, 4, 5, 6, 7}, From: 0, To: 1440, LateMinutes: 10})
	must(err)
	now := time.Now().Truncate(time.Minute)
	save := func(name, status string, start time.Time) {
		_, err := srv.Booking.SaveBooking(ctx, boss, booking.Booking{ResourceID: res, Start: start, End: start.Add(30 * time.Minute),
			ClientName: name, ClientPhone: "+7 900 555-00-11", Status: status})
		must(err)
	}
	save("Клиент Опоздал", "confirmed", now.Add(-20*time.Minute)) // late today
	save("Клиент Ждёт", "", now.Add(48*time.Hour))                // not confirmed, to come
	save("Клиент Подтвердил", "confirmed", now.Add(72*time.Hour))

	// the counter: the boss made both bookings, Anna — none
	counter := func(page string) string {
		_, a, ok := strings.Cut(page, `data-counter="booking"`)
		a, _, _ = strings.Cut(a, "</a>")
		if !ok || strings.Contains(a, "hidden") {
			return ""
		}
		_, n, _ := strings.Cut(a, ">")
		return n
	}
	if n := counter(get(boss, "/booking")); n != "2" {
		t.Fatalf("the boss's counter: %q", n)
	}
	if n := counter(get(ann, "/booking")); n != "" {
		t.Fatalf("Anna's counter: %q", n)
	}
	// the list: all of the month, the presets, the search
	p := get(ann, "/booking/list")
	for _, s := range []string{"Клиент Опоздал", "Клиент Ждёт", "Клиент Подтвердил", "Мастер Ногтей", `class="pill bk-st bk-late"`} {
		if !strings.Contains(p, s) {
			t.Fatalf("the list has no %q", s)
		}
	}
	if p := get(ann, "/booking/list?status=attention&mine=1"); strings.Contains(p, "Клиент Ждёт") || strings.Contains(p, "Клиент Опоздал") {
		t.Fatalf("Anna's attention list is not empty")
	}
	p = get(boss, "/booking/list?status=attention&mine=1")
	if !strings.Contains(p, "Клиент Опоздал") || !strings.Contains(p, "Клиент Ждёт") || strings.Contains(p, "Клиент Подтвердил") {
		t.Fatalf("the boss's attention list is wrong")
	}
	if p := get(ann, "/booking/list?q=%D0%9F%D0%BE%D0%B4%D1%82%D0%B2"); !strings.Contains(p, "Клиент Подтвердил") || strings.Contains(p, "Клиент Ждёт") {
		t.Fatalf("search by the name")
	}

	// REST: the same filter, mine — the key owner's
	k, err := keys.Create(ctx, boss, "k", []string{"booking:read"}, time.Hour)
	must(err)
	call := func(path string) []apiBkBooking {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var out []apiBkBooking
		must(json.NewDecoder(res.Body).Decode(&out))
		return out
	}
	from, to := now.AddDate(0, 0, -1).Format("2006-01-02"), now.AddDate(0, 0, 5).Format("2006-01-02")
	if l := call("/api/v1/booking/bookings?from=" + from + "&to=" + to + "&status=late"); len(l) != 1 || l[0].ClientName != "Клиент Опоздал" {
		t.Fatalf("REST late: %+v", l)
	}
	if l := call("/api/v1/booking/bookings?from=" + from + "&to=" + to + "&status=attention&mine=1"); len(l) != 2 {
		t.Fatalf("REST attention: %+v", l)
	}
	if l := call("/api/v1/booking/bookings?from=" + from + "&to=" + to + "&q=555-00"); len(l) != 3 {
		t.Fatalf("REST search: %+v", l)
	}
}
