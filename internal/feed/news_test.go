package feed

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestFeedNews: the «Лента» counter of the menu counts the posts of others since the person last opened the feed
// (not the hidden ones, not those for others) and the comments of others to the posts they follow; opening the feed
// starts it over and tells the moment before, so what came since is marked new.
func TestFeedNews(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, n).Scan(&id))
		return id
	}
	anna, ivan, olga := newUser("anna"), newUser("ivan"), newUser("olga")
	s := NewStore(pool, tasks.NewStore(pool))
	news := func(user string, want int) {
		t.Helper()
		n, err := s.News(ctx, user)
		must(err)
		if n != want {
			t.Fatalf("news = %d, want %d", n, want)
		}
	}
	// never opened: the last week is new
	old, err := s.CreatePost(ctx, anna, "", "", "Старое", false)
	must(err)
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	must2(pool.Exec(ctx, `UPDATE feed_posts SET created_at = now() - interval '8 days' WHERE id = $1`, old))
	p1, err := s.CreatePost(ctx, anna, "", "", "Новость", false)
	must(err)
	news(ivan, 1)
	news(anna, 0) // one's own is not new
	since, err := s.Seen(ctx, ivan)
	must(err)
	if time.Since(since) < 6*24*time.Hour {
		t.Fatalf("first visit: since %v, want a week ago", since)
	}
	news(ivan, 0)

	// opened now: what comes later is new
	must2(pool.Exec(ctx, `UPDATE feed_posts SET created_at = created_at - interval '2 minutes'`))
	must2(pool.Exec(ctx, `UPDATE feed_seen SET seen_at = now() - interval '1 minute' WHERE user_id = $1`, ivan))
	p2, err := s.CreatePostTo(ctx, anna, "", Audience{Users: []string{olga}}, "", "Только Ольге", false)
	must(err)
	news(ivan, 0) // not for him
	news(olga, 2)
	p3, err := s.CreatePost(ctx, olga, "", "", "Ещё", false)
	must(err)
	news(ivan, 1)
	_, err = s.Mark(ctx, ivan, p3, "hide")
	must(err)
	news(ivan, 0) // hidden
	// comments: to the posts he follows (his own, by «Следить», his comment) only
	mine, err := s.CreatePost(ctx, ivan, "", "", "Моё", false)
	must(err)
	must2(s.AddComment(ctx, anna, mine, "К твоему"))
	must2(s.AddComment(ctx, anna, p1, "Не к твоему"))
	news(ivan, 1)
	must2(s.AddComment(ctx, ivan, p1, "Моё к нему"))
	must2(s.AddComment(ctx, olga, p1, "Теперь следит"))
	p4, err := s.CreatePost(ctx, olga, "", "", "Последнее", false)
	must(err)
	news(ivan, 4) // he follows p1 now (his comment): its comments since his visit count, the earlier one too

	prev, err := s.Seen(ctx, ivan)
	must(err)
	if time.Since(prev) < 30*time.Second || time.Since(prev) > 2*time.Minute {
		t.Fatalf("since = %v, want a minute ago", prev)
	}
	news(ivan, 0)
	items, err := s.List(ctx, ivan, Filter{})
	must(err)
	MarkNew(items, ivan, prev)
	got := map[string]bool{}
	for _, it := range items {
		if it.Post == nil {
			continue
		}
		got[it.Post.ID] = it.Post.New
		for _, c := range it.Post.Comments {
			if c.New != (c.AuthorID != ivan) {
				t.Fatalf("comment %q new = %v", c.Body, c.New)
			}
		}
	}
	if _, ok := got[p2]; ok || got[p1] || got[mine] || !got[p4] || len(got) != 4 {
		t.Fatalf("new posts: %v (p1 %s, mine %s, p4 %s)", got, p1, mine, p4)
	}
}
