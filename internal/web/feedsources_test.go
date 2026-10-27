package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/kb"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestFeedSourcesHTTP: the events of other sections in the feed — «Приветствуем нового сотрудника» to
// all, a meeting to its people (and those invited later), a new page of the knowledge base to all employees; each with
// comments; a deleted meeting or page leaves the feed; the rights of the feed do not apply. And the right column of the
// feed: the important posts not confirmed yet.
func TestFeedSourcesHTTP(t *testing.T) {
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@go24.test') RETURNING id`, n).Scan(&id))
		return id
	}
	anna, ivan, olga, petr := newUser("Анна"), newUser("Иван"), newUser("Ольга"), newUser("Пётр")
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool), Calendar: calendar.NewStore(pool),
		KB: kb.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// only Anna may write to all employees: the events are not of the rights
	must(srv.Feed.SaveRights(tasks.WithAdmin(ctx), anna, feed.Rights{All: feed.Audience{Users: []string{anna}}}))

	srv.feedWelcome(ctx, petr, "Пётр")
	start := time.Now().Add(24 * time.Hour).Truncate(time.Hour)
	meet, err := srv.Calendar.Save(ctx, ivan, calendar.Event{Title: "Планёрка", Where: "Переговорная", Start: start, End: start.Add(time.Hour)}, []string{olga})
	must(err)
	srv.feedMeeting(ctx, ivan, meet)
	alone, err := srv.Calendar.Save(ctx, ivan, calendar.Event{Title: "Один", Start: start, End: start.Add(time.Hour)}, nil)
	must(err)
	srv.feedMeeting(ctx, ivan, alone) // nobody invited — not in the feed
	space, err := srv.KB.CreateSpace(ctx, olga, "Регламенты", "", "")
	must(err)
	page, err := srv.KB.CreatePage(ctx, olga, space, "", "Отпуска", "Как оформить")
	must(err)
	srv.feedKBPage(ctx, olga, page)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	get := func(who, path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(rec.Result().Cookies()[0])
		req.Header.Set("Accept-Language", "ru-RU")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("GET %s: %d", path, res.StatusCode)
		}
		return string(raw)
	}
	page1 := get(anna, "/feed")
	for _, want := range []string{"Приветствуем нового сотрудника", "/u/" + petr, "Новая страница базы знаний", `/kb/` + space + `/` + page, "Регламенты"} {
		if !strings.Contains(page1, want) {
			t.Fatalf("anna's feed has no %q", want)
		}
	}
	if strings.Contains(page1, "Планёрка") || strings.Contains(page1, "Один") {
		t.Fatal("anna sees a meeting she is not invited to")
	}
	if p := get(olga, "/feed"); !strings.Contains(p, "/calendar/"+meet) || !strings.Contains(p, "Переговорная") {
		t.Fatal("olga does not see the meeting she is invited to")
	}
	// invited later: sees it; the post stays one
	e, err := srv.Calendar.Get(ctx, ivan, meet)
	must(err)
	e.Title = "Планёрка в 10"
	_, err = srv.Calendar.Save(ctx, ivan, e, []string{olga, anna})
	must(err)
	srv.feedMeeting(ctx, ivan, meet)
	if p := get(anna, "/feed?kind=meeting"); !strings.Contains(p, "Планёрка в 10") || strings.Contains(p, "Отпуска") {
		t.Fatal("anna: the meeting after she was invited, and only meetings")
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM feed_posts WHERE source <> ''`).Scan(&n))
	if n != 3 {
		t.Fatalf("%d posts of events, want 3", n)
	}
	// comments as on any post
	id, err := srv.Feed.Announced(ctx, feed.SourceKB, page)
	must(err)
	_, err = srv.Feed.AddComment(ctx, petr, id, "Спасибо")
	must(err)
	// the right column: an important post not confirmed yet, then confirmed
	imp, err := srv.Feed.CreatePost(ctx, anna, "", "Собрание", "Всем быть", true)
	must(err)
	if p := get(ivan, "/feed"); !strings.Contains(p, `class="fside"`) || !strings.Contains(p, `class="fside-row" href="/feed?post=`+imp) {
		t.Fatal("ivan: the important post is not in the right column")
	}
	_, err = srv.Feed.MarkRead(ctx, ivan, imp)
	must(err)
	if p := get(ivan, "/feed"); strings.Contains(p, `class="fside-row" href="/feed?post=`+imp) {
		t.Fatal("ivan: a confirmed important post stays in the right column")
	}
	// deleted: out of the feed
	srv.withdraw(ctx, feed.SourceMeeting, meet)
	srv.withdraw(ctx, feed.SourceKB, page)
	// the posts only: «Ближайшие события» of the right column still has the meeting itself
	if p, _, _ := strings.Cut(get(anna, "/feed"), `<aside class="fside">`); strings.Contains(p, "Планёрка") || strings.Contains(p, "Отпуска") {
		t.Fatal("withdrawn posts are in the feed")
	}
}
