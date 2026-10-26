package crm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCRMTimelinePins: any line of a card's timeline is pinned and unpinned; a comment is edited (marked so) and
// deleted by its author or an administrator only; a line of another card or another kind is not found.
func TestCRMTimelinePins(t *testing.T) {
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
	var anna, boris string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	s := NewStore(pool)
	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка"})
	must(err)
	other, err := s.SaveLead(ctx, anna, Lead{Title: "Другая"})
	must(err)
	must(s.Comment(ctx, anna, "lead", lead, "Позвонить"))
	line := func(kind string) Event {
		t.Helper()
		ev, err := s.Timeline(ctx, "lead", lead)
		must(err)
		for _, e := range ev {
			if e.Kind == kind {
				return e
			}
		}
		t.Fatalf("no %s in %+v", kind, ev)
		return Event{}
	}
	c, created := line("comment"), line("created")
	if c.AuthorID != anna || c.Pinned || c.Edited {
		t.Fatalf("comment %+v", c)
	}

	must(s.Pin(ctx, "lead", lead, created.ID, true))
	must(s.Pin(ctx, "lead", lead, c.ID, true))
	if !line("created").Pinned || !line("comment").Pinned {
		t.Fatal("not pinned")
	}
	must(s.Pin(ctx, "lead", lead, created.ID, false))
	if line("created").Pinned {
		t.Fatal("not unpinned")
	}
	if err := s.Pin(ctx, "lead", other, c.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pin of another card: %v", err)
	}

	if err := s.EditComment(ctx, boris, "lead", lead, c.ID, "Чужое"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another's edit: %v", err)
	}
	if err := s.EditComment(ctx, anna, "lead", lead, created.ID, "Не комментарий"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit of a non-comment: %v", err)
	}
	if err := s.EditComment(ctx, anna, "lead", lead, c.ID, "  "); err == nil {
		t.Fatal("empty edit")
	}
	must(s.EditComment(ctx, anna, "lead", lead, c.ID, "Позвонить завтра"))
	if e := line("comment"); e.Body != "Позвонить завтра" || !e.Edited || !e.Pinned {
		t.Fatalf("edited %+v", e)
	}
	must(s.EditComment(tasks.WithAdmin(ctx), boris, "lead", lead, c.ID, "Позвонить в пятницу"))

	if err := s.DeleteComment(ctx, boris, "lead", lead, c.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("another's delete: %v", err)
	}
	must(s.DeleteComment(ctx, anna, "lead", lead, c.ID))
	ev, err := s.Timeline(ctx, "lead", lead)
	must(err)
	for _, e := range ev {
		if e.Kind == "comment" {
			t.Fatalf("not deleted %+v", ev)
		}
	}
}
