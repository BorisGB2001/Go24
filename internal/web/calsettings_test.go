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
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCalendarSettingsHTTP: «Настройки календаря» — the week from Sunday in the month and the week, the hour grid of
// the working hours widened to an early meeting, days off shaded, declined meetings and task deadlines hidden, the
// reminder of a new meeting; wrong hours are refused.
func TestCalendarSettingsHTTP(t *testing.T) {
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
	boss, anna := user("Босс"), user("Анна")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Calendar: calendar.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wed := time.Date(2026, 11, 4, 0, 0, 0, 0, time.Local)
	_, err = srv.Calendar.Save(ctx, anna, calendar.Event{Title: "Ранний созвон", Start: wed.Add(5 * time.Hour), End: wed.Add(6 * time.Hour)}, nil)
	must(err)
	no, err := srv.Calendar.Save(ctx, boss, calendar.Event{Title: "Не пойду", Start: wed.Add(12 * time.Hour), End: wed.Add(13 * time.Hour)}, []string{anna})
	must(err)
	must(srv.Calendar.Respond(ctx, anna, no, "declined"))
	_, err = srv.Tasks.CreateProject(ctx, anna, "GO", "go", "")
	must(err)
	p, _ := srv.Tasks.ProjectByKey(ctx, anna, "GO")
	dl := wed.Add(16 * time.Hour)
	_, err = srv.Tasks.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Сдать отчёт", ResponsibleID: anna, Deadline: &dl})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values, want int) (string, *http.Response) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(cookie)
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res
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

	// the defaults: the week from Monday, 9–18 with the grid widened to the meeting at 5, everything shown
	week, _ := do("GET", "/calendar?view=week&date=2026-11-04", nil, http.StatusOK)
	has(week, `data-date="2026-11-02"`, `data-hour0="5"`, "Не пойду", "Сдать отчёт", `class="cg-col off" style="height`)
	hasnt(week, `data-date="2026-11-01"`)
	form, _ := do("GET", "/calendar/settings", nil, http.StatusOK)
	has(form, `<option value="1" selected>`, `name="show_declined" checked`, `value="15" selected`)

	_, res := do("POST", "/calendar/settings", url.Values{"week_start": {"7"}, "work_from": {"8"}, "work_to": {"17"},
		"days_off": {"5", "6"}, "remind": {"60"}}, http.StatusSeeOther)
	if !strings.HasPrefix(res.Header.Get("Location"), "/calendar/settings?msg=") {
		t.Fatalf("redirect to %q", res.Header.Get("Location"))
	}
	cs, err := srv.Calendar.Settings(ctx, anna)
	must(err)
	if cs.WeekStart != 7 || cs.WorkFrom != 8 || cs.WorkTo != 17 || len(cs.DaysOff) != 2 || cs.ShowDeclined || cs.ShowTasks || cs.Remind != 60 {
		t.Fatalf("settings = %+v", cs)
	}
	week, _ = do("GET", "/calendar?view=week&date=2026-11-04", nil, http.StatusOK)
	has(week, `data-date="2026-11-01"`, `data-date="2026-11-07"`, `data-hour0="5"`, "Ранний созвон", "--work0:144px;--work1:576px")
	hasnt(week, `data-date="2026-11-08"`, "Не пойду", "Сдать отчёт")
	month, _ := do("GET", "/calendar?month=2026-11", nil, http.StatusOK)
	has(month, `<div class="calday off" data-date="2026-11-06"`) // Friday off
	if i, j := strings.Index(month, `data-date="2026-11-01"`), strings.Index(month, `data-date="2026-11-02"`); i < 0 || i > j {
		t.Fatal("the month does not begin on Sunday 1 November")
	}
	list, _ := do("GET", "/calendar?view=list&date=2026-11-04", nil, http.StatusOK)
	hasnt(list, "Не пойду", "Сдать отчёт")
	newForm, _ := do("GET", "/calendar/new?date=2026-11-04", nil, http.StatusOK)
	has(newForm, `<option value="60" selected>`)
	if found, _ := do("GET", "/calendar?preset=declined", nil, http.StatusOK); !strings.Contains(found, "Не пойду") {
		t.Fatal("the preset «Отклонённые» finds the declined meeting anyway")
	}

	do("POST", "/calendar/settings", url.Values{"week_start": {"1"}, "work_from": {"18"}, "work_to": {"9"}, "remind": {"15"}}, http.StatusSeeOther)
	if cs, err = srv.Calendar.Settings(ctx, anna); err != nil || cs.WeekStart != 7 {
		t.Fatalf("wrong hours saved: %+v %v", cs, err)
	}
	do("POST", "/calendar/settings", url.Values{"week_start": {"1"}, "work_from": {"9"}, "work_to": {"18"}, "remind": {"-1"}}, http.StatusSeeOther)
	newForm, _ = do("GET", "/calendar/new?date=2026-11-04", nil, http.StatusOK)
	if i, j := strings.Index(newForm, "data-reminds>"), strings.Index(newForm, "<template data-remind-row>"); i < 0 || j < i ||
		strings.Contains(newForm[i:j], `name="remind"`) {
		t.Fatal("a new meeting is reminded though the settings say not to")
	}
}
