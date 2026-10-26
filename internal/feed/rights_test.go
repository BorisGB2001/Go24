package feed

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// TestFeedRights: «Настройки ленты» — to all employees writes the chosen (by name or through a department with its
// sub-departments), an important post — the chosen; the others write to addressees; an administrator always may.
func TestFeedRights(t *testing.T) {
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
	var root, sales, opt string
	if err := pool.QueryRow(ctx, `SELECT id FROM departments WHERE parent_id IS NULL`).Scan(&root); err != nil {
		must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	}
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Продажи', $1) RETURNING id`, root).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Опт', $1) RETURNING id`, sales).Scan(&opt))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, olga, opt)
	must(err)
	s := NewStore(pool, tasks.NewStore(pool))
	admin := tasks.WithAdmin(ctx)

	// nothing set: everyone may everything
	_, err = s.CreatePost(ctx, ivan, "", "", "Всем", true)
	must(err)
	if err := s.SaveRights(ctx, anna, Rights{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("save by not an admin: %v", err)
	}
	must(s.SaveRights(admin, anna, Rights{All: Audience{Depts: []string{sales}}, Important: Audience{Users: []string{anna}}}))
	r, err := s.GetRights(ctx)
	must(err)
	if len(r.All.Depts) != 1 || len(r.Important.Users) != 1 {
		t.Fatalf("rights = %+v", r)
	}
	names, err := s.Names(ctx, r.All)
	must(err)
	if len(names) != 1 || names[0].Name != "Продажи" || !names[0].Dept {
		t.Fatalf("names = %+v", names)
	}
	may := func(user string, all, imp bool) {
		t.Helper()
		a, i, err := s.May(ctx, user)
		must(err)
		if a != all || i != imp {
			t.Fatalf("may(%s) = %v %v, want %v %v", user, a, i, all, imp)
		}
	}
	may(olga, true, false) // through «Опт» under «Продажи»
	may(anna, false, true)
	may(ivan, false, false)

	if _, err := s.CreatePost(ctx, ivan, "", "", "Всем", false); !errors.Is(err, ErrNoPostAll) {
		t.Fatalf("ivan to all: %v", err)
	}
	_, err = s.CreatePostTo(ctx, ivan, "", Audience{Users: []string{olga}}, "", "Ольге", false)
	must(err) // to addressees — anyone
	if _, err := s.CreatePostTo(ctx, anna, "", Audience{Users: []string{olga}}, "", "Важно", true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePostTo(ctx, olga, "", Audience{Users: []string{anna}}, "", "Важно", true); !errors.Is(err, ErrNoImportant) {
		t.Fatalf("olga important: %v", err)
	}
	p, err := s.CreatePost(ctx, olga, "", "", "Всем от Ольги", false)
	must(err)
	_, err = s.CreatePost(admin, ivan, "", "", "Всем от админа", true)
	must(err)

	// edits: making it important, moving to all employees — by the rights of the one who changes it
	if _, err := s.SetImportant(ctx, olga, p, true, time.Time{}); !errors.Is(err, ErrNoImportant) {
		t.Fatalf("olga makes important: %v", err)
	}
	limited, err := s.CreatePostTo(ctx, ivan, "", Audience{Users: []string{olga}}, "", "Только Ольге", false)
	must(err)
	if _, _, err := s.SetAudience(ctx, ivan, limited, "", Audience{}); !errors.Is(err, ErrNoPostAll) {
		t.Fatalf("ivan moves to all: %v", err)
	}
}
