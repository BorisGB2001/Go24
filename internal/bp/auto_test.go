package bp

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestAutoStart: a template of deals starts by itself when a deal is added, another when it is changed (an edit or a
// stage move), once while it runs; a smart process's template starts on its cards only; the history before starts nothing.
func TestAutoStart(t *testing.T) {
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
	autoLag = 0
	var ann, boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'ann', 'ann@t') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	cs := crm.NewStore(pool)
	s := NewStore(pool, tasks.NewStore(pool), cs, company.NewStore(pool))
	pipes, err := cs.Pipelines(ctx)
	must(err)
	old, err := cs.SaveDeal(ctx, ann, crm.Deal{Title: "Старая", PipelineID: pipes[0].ID})
	must(err)
	// the migration sets the cursor after the history: here, after the old deal
	_, err = pool.Exec(ctx, `UPDATE bp_cursor SET last_id = (SELECT max(id) FROM crm_timeline)`)
	must(err)

	// a smart process with its own pipeline
	typeID, err := cs.AddType(ctx, "Договоры", "#3b82f6")
	must(err)
	deals := pipes[0]
	pipes, err = cs.PipelinesOf(ctx, typeID)
	must(err)
	if len(pipes) == 0 {
		t.Fatal("no pipeline of the smart process")
	}
	smart := pipes[0]

	onAdd, err := s.SaveTemplate(ctx, boss, Template{Name: "Новая сделка", Entity: "deal", Active: true, AutoCreate: true,
		Steps: []Step{{Kind: "approve", To: boss, Text: "Проверить сделку {title}"}}})
	must(err)
	onChange, err := s.SaveTemplate(ctx, boss, Template{Name: "Изменение", Entity: "deal", Active: true, AutoChange: true,
		Steps: []Step{{Kind: "notify", To: "starter", Text: "Сделка {title} изменена"}}})
	must(err)
	onSmart, err := s.SaveTemplate(ctx, boss, Template{Name: "Договор", Entity: "deal", TypeID: typeID, Active: true, AutoCreate: true,
		Steps: []Step{{Kind: "notify", To: "starter", Text: "Новый договор"}}})
	must(err)
	if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Сам по себе", Active: true, AutoCreate: true,
		Steps: []Step{{Kind: "notify", To: "starter", Text: "—"}}}); err != nil {
		t.Fatal(err)
	}

	count := func(template, card string) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM bp_instances WHERE template_id::text = $1 AND entity_id::text = $2`, template, card).Scan(&n))
		return n
	}
	auto := func() {
		t.Helper()
		for _, err := range s.AutoStart(ctx) {
			t.Fatal(err)
		}
	}

	deal, err := cs.SaveDeal(ctx, ann, crm.Deal{Title: "Поставка", PipelineID: deals.ID})
	must(err)
	item, err := cs.SaveDeal(ctx, ann, crm.Deal{Title: "Договор №1", PipelineID: smart.ID})
	must(err)
	auto()
	if count(onAdd, deal) != 1 || count(onChange, deal) != 0 || count(onSmart, deal) != 0 {
		t.Fatalf("a new deal: on add %d, on change %d, smart %d", count(onAdd, deal), count(onChange, deal), count(onSmart, deal))
	}
	if count(onSmart, item) != 1 || count(onAdd, item) != 0 {
		t.Fatalf("a smart process's card: smart %d, deals' %d", count(onSmart, item), count(onAdd, item))
	}
	if count(onAdd, old) != 0 {
		t.Fatal("the history before autostart started a process")
	}
	var starter, line string
	must(pool.QueryRow(ctx, `SELECT i.started_by::text, l.kind FROM bp_instances i JOIN bp_log l ON l.instance_id = i.id
		WHERE i.template_id::text = $1 ORDER BY l.id LIMIT 1`, onAdd).Scan(&starter, &line))
	if starter != ann || line != "autostarted" {
		t.Fatalf("started by %s, history %q", starter, line)
	}
	if mine, _ := s.MyApprovals(ctx, boss); len(mine) != 1 || mine[0].Text != "Проверить сделку Поставка" {
		t.Fatalf("boss's approvals = %+v", mine)
	}

	// a stage move is a change; the process «on add» still waits on the boss, a card's second add-event cannot come
	_, err = cs.MoveDeal(ctx, ann, deal, deals.Stages[1].ID)
	must(err)
	auto()
	if count(onChange, deal) != 1 || count(onAdd, deal) != 1 {
		t.Fatalf("after a move: on change %d, on add %d", count(onChange, deal), count(onAdd, deal))
	}
	auto() // nothing new — nothing starts
	if count(onChange, deal) != 1 {
		t.Fatal("the same events started a process twice")
	}

	// the card templates of a smart process's card are its own
	list, err := s.CardTemplates(ctx, "deal", item)
	must(err)
	if len(list) != 1 || list[0].ID != onSmart {
		t.Fatalf("templates of a smart card: %+v", list)
	}
	list, err = s.CardTemplates(ctx, "deal", deal)
	must(err)
	if len(list) != 2 {
		t.Fatalf("templates of a deal: %d", len(list))
	}
}
