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

// TestFeedFilterHTTP: the feed's «Упоминания» tab, «Кому», the period, «Сохранить фильтр» and its chip, deleting it;
// REST view=mentions, dest and period.
func TestFeedFilterHTTP(t *testing.T) {
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
	var anna, ivan, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('anna', 'Анна') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ivan', 'Иван') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, sales)
	must(err)
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

	_, err = srv.Feed.CreatePost(ctx, ivan, "", "", "Анна, нужен отчёт", false, anna)
	must(err)
	_, err = srv.Feed.CreatePostTo(ctx, ivan, "", feed.Audience{Depts: []string{sales}}, "", "Только отделу продаж", false)
	must(err)
	_, err = srv.Feed.CreatePost(ctx, ivan, "", "", "Всем привет", false)
	must(err)

	page, _ := do("GET", "/feed?view=mentions", nil, 200)
	if !strings.Contains(page, "нужен отчёт") || strings.Contains(page, "Всем привет") {
		t.Fatal("«Упоминания» shows other posts or not the mention")
	}
	page, _ = do("GET", "/feed?dest=dept:"+sales, nil, 200)
	if !strings.Contains(page, "Только отделу продаж") || strings.Contains(page, "Всем привет") || !strings.Contains(page, "🏢 Продажи") {
		t.Fatal("«Кому» = a department")
	}
	page, _ = do("GET", "/feed?period=yesterday", nil, 200)
	if strings.Contains(page, "Всем привет") {
		t.Fatal("yesterday shows today's posts")
	}
	if page, _ = do("GET", "/feed?period=today", nil, 200); !strings.Contains(page, "Всем привет") {
		t.Fatal("today")
	}

	_, loc := do("POST", "/feed/filters", url.Values{"name": {"Мне"}, "query": {"view=mentions&period=week"}}, 303)
	if loc != "/feed?period=week&view=mentions" {
		t.Fatalf("after saving: %q", loc)
	}
	page, _ = do("GET", "/feed?period=week&view=mentions", nil, 200)
	if !strings.Contains(page, `class="fsaved-chip on"`) || !strings.Contains(page, "Мне") {
		t.Fatal("the saved filter is not the current chip")
	}
	if _, loc := do("POST", "/feed/filters", url.Values{"name": {" "}, "query": {"q=x"}}, 303); !strings.HasPrefix(loc, "/feed?msg=") {
		t.Fatalf("a filter without a name: %q", loc)
	}
	do("POST", "/feed/filters/delete", url.Values{"name": {"Мне"}}, 303)
	if list, err := srv.Feed.SavedFilters(ctx, anna); err != nil || len(list) != 0 {
		t.Fatalf("after deleting: %+v %v", list, err)
	}

	k, err := keys.Create(ctx, anna, "k", []string{"feed:read"}, time.Hour)
	must(err)
	count := func(path string, want int) int {
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
		return len(out.Posts)
	}
	if n := count("/api/v1/feed/posts?view=mentions", 200); n != 1 {
		t.Fatalf("REST mentions: %d", n)
	}
	if n := count("/api/v1/feed/posts?dest=dept:"+sales, 200); n != 1 {
		t.Fatalf("REST dest: %d", n)
	}
	if n := count("/api/v1/feed/posts?period=yesterday", 200); n != 0 {
		t.Fatalf("REST yesterday: %d", n)
	}
	count("/api/v1/feed/posts?period=range", 422)
	count("/api/v1/feed/posts?till=bad", 422)
}
