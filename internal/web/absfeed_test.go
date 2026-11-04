package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestAbsenceFeedHTTP: «Подключить к Outlook» of the absence chart — an employee gets a secret link, the feed (no
// sign-in) has the absences as whole days with the person acting, a department's link only its people; a new link
// stops the old one, as does leaving; an unknown token or department is not found; a guest gets no link.
func TestAbsenceFeedHTTP(t *testing.T) {
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
	anna, petr, guest := user("Анна"), user("Пётр"), user("Гость")
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest)
	must(err)
	var root, sales, dev string // one root: the company, the departments under it
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Продажи', $1) RETURNING id`, root).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Разработка', $1) RETURNING id`, root).Scan(&dev))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4)`, anna, sales, petr, dev)
	must(err)
	cs := company.NewStore(pool)
	today := time.Now().Truncate(24 * time.Hour)
	must(cs.AddAbsence(ctx, anna, anna, "vacation", today.AddDate(0, 0, 3), today.AddDate(0, 0, 5), "море", false, petr, []string{"view"}))
	must(cs.AddAbsence(ctx, petr, petr, "sick", today, today, "", false, "", nil))

	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: cs, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, guest} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if who != "" {
			req.AddCookie(as[who])
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	feedPath := regexp.MustCompile(`https://go24\.test(/company/absences/feed/[0-9a-f]+\.ics)`)
	link := func() string {
		t.Helper()
		st, body := do(anna, "GET", "/company/absences/sync", nil)
		if st != http.StatusOK {
			t.Fatalf("sync page: %d", st)
		}
		m := feedPath.FindStringSubmatch(body)
		if m == nil {
			return ""
		}
		return m[1]
	}

	if _, body := do(anna, "GET", "/company/absences", nil); !strings.Contains(body, "/company/absences/sync") {
		t.Fatal("the absence chart does not link «Outlook and phone»")
	}
	if link() != "" {
		t.Fatal("a link before it was asked for")
	}
	if st, _ := do(anna, "POST", "/company/absences/sync", url.Values{}); st != http.StatusSeeOther {
		t.Fatalf("issue the link: %d", st)
	}
	first := link()
	if first == "" {
		t.Fatal("no link after issuing it")
	}
	st, ics := do("", "GET", first, nil)
	if st != http.StatusOK || !strings.HasPrefix(ics, "BEGIN:VCALENDAR\r\n") {
		t.Fatalf("feed: %d %q", st, ics)
	}
	for _, want := range []string{"UID:absence-", "DTSTART;VALUE=DATE:" + today.AddDate(0, 0, 3).Format("20060102"),
		"DTEND;VALUE=DATE:" + today.AddDate(0, 0, 6).Format("20060102"), "SUMMARY:Анна — ", "Пётр", "море", "TRANSP:TRANSPARENT"} {
		if !strings.Contains(strings.ReplaceAll(ics, "\r\n ", ""), want) { // long lines are folded at 75 bytes
			t.Fatalf("feed has no %q:\n%s", want, ics)
		}
	}
	// a department's link: its people only, the department in the calendar's name
	if _, body := do(anna, "GET", "/company/absences/sync?dept="+sales, nil); !strings.Contains(body, "?dept="+sales) {
		t.Fatal("the department's link is not shown")
	}
	if _, ics := do("", "GET", first+"?dept="+sales, nil); !strings.Contains(ics, "SUMMARY:Анна") || strings.Contains(ics, "SUMMARY:Пётр") ||
		!strings.Contains(ics, "Продажи") {
		t.Fatalf("department feed:\n%s", ics)
	}
	if st, _ := do("", "GET", first+"?dept=00000000-0000-0000-0000-000000000000", nil); st != http.StatusNotFound {
		t.Fatalf("unknown department: %d", st)
	}
	// a new link stops the old one
	do(anna, "POST", "/company/absences/sync", url.Values{})
	second := link()
	if second == first {
		t.Fatal("the link was not renewed")
	}
	if st, _ := do("", "GET", first, nil); st != http.StatusNotFound {
		t.Fatalf("the old link works: %d", st)
	}
	if st, _ := do("", "GET", "/company/absences/feed/nope.ics", nil); st != http.StatusNotFound {
		t.Fatalf("unknown token: %d", st)
	}
	// a guest of a collab gets no link
	if st, _ := do(guest, "POST", "/company/absences/sync", url.Values{}); st == http.StatusSeeOther {
		t.Fatal("a guest got a link")
	}
	// leaving stops the link
	_, err = pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, anna)
	must(err)
	if st, _ := do("", "GET", second, nil); st != http.StatusNotFound {
		t.Fatalf("the link of a person who left works: %d", st)
	}
}
