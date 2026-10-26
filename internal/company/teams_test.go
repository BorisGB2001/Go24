package company

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestTeams: «Команды» of the structure — people and roles, the head's rights, the employee list by team, the
// profile, a department's teams going up when it is deleted.
func TestTeams(t *testing.T) {
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
	wantKey := func(err error, key string) {
		t.Helper()
		var ie *i18n.Error
		if !errors.As(err, &ie) || ie.Key != key {
			t.Fatalf("err = %v, want %s", err, key)
		}
	}
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@test') RETURNING id`, name).Scan(&id))
		return id
	}
	lead, dep, ann, bob, out := newUser("lead"), newUser("dep"), newUser("ann"), newUser("bob"), newUser("out")
	s := NewStore(pool)
	must(s.EnsureRoot(ctx, "Компания"))
	root, _, err := s.Tree(ctx)
	must(err)
	sales, err := s.CreateDepartment(ctx, root.ID, "Продажи", "")
	must(err)
	b2b, err := s.CreateDepartment(ctx, sales, "Корпоративные", "")
	must(err)

	_, err = s.CreateTeam(ctx, TeamInput{DepartmentID: b2b, Name: "  "})
	wantKey(err, "err.team_name")
	_, err = s.CreateTeam(ctx, TeamInput{Name: "Без отдела"})
	wantKey(err, "err.bad_form")
	if _, err = s.CreateTeam(ctx, TeamInput{DepartmentID: "00000000-0000-0000-0000-000000000000", Name: "X"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown department: %v", err)
	}
	// the head given also as a deputy and a member stays the head; a deputy listed among members — a deputy
	id, err := s.CreateTeam(ctx, TeamInput{DepartmentID: b2b, Name: " Проект Альфа ", Description: "Запуск", HeadID: lead,
		Deputies: []string{dep, lead}, Members: []string{ann, dep, bob, "", "bad"}})
	must(err)
	tm, err := s.Team(ctx, id)
	must(err)
	if tm.Name != "Проект Альфа" || tm.Department != "Корпоративные" || tm.Head == nil || tm.Head.ID != lead ||
		len(tm.Deputies) != 1 || tm.Deputies[0].ID != dep || len(tm.Members) != 2 || tm.Total() != 4 {
		t.Fatalf("team: %+v", tm)
	}
	_, flat, err := s.Tree(ctx)
	must(err)
	for _, d := range flat {
		if (d.ID == b2b) != (len(d.Teams) == 1) {
			t.Fatalf("%s has teams %v", d.Name, d.Teams)
		}
	}

	// rights: administrators and the team's head; the head keeps the department and the head
	for _, c := range []struct {
		who   string
		admin bool
		ok    bool
	}{{lead, false, true}, {dep, false, false}, {out, false, false}, {out, true, true}} {
		ok, err := s.CanEditTeam(ctx, c.who, c.admin, id)
		must(err)
		if ok != c.ok {
			t.Fatalf("can edit (%s, admin %v) = %v", c.who, c.admin, ok)
		}
	}
	must(s.UpdateTeam(ctx, id, TeamInput{DepartmentID: sales, Name: "Альфа", HeadID: out, Members: []string{ann, out}}, true))
	tm, err = s.Team(ctx, id)
	must(err)
	if tm.Name != "Альфа" || tm.DepartmentID != b2b || tm.Head.ID != lead || len(tm.Deputies) != 0 || len(tm.Members) != 2 {
		t.Fatalf("saved by the head: %+v", tm)
	}
	must(s.UpdateTeam(ctx, id, TeamInput{DepartmentID: b2b, Name: "Альфа", HeadID: "", Members: []string{ann, bob}}, false))
	tm, err = s.Team(ctx, id)
	must(err)
	if tm.Head != nil || tm.Total() != 2 {
		t.Fatalf("no head: %+v", tm)
	}
	must(s.UpdateTeam(ctx, id, TeamInput{DepartmentID: b2b, Name: "Альфа", HeadID: lead, Members: []string{ann, bob}}, false))

	// a person in two teams; the list by team and the profile
	beta, err := s.CreateTeam(ctx, TeamInput{DepartmentID: sales, Name: "Бета", Members: []string{ann}})
	must(err)
	list, err := s.Employees(ctx, Filter{TeamID: id})
	must(err)
	if len(list) != 3 {
		t.Fatalf("the team's people: %d", len(list))
	}
	p, err := s.Profile(ctx, ann)
	must(err)
	if len(p.Teams) != 2 || p.Teams[0].Name != "Альфа" || p.Teams[1].ID != beta {
		t.Fatalf("ann's teams: %+v", p.Teams)
	}

	// a deleted department gives its teams to the one above
	must(s.DeleteDepartment(ctx, b2b))
	tm, err = s.Team(ctx, id)
	must(err)
	if tm.DepartmentID != sales || tm.Total() != 3 {
		t.Fatalf("after the department is gone: %+v", tm)
	}
	must(s.DeleteTeam(ctx, beta))
	if err := s.DeleteTeam(ctx, beta); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
	if _, err := s.Team(ctx, "nonsense"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a bad id: %v", err)
	}
	p, err = s.Profile(ctx, ann)
	must(err)
	if len(p.Teams) != 1 {
		t.Fatalf("after the team is deleted: %+v", p.Teams)
	}
}
