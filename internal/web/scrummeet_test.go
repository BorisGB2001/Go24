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
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestScrumMeetings: «Мероприятия» of a scrum team — a meeting of a template (the daily one: the team invited, a video
// call, every day till the active sprint ends; the review on the sprint's last day), the next meetings of the project's
// calendar on the planning with «Присоединиться».
func TestScrumMeetings(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna := newUser("Борис"), newUser("Анна")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "SM", "Скрам", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "SM")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))

	srv := &Server{DB: pool, Tasks: ts, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b), res.Header.Get("Location")
	}

	has := func(page string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Fatalf("page lacks %q", w)
			}
		}
	}
	must(ts.SetProjectKind(ctx, p, "scrum"))
	p, _ = ts.ProjectByKey(ctx, boss, "SM")
	must(ts.CreateSprint(ctx, p, "Спринт 1"))
	plan, err := ts.ScrumPlanFor(ctx, boss, p)
	must(err)
	sp := plan.Sprints[0].ID
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Экран входа"})
	must(err)
	tid, _ := ts.TaskIDByKey(ctx, key)
	must(ts.MoveToSprint(ctx, boss, tid, sp))
	today := time.Now()
	start, end := today.AddDate(0, 0, -1), today.AddDate(0, 0, 10)
	must(ts.UpdateSprint(ctx, p, sp, "Спринт 1", "", &start, &end))
	must(ts.StartSprint(ctx, p, sp))

	page, _ := do(boss, "GET", "/p/SM/scrum", nil)
	has(page, "Мероприятия", "/calendar/new?project=SM&amp;scrum=daily", "/calendar/new?project=SM&amp;scrum=retro", "Ближайших мероприятий нет")

	form, _ := do(boss, "GET", "/calendar/new?project=SM&scrum=daily", nil)
	has(form, `value="Ежедневный митинг"`, "что сделал вчера", `<option value="daily" selected>`, `name="invite" value="`+anna+`"`,
		`name="video" checked`, `name="until" value="`+end.Format("2006-01-02")+`"`, `value="`+p.ID+`" selected`)
	form, _ = do(boss, "GET", "/calendar/new?project=SM&scrum=review", nil)
	has(form, `value="Обзор спринта"`, `name="start" value="`+end.Format("2006-01-02")+`T15:00"`)

	at := time.Now().Add(10 * time.Minute).Truncate(time.Minute)
	if _, err := srv.Calendar.Save(ctx, boss, calendar.Event{Title: "Ежедневный митинг", ProjectID: p.ID, Start: at,
		End: at.Add(15 * time.Minute), Repeat: "daily", Every: 1, Video: true}, []string{anna}); err != nil {
		t.Fatal(err)
	}
	page, _ = do(anna, "GET", "/p/SM/scrum", nil) // a member, neither the owner nor the scrum master (none is set)
	has(page, "Ежедневный митинг", "?at="+strings.ReplaceAll(at.Format("2006-01-02T15:04"), ":", "%3a"), `class="button primary">`, "Присоединиться")
	if strings.Contains(page, "Ближайших мероприятий нет") || strings.Count(page, `class="meetline"`) != 1 {
		t.Fatal("the daily meeting is to be shown once")
	}
}
