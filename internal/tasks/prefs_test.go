package tasks

import (
	"context"
	"os"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestWidths: dragged widths are kept per user and list; bad ones are refused, none forget the list.
func TestWidths(t *testing.T) {
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
	a, b := user("Анна"), user("Борис")
	s := NewStore(pool)
	must(s.SetWidths(ctx, a, "tasks", map[string]int{"title": 320, "id": 80}))
	must(s.SetWidths(ctx, a, "pane:task-chat", map[string]int{"w": 500}))
	must(s.SetWidths(ctx, a, "tasks", map[string]int{"title": 300}))
	must(s.SetWidths(ctx, b, "tasks", map[string]int{"title": 200}))
	got, err := s.Widths(ctx, a)
	must(err)
	if want := map[string]map[string]int{"tasks": {"title": 300}, "pane:task-chat": {"w": 500}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("widths = %v", got)
	}
	for _, bad := range []map[string]int{{"x": 5}, {"x": 9000}, {"": 100}} {
		if s.SetWidths(ctx, a, "tasks", bad) == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if s.SetWidths(ctx, a, "", map[string]int{"x": 100}) == nil {
		t.Fatal("a list without a name accepted")
	}
	must(s.SetWidths(ctx, a, "tasks", nil))
	if got, _ = s.Widths(ctx, a); len(got) != 1 || got["tasks"] != nil {
		t.Fatalf("after forgetting: %v", got)
	}
}
