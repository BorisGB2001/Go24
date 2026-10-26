package bp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestPick: «Выбор сотрудника» in turn goes round the people, skipping the absent, and the one picked does the next
// step; when all are absent — the reserve; at random — one of the people.
func TestPick(t *testing.T) {
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
	ann, ivan, olga, petr, boss := newUser("ann"), newUser("ivan"), newUser("olga"), newUser("petr"), newUser("boss")
	cs := company.NewStore(pool)
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), cs)

	var ve *i18n.Error
	who := Field{Key: "who", Label: "Исполнитель", Kind: "user", Scope: "var"}
	for name, st := range map[string]Step{
		"no people":        {Kind: "pick", Field: "who"},
		"a string field":   {Kind: "pick", Field: "title", Users: []string{ivan}},
		"a bad mode":       {Kind: "pick", Field: "who", Users: []string{ivan}, Mode: "all"},
		"skip of a notify": {Kind: "notify", To: "starter", Text: "x", Skip: true},
	} {
		if _, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Fields: []Field{who, {Key: "title", Label: "Тема", Kind: "string"}},
			Steps: []Step{st}}); !errors.As(err, &ve) {
			t.Fatalf("%s: %v", name, err)
		}
	}

	tpl, err := s.SaveTemplate(ctx, ann, Template{Name: "Заявка", Active: true, Fields: []Field{who},
		Steps: []Step{{Kind: "pick", Field: "who", Users: []string{ivan, olga, petr, "manager"}, Mode: "queue", Skip: true, To: boss},
			{Kind: "approve", To: FieldPrefix + "who", Text: "Заявка"}}})
	must(err)
	run := func() string {
		t.Helper()
		id, err := s.Start(ctx, ann, tpl, "", nil)
		must(err)
		x, err := s.Instance(ctx, ann, id)
		must(err)
		return x.Values["who"]
	}
	// ann has no manager: three people in turn, then round again
	for i, want := range []string{ivan, olga, petr, ivan} {
		if got := run(); got != want {
			t.Fatalf("run %d: picked %s, want %s", i+1, got, want)
		}
	}
	if mine, _ := s.MyApprovals(ctx, olga); len(mine) != 1 {
		t.Fatalf("the picked one is not asked: %+v", mine)
	}
	today := time.Now()
	must(cs.AddAbsence(ctx, olga, olga, "vacation", today, today, "", true, "", nil))
	if got := run(); got != petr {
		t.Fatalf("olga is on vacation: picked %s", got)
	}
	must(cs.AddAbsence(ctx, ivan, ivan, "sick", today, today, "", true, "", nil))
	must(cs.AddAbsence(ctx, petr, petr, "trip", today, today, "", true, "", nil))
	if got := run(); got != boss {
		t.Fatalf("all absent: picked %s, not the reserve", got)
	}

	tpl, err = s.SaveTemplate(ctx, ann, Template{Name: "Случайно", Active: true, Fields: []Field{who},
		Steps: []Step{{Kind: "pick", Field: "who", Users: []string{ivan, olga}}, {Kind: "log", Text: "{who}"}}})
	must(err)
	for range 5 {
		if got := run(); got != ivan && got != olga {
			t.Fatalf("at random: %s", got)
		}
	}
}
