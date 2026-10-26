package live

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestListen: a new task_comments row reaches a subscriber of its task through NOTIFY.
func TestListen(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	defer cancel() // runs first: the listener releases its connection before the pool closes
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var user, project, task string
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('u', 'u') RETURNING id`).Scan(&user))
	must(pool.QueryRow(ctx, `INSERT INTO projects (key, name, owner_id) VALUES ('GO', 'go', $1) RETURNING id`, user).Scan(&project))
	must(pool.QueryRow(ctx, `INSERT INTO tasks (project_id, number, title, creator_id, responsible_id) VALUES ($1, 1, 't', $2, $2) RETURNING id`,
		project, user).Scan(&task))

	b := NewBroker()
	got, stop := b.Subscribe(task)
	defer stop()
	done := make(chan struct{})
	go func() {
		b.Listen(ctx, pool, "go24_comments", slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	defer func() { cancel(); <-done }()
	var comment string
	deadline := time.After(10 * time.Second)
	for comment == "" { // the listener needs a moment to LISTEN; insert until a notification arrives
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO task_comments (task_id, author_id, body) VALUES ($1, $2, 'hi') RETURNING id`, task, user).Scan(&id))
		select {
		case c := <-got:
			comment = c
		case <-time.After(300 * time.Millisecond):
		case <-deadline:
			t.Fatal("no notification")
		}
	}
	var exists bool
	must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM task_comments WHERE id = $1 AND task_id = $2)`, comment, task).Scan(&exists))
	if !exists {
		t.Fatalf("notified id %q is not a comment of the task", comment)
	}
}
