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

// TestVerdict: an approval by a vote ends as soon as the share is reached or cannot be any more; with «wait for
// everyone» — when everyone voted.
func TestVerdict(t *testing.T) {
	vote := Step{Mode: "vote", Percent: 60}
	all := Step{Mode: "vote", Percent: 60, WaitAll: true}
	for _, c := range []struct {
		st                   Step
		total, yes, no, seen int
		want                 string
	}{
		{vote, 5, 2, 0, 0, ""},
		{vote, 5, 3, 0, 0, "approved"}, // 60 %
		{vote, 5, 0, 2, 0, ""},         // 3 of 5 may still approve
		{vote, 5, 1, 3, 0, "rejected"}, // 2 of 5 at most
		{all, 5, 3, 0, 0, ""},
		{all, 5, 3, 2, 0, "approved"},
		{all, 5, 2, 3, 0, "rejected"},
		{Step{Mode: "all"}, 2, 1, 0, 0, ""},
		{Step{Mode: "majority"}, 3, 2, 0, 0, "approved"},
	} {
		if got := Verdict("approve", c.st, c.total, c.yes, c.no, c.seen, "approve"); got != c.want {
			t.Errorf("%+v: %q, want %q", c, got, c.want)
		}
	}
	if Verdict("approve", Step{}, 3, 0, 1, 0, "reject") != "rejected" || Verdict("review", Step{}, 2, 0, 0, 1, "seen") != "" {
		t.Fatal("any, review")
	}
	for _, c := range []struct {
		remark, decision string
		want             bool
	}{{"", "reject", false}, {"yes", "seen", true}, {"approve", "answer", true}, {"approve", "reject", false}, {"reject", "reject", true}, {"reject", "approve", false}} {
		if NeedsComment(c.remark, c.decision) != c.want {
			t.Errorf("%+v", c)
		}
	}
}

// TestApprovalTimeout: «Время на выполнение» of an approval, a review and a request, the required comment and the
// vote approval activity.
func TestApprovalTimeout(t *testing.T) {
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
	boss, ann, bob, eve := newUser("boss"), newUser("ann"), newUser("bob"), newUser("eve")
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))
	var ve *i18n.Error
	is := func(err error, key string) {
		t.Helper()
		if !errors.As(err, &ve) || ve.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}
	for _, st := range []Step{
		{Kind: "notify", To: "starter", Text: "x", Timeout: 60},            // not of an approval
		{Kind: "approve", To: boss, Text: "x", Timeout: MaxTimeout + 1},    // more than a year
		{Kind: "approve", To: boss, Text: "x", Mode: "vote"},               // no share
		{Kind: "approve", To: boss, Text: "x", Percent: 50},                // a share without a vote
		{Kind: "approve", To: boss, Text: "x", Mode: "vote", Percent: 101}, // more than all
		{Kind: "approve", To: boss, Text: "x", Remark: "never"},
		{Kind: "review", To: boss, Text: "x", No: "Нет"}, // a review has one button
	} {
		_, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Steps: []Step{st}})
		is(err, "err.bp_step")
	}
	mine := func(user string) Approval {
		t.Helper()
		list, err := s.MyApprovals(ctx, user)
		must(err)
		if len(list) != 1 {
			t.Fatalf("%d approvals", len(list))
		}
		return list[0]
	}
	start := func(steps ...Step) string {
		t.Helper()
		tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Отпуск", Active: true, Steps: steps})
		must(err)
		id, err := s.Start(ctx, ann, tpl, "", nil)
		must(err)
		return id
	}
	later := time.Now().Add(25 * time.Hour)
	done := Step{Kind: "notify", To: "starter", Text: "готово"}

	// a day to approve, a comment to reject, own buttons: boss is silent — not approved, the process ends rejected
	id := start(Step{Kind: "approve", To: boss, Text: "Отпуск?", Timeout: 24 * 60, Remark: "reject", Yes: "Согласен", No: "Против"}, done)
	a := mine(boss)
	if a.Due == nil || a.Due.Before(time.Now().Add(23*time.Hour)) || a.Remark != "reject" || a.Yes != "Согласен" || a.No != "Против" {
		t.Fatalf("%+v", a)
	}
	is(s.Decide(ctx, boss, a.ID, "reject", " "), "err.bp_comment_required")
	if errs := s.WakeDue(ctx, time.Now()); len(errs) > 0 { // not yet
		t.Fatal(errs)
	}
	if errs := s.WakeDue(ctx, later); len(errs) > 0 {
		t.Fatal(errs)
	}
	x, _ := s.Instance(ctx, ann, id)
	if x.Status != "rejected" {
		t.Fatalf("status %s", x.Status)
	}
	if list, _ := s.MyApprovals(ctx, boss); len(list) != 0 {
		t.Fatal("the task stays")
	}
	lines, _ := s.Log(ctx, id)
	if lines[len(lines)-1].Kind != "timeout_rejected" {
		t.Fatalf("history %+v", lines[len(lines)-1])
	}

	// a vote of 50 %: bob approves, eve and boss are silent — the votes given decide when the time is over
	id = start(Step{Kind: "approve", To: boss, Users: []string{bob, eve}, Text: "Купить?", Mode: "vote", Percent: 50, Timeout: 60}, done)
	must(s.Decide(ctx, bob, mine(bob).ID, "approve", ""))
	if x, _ = s.Instance(ctx, ann, id); x.Step != 0 { // 1 of 3
		t.Fatalf("step %d", x.Step)
	}
	if errs := s.WakeDue(ctx, later); len(errs) > 0 {
		t.Fatal(errs)
	}
	if x, _ = s.Instance(ctx, ann, id); x.Status != "done" {
		t.Fatalf("status %s", x.Status)
	}

	// a vote of 60 % till everyone: two of three approve — approved once the third rejects
	id = start(Step{Kind: "approve", To: boss, Users: []string{bob, eve}, Text: "Купить?", Mode: "vote", Percent: 60, WaitAll: true}, done)
	must(s.Decide(ctx, bob, mine(bob).ID, "approve", ""))
	must(s.Decide(ctx, eve, mine(eve).ID, "approve", ""))
	if x, _ = s.Instance(ctx, ann, id); x.Step != 0 {
		t.Fatal("decided before everyone voted")
	}
	must(s.Decide(ctx, boss, mine(boss).ID, "reject", ""))
	if x, _ = s.Instance(ctx, ann, id); x.Status != "done" {
		t.Fatalf("status %s", x.Status)
	}

	// a request with a required comment, unanswered in time: the process goes on without its fields
	id = start(Step{Kind: "request", To: bob, Text: "Сумма?", Fields: []Field{{Key: "sum", Label: "Сумма", Kind: "number"}}, Timeout: 30, Remark: "yes"}, done)
	is(s.Answer(ctx, bob, mine(bob).ID, map[string]string{"sum": "5"}, ""), "err.bp_comment_required")
	if errs := s.WakeDue(ctx, later); len(errs) > 0 {
		t.Fatal(errs)
	}
	if x, _ = s.Instance(ctx, ann, id); x.Status != "done" {
		t.Fatalf("status %s", x.Status)
	}
	lines, _ = s.Log(ctx, id)
	var timeouts int
	for _, l := range lines {
		if l.Kind == "timeout" {
			timeouts++
		}
	}
	if timeouts != 1 {
		t.Fatalf("history %+v", lines)
	}
}
