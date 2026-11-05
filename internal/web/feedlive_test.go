package web

import (
	"bufio"
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

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/live"
	"go24/internal/tasks"
)

// TestFeedLiveComments: an open feed gets the comments of colleagues as they come, change and go
// (GO24DEV-1028); a filtered feed asks for comments only and gets no new posts.
func TestFeedLiveComments(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Feed: feed.NewStore(pool, ts), Live: live.NewBroker(),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	lctx, stopLive := context.WithCancel(ctx)
	defer stopLive()
	go srv.Live.Listen(lctx, pool, "go24_feed", srv.Log)
	defer func(d time.Duration) { liveCommentDelay = d }(liveCommentDelay)
	liveCommentDelay = 50 * time.Millisecond
	post, err := srv.Feed.CreatePost(ctx, anna, "", "", "Новость", false)
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()

	type event struct {
		Name string
		Data map[string]string
	}
	open := func(path string) (<-chan event, func()) {
		rctx, cancel := context.WithCancel(ctx)
		req, _ := http.NewRequestWithContext(rctx, "GET", hs.URL+path, nil)
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: ivan}))
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := http.DefaultClient.Do(req)
		must(err)
		ch := make(chan event, 16)
		go func() {
			defer res.Body.Close()
			sc := bufio.NewScanner(res.Body)
			var name string
			for sc.Scan() {
				line := sc.Text()
				if v, ok := strings.CutPrefix(line, "event: "); ok {
					name = v
				} else if v, ok := strings.CutPrefix(line, "data: "); ok {
					var d map[string]string
					_ = json.Unmarshal([]byte(v), &d)
					ch <- event{name, d}
				}
			}
		}()
		return ch, cancel
	}
	next := func(ch <-chan event, want string) event {
		t.Helper()
		select {
		case e := <-ch:
			if e.Name != want {
				t.Fatalf("event %q %v, want %q", e.Name, e.Data, want)
			}
			return e
		case <-time.After(5 * time.Second):
			t.Fatalf("no %q event", want)
		}
		return event{}
	}
	none := func(ch <-chan event) {
		t.Helper()
		select {
		case e := <-ch:
			t.Fatalf("unexpected event %q %v", e.Name, e.Data)
		case <-time.After(300 * time.Millisecond):
		}
	}

	ch, stop := open("/feed/live")
	defer stop()
	time.Sleep(300 * time.Millisecond) // LISTEN and the subscription are on
	c, err := srv.Feed.AddComment(ctx, anna, post, "Первый комментарий")
	must(err)
	e := next(ch, "comment")
	if e.Data["post"] != post || e.Data["id"] != c.ID || !strings.Contains(e.Data["html"], "Первый комментарий") ||
		!strings.Contains(e.Data["html"], `class="fc fnew"`) || strings.Contains(e.Data["html"], "data-cedit-form") {
		t.Errorf("a live comment of a colleague: %v", e.Data)
	}
	must(srv.Feed.EditComment(ctx, anna, c.ID, "Исправленный"))
	if e := next(ch, "cedit"); e.Data["id"] != c.ID || !strings.Contains(e.Data["html"], "Исправленный") {
		t.Errorf("a live edit: %v", e.Data)
	}
	must(srv.Feed.DeleteComment(ctx, anna, c.ID))
	if e := next(ch, "cdel"); e.Data["id"] != c.ID {
		t.Errorf("a live deletion: %v", e.Data)
	}
	// a comment deleted before its moment (the messenger did not take it) is not shown
	c2, err := srv.Feed.AddComment(ctx, anna, post, "Сразу удалён")
	must(err)
	must(srv.Feed.DeleteComment(ctx, anna, c2.ID))
	next(ch, "cdel")
	none(ch)
	// a new post still comes to the unfiltered feed, not to a filtered one
	fch, fstop := open("/feed/live?only=comments")
	defer fstop()
	time.Sleep(200 * time.Millisecond)
	p2, err := srv.Feed.CreatePost(ctx, anna, "", "", "Вторая новость", false)
	must(err)
	if e := next(ch, "post"); e.Data["id"] != p2 {
		t.Errorf("a live post: %v", e.Data)
	}
	none(fch)
	// the own comment is not marked new
	_, err = srv.Feed.AddComment(ctx, ivan, p2, "Мой")
	must(err)
	if e := next(fch, "comment"); strings.Contains(e.Data["html"], "fnew") || !strings.Contains(e.Data["html"], "data-cedit-form") {
		t.Errorf("an own live comment: %v", e.Data)
	}
}
