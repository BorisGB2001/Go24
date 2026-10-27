package tasks

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestCommentEdit: an answer quotes the message it answers (one of another task is ignored); only the author
// changes a message («изменено»), the author or an administrator deletes it, answers then lose the quote; the
// card shows all of it.
func TestCommentEdit(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, worker, admin := user("Борис Г"), user("Иван Петров"), user("Админ")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "GO")
	must(err)
	newTask := func(title string) string {
		key, err := s.CreateTask(ctx, boss, p, NewTask{Title: title, ResponsibleID: worker})
		must(err)
		id, err := s.TaskIDByKey(ctx, key)
		must(err)
		return id
	}
	id, other := newTask("Отчёт"), newTask("Другая")

	q, err := s.PostComment(ctx, boss, id, "Когда будет отчёт?")
	must(err)
	far, err := s.PostComment(ctx, boss, other, "Из другой задачи")
	must(err)
	a, err := s.PostReply(ctx, worker, id, q.ID, "Завтра")
	must(err)
	if a.ReplyTo != q.ID || a.ReplyName != "Борис Г" || a.ReplyBody != "Когда будет отчёт?" || a.EditedAt != nil {
		t.Fatalf("answer: %+v", a)
	}
	for _, bad := range []string{far.ID, "garbage"} {
		c, err := s.PostReply(ctx, worker, id, bad, "Без цитаты")
		must(err)
		if c.ReplyTo != "" {
			t.Fatalf("a reply to %q must not quote: %+v", bad, c)
		}
	}

	// only the author changes it
	if _, err := s.EditComment(ctx, boss, id, a.ID, "Сегодня"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("someone else's message: %v", err)
	}
	if _, err := s.EditComment(ctx, worker, id, a.ID, "  "); err == nil {
		t.Fatal("an empty text is refused")
	}
	if _, err := s.EditComment(ctx, worker, other, a.ID, "Сегодня"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a message of another task: %v", err)
	}
	e, err := s.EditComment(ctx, worker, id, a.ID, "Сегодня вечером")
	must(err)
	if e.Body != "Сегодня вечером" || e.EditedAt == nil || e.ReplyTo != q.ID {
		t.Fatalf("edited: %+v", e)
	}

	// the author or an administrator deletes it; the answer keeps its text, the quote goes
	if err := s.DeleteComment(ctx, worker, id, q.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a participant deletes someone else's message: %v", err)
	}
	must(s.DeleteComment(WithAdmin(ctx), admin, id, q.ID))
	if _, err := s.CommentByID(ctx, id, q.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	c, err := s.CommentByID(ctx, id, a.ID)
	must(err)
	if c.ReplyTo != "" || c.Body != "Сегодня вечером" {
		t.Fatalf("the answer after the deletion: %+v", c)
	}
	must(s.DeleteComment(ctx, worker, id, a.ID))
	card, err := s.GetCard(ctx, boss, id)
	must(err)
	if len(card.Comments) != 2 {
		t.Fatalf("left: %+v", card.Comments)
	}
}
