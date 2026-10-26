package feed

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestComments: the menu of a comment — the author edits («изменено»), the author or an
// administrator deletes; a comment is seen with its post only; the link of a comment shows its post alone.
func TestComments(t *testing.T) {
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
	boss, anna, ivan := newUser("boss"), newUser("anna"), newUser("ivan")
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts)
	_, err = ts.CreateProject(ctx, boss, "GO", "go24", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "GO")
	post, err := s.CreatePost(ctx, boss, "", "", "Новость", false)
	must(err)
	secret, err := s.CreatePost(ctx, boss, p.ID, "", "Проекту", false)
	must(err)

	c, err := s.AddComment(ctx, anna, post, "Здорово")
	must(err)
	if !c.CanEdit || !c.CanDelete {
		t.Fatalf("own new comment: %+v", c)
	}
	got, err := s.Comment(ctx, ivan, c.ID)
	must(err)
	if got.CanEdit || got.CanDelete || got.Edited || got.Body != "Здорово" {
		t.Fatalf("someone else's comment: %+v", got)
	}
	if got, _ = s.Comment(tasks.WithAdmin(ctx), ivan, c.ID); got.CanEdit || !got.CanDelete {
		t.Fatalf("administrator deletes, does not edit: %+v", got)
	}
	if err := s.EditComment(ctx, ivan, c.ID, "чужое"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("edit by another: %v", err)
	}
	if err := s.EditComment(ctx, anna, c.ID, "  "); err == nil {
		t.Fatal("empty edit is refused")
	}
	must(s.EditComment(ctx, anna, c.ID, "Очень здорово"))
	got, err = s.Comment(ctx, anna, c.ID)
	must(err)
	if !got.Edited || got.Body != "Очень здорово" || !got.CanEdit {
		t.Fatalf("edited: %+v", got)
	}
	items, err := s.List(ctx, ivan, Filter{})
	must(err)
	var seen *Comment
	for _, it := range items {
		if it.Post != nil && it.Post.ID == post {
			seen = &it.Post.Comments[0]
		}
	}
	if seen == nil || !seen.Edited || seen.CanEdit || seen.CanDelete {
		t.Fatalf("in the feed: %+v", seen)
	}

	sc, err := s.AddComment(ctx, boss, secret, "Только своим")
	must(err)
	if _, err := s.Comment(ctx, anna, sc.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("comment of a project post to an outsider: %v", err)
	}
	author, _, _, err := s.CommentBridge(ctx, sc.ID)
	must(err)
	if author != boss {
		t.Fatalf("bridge author %s", author)
	}

	// the link of a comment: its post alone, invisible posts stay hidden
	one, err := s.List(ctx, ivan, Filter{Post: post})
	must(err)
	if len(one) != 1 || one[0].Post == nil || one[0].Post.ID != post {
		t.Fatalf("one post: %+v", one)
	}
	if none, _ := s.List(ctx, anna, Filter{Post: secret}); len(none) != 0 {
		t.Fatalf("a hidden post by its link: %+v", none)
	}
	if none, err := s.List(ctx, anna, Filter{Post: "not-a-uuid"}); err != nil || len(none) != 0 {
		t.Fatalf("a wrong link: %v %+v", err, none)
	}

	if err := s.DeleteComment(ctx, ivan, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("delete by another: %v", err)
	}
	must(s.DeleteComment(tasks.WithAdmin(ctx), ivan, c.ID))
	if _, err := s.Comment(ctx, anna, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	c2, err := s.AddComment(ctx, anna, post, "ещё")
	must(err)
	must(s.DeleteComment(ctx, anna, c2.ID))
}
