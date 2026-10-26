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

// TestHideViewsUntil: «Скрыть» takes a post out of one's own feed only (it is in «Скрытые» and by its link);
// «Просмотры» count the others who saw a post they may read; an important post «до даты» stops being important
// after it — out of «Важные» and of the unread badge.
func TestHideViewsUntil(t *testing.T) {
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
	ids := func(user string, f Filter) []string {
		t.Helper()
		items, err := s.List(ctx, user, f)
		must(err)
		var out []string
		for _, it := range items {
			if it.Post != nil {
				out = append(out, it.Post.ID)
			}
		}
		return out
	}
	post, err := s.CreatePost(ctx, anna, "", "", "Новость", false)
	must(err)
	private, err := s.CreatePostTo(ctx, anna, "", Audience{Users: []string{ivan}}, "", "Только Ивану", false)
	must(err)

	// «Скрыть»
	on, err := s.Mark(ctx, ivan, post, "hide")
	must(err)
	if !on {
		t.Fatal("hide is not on")
	}
	if got := ids(ivan, Filter{}); len(got) != 1 || got[0] != private {
		t.Fatalf("ivan's feed after hiding: %v", got)
	}
	if got := ids(ivan, Filter{Hidden: true}); len(got) != 1 || got[0] != post {
		t.Fatalf("«Скрытые»: %v", got)
	}
	if got := ids(ivan, Filter{Post: post}); len(got) != 1 {
		t.Fatalf("a hidden post by its link: %v", got)
	}
	if got := ids(olga, Filter{}); len(got) != 1 || got[0] != post {
		t.Fatalf("olga's feed: %v", got)
	}
	if on, err = s.Mark(ctx, ivan, post, "hide"); err != nil || on {
		t.Fatalf("«Вернуть в ленту»: %v %v", on, err)
	}
	if got := ids(ivan, Filter{}); len(got) != 2 {
		t.Fatalf("ivan's feed after bringing back: %v", got)
	}
	if _, err := s.Mark(ctx, olga, private, "hide"); err != ErrForbidden {
		t.Fatalf("hiding a post one may not see: %v", err)
	}

	// «Просмотры»: the author and a post one may not read do not count, a second view neither
	must(s.See(ctx, ivan, []string{post, private}))
	must(s.See(ctx, ivan, []string{post}))
	must(s.See(ctx, olga, []string{post, private}))
	must(s.See(ctx, anna, []string{post}))
	views := func(id string) int {
		t.Helper()
		it, err := s.Item(ctx, anna, id)
		must(err)
		return it.Post.Views
	}
	if views(post) != 2 || views(private) != 1 {
		t.Fatalf("views: %d %d", views(post), views(private))
	}
	vs, err := s.Viewers(ctx, ivan, post)
	must(err)
	if len(vs) != 2 || vs[0].ID != olga {
		t.Fatalf("viewers: %+v", vs)
	}
	if _, err := s.Viewers(ctx, olga, private); err != ErrForbidden {
		t.Fatalf("viewers of a post one may not see: %v", err)
	}

	// «Важное до»
	imp, err := s.CreatePost(ctx, anna, "", "", "Важно", true)
	must(err)
	if err := s.SetUntil(ctx, ivan, imp, time.Now().Add(time.Hour)); err != ErrForbidden {
		t.Fatalf("SetUntil by another: %v", err)
	}
	if err := s.SetUntil(ctx, anna, imp, time.Now().Add(-time.Hour)); err == nil {
		t.Fatal("SetUntil in the past")
	}
	must(s.SetUntil(ctx, anna, imp, time.Now().Add(time.Hour)))
	it, err := s.Item(ctx, ivan, imp)
	must(err)
	if !it.Post.Important || it.Post.Until.IsZero() {
		t.Fatalf("important until: %v %v", it.Post.Important, it.Post.Until)
	}
	if n, _ := s.Unread(ctx, ivan); n != 1 {
		t.Fatalf("unread before the date: %d", n)
	}
	_, err = pool.Exec(ctx, `UPDATE feed_posts SET important_until = now() - interval '1 minute' WHERE id = $1`, imp)
	must(err)
	if n, _ := s.Unread(ctx, ivan); n != 0 {
		t.Fatalf("unread after the date: %d", n)
	}
	if got := ids(ivan, Filter{Important: true}); len(got) != 0 {
		t.Fatalf("«Важные» after the date: %v", got)
	}
	if it, _ := s.Item(ctx, ivan, imp); it.Post.Important {
		t.Fatal("still important after the date")
	}
	if it, _ := s.Item(ctx, ivan, post); !it.Post.Until.IsZero() {
		t.Fatalf("an ordinary post has a date: %v", it.Post.Until)
	}
}
