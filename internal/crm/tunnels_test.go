package crm

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestTunnels: a copy robot makes the deal's copy (client, products, responsible) in a stage of another pipeline and
// links both histories, the copy's stage robots run on it; a move robot moves the deal itself (the history says so);
// a tunnel leads only into another pipeline of the same kind of cards, and a ring of tunnels stops.
func TestTunnels(t *testing.T) {
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
	var boss, seller string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('seller', 'seller', 'seller@t') RETURNING id`).Scan(&seller))
	s := NewStore(pool)
	_, err = s.AddPipeline(ctx, "Тендеры")
	must(err)
	_, err = s.AddPipeline(ctx, "Сервис")
	must(err)
	pipes, err := s.Pipelines(ctx)
	must(err)
	sales, tenders, service := pipes[0], pipes[1], pipes[2]
	typ, err := s.AddType(ctx, "Проекты", "")
	must(err)
	smart, err := s.PipelinesOf(ctx, typ)
	must(err)

	var ve *i18n.Error
	if err := s.AddRobot(ctx, boss, sales.Stages[1].ID, "copy", RobotParams{Stage: sales.Stages[2].ID}); !errors.As(err, &ve) || ve.Key != "err.robot_tunnel" {
		t.Fatalf("a tunnel into its own pipeline: %v", err)
	}
	if err := s.AddRobot(ctx, boss, sales.Stages[1].ID, "copy", RobotParams{Stage: smart[0].Stages[0].ID}); !errors.As(err, &ve) {
		t.Fatalf("a tunnel into a smart process: %v", err)
	}
	leads, err := s.LeadStages(ctx)
	must(err)
	if err := s.AddRobot(ctx, boss, leads[0].ID, "move", RobotParams{Stage: tenders.Stages[0].ID}); !errors.As(err, &ve) {
		t.Fatalf("a tunnel from leads: %v", err)
	}
	// sales «preparation» → a copy in tenders' second stage, which says hello in a comment; tenders' third → service
	must(s.AddRobot(ctx, boss, sales.Stages[1].ID, "copy", RobotParams{Stage: tenders.Stages[1].ID}))
	must(s.AddRobot(ctx, boss, tenders.Stages[1].ID, "comment", RobotParams{Text: "Тендер по {title}"}))
	must(s.AddRobot(ctx, boss, tenders.Stages[2].ID, "move", RobotParams{Stage: service.Stages[0].ID}))

	contact, err := s.SaveContact(ctx, boss, Contact{Name: "Иван Петров"})
	must(err)
	deal, err := s.SaveDeal(ctx, boss, Deal{Title: "Поставка", PipelineID: sales.ID, Amount: 1000, Contact: Ref{ID: contact}, Responsible: Ref{ID: seller}})
	must(err)
	_, err = s.SetProductRows(ctx, boss, "deal", deal, []ProductRow{{Name: "Лицензия", Price: 1000, Quantity: 1}})
	must(err)
	_, err = s.MoveDeal(ctx, boss, deal, sales.Stages[1].ID)
	must(err)
	var copyID string
	must(pool.QueryRow(ctx, `SELECT id FROM crm_deals WHERE id::text <> $1`, deal).Scan(&copyID))
	cp, err := s.Deal(ctx, copyID)
	must(err)
	if cp.PipelineID != tenders.ID || cp.Stage.ID != tenders.Stages[1].ID || cp.Title != "Поставка" || cp.Contact.ID != contact || cp.Responsible.ID != seller {
		t.Fatalf("the copy: %+v", cp)
	}
	if rows, err := s.ProductRows(ctx, "deal", copyID); err != nil || len(rows) != 1 {
		t.Fatalf("the copy's products: %v %v", rows, err)
	}
	if orig, _ := s.Deal(ctx, deal); orig.PipelineID != sales.ID || orig.Stage.ID != sales.Stages[1].ID {
		t.Fatalf("the deal stays: %+v", orig)
	}
	has := func(id, kind, body string) bool {
		tl, err := s.Timeline(ctx, "deal", id)
		must(err)
		for _, e := range tl {
			if e.Kind == kind && strings.Contains(e.Body, body) {
				return true
			}
		}
		return false
	}
	if !has(deal, "tunnel_to", copyID+"|Поставка") || !has(copyID, "tunnel_from", deal+"|Поставка") || !has(copyID, "robot", "Тендер по Поставка") {
		t.Fatal("the histories of a tunnel")
	}

	// the copy goes on in tenders and is moved into service as a whole
	_, err = s.MoveDeal(ctx, boss, copyID, tenders.Stages[2].ID)
	must(err)
	if cp, _ = s.Deal(ctx, copyID); cp.PipelineID != service.ID || cp.Stage.ID != service.Stages[0].ID {
		t.Fatalf("moved to service: %+v", cp)
	}
	if !has(copyID, "pipeline", "Тендеры → Сервис") {
		t.Fatal("the history of a move")
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_deals`).Scan(&n))
	if n != 2 {
		t.Fatalf("deals: %d", n)
	}
	// a stage of a smart process is no stage for a deal
	if _, err := s.MoveDeal(ctx, boss, deal, smart[0].Stages[0].ID); !errors.As(err, &ve) {
		t.Fatalf("a deal into a smart process: %v", err)
	}

	// a ring of tunnels (service ↔ sales) stops
	must(s.AddRobot(ctx, boss, service.Stages[1].ID, "copy", RobotParams{Stage: sales.Stages[3].ID}))
	must(s.AddRobot(ctx, boss, sales.Stages[3].ID, "copy", RobotParams{Stage: service.Stages[1].ID}))
	_, err = s.MoveDeal(ctx, boss, copyID, service.Stages[1].ID)
	must(err)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_deals`).Scan(&n))
	if n < 3 || n > 2+maxRobotDepth+1 {
		t.Fatalf("a ring of tunnels made %d deals", n)
	}
}
