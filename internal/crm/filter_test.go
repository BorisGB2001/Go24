package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestFilter: the lists filter by kind of stage, stage, responsible, created dates and amount, search the client too,
// sort; the responsible is changed.
func TestFilter(t *testing.T) {
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
	anna, petr := newUser("anna"), newUser("petr")
	s := NewStore(pool)
	pipes, err := s.Pipelines(ctx)
	must(err)
	p := pipes[0]
	co, err := s.SaveCompany(ctx, anna, Company{Title: "Ромашка"})
	must(err)
	small, err := s.SaveDeal(ctx, anna, Deal{Title: "Малая", PipelineID: p.ID, Amount: 100})
	must(err)
	big, err := s.SaveDeal(ctx, anna, Deal{Title: "Крупная", PipelineID: p.ID, Amount: 5000, Company: Ref{ID: co}, Responsible: Ref{ID: petr}})
	must(err)
	won, err := s.SaveDeal(ctx, anna, Deal{Title: "Выигранная", PipelineID: p.ID, Amount: 700})
	must(err)
	_, err = s.MoveDeal(ctx, anna, won, p.Stages[len(p.Stages)-2].ID)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET created_at = now() - interval '10 days' WHERE id = $1`, small)
	must(err)

	ids := func(f Filter) (out []string) {
		t.Helper()
		list, err := s.DealList(ctx, "", p.ID, f)
		must(err)
		for _, d := range list {
			out = append(out, d.ID)
		}
		return out
	}
	eq := func(name string, got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %v, want %v", name, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%s: %v, want %v", name, got, want)
			}
		}
	}
	min, max := 500.0, 1000.0
	week := time.Now().AddDate(0, 0, -7)
	eq("in work", ids(Filter{Semantic: "process", Sort: "amount", Asc: true}), small, big)
	eq("won", ids(Filter{Semantic: "success"}), won)
	eq("stage", ids(Filter{Stage: p.Stages[0].ID, Sort: "title", Asc: true}), big, small)
	eq("petr's", ids(Filter{Responsible: petr}), big)
	eq("amount", ids(Filter{AmountMin: &min, AmountMax: &max}), won)
	eq("created", ids(Filter{To: &week}), small)
	eq("by client", ids(Filter{Q: "ромаш"}), big)
	eq("by amount, desc", ids(Filter{Sort: "amount"}), big, won, small)

	must(s.SetResponsible(ctx, anna, "deal", small, petr))
	eq("petr's now", ids(Filter{Responsible: petr, Sort: "amount", Asc: true}), small, big)

	lead, err := s.SaveLead(ctx, anna, Lead{Title: "Заявка", Phone: "+7 900"})
	must(err)
	leads, err := s.LeadList(ctx, "", Filter{Q: "900", Semantic: "process"})
	must(err)
	if len(leads) != 1 || leads[0].ID != lead {
		t.Fatalf("leads: %+v", leads)
	}
}
