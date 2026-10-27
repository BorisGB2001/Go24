package bp

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestRobotStart: the CRM robot «Запустить бизнес-процесс» starts a template of the card's kind when a deal enters
// its stage — with a required field of the form left empty, as an autostart; a smart process's template does not fit
// a stage of deals; the history names the process.
func TestRobotStart(t *testing.T) {
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
	var ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'ann', 'ann@t') RETURNING id`).Scan(&ann))
	cs := crm.NewStore(pool)
	s := NewStore(pool, tasks.NewStore(pool), cs, company.NewStore(pool))
	cs.StartBP = s.StartRobot
	pipes, err := cs.Pipelines(ctx)
	must(err)
	st := pipes[0].Stages
	typeID, err := cs.AddType(ctx, "Договоры", "#3b82f6")
	must(err)
	smart, err := s.SaveTemplate(ctx, ann, Template{Name: "Договор", Entity: "deal", TypeID: typeID, Active: true,
		Steps: []Step{{Kind: "notify", To: "starter", Text: "—"}}})
	must(err)
	lead, err := s.SaveTemplate(ctx, ann, Template{Name: "Лид", Entity: "lead", Active: true, Steps: []Step{{Kind: "notify", To: "starter", Text: "—"}}})
	must(err)
	tpl, err := s.SaveTemplate(ctx, ann, Template{Name: "Проверка", Entity: "deal", Active: true,
		Fields: []Field{{Key: "note", Label: "Комментарий", Kind: "string", Required: true}},
		Steps:  []Step{{Kind: "notify", To: "starter", Text: "Проверить {title}"}}})
	must(err)
	var ve *i18n.Error
	for _, other := range []string{smart, lead, ""} {
		if err := cs.AddRobot(ctx, ann, st[1].ID, "bp", crm.RobotParams{BP: other}); !errors.As(err, &ve) || ve.Key != "err.robot_bp" {
			t.Fatalf("a template of another kind %q: %v", other, err)
		}
	}
	must(cs.AddRobot(ctx, ann, st[1].ID, "bp", crm.RobotParams{BP: tpl}))
	deal, err := cs.SaveDeal(ctx, ann, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: ann}})
	must(err)
	_, err = cs.MoveDeal(ctx, ann, deal, st[1].ID)
	must(err)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM bp_instances WHERE template_id::text = $1 AND entity_id::text = $2 AND started_by::text = $3`,
		tpl, deal, ann).Scan(&n))
	var said string
	must(pool.QueryRow(ctx, `SELECT coalesce(string_agg(body, '|'), '') FROM crm_timeline WHERE entity = 'deal' AND entity_id::text = $1 AND kind = 'robot'`,
		deal).Scan(&said))
	if n != 1 || said != "bp:Проверка" {
		t.Fatalf("started %d, the history %q", n, said)
	}
	// switched off since: the robot's failure is in the history
	_, err = pool.Exec(ctx, `UPDATE bp_templates SET active = false WHERE id::text = $1`, tpl)
	must(err)
	_, err = cs.MoveDeal(ctx, ann, deal, st[0].ID)
	must(err)
	_, err = cs.MoveDeal(ctx, ann, deal, st[1].ID)
	must(err)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity = 'deal' AND entity_id::text = $1 AND kind = 'robot' AND body = 'fail:bp:err.bp_inactive'`,
		deal).Scan(&n))
	if n != 1 {
		t.Fatal("an inactive template's failure")
	}
}
