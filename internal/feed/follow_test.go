package feed

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestFollow: «Следить / Не следить» — a new comment is told to everyone who follows the post: the
// author («прокомментировал ваше сообщение»), those who commented or were mentioned and those who chose «Следить»
// («…сообщение, за которым вы следите»); «Не следить» stops it until one's own next comment; the follower must
// still be able to read the post.
func TestFollow(t *testing.T) {
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
	boss, anna, ivan, olga, petr := newUser("boss"), newUser("anna"), newUser("ivan"), newUser("olga"), newUser("petr")
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	// kinds: the notifications of the post each user got, in order
	kinds := func(post, user string) []string {
		t.Helper()
		var k []string
		must(pool.QueryRow(ctx, `SELECT coalesce(array_agg(kind ORDER BY created_at, id), '{}') FROM notifications
			WHERE post_id = $1 AND user_id = $2`, post, user).Scan(&k))
		return k
	}
	same := func(what string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", what, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: %v, want %v", what, got, want)
			}
		}
	}
	following := func(post, user string) bool {
		t.Helper()
		items, err := s.List(ctx, user, Filter{Post: post})
		must(err)
		if len(items) != 1 || items[0].Post == nil {
			t.Fatalf("post %s for %s: %+v", post, user, items)
		}
		return items[0].Post.Following
	}

	post, err := s.CreatePost(ctx, boss, "", "", "Новость", false)
	must(err)
	if !following(post, boss) || following(post, anna) {
		t.Fatal("only the author follows a new post")
	}
	_, err = s.AddComment(ctx, anna, post, "Здорово")
	must(err)
	same("author", kinds(post, boss), "post_comment")
	if !following(post, anna) {
		t.Fatal("a commenter follows the post")
	}
	// ivan comments: the author and anna are told; olga is mentioned and from then on follows it
	_, err = s.AddComment(ctx, ivan, post, "Согласен", olga)
	must(err)
	same("author", kinds(post, boss), "post_comment", "post_comment")
	same("commenter", kinds(post, anna), "post_followed")
	same("mentioned", kinds(post, olga), "mention")
	same("own comment", kinds(post, ivan))
	_, err = s.AddComment(ctx, boss, post, "Спасибо")
	must(err)
	same("mentioned follows", kinds(post, olga), "mention", "post_followed")
	same("commenter", kinds(post, ivan), "post_followed")

	// «Следить» by petr, «Не следить» by anna and by the author
	on, err := s.Mark(ctx, petr, post, "follow")
	must(err)
	if !on || !following(post, petr) {
		t.Fatal("«Следить»")
	}
	for _, u := range []string{anna, boss} {
		on, err := s.Mark(ctx, u, post, "follow")
		must(err)
		if on || following(post, u) {
			t.Fatalf("«Не следить» by %s", u)
		}
	}
	_, err = s.AddComment(ctx, olga, post, "Ок")
	must(err)
	same("followed by choice", kinds(post, petr), "post_followed")
	same("unfollowed", kinds(post, anna), "post_followed", "post_followed")
	same("author unfollowed", kinds(post, boss), "post_comment", "post_comment")
	// anna's own comment makes her follow again
	_, err = s.AddComment(ctx, anna, post, "Ещё")
	must(err)
	if !following(post, anna) {
		t.Fatal("a comment after «Не следить» follows again")
	}
	_, err = s.AddComment(ctx, petr, post, "И я")
	must(err)
	same("follows again", kinds(post, anna), "post_followed", "post_followed", "post_followed")
	// a mention of someone who unfollowed is still a mention, without changing their choice
	_, err = s.AddComment(ctx, petr, post, "Борис?", boss)
	must(err)
	same("unfollowed mentioned", kinds(post, boss), "post_comment", "post_comment", "mention")
	if following(post, boss) {
		t.Fatal("a mention keeps «Не следить»")
	}

	// a project post: a follower who left the project is no longer told
	_, err = ts.CreateProject(ctx, boss, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "GO")
	_, err = pool.Exec(ctx, `INSERT INTO project_members (project_id, user_id, role) VALUES ($1, $2, 'member')`, p.ID, ivan)
	must(err)
	secret, err := s.CreatePost(ctx, boss, p.ID, "", "Проекту", false)
	must(err)
	_, err = s.AddComment(ctx, ivan, secret, "Вижу")
	must(err)
	_, err = pool.Exec(ctx, `DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, p.ID, ivan)
	must(err)
	_, err = s.AddComment(ctx, boss, secret, "Уже нет")
	must(err)
	same("left the project", kinds(secret, ivan))
	if _, err := s.Mark(ctx, ivan, secret, "follow"); err == nil {
		t.Fatal("«Следить» for a post one cannot see")
	}
}
