package openlines

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestStats: «Статистика» counts the dialogues of the period by lines, operators and days — new, closed, without an
// answer, the average first answer and time to close, the visitors' votes; spam is apart, the filters narrow it.
func TestStats(t *testing.T) {
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
	boss, a, b := newUser("boss"), newUser("a"), newUser("b")
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, _ ...any) string { return key }
	w1, err := s.SaveWidget(ctx, boss, Widget{Name: "Сайт", Active: true, Operators: []string{a}, VoteOn: true})
	must(err)
	w2, err := s.SaveWidget(ctx, boss, Widget{Name: "Магазин", Active: true, Operators: []string{b}})
	must(err)
	dialogue := func(wid, text string) (string, string) {
		sid, tok, err := s.Visit(ctx, wid, "", "")
		must(err)
		must(s.VisitorSay(ctx, wid, tok, text))
		return sid, tok
	}
	// the first line: answered in 2 minutes and closed in 10, rated 👍; one waits; a visitor who only opened the widget
	d1, t1 := dialogue(w1, "Здравствуйте")
	must(s.Reply(ctx, a, d1, "Добрый день"))
	must(s.SetStatus(ctx, a, d1, false))
	must(s.VisitorVote(ctx, w1, t1, 1, ""))
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET started_at = now() - interval '10 minutes', answered_at = now() - interval '8 minutes',
		closed_at = now() WHERE id = $1`, d1)
	must(err)
	dialogue(w1, "Есть кто?")
	_, _, err = s.Visit(ctx, w1, "", "")
	must(err)
	// the second line: answered in 30 seconds and still open; spam
	d3, _ := dialogue(w2, "Цена?")
	must(s.Reply(ctx, b, d3, "100"))
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET answered_at = started_at + interval '30 seconds' WHERE id = $1`, d3)
	must(err)
	d4, _ := dialogue(w2, "Купите рекламу")
	must(s.Spam(ctx, b, d4))
	// last month's dialogue: neither new nor closed today
	d5, _ := dialogue(w2, "Давно")
	_, err = pool.Exec(ctx, `UPDATE ol_sessions SET started_at = now() - interval '40 days' WHERE id = $1`, d5)
	must(err)

	today := time.Now()
	st, err := s.Stats(ctx, StatsFilter{From: today, To: today})
	must(err)
	tot := st.Total
	if tot.New != 3 || tot.Closed != 1 || tot.NoAnswer != 1 || tot.Spam != 1 || tot.Up != 1 || tot.Down != 0 {
		t.Fatalf("totals: %+v", tot)
	}
	if got := tot.FirstAnswer(); got != 75*time.Second { // (2 min + 30 s) / 2
		t.Fatalf("first answer: %v", got)
	}
	if got := tot.ToClose(); got != 10*time.Minute {
		t.Fatalf("to close: %v", got)
	}
	if len(st.Lines) != 2 || st.Lines[0].Name != "Сайт" || st.Lines[0].New != 2 || st.Lines[1].New != 1 || st.Lines[1].Spam != 1 {
		t.Fatalf("lines: %+v", st.Lines)
	}
	ops := map[string]StatsRow{}
	for _, r := range st.Operators {
		ops[r.Key] = r
	}
	if ops[a].New != 1 || ops[a].Closed != 1 || ops[b].New != 1 || ops[b].FirstAnswer() != 30*time.Second || ops[""].NoAnswer != 1 { // nobody has answered the waiting one yet
		t.Fatalf("operators: %+v", st.Operators)
	}
	if len(st.Days) != 1 || st.Days[0].New != 3 {
		t.Fatalf("days: %+v", st.Days)
	}

	// a week: every day has its row; the filters by line and operator
	st, err = s.Stats(ctx, StatsFilter{From: today.AddDate(0, 0, -6), To: today, Widget: w2})
	must(err)
	if len(st.Days) != 7 || st.Days[6].New != 1 || st.Days[0].New != 0 || st.Total.New != 1 || st.Total.Spam != 1 {
		t.Fatalf("a week of the second line: %+v", st)
	}
	st, err = s.Stats(ctx, StatsFilter{From: today, To: today, OperatorID: a})
	must(err)
	if st.Total.New != 1 || len(st.Lines) != 1 {
		t.Fatalf("the operator's: %+v", st.Total)
	}
	st, err = s.Stats(ctx, StatsFilter{From: today.AddDate(0, 0, -45), To: today.AddDate(0, 0, -35)})
	must(err)
	if st.Total.New != 1 || st.Total.NoAnswer != 1 {
		t.Fatalf("last month: %+v", st.Total)
	}

	// reopening clears the closing time
	must(s.SetStatus(ctx, a, d1, true))
	st, err = s.Stats(ctx, StatsFilter{From: today, To: today})
	must(err)
	if st.Total.Closed != 0 {
		t.Fatalf("reopened: %+v", st.Total)
	}
}
