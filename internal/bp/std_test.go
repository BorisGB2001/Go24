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
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestStandard: the standard templates install once each, with the accountant; «Отпуск» started by hand is a post the
// manager and the accountant come to see, approved it goes to the absence chart; «Счёт на оплату» is paid by the
// accountant's answer.
func TestStandard(t *testing.T) {
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
	boss, ann, acc, other := newUser("boss"), newUser("ann"), newUser("acc"), newUser("other")
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Отдел', $1) RETURNING id`, boss).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, ann, dept)
	must(err)
	ts := tasks.NewStore(pool)
	s := NewStore(pool, ts, crm.NewStore(pool), company.NewStore(pool))
	fs := feed.NewStore(pool, ts)
	s.Feed = fs
	tr := func(k string) string { return k }

	var ve *i18n.Error
	if _, err := s.InstallStandard(ctx, boss, "", tr); !errors.As(err, &ve) || ve.Key != "err.bp_accountant" {
		t.Fatalf("installed without the accountant: %v", err)
	}
	n, err := s.InstallStandard(ctx, boss, acc, tr)
	must(err)
	if n != len(StdKeys) {
		t.Fatalf("installed %d", n)
	}
	if n, _ := s.InstallStandard(ctx, boss, acc, tr); n != 0 {
		t.Fatalf("installed twice: %d", n)
	}
	if keys, _ := s.Installed(ctx); len(keys) != len(StdKeys) {
		t.Fatalf("installed = %v", keys)
	}
	list, err := s.Templates(ctx, "none", true)
	must(err)
	byName := map[string]string{}
	for _, t := range list {
		byName[t.Name] = t.ID
	}

	// Отпуск
	if _, err := s.Start(ctx, ann, byName["bp.std.vacation"], "", map[string]string{"from": "2026-11-10", "to": "2026-11-05"}); err != nil {
		t.Fatal(err) // the dates in the wrong order fail at the absence step, not at the start
	}
	id, err := s.Start(ctx, ann, byName["bp.std.vacation"], "", map[string]string{"from": "2026-11-02", "to": "2026-11-15", "comment": "Море"})
	must(err)
	posts := func(user string) []feed.Item {
		t.Helper()
		items, err := fs.List(ctx, user, feed.Filter{Kind: "bp"})
		must(err)
		return items
	}
	if p := posts(ann); len(p) != 2 || p[0].Post.Process != id || p[0].Post.Title != "bp.std.vacation" ||
		p[0].Post.Body != "bp.std.f.from: 2026-11-02\nbp.std.f.to: 2026-11-15\nbp.std.f.comment: Море" {
		t.Fatalf("ann's posts = %+v", p)
	}
	if p := posts(boss); len(p) != 2 {
		t.Fatalf("the manager does not see the posts: %d", len(p))
	}
	if p := posts(other); len(p) != 0 {
		t.Fatalf("someone outside sees the post: %d", len(p))
	}
	mine, _ := s.MyApprovals(ctx, boss)
	var ap Approval
	for _, a := range mine {
		if a.InstanceID == id {
			ap = a
		}
	}
	if ap.Text != "bp.std.s.vacation_approve" {
		t.Fatalf("approval = %+v", mine)
	}
	must(s.Decide(ctx, boss, ap.ID, "approve", ""))
	x, _ := s.instance(ctx, id)
	if x.Status != "done" {
		t.Fatalf("status = %s", x.Status)
	}
	from, _ := time.ParseInLocation("2006-01-02", "2026-11-01", time.Local)
	abs, err := s.Company.Absences(ctx, from, from.AddDate(0, 1, 0))
	must(err)
	if len(abs) != 1 || abs[0].UserID != ann || abs[0].Kind != "vacation" || abs[0].From.Format("2006-01-02") != "2026-11-02" ||
		abs[0].To.Format("2006-01-02") != "2026-11-15" || abs[0].Comment != "Море" {
		t.Fatalf("absences = %+v", abs)
	}
	if p := posts(acc); len(p) != 1 || p[0].Post.Process != id {
		t.Fatalf("the accountant told does not see the post: %+v", p)
	}
	// the other, with the dates wrong, failed after the approval
	for _, a := range mine {
		if a.InstanceID != id {
			must(s.Decide(ctx, boss, a.ID, "approve", ""))
			if x, _ := s.instance(ctx, a.InstanceID); x.Status != "failed" {
				t.Fatalf("wrong dates: %s", x.Status)
			}
		}
	}

	// Счёт на оплату
	inv, err := s.Start(ctx, ann, byName["bp.std.invoice"], "", map[string]string{"what": "Бумага", "sum": "1200"})
	must(err)
	mine, _ = s.MyApprovals(ctx, boss)
	must(s.Decide(ctx, boss, mine[0].ID, "approve", ""))
	mine, _ = s.MyApprovals(ctx, acc)
	if len(mine) != 1 || mine[0].Kind != "request" || !mine[0].CanReject {
		t.Fatalf("accountant's = %+v", mine)
	}
	must(s.Answer(ctx, acc, mine[0].ID, map[string]string{"paid": "2026-10-27"}, ""))
	if x, _ := s.instance(ctx, inv); x.Status != "done" || x.Values["paid"] != "2026-10-27" {
		t.Fatalf("invoice = %s %v", x.Status, x.Values)
	}
}
