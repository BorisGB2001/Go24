package tasks

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestGroupTaskPerms: «Права доступа» of a group to its tasks — by default all members create tasks and the owner
// and moderators change and delete any of them; narrowed and widened, the cards, CreateTask and DeleteTask follow.
func TestGroupTaskPerms(t *testing.T) {
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
	owner, mod, member := newUser("owner"), newUser("mod"), newUser("member")
	s := NewStore(pool)
	_, err = s.CreateProject(ctx, owner, "GP", "Группа", "")
	must(err)
	p, _ := s.ProjectByKey(ctx, owner, "GP")
	must(s.SetMember(ctx, p, mod, "manager"))
	must(s.SetMember(ctx, p, member, "member"))
	proj := func(u string) Project {
		t.Helper()
		x, err := s.ProjectByKey(ctx, u, "GP")
		must(err)
		return x
	}
	if tp := proj(owner).TaskPerms(); tp != DefaultTaskPerms {
		t.Fatalf("defaults = %+v", tp)
	}
	create := func(u string) (string, error) {
		key, err := s.CreateTask(ctx, u, proj(u), NewTask{Title: "Задача " + u, Priority: 1})
		if err != nil {
			return "", err
		}
		id, _ := s.TaskIDByKey(ctx, key)
		return id, nil
	}
	ownerTask, err := create(owner)
	must(err)
	memberTask, err := create(member)
	must(err)
	card := func(u, id string) TaskCard {
		t.Helper()
		c, err := s.GetCard(ctx, u, id)
		must(err)
		return c
	}
	// the defaults: the moderator changes and deletes the owner's task, a member only their own
	if c := card(mod, ownerTask); !c.Roles.Manager || !c.CanDelete {
		t.Fatalf("moderator by default: manager %v, delete %v", c.Roles.Manager, c.CanDelete)
	}
	if c := card(member, ownerTask); c.Roles.Manager || c.CanDelete {
		t.Fatalf("member by default: manager %v, delete %v", c.Roles.Manager, c.CanDelete)
	}
	if c := card(member, memberTask); !c.Roles.Creator || !c.CanDelete {
		t.Fatal("the creator lost their own task")
	}

	// a member may not change the rights; the owner narrows creating to the moderators and widens changing to all
	if err := s.SetTaskPerms(ctx, proj(member), TaskPerms{Create: "owner", Edit: "owner", Delete: "owner"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member sets the rights: %v", err)
	}
	if err := s.SetTaskPerms(ctx, proj(owner), TaskPerms{Create: "managers", Edit: "anyone", Delete: "owner"}); err == nil {
		t.Fatal("an unknown level is taken")
	}
	must(s.SetTaskPerms(ctx, proj(owner), TaskPerms{Create: "managers", Edit: "members", Delete: "owner"}))
	if proj(member).CanCreate() || !proj(mod).CanCreate() {
		t.Fatal("CanCreate does not follow «Создание задач»")
	}
	if _, err := create(member); !errors.Is(err, ErrForbidden) {
		t.Fatalf("member creates with «managers»: %v", err)
	}
	if _, err := create(mod); err != nil {
		t.Fatal(err)
	}
	if c := card(member, ownerTask); !c.Roles.Manager || c.CanDelete {
		t.Fatalf("member with edit=members, delete=owner: manager %v, delete %v", c.Roles.Manager, c.CanDelete)
	}
	if c := card(mod, ownerTask); c.CanDelete {
		t.Fatal("the moderator deletes with delete=owner")
	}
	if err := s.DeleteTask(ctx, mod, memberTask); !errors.Is(err, ErrForbidden) {
		t.Fatalf("moderator deletes another's task with delete=owner: %v", err)
	}
	must(s.DeleteTask(ctx, owner, memberTask))

	// «Изменение задач» — only the owner: the moderator no longer manages others' tasks
	must(s.SetTaskPerms(ctx, proj(owner), TaskPerms{Create: "members", Edit: "owner", Delete: "managers"}))
	if c := card(mod, ownerTask); c.Roles.Manager || !c.CanDelete {
		t.Fatalf("moderator with edit=owner, delete=managers: manager %v, delete %v", c.Roles.Manager, c.CanDelete)
	}
	if err := s.UpdateTask(ctx, mod, ownerTask, TaskUpdate{Title: "Чужая", Priority: 1}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("moderator changes the owner's task with edit=owner: %v", err)
	}
}
