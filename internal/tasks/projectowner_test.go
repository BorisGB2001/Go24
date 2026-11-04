package tasks

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestTransferAndDeleteProject: «Сменить владельца» and «Удалить группу» of a group — the former owner stays a
// moderator, the new one is told; a group with tasks is not deleted, an empty one is, with its trash.
func TestTransferAndDeleteProject(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	owner, mod, outsider := newUser("owner"), newUser("mod"), newUser("outsider")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, owner, "GRP", "Группа", "")
	must(err)
	p, _ := s.ProjectByKey(ctx, owner, "GRP")
	must(s.SetMember(ctx, p, mod, "manager"))

	pm, _ := s.ProjectByKey(ctx, mod, "GRP")
	if err := s.TransferProject(ctx, pm, mod, mod); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a moderator transfers: %v", err)
	}
	if err := s.TransferProject(ctx, p, owner, outsider); err == nil {
		t.Fatal("a non-member becomes the owner")
	}
	must(s.TransferProject(ctx, p, owner, mod))
	p, _ = s.ProjectByKey(ctx, owner, "GRP")
	pm, _ = s.ProjectByKey(ctx, mod, "GRP")
	if p.MyRole != "manager" || pm.MyRole != "owner" {
		t.Fatalf("roles: former %q, new %q", p.MyRole, pm.MyRole)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'project_owner' AND url = '/p/GRP'`, mod).Scan(&n))
	if n != 1 {
		t.Fatalf("notifications = %d", n)
	}

	key, err := s.CreateTask(ctx, mod, pm, NewTask{Title: "Задача", ResponsibleID: mod})
	must(err)
	if err := s.DeleteProject(ctx, p, owner, "Группа"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a moderator deletes: %v", err)
	}
	if err := s.DeleteProject(ctx, pm, mod, "Не то"); err == nil {
		t.Fatal("deleted with a wrong name")
	}
	if err := s.DeleteProject(ctx, pm, mod, "Группа"); err == nil {
		t.Fatal("a group with tasks is deleted")
	}
	_, err = pool.Exec(ctx, `UPDATE tasks_all SET deleted_at = now() WHERE project_id = $1`, pm.ID) // the task is in the trash
	must(err)
	must(s.DeleteProject(ctx, pm, mod, " Группа "))
	if _, err := s.ProjectByKey(ctx, mod, "GRP"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the group is still there: %v", err)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks_all WHERE project_id = $1`, pm.ID).Scan(&n))
	if n != 0 {
		t.Fatalf("%s is left: %d", key, n)
	}
}
