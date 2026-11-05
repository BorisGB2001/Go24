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
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestFeedActivityHTTP: the feed by the last activity — a new comment lifts a post, «По дате
// публикации» keeps the posts' order; «Экспертный режим» leaves the events of tasks out; REST sort=activity.
func TestFeedActivityHTTP(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ivan', 'Иван') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values, want int) (string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
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
	// before: a is above b on the page
	before := func(page, a, b string) bool {
		i, j := strings.Index(page, a), strings.Index(page, b)
		return i >= 0 && j >= 0 && i < j
	}

	old, err := srv.Feed.CreatePost(ctx, ivan, "", "", "Старое сообщение", false)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE feed_posts SET created_at = now() - interval '2 hours', activity_at = now() - interval '2 hours' WHERE id = $1`, old)
	must(err)
	_, err = srv.Feed.CreatePost(ctx, ivan, "", "", "Новое сообщение", false)
	must(err)
	var at time.Time
	must(pool.QueryRow(ctx, `SELECT activity_at FROM feed_posts WHERE id = $1`, old).Scan(&at))
	if time.Since(at) < time.Hour {
		t.Fatalf("a post's activity is not its time: %v", at)
	}

	page, _ := do("GET", "/feed", nil, 200)
	if !before(page, "Новое сообщение", "Старое сообщение") {
		t.Fatal("without comments the newer post is first")
	}
	if !strings.Contains(page, `action="/feed/view"`) || !strings.Contains(page, `name="sort" value="activity" checked`) {
		t.Fatal("no «Вид ленты» menu, or not «by activity» by default")
	}
	_, err = srv.Feed.AddComment(ctx, ivan, old, "Свежий комментарий")
	must(err)
	page, _ = do("GET", "/feed", nil, 200)
	if !before(page, "Старое сообщение", "Новое сообщение") {
		t.Fatal("a new comment does not lift the post")
	}

	_, loc := do("POST", "/feed/view", url.Values{"sort": {"date"}, "back": {"/feed?before=2020-01-01T00:00:00Z&q=x"}}, 303)
	if loc != "/feed?q=x" {
		t.Fatalf("back after the view: %q", loc)
	}
	if _, loc := do("POST", "/feed/view", url.Values{"sort": {"date"}, "back": {"//evil.example/feed"}}, 303); loc != "/feed" {
		t.Fatalf("back to another site: %q", loc)
	}
	page, _ = do("GET", "/feed", nil, 200)
	if !before(page, "Новое сообщение", "Старое сообщение") || !strings.Contains(page, `name="sort" value="date" checked`) {
		t.Fatal("«По дате публикации»")
	}

	// «Экспертный режим»: the events of tasks are left out, posts stay; «Тип: задачи» still shows them
	_, err = ts.CreateProject(ctx, anna, "EX", "Эксперт", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "EX")
	must(err)
	_, err = ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Задача для ленты"})
	must(err)
	if page, _ = do("GET", "/feed", nil, 200); !strings.Contains(page, "Задача для ленты") {
		t.Fatal("no task event in the feed")
	}
	do("POST", "/feed/view", url.Values{"sort": {"activity"}, "expert": {"1"}}, 303)
	page, _ = do("GET", "/feed", nil, 200)
	if strings.Contains(page, "Задача для ленты") || !strings.Contains(page, "Старое сообщение") || !strings.Contains(page, `name="expert" value="1" checked`) {
		t.Fatal("«Экспертный режим» shows the events of tasks or hides the posts")
	}
	if page, _ = do("GET", "/feed?kind=tasks", nil, 200); !strings.Contains(page, "Задача для ленты") {
		t.Fatal("«Тип: задачи» in the expert mode")
	}
	if v, err := srv.Feed.View(ctx, anna); err != nil || v.ByDate || !v.Expert {
		t.Fatalf("the view kept: %+v %v", v, err)
	}

	// REST: by default by the post's time, sort=activity — by the last activity
	k, err := keys.Create(ctx, anna, "k", []string{"feed:read"}, time.Hour)
	must(err)
	first := func(path string, want int) string {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d", path, res.StatusCode, want)
		}
		var out struct{ Posts []apiFeedPost }
		_ = json.NewDecoder(res.Body).Decode(&out)
		if len(out.Posts) == 0 {
			return ""
		}
		if out.Posts[0].ActivityAt.Before(out.Posts[0].CreatedAt) {
			t.Fatalf("activity_at before created_at: %+v", out.Posts[0])
		}
		return out.Posts[0].Body
	}
	if b := first("/api/v1/feed/posts", 200); b != "Новое сообщение" {
		t.Fatalf("REST by date: %q", b)
	}
	if b := first("/api/v1/feed/posts?sort=activity", 200); b != "Старое сообщение" {
		t.Fatalf("REST by activity: %q", b)
	}
	first("/api/v1/feed/posts?sort=x", 422)
}
