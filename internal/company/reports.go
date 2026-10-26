package company

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/worktime"
)

// «Рабочие отчёты»: an employee reports to their manager for a day, a week or a month — the time worked,
// the tasks and meetings of the period and a text; the manager marks the report and both discuss it.

// ReportPeriods are how often reports are due ("" — not required).
var ReportPeriods = []string{"day", "week", "month"}

// ReportMarks are a manager's marks of a report.
var ReportMarks = []string{"good", "bad", "none"}

// ReportRule is when reports are due: every working day, on a weekday (time.Weekday) or on a day of the month (a
// shorter month — its last day).
type ReportRule struct {
	Period   string `json:"period"`
	Weekday  int    `json:"weekday"`
	Monthday int    `json:"monthday"`
}

// ReportRules: the company's rule and the departments' own (they hold for the sub-departments too).
type ReportRules struct {
	ReportRule
	Depts map[string]ReportRule `json:"depts,omitempty"`
}

const settingReports = "work_reports"

func (r *ReportRule) clean() error {
	if r.Period != "" && !slices.Contains(ReportPeriods, r.Period) || r.Weekday < 0 || r.Weekday > 6 || r.Monthday < 0 || r.Monthday > 31 {
		return i18n.E("err.bad_form")
	}
	if r.Period != "week" {
		r.Weekday = 0
	}
	if r.Period != "month" {
		r.Monthday = 0
	} else if r.Monthday == 0 {
		r.Monthday = 31
	}
	return nil
}

// IsDay: a report is due on the day.
func (r ReportRule) IsDay(d time.Time, sched worktime.Schedule) bool {
	switch r.Period {
	case "day":
		return sched.WorkingDay(d)
	case "week":
		return int(d.Weekday()) == r.Weekday
	case "month":
		last := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, d.Location()).Day()
		return d.Day() == min(r.Monthday, last)
	}
	return false
}

// before is the last report day before d (ok false — none within two months).
func (r ReportRule) before(d time.Time, sched worktime.Schedule) (time.Time, bool) {
	for i := 1; i <= 62; i++ {
		if p := d.AddDate(0, 0, -i); r.IsDay(p, sched) {
			return p, true
		}
	}
	return time.Time{}, false
}

// ReportPeriod is the period of the next report: due — its report day has come (To is that day); not due — a report
// sent now covers the days up to today.
type ReportPeriod struct {
	From, To time.Time
	Due      bool
	Rule     ReportRule
}

// Empty: everything up to today is reported.
func (p ReportPeriod) Empty() bool { return p.From.After(p.To) }

// NextPeriod computes the next report's period by the rule from the end of the last report (nil — none sent yet):
// it starts after the previous report day (a daily report covers its day) and not before the last report ended.
func NextPeriod(r ReportRule, sched worktime.Schedule, last *time.Time, now time.Time) ReportPeriod {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	p := ReportPeriod{To: today, Rule: r}
	if r.Period != "" {
		day := today
		if !r.IsDay(day, sched) {
			day, _ = r.before(today.AddDate(0, 0, 1), sched)
		}
		if !day.IsZero() && (last == nil || last.Before(day)) {
			p.To, p.Due = day, true
		}
	}
	p.From = p.To
	if r.Period != "day" && r.Period != "" {
		if prev, ok := r.before(p.To, sched); ok {
			p.From = prev.AddDate(0, 0, 1)
		}
	}
	if last != nil {
		if r.Period == "" { // reports at will cover the days since the last one, a month at most
			p.From = maxTime(last.AddDate(0, 0, 1), p.To.AddDate(0, 0, -30))
		} else {
			p.From = maxTime(p.From, last.AddDate(0, 0, 1))
		}
	}
	return p
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// ReportRules are the rules of reports of the company.
func (s *Store) ReportRules(ctx context.Context) (ReportRules, error) {
	var rr ReportRules
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT value FROM company_settings WHERE key = $1`, settingReports).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return rr, nil
	}
	if err != nil {
		return rr, err
	}
	if json.Unmarshal(raw, &rr) != nil {
		return ReportRules{}, nil
	}
	return rr, nil
}

// SetReportRules saves the rules (an administrator — checked by the caller); unknown departments are dropped.
func (s *Store) SetReportRules(ctx context.Context, actor string, rr ReportRules) error {
	if err := rr.clean(); err != nil {
		return err
	}
	ids := []string{}
	for id := range rr.Depts {
		ids = append(ids, id)
	}
	rows, err := s.db.Query(ctx, `SELECT id::text FROM departments WHERE id::text = ANY($1)`, ids)
	if err != nil {
		return err
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	depts := map[string]ReportRule{}
	for id, r := range rr.Depts {
		if !slices.Contains(have, id) {
			continue
		}
		if err := r.clean(); err != nil {
			return err
		}
		depts[id] = r
	}
	rr.Depts = depts
	raw, _ := json.Marshal(rr)
	_, err = s.db.Exec(ctx, `INSERT INTO company_settings (key, value, updated_by) VALUES ($1, $2, $3)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		settingReports, raw, actor)
	return err
}

// RuleFor is the rule of the person: of their nearest department with its own rule, else the company's.
func (s *Store) RuleFor(ctx context.Context, userID string) (ReportRule, error) {
	rr, err := s.ReportRules(ctx)
	if err != nil || len(rr.Depts) == 0 {
		return rr.ReportRule, err
	}
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT d.id, d.parent_id, 0 AS lvl FROM user_departments ud JOIN departments d ON d.id = ud.department_id
			WHERE ud.user_id::text = $1
			UNION ALL SELECT p.id, p.parent_id, up.lvl + 1 FROM departments p JOIN up ON p.id = up.parent_id WHERE up.lvl < 50)
		SELECT id::text FROM up ORDER BY lvl, id`, userID)
	if err != nil {
		return rr.ReportRule, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	for _, id := range ids {
		if r, ok := rr.Depts[id]; ok {
			return r, err
		}
	}
	return rr.ReportRule, err
}

// ReportTask is a task of a report: closed in the period or with the time the person logged in it.
type ReportTask struct {
	Key     string `json:"key"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Closed  bool   `json:"closed"`
	Seconds int    `json:"seconds"`
}

// ReportEvent is a meeting of a report.
type ReportEvent struct {
	ID     string    `json:"id"`
	Title  string    `json:"title"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	AllDay bool      `json:"all_day"`
}

// ReportItems are what the period holds; a report keeps them as they were when it was sent.
type ReportItems struct {
	Tasks  []ReportTask  `json:"tasks"`
	Events []ReportEvent `json:"events"`
}

// PeriodTasks are the person's tasks of [from, to]: closed by them as the responsible, with time they logged, and
// those in progress now.
func (s *Store) PeriodTasks(ctx context.Context, userID string, from, to time.Time) ([]ReportTask, error) {
	rows, err := s.db.Query(ctx, `WITH spent AS (SELECT task_id, sum(seconds)::int AS secs FROM time_entries
			WHERE user_id::text = $1 AND spent_on BETWEEN $2 AND $3 GROUP BY task_id)
		SELECT p.key || '-' || t.number, t.title, t.status,
			t.status = 'completed' AND t.closed_at >= $2::date AND t.closed_at < $3::date + 1 AS closed, coalesce(sp.secs, 0)
		FROM tasks t JOIN projects p ON p.id = t.project_id LEFT JOIN spent sp ON sp.task_id = t.id
		WHERE sp.secs IS NOT NULL OR t.responsible_id::text = $1 AND (t.status = 'in_progress'
			OR t.status = 'completed' AND t.closed_at >= $2::date AND t.closed_at < $3::date + 1)
		ORDER BY closed DESC, 5 DESC, t.closed_at DESC NULLS LAST LIMIT 200`, userID, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ReportTask, error) {
		var t ReportTask
		err := r.Scan(&t.Key, &t.Title, &t.Status, &t.Closed, &t.Seconds)
		return t, err
	})
}

// WorkReport is a sent report.
type WorkReport struct {
	ID               int64
	UserID, UserName string
	PhotoKey         string
	From, To         time.Time
	Body             string
	Worked           int
	Items            ReportItems
	Sent             time.Time
	Mark             string // "" — not checked yet
	MarkedBy         string // the name
	MarkedAt         *time.Time
	Comments         []ReportComment
	CommentCount     int
}

func (r WorkReport) PhotoURL() string { return PhotoURL(r.UserID, r.PhotoKey) }

// ReportComment is a comment of a report.
type ReportComment struct {
	ID                   int64
	AuthorID, AuthorName string
	PhotoKey             string
	Body                 string
	At                   time.Time
}

func (c ReportComment) PhotoURL() string { return PhotoURL(c.AuthorID, c.PhotoKey) }

const reportSQL = `SELECT r.id, r.user_id::text, u.name, u.photo_key, r.date_from, r.date_to, r.body, r.worked, r.items, r.sent_at,
	r.mark, coalesce(m.name, ''), r.marked_at, (SELECT count(*) FROM work_report_comments c WHERE c.report_id = r.id)
	FROM work_reports r JOIN users u ON u.id = r.user_id LEFT JOIN users m ON m.id = r.marked_by`

func scanReports(rows pgx.Rows, err error) ([]WorkReport, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (WorkReport, error) {
		var r WorkReport
		var items []byte
		err := row.Scan(&r.ID, &r.UserID, &r.UserName, &r.PhotoKey, &r.From, &r.To, &r.Body, &r.Worked, &items, &r.Sent,
			&r.Mark, &r.MarkedBy, &r.MarkedAt, &r.CommentCount)
		if err == nil {
			_ = json.Unmarshal(items, &r.Items)
		}
		return r, err
	})
}

// LastReportTo is the end of the person's last report (nil — none).
func (s *Store) LastReportTo(ctx context.Context, userID string) (*time.Time, error) {
	var to *time.Time
	err := s.db.QueryRow(ctx, `SELECT max(date_to) FROM work_reports WHERE user_id::text = $1`, userID).Scan(&to)
	if to != nil {
		t := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.Local)
		to = &t
	}
	return to, err
}

// NextReport is the period of the person's next report by their rule.
func (s *Store) NextReport(ctx context.Context, userID string, sched worktime.Schedule, now time.Time) (ReportPeriod, error) {
	r, err := s.RuleFor(ctx, userID)
	if err != nil {
		return ReportPeriod{}, err
	}
	last, err := s.LastReportTo(ctx, userID)
	if err != nil {
		return ReportPeriod{}, err
	}
	return NextPeriod(r, sched, last, now), nil
}

// ReportDue is the period of the person's report when its day has come and it is not sent (nil — nothing due).
func (s *Store) ReportDue(ctx context.Context, userID string, sched worktime.Schedule, now time.Time) (*ReportPeriod, error) {
	r, err := s.RuleFor(ctx, userID)
	if err != nil || r.Period == "" {
		return nil, err
	}
	last, err := s.LastReportTo(ctx, userID)
	if err != nil {
		return nil, err
	}
	if p := NextPeriod(r, sched, last, now); p.Due && !p.Empty() {
		return &p, nil
	}
	return nil, nil
}

// SendReport sends the person's report for the period (it must start after their last report) to their managers.
func (s *Store) SendReport(ctx context.Context, userID string, p ReportPeriod, body string, items ReportItems, now time.Time) (int64, error) {
	body = strings.TrimSpace(body)
	if len([]rune(body)) > 10000 {
		return 0, i18n.E("err.work_report_long")
	}
	if p.Empty() || p.To.After(now) {
		return 0, i18n.E("err.work_report_period")
	}
	days, err := s.Workdays(ctx, userID, p.From, p.To.AddDate(0, 0, 1))
	if err != nil {
		return 0, err
	}
	worked := 0
	for _, d := range days {
		worked += d.Worked(now)
	}
	raw, _ := json.Marshal(items)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// one report at a time: the person's row is locked while the periods are compared
	if _, err := tx.Exec(ctx, `SELECT 1 FROM users WHERE id::text = $1 FOR UPDATE`, userID); err != nil {
		return 0, err
	}
	var overlap bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM work_reports WHERE user_id::text = $1 AND date_to >= $2)`,
		userID, p.From.Format(time.DateOnly)).Scan(&overlap); err != nil {
		return 0, err
	}
	if overlap {
		return 0, i18n.E("err.work_report_period")
	}
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO work_reports (user_id, date_from, date_to, body, worked, items, sent_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, userID, p.From.Format(time.DateOnly), p.To.Format(time.DateOnly),
		body, worked, raw, now).Scan(&id); err != nil {
		return 0, err
	}
	managers, err := s.Managers(ctx, userID)
	if err != nil {
		return 0, err
	}
	var to []string
	for _, m := range managers {
		to = append(to, m.ID)
	}
	if err := notifyReport(ctx, tx, to, userID, "work_report", periodText(p.From, p.To), id); err != nil {
		return 0, err
	}
	return id, tx.Commit(ctx)
}

func periodText(from, to time.Time) string {
	if from.Equal(to) {
		return from.Format("02.01.2006")
	}
	return from.Format("02.01.2006") + " — " + to.Format("02.01.2006")
}

func reportLink(id int64) string { return "/worktime/reports/" + strconv.FormatInt(id, 10) }

func notifyReport(ctx context.Context, tx pgx.Tx, to []string, actor, kind, body string, id int64) error {
	slices.Sort(to)
	for _, u := range slices.Compact(to) {
		if u == actor {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, $3, $4, $5)`,
			u, actor, kind, body, reportLink(id)); err != nil {
			return err
		}
	}
	return nil
}

// Report is a report with its comments, for its author, their managers and administrators.
func (s *Store) Report(ctx context.Context, viewer string, id int64, admin bool) (WorkReport, error) {
	list, err := scanReports(s.db.Query(ctx, reportSQL+` WHERE r.id = $1`, id))
	if err != nil {
		return WorkReport{}, err
	}
	if len(list) == 0 {
		return WorkReport{}, ErrNotFound
	}
	r := list[0]
	if r.UserID != viewer {
		if ok, err := s.Manages(ctx, viewer, r.UserID, admin); err != nil {
			return r, err
		} else if !ok {
			return WorkReport{}, ErrNotFound
		}
	}
	rows, err := s.db.Query(ctx, `SELECT c.id, coalesce(c.author_id::text, ''), coalesce(u.name, ''), coalesce(u.photo_key, ''), c.body, c.created_at
		FROM work_report_comments c LEFT JOIN users u ON u.id = c.author_id WHERE c.report_id = $1 ORDER BY c.id`, id)
	if err != nil {
		return r, err
	}
	r.Comments, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ReportComment])
	return r, err
}

// ReportFilter selects reports: of the people (nil — everyone), not checked yet, sent for a period touching [From, To).
type ReportFilter struct {
	People    []string
	Unchecked bool
	From, To  time.Time
}

// Reports are the reports by the filter, the newest periods first.
func (s *Store) Reports(ctx context.Context, f ReportFilter) ([]WorkReport, error) {
	where, args := []string{"true"}, []any{}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if f.People != nil {
		where = append(where, "r.user_id::text = ANY("+arg(f.People)+")")
	}
	if f.Unchecked {
		where = append(where, "r.mark = ''")
	}
	if !f.From.IsZero() {
		where = append(where, "r.date_to >= "+arg(f.From.Format(time.DateOnly)))
	}
	if !f.To.IsZero() {
		where = append(where, "r.date_from < "+arg(f.To.Format(time.DateOnly)))
	}
	return scanReports(s.db.Query(ctx, reportSQL+` WHERE `+strings.Join(where, " AND ")+` ORDER BY r.date_to DESC, u.name LIMIT 500`, args...))
}

// MarkReport: a manager of the author (or an administrator, but not the author) marks the report, with a comment.
func (s *Store) MarkReport(ctx context.Context, actor string, id int64, mark, comment string, admin bool, now time.Time) error {
	if !slices.Contains(ReportMarks, mark) {
		return i18n.E("err.bad_form")
	}
	r, err := s.Report(ctx, actor, id, admin)
	if err != nil {
		return err
	}
	if r.UserID == actor {
		return ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE work_reports SET mark = $2, marked_by = $3, marked_at = $4 WHERE id = $1`, id, mark, actor, now); err != nil {
		return err
	}
	if comment = strings.TrimSpace(comment); comment != "" {
		if err := addComment(ctx, tx, id, actor, comment, now); err != nil {
			return err
		}
	}
	if err := notifyReport(ctx, tx, []string{r.UserID}, actor, "work_report_mark", mark+"|"+periodText(r.From, r.To), id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func addComment(ctx context.Context, tx pgx.Tx, id int64, actor, body string, now time.Time) error {
	if len([]rune(body)) > 5000 {
		return i18n.E("err.work_report_comment")
	}
	_, err := tx.Exec(ctx, `INSERT INTO work_report_comments (report_id, author_id, body, created_at) VALUES ($1, $2, $3, $4)`, id, actor, body, now)
	return err
}

// CommentReport adds a comment; the author of the report, their managers and those who commented learn of it.
func (s *Store) CommentReport(ctx context.Context, actor string, id int64, body string, admin bool, now time.Time) error {
	if body = strings.TrimSpace(body); body == "" {
		return i18n.E("err.work_report_comment")
	}
	r, err := s.Report(ctx, actor, id, admin)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := addComment(ctx, tx, id, actor, body, now); err != nil {
		return err
	}
	to := []string{r.UserID}
	managers, err := s.Managers(ctx, r.UserID)
	if err != nil {
		return err
	}
	for _, m := range managers {
		to = append(to, m.ID)
	}
	for _, c := range r.Comments {
		if c.AuthorID != "" {
			to = append(to, c.AuthorID)
		}
	}
	if err := notifyReport(ctx, tx, to, actor, "work_report_comment", periodText(r.From, r.To)+" · "+r.UserName, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
