package company

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestWorktimeRights: by default the heads and deputies see and change the working time of their departments with the
// sub-departments, nobody else does; the rights widen it to everyone's, give an employee their colleagues, split
// seeing from changing and give the settings; an administrator may everything, a guest nothing.
func TestWorktimeRights(t *testing.T) {
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
	user := func(sub, name string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	boss, dep, anna, olga, kim, ivan := user("boss", "Борис"), user("dep", "Вера"), user("anna", "Анна"), user("olga", "Ольга"),
		user("kim", "Ким"), user("ivan", "Иван")
	guest := user("guest", "Гость")
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest)
	must(err)
	var root, sales, east, it string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Продажи', $1, $2) RETURNING id`, root, boss).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Восток', $1, $2) RETURNING id`, sales, kim).Scan(&east))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('ИТ', $1) RETURNING id`, root).Scan(&it))
	for _, x := range [][2]string{{anna, sales}, {olga, east}, {ivan, it}} {
		_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, x[0], x[1])
		must(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO department_deputies (department_id, user_id) VALUES ($1, $2)`, sales, dep)
	must(err)
	s := NewStore(pool)
	can := func(actor, who, action string, want bool) {
		t.Helper()
		got, err := s.WorktimeCan(ctx, actor, who, false, action)
		must(err)
		if got != want {
			t.Errorf("%s %s of %s = %v, want %v", actor, action, who, got, want)
		}
	}
	people := func(actor, action string, want ...string) {
		t.Helper()
		list, err := s.WorktimePeople(ctx, actor, false, action)
		must(err)
		var got []string
		for _, p := range list {
			got = append(got, p.ID)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s %s people = %v, want %v", actor, action, got, want)
		}
	}
	settings := func(actor string, want bool) {
		t.Helper()
		got, err := s.WorktimeSettings(ctx, actor, false)
		must(err)
		if got != want {
			t.Errorf("%s settings = %v, want %v", actor, got, want)
		}
	}

	// the defaults: the head and the deputy — their departments with the sub-departments (and the heads of those)
	people(boss, "edit", dep, anna, olga, kim)
	people(dep, "view", anna, olga, kim, boss)
	people(kim, "view", olga)
	people(anna, "view")
	can(boss, olga, "edit", true)
	can(boss, ivan, "view", false)
	can(anna, olga, "view", false)
	can(boss, boss, "view", false)
	settings(boss, false)
	if ok, _ := s.WorktimeCan(ctx, anna, ivan, true, "edit"); !ok {
		t.Error("an administrator may change anyone's time")
	}
	if ok, _ := s.WorktimeSettings(ctx, anna, true); !ok {
		t.Error("an administrator may change the settings")
	}

	// heads see everyone but change only their own departments, employees see their colleagues, deputies have
	// the settings
	must(s.SetWorktimeRights(ctx, StructRights{
		"head": {"view": "all", "edit": "own"}, "employee": {"view": "own"}, "deputy": {"settings": "all", "edit": "bad"},
		"nobody": {"view": "all"}}))
	r, err := s.WorktimeRights(ctx)
	must(err)
	if r.Scope("head", "view") != "all" || r.Scope("deputy", "edit") != "" || r.Scope("deputy", "settings") != "all" {
		t.Errorf("rights = %v", r)
	}
	people(boss, "view", dep, anna, olga, kim, ivan)
	people(kim, "view", boss, dep, anna, olga, ivan)
	people(kim, "edit", olga)
	can(kim, ivan, "edit", false)
	people(anna, "view", boss, dep, olga, kim)
	people(ivan, "view")
	can(anna, olga, "edit", false)
	people(dep, "view")
	settings(dep, true)
	settings(boss, false)
	people(guest, "view")
	can(guest, anna, "view", false)

	// «own» settings are dropped: settings are all or nothing
	must(s.SetWorktimeRights(ctx, StructRights{"head": {"settings": "own"}}))
	settings(boss, false)
}
