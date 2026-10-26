package crm

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRobotShare: the responsible robot among several people gives cards in turn passing over the absent, evenly to
// whom has the fewest open cards, at random to one of them; an unknown person is refused.
func TestRobotShare(t *testing.T) {
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
	boss, a, b, c := newUser("boss"), newUser("a"), newUser("b"), newUser("c")
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', current_date - 1, current_date + 1, $1)`, c)
	must(err)
	s := NewStore(pool)
	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages
	var ve *i18n.Error
	if err := s.AddRobot(ctx, boss, st[1].ID, "responsible", RobotParams{Users: []string{a, "00000000-0000-0000-0000-000000000000"}}); !errors.As(err, &ve) {
		t.Fatalf("a responsible robot with an unknown person: %v", err)
	}
	must(s.AddRobot(ctx, boss, st[1].ID, "responsible", RobotParams{Users: []string{a, b, c, a}, Mode: "queue", Skip: true}))
	must(s.AddRobot(ctx, boss, st[2].ID, "responsible", RobotParams{Users: []string{a, b, c}, Mode: "even"}))
	must(s.AddRobot(ctx, boss, st[3].ID, "responsible", RobotParams{Users: []string{a, b}, Mode: "random"}))
	rb, err := s.Robots(ctx, []string{st[1].ID})
	must(err)
	if p := rb[0].Params; len(p.Users) != 3 || p.To != a {
		t.Fatalf("robot people = %+v", p)
	}
	deal := func(stage string) string {
		t.Helper()
		id, err := s.SaveDeal(ctx, boss, Deal{Title: "x", PipelineID: pipes[0].ID, Responsible: Ref{ID: boss}})
		must(err)
		_, err = s.MoveDeal(ctx, boss, id, stage)
		must(err)
		d, err := s.Deal(ctx, id)
		must(err)
		return d.Responsible.ID
	}
	var got []string
	for range 4 {
		got = append(got, deal(st[1].ID))
	}
	if !slices.Equal(got, []string{a, b, a, b}) { // c is absent
		t.Fatalf("in turn: %v", got)
	}
	got = nil
	for range 3 { // a and b have 2 open deals each, c none
		got = append(got, deal(st[2].ID))
	}
	if !slices.Equal(got, []string{c, c, a}) {
		t.Fatalf("evenly: %v", got)
	}
	if r := deal(st[3].ID); r != a && r != b {
		t.Fatalf("at random: %s", r)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE kind = 'robot' AND body = 'responsible:c'`).Scan(&n))
	if n != 2 {
		t.Fatalf("history of handing to c: %d", n)
	}
}
