package crm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestCRMScoring: a pipeline learns only with enough closed deals; an open deal like the won ones scores high, like the
// lost ones low, with the source among the reasons; a closed deal or a pipeline turned off has no score; the worker
// learns again a day later.
func TestCRMScoring(t *testing.T) {
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
	var anna, site, cold string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO crm_dict (kind, name) VALUES ('source', 'Сайт') RETURNING id`).Scan(&site))
	must(pool.QueryRow(ctx, `INSERT INTO crm_dict (kind, name) VALUES ('source', 'Холодный звонок') RETURNING id`).Scan(&cold))
	s := NewStore(pool)
	pipes, err := s.Pipelines(ctx)
	must(err)
	p := pipes[0]
	stage := map[string]string{}
	for _, st := range p.Stages {
		if stage[st.Semantic] == "" {
			stage[st.Semantic] = st.ID
		}
	}
	deal := func(title, source, semantic string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO crm_deals (title, pipeline_id, stage_id, amount, responsible_id, created_by, source_id, closed_at)
			VALUES ($1, $2, $3, 1000, $4, $4, $5, CASE WHEN $6 <> 'process' THEN now() END) RETURNING id`,
			title, p.ID, stage[semantic], anna, source, semantic).Scan(&id))
		return id
	}

	// too few closed deals: no model
	for i := 0; i < 5; i++ {
		deal("won", site, "success")
	}
	must(s.SetScoring(ctx, anna, p.ID, true))
	list, err := s.ScoringList(ctx)
	must(err)
	if len(list) == 0 || !list[0].Enabled || list[0].Ready() || list[0].Samples != 5 || list[0].TrainedAt == nil {
		t.Fatalf("not ready: %+v", list)
	}
	open := deal("open", site, "process")
	if sc, err := s.ScoreOf(ctx, open); err != nil || sc != nil {
		t.Fatalf("score without a model: %+v %v", sc, err)
	}

	// enough: from the site they buy, from cold calls they do not
	for i := 0; i < 15; i++ {
		deal("won", site, "success")
		deal("lost", cold, "failure")
	}
	deal("lost", site, "failure")
	deal("won", cold, "success")
	must(s.TrainScoring(ctx, p.ID))
	list, err = s.ScoringList(ctx)
	must(err)
	if c := list[0]; !c.Ready() || c.Samples != 37 || c.Won != 21 || c.Accuracy < 0.9 {
		t.Fatalf("trained: %+v", c)
	}
	coldOpen := deal("open cold", cold, "process")
	won := deal("closed", site, "success")
	scores, err := s.Scores(ctx, []string{open, coldOpen, won})
	must(err)
	if scores[open] == nil || scores[coldOpen] == nil || scores[won] != nil {
		t.Fatalf("scores: %+v", scores)
	}
	if scores[open].Percent < 80 || scores[coldOpen].Percent > 20 {
		t.Fatalf("site %d%%, cold %d%%", scores[open].Percent, scores[coldOpen].Percent)
	}
	f := scores[open].Factors
	if len(f) == 0 || f[0].Feature != "origin" || !f[0].Up || f[0].Dict.Name != "Сайт" {
		t.Fatalf("factors of the site deal: %+v", f)
	}
	if f := scores[coldOpen].Factors; len(f) == 0 || f[len(f)-1].Feature != "origin" || f[len(f)-1].Up {
		t.Fatalf("factors of the cold deal: %+v", f)
	}

	// the worker learns again after a day
	if n, err := s.TrainScoringDue(ctx, time.Now()); err != nil || n != 0 {
		t.Fatalf("due now: %d %v", n, err)
	}
	if n, err := s.TrainScoringDue(ctx, time.Now().Add(25*time.Hour)); err != nil || n != 1 {
		t.Fatalf("due tomorrow: %d %v", n, err)
	}

	// turned off: no scores
	must(s.SetScoring(ctx, anna, p.ID, false))
	if sc, err := s.ScoreOf(ctx, open); err != nil || sc != nil {
		t.Fatalf("score turned off: %+v %v", sc, err)
	}
	if err := s.SetScoring(ctx, anna, "00000000-0000-0000-0000-000000000000", true); err != ErrNotFound {
		t.Fatalf("unknown pipeline: %v", err)
	}
}
