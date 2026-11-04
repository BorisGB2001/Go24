package crm

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRobotBased: «Конвертировать лид» converts a lead entering its stage (once), «Создать на основании» makes a lead
// and a deal of a deal, both telling of each other, «Запросить дополнительную информацию» asks for empty fields, holds
// the stage's next robots and lets them run when the fields are filled.
func TestRobotBased(t *testing.T) {
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
	var boss, a string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('a', 'a', 'a@t') RETURNING id`).Scan(&a))
	s := NewStore(pool)
	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages
	leads, err := s.LeadStages(ctx)
	must(err)
	var ve *i18n.Error
	if err := s.AddRobot(ctx, boss, st[1].ID, "convert", RobotParams{}); !errors.As(err, &ve) || ve.Key != "err.robot_convert" {
		t.Fatalf("convert at a deal stage: %v", err)
	}
	if err := s.AddRobot(ctx, boss, st[1].ID, "create", RobotParams{Stage: st[1].ID}); !errors.As(err, &ve) || ve.Key != "err.robot_create" {
		t.Fatalf("create into its own stage: %v", err)
	}
	if err := s.AddRobot(ctx, boss, st[1].ID, "request", RobotParams{Text: "?", Fields: []string{"name"}}); !errors.As(err, &ve) || ve.Key != "err.robot_request" {
		t.Fatalf("a lead's field asked of a deal: %v", err)
	}
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, q, args...).Scan(&n))
		return n
	}

	// convert
	must(s.AddRobot(ctx, boss, leads[1].ID, "convert", RobotParams{Pipe: pipes[0].ID}))
	lead, err := s.SaveLead(ctx, boss, Lead{Title: "Лид", Name: "Иван", Responsible: Ref{ID: boss}})
	must(err)
	_, err = s.MoveLead(ctx, boss, lead, leads[1].ID)
	must(err)
	l, err := s.Lead(ctx, lead)
	must(err)
	if l.DealID == "" || l.ContactID == "" || l.Stage.Semantic != "success" {
		t.Fatalf("the lead is not converted: %+v", l)
	}

	// create: a lead (to a) and a deal of a deal
	must(s.AddRobot(ctx, boss, st[1].ID, "create", RobotParams{Stage: leads[0].ID, To: a, Text: "Повтор {title}"}))
	must(s.AddRobot(ctx, boss, st[1].ID, "create", RobotParams{Stage: st[0].ID}))
	deal, err := s.SaveDeal(ctx, boss, Deal{Title: "Поставка", PipelineID: pipes[0].ID, Amount: 500, Contact: Ref{ID: l.ContactID}, Responsible: Ref{ID: boss}})
	must(err)
	_, err = s.MoveDeal(ctx, boss, deal, st[1].ID)
	must(err)
	if n := count(`SELECT count(*) FROM crm_leads WHERE title = 'Повтор Поставка' AND responsible_id = $1 AND amount = 500 AND contact_id = $2`, a, l.ContactID); n != 1 {
		t.Fatalf("leads made: %d", n)
	}
	if n := count(`SELECT count(*) FROM crm_deals WHERE title = 'Поставка' AND id::text <> $1 AND stage_id::text = $2`, deal, st[0].ID); n != 1 {
		t.Fatalf("deals made: %d", n)
	}
	if n := count(`SELECT count(*) FROM crm_timeline WHERE entity = 'deal' AND entity_id = $1 AND kind = 'based_to'`, deal); n != 2 {
		t.Fatalf("based_to events: %d", n)
	}
	if n := count(`SELECT count(*) FROM crm_timeline WHERE kind = 'based_from' AND body = 'deal:' || $1`, deal); n != 2 {
		t.Fatalf("based_from events: %d", n)
	}

	// request: holds the comment robot after it until the comment field is filled
	must(s.AddRobot(ctx, boss, st[2].ID, "request", RobotParams{Text: "Уточните", Fields: []string{"comment", "title", "comment"}}))
	must(s.AddRobot(ctx, boss, st[2].ID, "comment", RobotParams{Text: "после запроса"}))
	_, err = s.MoveDeal(ctx, boss, deal, st[2].ID)
	must(err)
	reqs, err := s.InfoRequests(ctx, "deal", deal)
	must(err)
	if len(reqs) != 1 || len(reqs[0].Fields) != 2 || len(reqs[0].Missing) != 1 || reqs[0].Missing[0] != "comment" || reqs[0].UserID != boss {
		t.Fatalf("requests: %+v", reqs)
	}
	if n := count(`SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'crm_robot'`, boss); n != 1 {
		t.Fatalf("notifications: %d", n)
	}
	held := `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND kind = 'robot' AND body = 'после запроса'`
	if n := count(held, deal); n != 0 {
		t.Fatal("the next robot did not wait")
	}
	must(s.CheckInfoRequests(ctx, boss, "deal", deal)) // nothing filled yet
	if n := count(held, deal); n != 0 {
		t.Fatal("ran while unanswered")
	}
	d, err := s.Deal(ctx, deal)
	must(err)
	d.Comment = "бюджет 1000"
	_, err = s.SaveDeal(ctx, boss, d)
	must(err)
	must(s.CheckInfoRequests(ctx, boss, "deal", deal))
	must(s.CheckInfoRequests(ctx, boss, "deal", deal)) // once
	if n := count(held, deal); n != 1 {
		t.Fatalf("the held robot ran %d times", n)
	}
	if reqs, _ := s.InfoRequests(ctx, "deal", deal); len(reqs) != 0 {
		t.Fatalf("still open: %+v", reqs)
	}

	// filled already: nothing is asked and nothing is held
	other, err := s.SaveDeal(ctx, boss, Deal{Title: "Другая", PipelineID: pipes[0].ID, Comment: "есть", Responsible: Ref{ID: boss}})
	must(err)
	_, err = s.MoveDeal(ctx, boss, other, st[2].ID)
	must(err)
	if n := count(held, other); n != 1 {
		t.Fatal("a request with nothing to ask held the robots")
	}
	if n := count(`SELECT count(*) FROM crm_info_requests WHERE entity_id = $1`, other); n != 0 {
		t.Fatalf("asked of a filled card: %d", n)
	}
}
