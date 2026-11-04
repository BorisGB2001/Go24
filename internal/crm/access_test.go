package crm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestPerms: the levels of the actions added later come from the ones a role had; a pipeline's own levels fall back
// to the deals' ones, a bad pipeline key is dropped.
func TestPerms(t *testing.T) {
	p := cleanPerms(Perms{"deal": {"read": "dept", "write": "own"}, "deal:" + "12345678-1234-1234-1234-123456789abc": {"read": "all", "write": "bad"},
		"deal:x": {"read": "all"}})
	if p.Level("deal", "add") != "own" || p.Level("deal", "import") != "own" || p.Level("deal", "export") != "dept" {
		t.Fatalf("the levels of an old role: %v", p["deal"])
	}
	key := PipelineKey("12345678-1234-1234-1234-123456789abc")
	if p.Level(key, "read") != "all" || p.Level(key, "write") != "own" {
		t.Fatalf("a pipeline's levels: %v", p[key])
	}
	if _, ok := p["deal:x"]; ok || PipelineKey("x") != "deal" {
		t.Fatal("a bad pipeline key kept")
	}
	if got := cleanPerms(Perms{"deal": {"read": "all", "export": "none"}}); got.Level("deal", "export") != "none" {
		t.Fatal("a saved export level replaced")
	}
}

// TestRightsPortal: a role reaches the people of a department and the ones below; adding, export and import have their
// own levels; the deals of a pipeline have theirs — in the lists, the checks of a card and on moving to the pipeline.
func TestRightsPortal(t *testing.T) {
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	ann, other := newUser("ann"), newUser("other")
	var sales, east string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Восток') RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, ann, east)
	must(err)
	s := NewStore(pool)
	second, err := s.AddPipeline(ctx, "Опт")
	must(err)
	pipes, err := s.Pipelines(ctx)
	must(err)
	var first Pipeline
	var wholesale Pipeline
	for _, p := range pipes {
		if p.ID == second {
			wholesale = p
		} else {
			first = p
		}
	}
	annDeal, err := s.SaveDeal(ctx, ann, Deal{Title: "Анны", PipelineID: first.ID})
	must(err)
	_, err = s.SaveDeal(ctx, other, Deal{Title: "Чужая", PipelineID: first.ID})
	must(err)
	otherOpt, err := s.SaveDeal(ctx, other, Deal{Title: "Чужая оптовая", PipelineID: wholesale.ID})
	must(err)

	role, err := s.SaveRole(ctx, Role{Name: "Менеджер", Perms: Perms{
		"deal":              {"read": "own", "add": "own", "write": "own", "export": "none", "import": "none"},
		PipelineKey(second): {"read": "all", "add": "none"}}})
	must(err)
	must(s.SetMember(ctx, role, "dept:"+sales, true))
	roles, err := s.Roles(ctx)
	must(err)
	found := false
	for _, r := range roles {
		for _, m := range r.Members {
			found = found || (r.ID == role && m.ID == "dept:"+sales && m.Name == "Продажи")
		}
	}
	if !found {
		t.Fatalf("the department among the role's members: %+v", roles)
	}

	count := func(pipe string) int {
		t.Helper()
		list, err := s.Deals(ctx, ann, pipe, "", "", "")
		must(err)
		return len(list)
	}
	if count(first.ID) != 1 || count(wholesale.ID) != 1 {
		t.Fatalf("visible deals by the role of the department above: %d in the first pipeline, %d in the second", count(first.ID), count(wholesale.ID))
	}
	if err := s.CheckCard(ctx, ann, "deal", "read", otherOpt, other); err != nil {
		t.Fatal("a deal of the pipeline read by all is closed:", err)
	}
	if err := s.CheckCard(ctx, ann, "deal", "write", otherOpt, other); !errors.Is(err, ErrForbidden) {
		t.Fatal("changing someone else's deal of the pipeline:", err)
	}
	if ok, _ := s.Can(ctx, ann, "deal", "add", ann); !ok {
		t.Fatal("adding own deals refused")
	}
	if ok, _ := s.Can(ctx, ann, PipelineKey(second), "add", ann); ok {
		t.Fatal("adding deals into a pipeline without adding")
	}
	if err := s.CheckMove(ctx, ann, annDeal, wholesale.Stages[0].ID); !errors.Is(err, ErrForbidden) {
		t.Fatal("a deal moved into a pipeline without adding:", err)
	}
	if err := s.CheckMove(ctx, ann, annDeal, first.Stages[1].ID); err != nil {
		t.Fatal("a stage of the same pipeline:", err)
	}
	if _, err := s.Exportable(ctx, ann, "deal"); !errors.Is(err, ErrForbidden) {
		t.Fatal("export without the right:", err)
	}
	if ok, _ := s.Can(ctx, ann, "lead", "import", ann); ok {
		t.Fatal("a role without leads imports them")
	}
	may, err := s.Exportable(ctx, other, "deal") // the default role: export as reading — all
	must(err)
	if !may(ann) {
		t.Fatal("the default role exports everything it reads")
	}

	must(s.SetMember(ctx, role, "dept:"+sales, false))
	if count(first.ID) != 2 {
		t.Fatal("without the department's role the default applies again")
	}
	if err := s.SetMember(ctx, role, "dept:bad", true); !errors.Is(err, ErrNotFound) {
		t.Fatal("a bad member:", err)
	}
}
