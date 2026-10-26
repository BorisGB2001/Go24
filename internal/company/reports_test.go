package company

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/worktime"
)

// TestReportPeriod: the period of the next report by the rule — daily, weekly, monthly, at will — and the last one sent.
func TestReportPeriod(t *testing.T) {
	sched := worktime.Default()
	d := func(m, day int) time.Time { return time.Date(2026, time.Month(m), day, 0, 0, 0, 0, time.Local) }
	at := func(m, day int) time.Time { return d(m, day).Add(15 * time.Hour) }
	ptr := func(t time.Time) *time.Time { return &t }
	week := ReportRule{Period: "week", Weekday: 5}
	for _, c := range []struct {
		name     string
		r        ReportRule
		last     *time.Time
		now      time.Time
		from, to time.Time
		due      bool
	}{
		{"daily", ReportRule{Period: "day"}, nil, at(10, 23), d(10, 23), d(10, 23), true},
		{"daily sent", ReportRule{Period: "day"}, ptr(d(10, 23)), at(10, 23), d(10, 24), d(10, 23), false},
		{"daily on Saturday: Friday's", ReportRule{Period: "day"}, ptr(d(10, 22)), at(10, 24), d(10, 23), d(10, 23), true},
		{"weekly on Friday", week, nil, at(10, 23), d(10, 17), d(10, 23), true},
		{"weekly, Friday sent, Monday", week, ptr(d(10, 23)), at(10, 26), d(10, 24), d(10, 26), false},
		{"weekly, Friday missed, Monday", week, ptr(d(10, 16)), at(10, 26), d(10, 17), d(10, 23), true},
		{"monthly on the 31st in November", ReportRule{Period: "month", Monthday: 31}, ptr(d(10, 31)), at(11, 30), d(11, 1), d(11, 30), true},
		{"monthly before the day", ReportRule{Period: "month", Monthday: 25}, ptr(d(9, 25)), at(10, 20), d(9, 26), d(10, 20), false},
		{"at will", ReportRule{}, ptr(d(10, 20)), at(10, 23), d(10, 21), d(10, 23), false},
		{"at will, first", ReportRule{}, nil, at(10, 23), d(10, 23), d(10, 23), false},
	} {
		p := NextPeriod(c.r, sched, c.last, c.now)
		if !p.From.Equal(c.from) || !p.To.Equal(c.to) || p.Due != c.due {
			t.Errorf("%s: %s — %s due %v, want %s — %s due %v", c.name, p.From.Format(time.DateOnly), p.To.Format(time.DateOnly), p.Due,
				c.from.Format(time.DateOnly), c.to.Format(time.DateOnly), c.due)
		}
	}
	if p := NextPeriod(ReportRule{Period: "day"}, sched, ptr(d(10, 23)), at(10, 23)); !p.Empty() {
		t.Errorf("daily sent: not empty %v", p)
	}
}

// TestReports: the rules of the company and a department, sending a report (once per period) to the manager, the
// mark and comments with their notifications, who sees whose reports.
func TestReports(t *testing.T) {
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
	user := func(sub, name string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	boss, anna, olga := user("boss", "Борис"), user("anna", "Анна"), user("olga", "Ольга")
	var top, sub string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Компания', $1) RETURNING id`, boss).Scan(&top))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Бухгалтерия', $1) RETURNING id`, top).Scan(&sub))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $3), ($2, $4)`, anna, olga, sub, top)
	must(err)
	s := NewStore(pool)

	must(s.SetReportRules(ctx, boss, ReportRules{ReportRule: ReportRule{Period: "week", Weekday: 5},
		Depts: map[string]ReportRule{top: {Period: "week", Weekday: 5}, sub: {Period: "day"}, "00000000-0000-0000-0000-000000000000": {Period: "day"}}}))
	rr, err := s.ReportRules(ctx)
	must(err)
	if len(rr.Depts) != 2 || rr.Period != "week" {
		t.Fatalf("rules %+v", rr)
	}
	if r, _ := s.RuleFor(ctx, anna); r.Period != "day" { // the sub-department's own
		t.Errorf("anna's rule %+v", r)
	}
	if r, _ := s.RuleFor(ctx, olga); r.Period != "week" || r.Weekday != 5 {
		t.Errorf("olga's rule %+v", r)
	}
	if err := s.SetReportRules(ctx, boss, ReportRules{ReportRule: ReportRule{Period: "year"}}); err == nil {
		t.Error("unknown period saved")
	}

	sched := worktime.Default()
	now := time.Date(2026, 10, 23, 18, 0, 0, 0, time.Local) // Friday
	start := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	_, err = pool.Exec(ctx, `INSERT INTO workdays (user_id, day, started_at, ended_at) VALUES ($1, '2026-10-23', $2, $3)`, anna, start, start.Add(8*time.Hour))
	must(err)
	due, err := s.ReportDue(ctx, anna, sched, now)
	must(err)
	if due == nil || !due.From.Equal(due.To) || due.To.Day() != 23 {
		t.Fatalf("due %+v", due)
	}
	id, err := s.SendReport(ctx, anna, *due, " Закрыла квартал ", ReportItems{Tasks: []ReportTask{{Key: "ACC-1", Title: "Квартал", Closed: true}}}, now)
	must(err)
	if _, err := s.SendReport(ctx, anna, *due, "ещё раз", ReportItems{}, now); !isKey(err, "err.work_report_period") {
		t.Errorf("same period twice: %v", err)
	}
	if due, _ := s.ReportDue(ctx, anna, sched, now); due != nil {
		t.Errorf("still due %+v", due)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'work_report'`, boss).Scan(&n))
	if n != 1 {
		t.Errorf("the boss got %d", n)
	}

	r, err := s.Report(ctx, boss, id, false)
	must(err)
	if r.Body != "Закрыла квартал" || r.Worked != 8*3600 || len(r.Items.Tasks) != 1 || r.Mark != "" {
		t.Errorf("report %+v", r)
	}
	if _, err := s.Report(ctx, olga, id, false); !errors.Is(err, ErrNotFound) { // not her manager
		t.Errorf("olga sees: %v", err)
	}
	if err := s.MarkReport(ctx, anna, id, "good", "", false, now); err == nil {
		t.Error("the author marked")
	}
	must(s.MarkReport(ctx, boss, id, "good", "Молодец", false, now))
	must(s.CommentReport(ctx, anna, id, "Спасибо", false, now))
	r, _ = s.Report(ctx, anna, id, false)
	if r.Mark != "good" || r.MarkedBy != "Борис" || len(r.Comments) != 2 || r.Comments[1].Body != "Спасибо" {
		t.Errorf("marked %+v", r)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind IN ('work_report_mark', 'work_report_comment')`, anna).Scan(&n))
	if n != 1 {
		t.Errorf("anna got %d", n)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'work_report_comment'`, boss).Scan(&n))
	if n != 1 {
		t.Errorf("the boss got %d comments", n)
	}

	list, err := s.Reports(ctx, ReportFilter{People: []string{anna}, Unchecked: true})
	must(err)
	if len(list) != 0 {
		t.Errorf("unchecked %d", len(list))
	}
	list, _ = s.Reports(ctx, ReportFilter{From: time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local), To: time.Date(2026, 11, 1, 0, 0, 0, 0, time.Local)})
	if len(list) != 1 || list[0].CommentCount != 2 {
		t.Errorf("october %+v", list)
	}
}

func isKey(err error, key string) bool {
	var e *i18n.Error
	return errors.As(err, &e) && e.Key == key
}
