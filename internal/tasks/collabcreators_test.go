package tasks

import (
	"context"
	"errors"
	"testing"

	"go24/internal/i18n"
)

// TestCollabCreateRights: the company's rule of who makes collabs — everybody by default, the chosen employees and
// departments (with sub-departments), administrators only, or nobody when collabs are off; guests never.
func TestCollabCreateRights(t *testing.T) {
	pool, s := freshDB(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@test', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	boss, chosen, deep, plain, guest := newUser("boss", false), newUser("chosen", false), newUser("deep", false), newUser("plain", false), newUser("guest", true)
	var root, sub string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Опт', $1) RETURNING id`, root).Scan(&sub))
	_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, deep, sub)
	must(err)
	admin := AsAdministrator(ctx)
	check := func(label string, ctx context.Context, user string, want bool) {
		t.Helper()
		may, err := s.MayCreateCollab(ctx, user)
		must(err)
		if may != want {
			t.Fatalf("%s: may = %v", label, may)
		}
		_, err = s.CreateCollab(ctx, user, "Коллаб "+label, "", nil)
		if want && err != nil || !want && !errors.Is(err, ErrForbidden) {
			t.Fatalf("%s: create = %v", label, err)
		}
	}

	if c, err := s.CollabCreators(ctx); err != nil || c.Who != "all" || c.Off {
		t.Fatalf("default = %+v, %v", c, err)
	}
	check("all: plain", ctx, plain, true)
	check("all: guest", ctx, guest, false)

	var ve *i18n.Error
	if err := s.SetCollabCreators(ctx, boss, CollabCreators{Who: "nobody"}); !errors.As(err, &ve) || ve.Key != "err.collab_creators_who" {
		t.Fatalf("unknown who: %v", err)
	}
	if err := s.SetCollabCreators(ctx, boss, CollabCreators{Who: "some"}); !errors.As(err, &ve) || ve.Key != "err.collab_creators_empty" {
		t.Fatalf("some of nobody: %v", err)
	}
	must(s.SetCollabCreators(ctx, boss, CollabCreators{Who: "some", Users: []string{chosen}, Depts: []string{root}}))
	check("some: chosen", ctx, chosen, true)
	check("some: sub-department", ctx, deep, true)
	check("some: plain", ctx, plain, false)
	check("some: admin", admin, boss, true)
	ds, err := s.DeptChoices(ctx, []string{root, "00000000-0000-0000-0000-000000000000"})
	must(err)
	if len(ds) != 1 || ds[0].ID != "dept:"+root || ds[0].Name != "Продажи" {
		t.Fatalf("dept choices = %+v", ds)
	}

	must(s.SetCollabCreators(ctx, boss, CollabCreators{Who: "admins", Users: []string{chosen}}))
	if c, _ := s.CollabCreators(ctx); c.Who != "admins" || c.Users != nil {
		t.Fatalf("admins kept people: %+v", c)
	}
	check("admins: chosen", ctx, chosen, false)
	check("admins: admin", admin, boss, true)
	check("admins: admin mode", WithAdmin(ctx), boss, true)
	check("admins: guest", admin, guest, false)

	must(s.SetCollabCreators(ctx, boss, CollabCreators{Off: true, Who: "all"}))
	check("off: plain", ctx, plain, false)
	check("off: admin", admin, boss, false)
	// what there is stays: the collabs made before work as they did
	if list, err := s.ListCollabs(ctx, plain, false, ""); err != nil || len(list) != 1 {
		t.Fatalf("collabs after off = %d, %v", len(list), err)
	}
}
