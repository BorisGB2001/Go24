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

// TestParallel: «Счёт на оплату» — the accountant and the lawyer approve at once; the lawyer's branch pauses, the
// branches join when both are over; a rejection in a branch ends the whole process.
func TestParallel(t *testing.T) {
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
	ann, buh, law := newUser("ann"), newUser("buh"), newUser("law")
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts, crm.NewStore(pool), company.NewStore(pool))
	steps := []Step{{Kind: "parallel", Branches: []int{4}, Goto: 6},
		{Kind: "approve", To: buh, Text: "Бухгалтерия: {sum}"},
		{Kind: "set", Field: "sum", Value: "{sum} ок"},
		{Kind: "approve", To: law, Text: "Юрист"},
		{Kind: "wait", Days: 1},
		{Kind: "notify", To: "starter", Text: "Согласовано: {sum}"}}
	id, err := s.SaveTemplate(ctx, ann, Template{Name: "Счёт", Active: true, Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "string"}},
		Steps: steps})
	must(err)
	of := func(user string) Approval {
		t.Helper()
		mine, err := s.MyApprovals(ctx, user)
		must(err)
		if len(mine) != 1 {
			t.Fatalf("approvals of %s: %+v", user, mine)
		}
		return mine[0]
	}
	p, err := s.Start(ctx, ann, id, "", map[string]string{"sum": "100"})
	must(err)
	x, err := s.Instance(ctx, ann, p)
	must(err)
	if x.Status != "running" || x.Step != 0 || len(x.Branches) != 2 || x.State(1) != "now" || x.State(3) != "now" || x.State(5) != "" {
		t.Fatalf("both branches wait: %+v", x)
	}
	must(s.Decide(ctx, law, of(law).ID, "approve", ""))
	if x, _ = s.Instance(ctx, ann, p); x.Branches[1] != 4 || !x.Current(4) || x.State(3) != "done" {
		t.Fatalf("the lawyer's branch pauses: %+v", x.Branches)
	}
	must(s.Decide(ctx, buh, of(buh).ID, "approve", ""))
	if x, _ = s.Instance(ctx, ann, p); x.Status != "running" || x.Values["sum"] != "100 ок" || x.Branches[0] != 3 || x.State(2) != "done" {
		t.Fatalf("the accountant's branch is over, the lawyer's waits: %s %v %v", x.Status, x.Values, x.Branches)
	}
	if errs := s.WakeDue(ctx, time.Now().Add(48*time.Hour)); len(errs) > 0 {
		t.Fatal(errs)
	}
	if x, _ = s.Instance(ctx, ann, p); x.Status != "done" || len(x.Branches) != 0 {
		t.Fatalf("joined: %s %v", x.Status, x.Branches)
	}
	var said string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE kind = 'bp' AND user_id = $1 ORDER BY id DESC OFFSET 1 LIMIT 1`, ann).Scan(&said))
	if said != "Согласовано: 100 ок" {
		t.Fatalf("told %q", said)
	}
	if lines, _ := s.Log(ctx, p); lines[len(lines)-3].Kind != "joined" {
		t.Fatalf("history: %+v", lines)
	}
	// the lawyer rejects: the process is rejected, the accountant is not asked any more
	p, err = s.Start(ctx, ann, id, "", map[string]string{"sum": "5"})
	must(err)
	must(s.Decide(ctx, law, of(law).ID, "reject", "Нет договора"))
	if x, _ = s.Instance(ctx, ann, p); x.Status != "rejected" {
		t.Fatalf("rejected in a branch: %s", x.Status)
	}
	if mine, _ := s.MyApprovals(ctx, buh); len(mine) != 0 {
		t.Fatalf("a rejected process asks: %+v", mine)
	}

	var ve *i18n.Error
	for _, bad := range [][]Step{
		{{Kind: "parallel", Branches: []int{2}, Goto: 3}, {Kind: "goto"}, {Kind: "wait", Days: 1}},                    // the first branch is empty
		{{Kind: "parallel", Branches: []int{3}, Goto: 4}, {Kind: "goto", Goto: 4}, {Kind: "wait", Days: 1}, steps[5]}, // a jump out of a branch
		{{Kind: "parallel", Branches: []int{3}}, {Kind: "parallel", Branches: []int{3}}, steps[5], steps[5]},          // nested
		{{Kind: "goto", Goto: 3}, {Kind: "parallel", Branches: []int{4}}, steps[5], steps[5]},                         // into a branch
		{{Kind: "notify", To: "starter", Text: "x", Branches: []int{2}}, steps[5]},                                    // not parallel
	} {
		if _, err := s.SaveTemplate(ctx, ann, Template{Name: "Плохой", Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "string"}},
			Steps: bad}); !errors.As(err, &ve) {
			t.Errorf("saved %+v: %v", bad, err)
		}
	}
}
