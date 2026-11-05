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
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestFreeSpans: the working hours of working days less the busy time, from now on the quarter-hour grid; spans
// shorter than asked and days with none are left out, days off are skipped.
func TestFreeSpans(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2030, 1, d, h, m, 0, 0, time.Local) } // 7 January 2030 — Monday
	set := calendar.DefaultSettings()
	busy := []calendar.Slot{
		{Start: at(8, 9, 0), End: at(8, 17, 45)},  // Tuesday: a quarter of an hour left
		{Start: at(7, 10, 0), End: at(7, 11, 20)}, // Monday
		{Start: at(7, 8, 0), End: at(7, 8, 30)},   // before the working day
		{Start: at(9, 12, 10), End: at(9, 13, 0)}, // Wednesday: off the grid
	}
	str := func(list []availDay) string {
		var b []string
		for _, d := range list {
			s := d.Day.Format("02")
			for _, sl := range d.Free {
				s += " " + sl.Start.Format("15:04") + "-" + sl.End.Format("15:04")
			}
			b = append(b, s)
		}
		return strings.Join(b, "; ")
	}
	got := str(freeSpans(busy, set, at(7, 0, 0), at(7, 9, 7), 3, 30*time.Minute))
	if want := "07 09:15-10:00 11:30-18:00; 09 09:00-12:00 13:00-18:00"; got != want {
		t.Errorf("free = %q, want %q", got, want)
	}
	// an hour at least: Monday morning is too short
	if got := str(freeSpans(busy, set, at(7, 0, 0), at(7, 9, 7), 1, time.Hour)); got != "07 11:30-18:00" {
		t.Errorf("an hour = %q", got)
	}
	// Friday and the next Monday, the weekend skipped; one's own working hours
	set.WorkFrom, set.WorkTo = 10, 16
	if got := str(freeSpans(nil, set, at(11, 0, 0), at(1, 0, 0), 2, 30*time.Minute)); got != "11 10:00-16:00; 14 10:00-16:00" {
		t.Errorf("weekend = %q", got)
	}
	// the day is over
	if got := str(freeSpans(nil, set, at(11, 0, 0), at(11, 15, 50), 1, 30*time.Minute)); got != "" {
		t.Errorf("late = %q", got)
	}
}

// TestMailAvailHTTP: «Вставить свободное время» — the editor's button and dialog, the user's free time less a meeting
// and an absence, the block of the spans chosen for the letter, REST.
func TestMailAvailHTTP(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), Calendar: calendar.NewStore(pool), I18n: bundle, Keys: keys,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseURL: "http://go24.test"}
	_, err = srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	do := func(method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "application/json")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}

	page := do("GET", "/mail?new=1", nil, 200)
	for _, want := range []string{"data-avail ", "data-avail-dlg", `data-a="length"`, "js/mailavail.js"} {
		if !strings.Contains(page, want) {
			t.Errorf("the editor has no %s", want)
		}
	}

	// the next Monday: a meeting 10–11, the Tuesday away
	mon := time.Now().AddDate(0, 0, 7)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	at := func(d, h int) time.Time {
		return time.Date(mon.Year(), mon.Month(), mon.Day()+d, h, 0, 0, 0, time.Local)
	}
	_, err = srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Планёрка", Start: at(0, 10), End: at(0, 11), Accessibility: "busy"}, nil)
	must(err)
	tue := at(1, 0)
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', $2, $2, $1)`, anna, tue)
	must(err)
	var got struct {
		TZ   string
		Days []struct {
			Date  string
			Spans []struct{ Start, End string }
		}
		Labels []string
	}
	must(json.Unmarshal([]byte(do("GET", "/mail/availability?from="+mon.Format("2006-01-02")+"&days=2&length=60", nil, 200)), &got))
	if len(got.Days) != 1 || got.Days[0].Date != mon.Format("2006-01-02") || len(got.Days[0].Spans) != 2 ||
		got.Days[0].Spans[0].Start != "09:00" || got.Days[0].Spans[0].End != "10:00" || got.Days[0].Spans[1].Start != "11:00" ||
		got.Days[0].Spans[1].End != "18:00" || len(got.Labels) != 1 || !strings.HasPrefix(got.Labels[0], "пн, ") || got.TZ == "" {
		t.Errorf("availability = %+v", got)
	}

	do("POST", "/mail/availability", url.Values{}, 422)
	do("POST", "/mail/availability", url.Values{"span": {"bad"}}, 422)
	d := mon.Format("2006-01-02")
	var block struct{ HTML string }
	must(json.Unmarshal([]byte(do("POST", "/mail/availability", url.Values{"span": {d + "T11:00/18:00", d + "T09:00/10:00"}}, 200)), &block))
	if !strings.Contains(block.HTML, "Мне удобно в это время") || !strings.Contains(block.HTML, "09:00–10:00, 11:00–18:00") ||
		!strings.Contains(block.HTML, mon.Format("02.01.2006")) || !strings.Contains(block.HTML, "Часовой пояс") {
		t.Errorf("block = %s", block.HTML)
	}

	tok, err := keys.Create(ctx, anna, "t", []string{"mail:read"}, 0)
	must(err)
	req, _ := http.NewRequest("GET", hs.URL+"/api/v1/mail/availability?from="+d+"&days=1&length=30", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(string(raw), `"start": "11:00"`) || strings.Contains(string(raw), "labels") {
		t.Errorf("REST: %d %s", res.StatusCode, raw)
	}
}
