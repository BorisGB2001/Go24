package telephony

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRenderPickup: with picking up on, both accounts of an employee may take calls (GO24_PICKOK) and ring and take
// within their groups; an employee of no group has no groups, yet may take a certain colleague's call (**101).
func TestRenderPickup(t *testing.T) {
	groups := pickupGroups(map[string][]string{"1": {"dabc"}}, []Group{{ID: "11112222-3333-4444-5555-666677778888", Users: []string{"1", "2"}}})
	conf := Render([]Line{{UserID: "1", Name: "Анна", Number: "101"}, {UserID: "2", Name: "Борис", Number: "102"}, {UserID: "3", Name: "Ева", Number: "103"}}, false, groups)
	if n := strings.Count(conf, "named_call_group = dabc,go24-q-111122223333\nnamed_pickup_group = dabc,go24-q-111122223333\n"); n != 2 {
		t.Errorf("Анна's groups on %d accounts:\n%s", n, conf)
	}
	if n := strings.Count(conf, "named_call_group = go24-q-111122223333\nnamed_pickup_group = go24-q-111122223333\n"); n != 2 {
		t.Errorf("Борис's groups on %d accounts:\n%s", n, conf)
	}
	if n := strings.Count(conf, "set_var = GO24_PICKOK=1\n"); n != 6 || strings.Count(conf, "named_call_group") != 4 {
		t.Errorf("GO24_PICKOK on %d accounts, or Ева has groups:\n%s", n, conf)
	}
	if g := groups["1"]; len(g) != 2 || g[0] != "dabc" {
		t.Errorf("the departments' groups changed: %v", g)
	}
}

// TestPickupGroups: an employee's pickup groups are their departments, the ones they head and are deputies of;
// picking up is on until turned off.
func TestPickupGroups(t *testing.T) {
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
	var ann, bob, eve, sales, east string
	for _, u := range []struct {
		id   *string
		name string
	}{{&ann, "ann"}, {&bob, "bob"}, {&eve, "eve"}} {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, u.name).Scan(u.id))
	}
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Продажи', $1) RETURNING id`, ann).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Восток') RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, bob, east)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO department_deputies (user_id, department_id) VALUES ($1, $2)`, eve, east)
	must(err)

	s := NewStore(pool, "secret")
	got, err := s.PickupGroups(ctx)
	must(err)
	key := func(id string) string { return "d" + strings.ReplaceAll(id, "-", "") }
	if g := got[ann]; len(g) != 1 || g[0] != key(sales) {
		t.Errorf("the head's groups: %v", g)
	}
	if g := got[bob]; len(g) != 1 || g[0] != key(east) {
		t.Errorf("the member's groups: %v", g)
	}
	if g := got[eve]; len(g) != 1 || g[0] != key(east) {
		t.Errorf("the deputy's groups: %v", g)
	}

	if on, err := s.Picking(ctx); err != nil || !on {
		t.Fatalf("picking up is off at first: %v %v", on, err)
	}
	must(s.SetPicking(ctx, ann, false))
	if on, err := s.Picking(ctx); err != nil || on {
		t.Fatalf("picking up is not off: %v %v", on, err)
	}
}
