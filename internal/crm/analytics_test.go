package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestAnalytics: the funnel counts the period's deals by stage; won and lost in the period with the win rate and the
// average; the managers; the leads by stage, their conversion and sources.
func TestAnalytics(t *testing.T) {
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
	won, lost := p.Stages[len(p.Stages)-2].ID, p.Stages[len(p.Stages)-1].ID
	deal := func(title string, amount float64, resp, stage string) string {
		id, err := s.SaveDeal(ctx, anna, Deal{Title: title, PipelineID: p.ID, Amount: amount, Responsible: Ref{ID: resp}})
		must(err)
		if stage != "" {
			_, err = s.MoveDeal(ctx, anna, id, stage)
			must(err)
		}
		return id
	}
	deal("a", 1000, anna, won)
	deal("b", 3000, anna, won)
	deal("c", 500, petr, lost)
	deal("d", 200, petr, "")
	old := deal("e", 9999, petr, "")
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET created_at = now() - interval '60 days' WHERE id = $1`, old)
	must(err)
	_, err = s.SaveLead(ctx, anna, Lead{Title: "1", Source: "Сайт"})
	must(err)
	l2, err := s.SaveLead(ctx, anna, Lead{Title: "2", Source: "Сайт"})
	must(err)
	_, err = s.SaveLead(ctx, anna, Lead{Title: "3"})
	must(err)
	_, err = s.ConvertLead(ctx, anna, l2)
	must(err)

	from := time.Now().AddDate(0, 0, -7)
	rep, err := s.Analytics(ctx, "", p.ID, from, time.Now().Add(time.Hour))
	must(err)
	if rep.Won != 2 || rep.WonSum != 4000 || rep.Lost != 1 || rep.WinRate() != 66 || rep.AverageWon() != 2000 {
		t.Fatalf("closed: %+v", rep)
	}
	inFunnel := 0
	for _, st := range rep.Funnel {
		inFunnel += st.Count
	}
	// the converted lead's deal is in the period too; the old deal is not
	if inFunnel != 5 || rep.Funnel[0].Count != 2 {
		t.Fatalf("funnel: %+v", rep.Funnel)
	}
	var pm Manager
	for _, m := range rep.Managers {
		if m.ID == petr {
			pm = m
		}
	}
	if pm.InWork != 2 || pm.InWorkSum != 10199 || pm.Lost != 1 || pm.WinRate() != 0 {
		t.Fatalf("petr: %+v", pm)
	}
	if rep.LeadsNew != 3 || rep.Converted != 1 || rep.Conversion() != 33 || len(rep.Sources) != 2 || rep.Sources[0].Name != "Сайт" || rep.Sources[0].Converted != 1 {
		t.Fatalf("leads: %d %d %+v", rep.LeadsNew, rep.Converted, rep.Sources)
	}
}
