package telephony

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRenderOutLimits: an employee with outgoing calls forbidden has no way out (go24-no-line), one with «CRM's
// clients only» goes out through go24-out-crm, the rest — straight through the line.
func TestRenderOutLimits(t *testing.T) {
	tr := Trunk{ID: "11111111-2222-3333-4444-555555555555", Name: "Линия", Number: "+74950000000", Host: "sip.example.com", Username: "u", OutAll: true}
	_, plan := RenderTrunks([]Trunk{tr}, map[string]string{"u1": "101", "u2": "102", "u3": "103"}, nil,
		map[string]string{"u1": "none", "u2": "crm"}, false, "")
	for _, want := range []string{
		"exten => _X./102,1,Gosub(go24-out-crm,s,1(" + tr.Key() + ",+74950000000,${EXTEN}))\n",
		"exten => _X./103,1,Gosub(go24-dial-out,s,1(" + tr.Key() + ",+74950000000,${EXTEN}))\n",
	} {
		if !strings.Contains(plan, want) {
			t.Errorf("no %q in\n%s", want, plan)
		}
	}
	if strings.Contains(plan, "/101,") {
		t.Errorf("101 may not call out:\n%s", plan)
	}
}

// TestRoles: the default role is everyone's without roles; a role is given to an employee or a department (and the
// ones below), a person has the highest level of their roles; the default role is not removed and has no members;
// Team is the people of one's departments and the ones below with their heads.
func TestRoles(t *testing.T) {
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
	var ann, bob, eve, top, sales, east string
	for _, u := range []struct {
		id   *string
		name string
	}{{&ann, "ann"}, {&bob, "bob"}, {&eve, "eve"}} {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, u.name).Scan(u.id))
	}
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&top))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name, head_id) VALUES ($1, 'Продажи', $2) RETURNING id`, top, ann).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Восток') RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4)`, ann, sales, bob, east)
	must(err)

	s := NewStore(pool, "secret")
	roles, err := s.Roles(ctx)
	must(err)
	if len(roles) != 2 || !roles[0].Default || roles[0].Code != "everyone" || roles[1].Code != "heads" {
		t.Fatalf("the built-in roles: %+v", roles)
	}
	p, err := s.PermsOf(ctx, bob)
	must(err)
	if p["calls"] != "own" || p["records"] != "own" || p["outgoing"] != "all" || p["settings"] != "none" {
		t.Fatalf("the default rights: %v", p)
	}
	var ie *i18n.Error
	if err := s.DeleteRole(ctx, roles[0].ID); !errors.As(err, &ie) {
		t.Fatalf("the default role deleted: %v", err)
	}
	if _, err := s.SaveRole(ctx, Role{Name: " "}, nil); !errors.As(err, &ie) {
		t.Fatalf("a role without a name: %v", err)
	}
	// the default role keeps no members
	_, err = s.SaveRole(ctx, Role{ID: roles[0].ID, Perms: roles[0].Perms}, []string{bob})
	must(err)
	// a department's role reaches the people of the departments below; the highest level wins
	ops, err := s.SaveRole(ctx, Role{Name: " Операторы ", Perms: Perms{"calls": "none", "records": "none", "outgoing": "crm", "settings": "all", "x": "y"}},
		[]string{"dept:" + sales, ""})
	must(err)
	_, err = s.SaveRole(ctx, Role{ID: roles[1].ID, Perms: roles[1].Perms}, []string{ann})
	must(err)
	if p, _ = s.PermsOf(ctx, bob); p["calls"] != "none" || p["outgoing"] != "crm" || p["settings"] != "all" {
		t.Fatalf("bob's rights by the department above: %v", p)
	}
	if p, _ = s.PermsOf(ctx, ann); p["calls"] != "dept" || p["records"] != "dept" || p["outgoing"] != "all" || p["settings"] != "all" {
		t.Fatalf("ann's rights of two roles: %v", p)
	}
	if p, _ = s.PermsOf(ctx, eve); p["calls"] != "own" || p["outgoing"] != "all" {
		t.Fatalf("eve has the default rights: %v", p)
	}
	if roles, _ = s.Roles(ctx); len(roles) != 3 || len(roles[0].Members) != 0 || roles[2].Name != "Операторы" || len(roles[2].Members) != 1 ||
		roles[2].Members[0].ID != "dept:"+sales || roles[2].Members[0].Name != "Продажи" || roles[2].Perms["x"] != "" {
		t.Fatalf("the roles: %+v", roles)
	}
	limits, err := s.OutLimits(ctx, []string{ann, bob, eve})
	must(err)
	if len(limits) != 1 || limits[bob] != "crm" {
		t.Fatalf("the limits: %v", limits)
	}
	team, err := s.Team(ctx, ann)
	must(err)
	slices.Sort(team)
	want := []string{ann, bob}
	slices.Sort(want)
	if !slices.Equal(team, want) {
		t.Fatalf("ann's team: %v", team)
	}
	if team, _ = s.Team(ctx, eve); !slices.Equal(team, []string{eve}) {
		t.Fatalf("eve's team: %v", team)
	}
	must(s.DeleteRole(ctx, ops))
	if p, _ = s.PermsOf(ctx, bob); p["calls"] != "own" || p["outgoing"] != "all" {
		t.Fatalf("bob's rights after the role is gone: %v", p)
	}
}
