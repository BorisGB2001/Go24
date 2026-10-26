package bi

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestAccess: a draft is the author's, a published dashboard everyone's; «Права доступа» of a dashboard and of its
// group close it to the others, open it to an employee or a department with its sub-departments, to view or change.
func TestAccess(t *testing.T) {
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
	user := func(sub string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, sub).Scan(&id))
		return
	}
	anna, petr, olga := user("anna"), user("petr"), user("olga")
	var sales, east string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Восток', $1) RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, olga, east)
	must(err)
	s := NewStore(pool, crm.NewStore(pool))
	sees := func(v string, id string) string {
		t.Helper()
		d, err := s.Dashboard(ctx, Viewer{ID: v}, id)
		if errors.Is(err, ErrNotFound) {
			return "-"
		}
		must(err)
		if list, _ := s.Dashboards(ctx, Viewer{ID: v}, "", ""); !func() bool {
			for _, x := range list {
				if x.ID == id {
					return true
				}
			}
			return false
		}() {
			t.Errorf("%s: the list has not %s", v, id)
		}
		return d.Access
	}
	id, err := s.SaveDashboard(ctx, anna, Dashboard{Title: "План"})
	must(err)
	if a, p := sees(anna, id), sees(petr, id); a != "edit" || p != "-" {
		t.Errorf("draft: anna %s, petr %s", a, p)
	}
	must(s.Publish(ctx, id, "published", ""))
	if p, o := sees(petr, id), sees(olga, id); p != "view" || o != "view" {
		t.Errorf("published: petr %s, olga %s", p, o)
	}
	if err := s.Publish(ctx, id, "secret", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad status: %v", err)
	}

	// a line for the department closes the dashboard to the others; a sub-department's employee has it
	on := On{Dashboard: id}
	must(s.SetRight(ctx, anna, on, "department", sales, "edit"))
	if p, o, a := sees(petr, id), sees(olga, id), sees(anna, id); p != "-" || o != "edit" || a != "edit" {
		t.Errorf("department: petr %s, olga %s, anna %s", p, o, a)
	}
	if d, _ := s.Dashboard(ctx, Viewer{ID: olga}, id); !d.CanEdit() || d.CanManage(Viewer{ID: olga}) {
		t.Errorf("an editor manages: %+v", d)
	}
	if err := s.SetRight(ctx, anna, on, "user", "nobody", "view"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such user: %v", err)
	}
	if err := s.SetRight(ctx, anna, on, "all", "", "full"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad access: %v", err)
	}

	// the group's lines are of its dashboards too; the best line wins
	g, err := s.SaveGroup(ctx, anna, "", " Финансы ")
	must(err)
	must(s.Publish(ctx, id, "published", g))
	must(s.SetRight(ctx, anna, On{Group: g}, "user", petr, "view"))
	must(s.SetRight(ctx, anna, On{Group: g}, "user", olga, "view"))
	if p, o := sees(petr, id), sees(olga, id); p != "view" || o != "edit" {
		t.Errorf("group: petr %s, olga %s", p, o)
	}
	if list, _ := s.Dashboards(ctx, Viewer{ID: petr}, "", g); len(list) != 1 || list[0].GroupTitle != "Финансы" {
		t.Errorf("of the group %+v", list)
	}
	if list, _ := s.Dashboards(ctx, Viewer{ID: petr}, "", "00000000-0000-0000-0000-000000000000"); len(list) != 0 {
		t.Errorf("of no such group %+v", list)
	}
	rights, err := s.Rights(ctx, On{Group: g})
	must(err)
	if len(rights) != 2 || rights[0].Kind != "user" || rights[0].Access != "view" {
		t.Errorf("group rights %+v", rights)
	}
	must(s.SetRight(ctx, anna, On{Group: g}, "user", petr, "edit")) // changed, not added
	if rights, _ = s.Rights(ctx, On{Group: g}); len(rights) != 2 || sees(petr, id) != "edit" {
		t.Errorf("changed %+v", rights)
	}
	must(s.DeleteRight(ctx, On{Group: g}, rights[0].ID))
	if err := s.DeleteRight(ctx, on, rights[1].ID); !errors.Is(err, ErrNotFound) { // a line of another
		t.Errorf("another's line: %v", err)
	}

	// the group deleted: the dashboard stays with its own lines; them cleared — everyone views it
	must(s.DeleteGroup(ctx, g))
	if d, _ := s.Dashboard(ctx, Viewer{ID: anna}, id); d.GroupID != "" {
		t.Errorf("group kept %+v", d)
	}
	if p := sees(petr, id); p != "-" {
		t.Errorf("after the group: petr %s", p)
	}
	must(s.ClearRights(ctx, on))
	if p := sees(petr, id); p != "view" {
		t.Errorf("cleared: petr %s", p)
	}

	// a copy is a draft of its maker, in no group
	must(s.Publish(ctx, id, "published", ""))
	cp, err := s.CopyDashboard(ctx, petr, id, "Мой план", nil)
	must(err)
	if d, _ := s.Dashboard(ctx, Viewer{ID: petr}, cp); d.Status != "draft" || d.Access != "edit" || sees(anna, cp) != "-" {
		t.Errorf("copy %+v", d)
	}
	if d, _ := s.Dashboard(ctx, Viewer{Admin: true}, cp); d.Access != "edit" {
		t.Errorf("an administrator %+v", d)
	}
}
