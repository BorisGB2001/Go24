package kb

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/tasks"
)

// testDB is a clean test database, or the test is skipped.
func testDB(t *testing.T) (context.Context, *pgxpool.Pool, func(error), func(string) string) {
	t.Helper()
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	return ctx, pool, must, newUser
}

// TestKnowledge: a company space and a project space, who reads and writes, the tree, the history, the search.
func TestKnowledge(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	var err error
	boss, dev, guest := newUser("boss"), newUser("dev"), newUser("guest")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "KB", "Проект", "")
	must(err)
	p, _ := ts.ProjectByKey(ctx, boss, "KB")
	must(ts.SetMember(ctx, p, dev, "viewer"))
	s := NewStore(pool)

	company, err := s.CreateSpace(ctx, boss, "Регламенты", "", "")
	must(err)
	proj, err := s.CreateSpace(ctx, boss, "Проектная", "", p.ID)
	must(err)
	if _, err := s.CreateSpace(ctx, guest, "Чужая", "", p.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a stranger made a project space: %v", err)
	}
	if list, _ := s.Spaces(ctx, guest); len(list) != 1 || list[0].ID != company || !list[0].CanEdit || list[0].CanManage {
		t.Fatalf("guest's spaces = %+v", list)
	}
	if sp, _ := s.Space(ctx, dev, proj); sp.CanEdit { // a viewer of the project reads only
		t.Fatal("a project viewer may write")
	}

	root, err := s.CreatePage(ctx, guest, company, "", "Отпуск", "# Как оформить\nЗаявление за **две недели**")
	must(err)
	child, err := s.CreatePage(ctx, boss, company, root, "Больничный", "Сообщить руководителю")
	must(err)
	if _, err := s.CreatePage(ctx, dev, proj, "", "x", ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a viewer wrote a page: %v", err)
	}
	if _, err := s.CreatePage(ctx, guest, proj, "", "x", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a stranger wrote into a project space: %v", err)
	}
	tree, _ := s.Tree(ctx, company)
	if len(tree) != 1 || len(tree[0].Children) != 1 || tree[0].Children[0].ID != child {
		t.Fatalf("tree = %+v", tree)
	}

	must(s.SavePage(ctx, dev, root, "Отпуск и отгулы", "Заявление за **две недели**, отгул — за день"))
	vs, _ := s.Versions(ctx, root)
	if len(vs) != 1 || vs[0].Title != "Отпуск" || vs[0].Editor != "guest" {
		t.Fatalf("versions = %+v", vs)
	}
	old, err := s.Version(ctx, guest, root, vs[0].ID)
	must(err)
	if old.Title != "Отпуск" {
		t.Fatalf("old version = %+v", old)
	}
	if hits, _ := s.Search(ctx, guest, "отгул", ""); len(hits) != 1 || hits[0].ID != root {
		t.Fatalf("search = %+v", hits)
	}
	if _, err := s.DeletePage(ctx, guest, root); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Page(ctx, boss, child); !errors.Is(err, ErrNotFound) {
		t.Fatal("the sub-page outlived its parent")
	}
	if err := s.DeleteSpace(ctx, guest, company); !errors.Is(err, ErrForbidden) {
		t.Fatalf("guest deleted the company space: %v", err)
	}
	must(s.DeleteSpace(ctx, boss, company))
}
