package crm

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// TestRobotsMore: a deed robot plans a deed, a mail robot writes to the deal's contact from the responsible, a
// responsible robot hands the card over, a stage robot moves it on (a ring of them stops); a robot for an amount
// skips other deals, and a waiting robot runs later only if the card is still in its stage.
func TestRobotsMore(t *testing.T) {
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
	boss, seller, senior := newUser("boss"), newUser("seller"), newUser("senior")
	s := NewStore(pool)
	type letter struct{ from, to, subject, text string }
	var sent []letter
	s.SendMail = func(_ context.Context, userID, to, subject, text string) error {
		sent = append(sent, letter{userID, to, subject, text})
		return nil
	}
	pipes, _ := s.Pipelines(ctx)
	st := pipes[0].Stages // new, preparation, invoice, ...
	var ve *i18n.Error
	if err := s.AddRobot(ctx, boss, st[1].ID, "stage", RobotParams{Stage: st[1].ID}); !errors.As(err, &ve) {
		t.Fatalf("a stage robot to its own stage: %v", err)
	}
	if err := s.AddRobot(ctx, boss, st[1].ID, "responsible", RobotParams{}); !errors.As(err, &ve) {
		t.Fatalf("a responsible robot without a person: %v", err)
	}
	if err := s.AddRobot(ctx, boss, st[1].ID, "notify", RobotParams{Text: "x", Delay: 5}); !errors.As(err, &ve) {
		t.Fatalf("a delay without a unit: %v", err)
	}
	must(s.AddRobot(ctx, boss, st[1].ID, "activity", RobotParams{Text: "Позвонить {client}", Act: "call", Days: 1}))
	must(s.AddRobot(ctx, boss, st[1].ID, "mail", RobotParams{Text: "Предложение по «{title}»", Details: "Здравствуйте, {client}!"}))
	must(s.AddRobot(ctx, boss, st[1].ID, "responsible", RobotParams{To: senior, Min: 100000}))
	must(s.AddRobot(ctx, boss, st[1].ID, "notify", RobotParams{Text: "Ждали", Delay: 2, Unit: "hour"}))
	// a ring: invoice → preparation → invoice … stops after a few moves
	must(s.AddRobot(ctx, boss, st[2].ID, "stage", RobotParams{Stage: st[1].ID}))
	must(s.AddRobot(ctx, boss, st[1].ID, "stage", RobotParams{Stage: st[2].ID, Min: 1000000}))

	contact, err := s.SaveContact(ctx, boss, Contact{Name: "Иван Петров", Email: "ivan@client.test"})
	must(err)
	small, err := s.SaveDeal(ctx, boss, Deal{Title: "Малая", PipelineID: pipes[0].ID, Amount: 5000, Contact: Ref{ID: contact}, Responsible: Ref{ID: seller}})
	must(err)
	big, err := s.SaveDeal(ctx, boss, Deal{Title: "Крупная", PipelineID: pipes[0].ID, Amount: 200000, Responsible: Ref{ID: seller}})
	must(err)
	_, err = s.MoveDeal(ctx, boss, small, st[1].ID)
	must(err)
	_, err = s.MoveDeal(ctx, boss, big, st[1].ID)
	must(err)

	acts, err := s.Activities(ctx, "deal", small)
	must(err)
	if len(acts) != 1 || acts[0].Kind != "call" || acts[0].Subject != "Позвонить Иван Петров" || acts[0].Responsible.ID != seller {
		t.Fatalf("robot deed = %+v", acts)
	}
	if len(sent) != 1 || sent[0] != (letter{seller, "ivan@client.test", "Предложение по «Малая»", "Здравствуйте, Иван Петров!"}) {
		t.Fatalf("robot letters = %+v", sent)
	}
	var failed int // the big deal has no client e-mail: the robot says so in the history
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND kind = 'robot' AND body = 'fail:mail:err.robot_email'`, big).Scan(&failed))
	if failed != 1 {
		t.Fatalf("a failed mail robot is not in the history")
	}
	d, _ := s.Deal(ctx, small)
	if d.Responsible.ID != seller {
		t.Fatalf("a small deal was handed over")
	}
	d, _ = s.Deal(ctx, big)
	if d.Responsible.ID != senior {
		t.Fatalf("the big deal's responsible = %s", d.Responsible.Name)
	}

	// waiting: nothing yet; two hours later the small deal (still there) gets it, the moved one does not
	_, err = s.MoveDeal(ctx, boss, big, st[3].ID)
	must(err)
	n, err := s.RunRobotsDue(ctx, time.Now())
	must(err)
	if n != 0 {
		t.Fatalf("waiting robots ran at once: %d", n)
	}
	n, err = s.RunRobotsDue(ctx, time.Now().Add(2*time.Hour+time.Minute))
	must(err)
	var waited int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'crm_robot' AND body = 'Ждали'`).Scan(&waited))
	if n != 1 || waited != 1 {
		t.Fatalf("waited robots ran %d, notified %d", n, waited)
	}

	// the ring stops: a million-deal bounces between two stages a few times and stays
	huge, err := s.SaveDeal(ctx, boss, Deal{Title: "Огромная", PipelineID: pipes[0].ID, Amount: 2000000, Responsible: Ref{ID: seller}})
	must(err)
	_, err = s.MoveDeal(ctx, boss, huge, st[2].ID)
	must(err)
	var moves int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity_id = $1 AND kind = 'stage'`, huge).Scan(&moves))
	if moves < 2 || moves > maxRobotDepth+2 {
		t.Fatalf("a ring of stage robots moved the deal %d times", moves)
	}
}
