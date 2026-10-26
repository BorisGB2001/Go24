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

// TestDelegate: «Делегировать» — to a subordinate by default, to nobody when the step says so, anyone's
// to anyone by an administrator; the new person decides, the history and the notifications tell it.
func TestDelegate(t *testing.T) {
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
	boss, ann, lawyer := newUser("boss"), newUser("ann"), newUser("lawyer")
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Отдел', $1) RETURNING id`, boss).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, ann, dept)
	must(err)
	s := NewStore(pool, tasks.NewStore(pool), crm.NewStore(pool), company.NewStore(pool))

	var ve *i18n.Error
	for _, st := range []Step{{Kind: "approve", To: boss, Text: "x", Delegate: "boss"}, {Kind: "notify", To: boss, Text: "x", Delegate: "all"}} {
		if _, err := s.SaveTemplate(ctx, boss, Template{Name: "Плохой", Steps: []Step{st}}); !errors.As(err, &ve) {
			t.Fatalf("a wrong delegation %+v saved: %v", st, err)
		}
	}
	tpl, err := s.SaveTemplate(ctx, boss, Template{Name: "Счёт", Active: true, Steps: []Step{
		{Kind: "approve", To: boss, Text: "Оплатить?"},
		{Kind: "review", To: boss, Text: "Счёт оплачен", Delegate: "none"}}})
	must(err)
	id, err := s.Start(ctx, ann, tpl, "", nil)
	must(err)
	mine := func(u string) []Approval {
		t.Helper()
		list, err := s.MyApprovals(ctx, u)
		must(err)
		return list
	}
	a := mine(boss)
	if len(a) != 1 || a[0].Delegate != "sub" {
		t.Fatalf("boss's approvals: %+v", a)
	}
	is := func(err error, key string) {
		t.Helper()
		if !errors.As(err, &ve) || ve.Key != key {
			t.Fatalf("want %s, got %v", key, err)
		}
	}
	_, err = s.Delegate(ctx, boss, a[0].ID, lawyer)
	is(err, "err.bp_delegate_sub") // not his subordinate
	if _, err := s.Delegate(ctx, ann, a[0].ID, ann); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's approval delegated: %v", err)
	}
	if got, err := s.Delegate(ctx, boss, a[0].ID, ann); err != nil || got != id {
		t.Fatalf("delegate to a subordinate: %q %v", got, err)
	}
	if len(mine(boss)) != 0 || len(mine(ann)) != 1 {
		t.Fatal("the approval is not ann's now")
	}
	var notified bool
	must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notifications WHERE user_id = $1 AND actor_id = $2 AND body = '📝 Оплатить?')`,
		ann, boss).Scan(&notified))
	if !notified {
		t.Fatal("ann is not told")
	}
	lines, err := s.Log(ctx, id)
	must(err)
	if l := lines[len(lines)-1]; l.Kind != "delegated" || l.Text != "ann" || l.Actor != "boss" {
		t.Fatalf("the history: %+v", l)
	}
	must(s.Decide(ctx, ann, a[0].ID, "approve", ""))

	// «без делегирования»: only an administrator hands it, and not to who already takes part
	r := mine(boss)
	if len(r) != 1 || r[0].Kind != "review" || r[0].Delegate != "none" {
		t.Fatalf("the review: %+v", r)
	}
	_, err = s.Delegate(ctx, boss, r[0].ID, ann)
	is(err, "err.bp_no_delegate")
	admin := tasks.WithAdmin(ctx)
	_, err = s.Delegate(admin, lawyer, r[0].ID, boss)
	is(err, "err.bp_delegate_user")
	if _, err := s.Delegate(admin, lawyer, r[0].ID, lawyer); err != nil {
		t.Fatal(err)
	}
	must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notifications WHERE user_id = $1 AND body = '↪ Счёт оплачен')`, boss).Scan(&notified))
	if !notified {
		t.Fatal("boss is not told his review is taken")
	}
	must(s.Decide(ctx, lawyer, r[0].ID, "seen", ""))
	x, err := s.Instance(ctx, ann, id)
	must(err)
	if x.Status != "done" {
		t.Fatalf("status %s", x.Status)
	}
}
