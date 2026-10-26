package tasks

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTaskViews: opening the card counts a view of the person (first and last time, how many times), the list
// goes the latest first; one who does not see the task does not learn who saw it.
func TestTaskViews(t *testing.T) {
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
	boss, worker, stranger := user("Борис Г"), user("Иван Петров"), user("Чужой")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, boss, "GO", "go", "")
	must(err)
	p, err := s.ProjectByKey(ctx, boss, "GO")
	must(err)
	key, err := s.CreateTask(ctx, boss, p, NewTask{Title: "Отчёт", ResponsibleID: worker})
	must(err)
	id, err := s.TaskIDByKey(ctx, key)
	must(err)

	list, err := s.Viewers(ctx, boss, id)
	must(err)
	if len(list) != 0 {
		t.Fatalf("nobody opened the task yet: %+v", list)
	}
	must(s.See(ctx, worker, id))
	must(s.See(ctx, boss, id))
	must(s.See(ctx, worker, id))
	list, err = s.Viewers(ctx, worker, id)
	must(err)
	if len(list) != 2 || list[0].ID != worker || list[0].Times != 2 || list[1].ID != boss || list[1].Times != 1 {
		t.Fatalf("viewers, the latest first: %+v", list)
	}
	if !list[0].Last.After(list[0].First) || list[0].Name != "Иван Петров" {
		t.Fatalf("the first and the last view: %+v", list[0])
	}
	if _, err := s.Viewers(ctx, stranger, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger: %v", err)
	}
	must(s.DeleteTask(ctx, boss, id)) // to the trash: the views stay with the task
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_views WHERE task_id = $1`, id).Scan(&n))
	if n != 2 {
		t.Fatalf("views of a task in the trash: %d", n)
	}
}
