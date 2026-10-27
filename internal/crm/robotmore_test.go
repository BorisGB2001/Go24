package crm

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRobotMore: the robots «Изменить наблюдателей» (add, remove, replace) and «Завершить дела» (all, or of a kind)
// when a deal enters their stage; the document robot without the generator fails into the history.
func TestRobotMore(t *testing.T) {
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
	s := NewStore(pool)
	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages
	var ve *i18n.Error
	if err := s.AddRobot(ctx, boss, st[1].ID, "observers", RobotParams{Obs: "add"}); !errors.As(err, &ve) {
		t.Fatalf("adding no one: %v", err)
	}
	if err := s.AddRobot(ctx, boss, st[1].ID, "document", RobotParams{Doc: "00000000-0000-0000-0000-000000000000"}); !errors.As(err, &ve) || ve.Key != "err.robot_doc" {
		t.Fatalf("an unknown document template: %v", err)
	}
	must(s.AddRobot(ctx, boss, st[1].ID, "observers", RobotParams{Obs: "add", Users: []string{a, b, a}}))
	must(s.AddRobot(ctx, boss, st[2].ID, "observers", RobotParams{Obs: "remove", Users: []string{a}}))
	must(s.AddRobot(ctx, boss, st[3].ID, "observers", RobotParams{Obs: "set", Users: []string{c}}))
	must(s.AddRobot(ctx, boss, st[2].ID, "close", RobotParams{Act: "call"}))
	must(s.AddRobot(ctx, boss, st[3].ID, "close", RobotParams{Act: "whatever"}))
	if rb, _ := s.Robots(ctx, []string{st[3].ID}); rb[1].Params.Act != "" || rb[0].Params.Obs != "set" {
		t.Fatalf("robots: %+v", rb)
	}
	deal, err := s.SaveDeal(ctx, boss, Deal{Title: "x", PipelineID: pipes[0].ID, Responsible: Ref{ID: boss}})
	must(err)
	for _, k := range []string{"call", "call", "meeting"} {
		_, err := s.AddActivity(ctx, boss, Activity{Entity: "deal", EntityID: deal, Kind: k, Subject: k, Deadline: time.Now().Add(time.Hour),
			Responsible: Ref{ID: boss}})
		must(err)
	}
	move := func(stage string) []string {
		t.Helper()
		_, err := s.MoveDeal(ctx, boss, deal, stage)
		must(err)
		obs, err := s.Observers(ctx, "deal", deal)
		must(err)
		ids := ObserverIDs(obs)
		slices.Sort(ids)
		return ids
	}
	open := func() int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_activities WHERE entity_id::text = $1 AND done_at IS NULL`, deal).Scan(&n))
		return n
	}
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }
	if got := move(st[1].ID); !slices.Equal(got, sorted(a, b)) {
		t.Fatalf("added: %v", got)
	}
	if got := move(st[2].ID); !slices.Equal(got, []string{b}) || open() != 1 {
		t.Fatalf("removed: %v, open deeds %d", got, open())
	}
	if got := move(st[3].ID); !slices.Equal(got, []string{c}) || open() != 0 {
		t.Fatalf("replaced: %v, open deeds %d", got, open())
	}
	var said []string
	rows, _ := pool.Query(ctx, `SELECT body FROM crm_timeline WHERE entity_id::text = $1 AND kind = 'robot' ORDER BY id`, deal)
	for rows.Next() {
		var x string
		must(rows.Scan(&x))
		said = append(said, x)
	}
	if !slices.Equal(said, []string{"close:2", "close:1"}) {
		t.Fatalf("the history: %v", said)
	}
	// a document robot whose template was there, with the generator off
	_, err = pool.Exec(ctx, `INSERT INTO crm_robots (stage_id, kind, params, created_by) VALUES ($1, 'document', '{"doc": "x"}', $2)`, st[4].ID, boss)
	must(err)
	_, err = s.MoveDeal(ctx, boss, deal, st[4].ID)
	must(err)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id::text = $1 AND body = 'fail:document:err.robot_doc'`, deal).Scan(&n))
	if n != 1 {
		t.Fatal("the document robot's failure")
	}
}
