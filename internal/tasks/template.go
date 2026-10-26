package tasks

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/worktime"
)

// «Шаблоны задач» (the portal tasks.template) and regular tasks (REPLICATE). A template has the fields of a task; its
// deadline is an offset from the moment a task is made. A regular template makes tasks itself, on its schedule,
// as the person who set it up. A template may have subtask templates («Подзадачи шаблона»): a task made from it gets
// a subtask from each of them. A template «for new users» sets its task to each employee who comes to go24 after
// it got the mark (RunNewUsers).

type Template struct {
	ID, CreatorID, CreatorName         string
	ProjectID, ProjectKey, ProjectName string
	Title, Description                 string
	ResponsibleID, ResponsibleName     string
	Priority                           Priority
	DeadlineAfter                      *int // minutes after creation (nil = no deadline)
	RequiresControl                    bool
	AllowChangeDeadline, MatchWorkTime bool
	Accomplices, Auditors, Checklist   []string
	Repeat                             Repeat
	ForNewUsers                        bool // «Шаблон для новых сотрудников»: never repeats, its responsible is each new employee
	CreatedCount                       int
	NextRun, LastRun                   *time.Time
	CreatedAt                          time.Time
	ParentID, ParentTitle              string     // a subtask template: its base template
	SubCount                           int        // subtask templates right under this one
	Subtasks                           []Template // Template only: the subtask templates, in the order they were made
	Shared                             string     // shared with the viewer (not theirs): "read" — «Чтение», "full" — «Полный доступ»
	Params
}

// CanEdit: the viewer may change the template (theirs, an administrator's or with «Полный доступ»).
func (t Template) CanEdit() bool { return t.Shared != "read" }

// Repeat is the schedule of a regular template.
type Repeat struct {
	On           bool
	Period       string // daily | weekly | monthly | yearly
	Every        int    // every N days / weeks / months / years
	WorkdaysOnly bool   // daily: working days only (weekends and holidays of the company schedule are skipped)
	WeekDays     []int  // weekly: 1 = Monday … 7 = Sunday
	MonthDay     int    // monthly, yearly: day of the month (past the month's end = its last day)
	YearMonth    int    // yearly: 1…12
	At           string // HH:MM
	StartsOn     time.Time
	EndsOn       *time.Time // nil = no end date
	Times        *int       // nil = no limit
}

var periods = []string{"daily", "weekly", "monthly", "yearly"}

func (r Repeat) validate() error {
	if !slices.Contains(periods, r.Period) {
		return invalid("err.repeat_period")
	}
	if r.Every < 1 || r.Every > 365 {
		return invalid("err.repeat_every")
	}
	if r.Period == "weekly" && len(r.WeekDays) == 0 {
		return invalid("err.repeat_weekdays")
	}
	for _, d := range r.WeekDays {
		if d < 1 || d > 7 {
			return invalid("err.repeat_weekdays")
		}
	}
	if r.MonthDay < 1 || r.MonthDay > 31 || r.YearMonth < 1 || r.YearMonth > 12 {
		return invalid("err.repeat_day")
	}
	if _, _, err := hhmm(r.At); err != nil {
		return invalid("err.repeat_time")
	}
	if r.EndsOn != nil && r.EndsOn.Before(r.StartsOn) {
		return invalid("err.repeat_end")
	}
	if r.Times != nil && *r.Times < 1 {
		return invalid("err.repeat_times")
	}
	return nil
}

func hhmm(s string) (int, int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, 0, err
	}
	return t.Hour(), t.Minute(), nil
}

// day is the calendar date of t as a UTC midnight (day arithmetic without daylight saving surprises).
func day(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func lastDay(y int, m time.Month) int { return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day() }

func (r Repeat) matches(d time.Time, sched worktime.Schedule) bool {
	start := day(r.StartsOn)
	switch r.Period {
	case "daily":
		n := int(d.Sub(start).Hours() / 24)
		if n%r.Every != 0 {
			return false
		}
		return !r.WorkdaysOnly || sched.WorkingDay(time.Date(d.Year(), d.Month(), d.Day(), 12, 0, 0, 0, time.Local))
	case "weekly":
		wd := int(d.Weekday())
		if wd == 0 {
			wd = 7
		}
		if !slices.Contains(r.WeekDays, wd) {
			return false
		}
		monday := func(t time.Time) time.Time {
			w := int(t.Weekday())
			if w == 0 {
				w = 7
			}
			return t.AddDate(0, 0, 1-w)
		}
		weeks := int(monday(d).Sub(monday(start)).Hours() / 24 / 7)
		return weeks%r.Every == 0
	case "monthly":
		months := (d.Year()-start.Year())*12 + int(d.Month()-start.Month())
		return months%r.Every == 0 && d.Day() == min(r.MonthDay, lastDay(d.Year(), d.Month()))
	case "yearly":
		return int(d.Month()) == r.YearMonth && d.Day() == min(r.MonthDay, lastDay(d.Year(), d.Month())) &&
			(d.Year()-start.Year())%r.Every == 0
	}
	return false
}

// Next is the first run after the given moment (nil: the schedule is over or off). made is how many tasks the
// template has made so far.
func (r Repeat) Next(after time.Time, made int, sched worktime.Schedule) *time.Time {
	if !r.On || (r.Times != nil && made >= *r.Times) {
		return nil
	}
	h, m, err := hhmm(r.At)
	if err != nil {
		return nil
	}
	d := day(after.In(time.Local))
	if s := day(r.StartsOn); d.Before(s) {
		d = s
	}
	limit := 400 * r.Every
	if r.Period == "yearly" {
		limit = 370 * r.Every
	}
	for i := 0; i <= limit; i++ {
		c := d.AddDate(0, 0, i)
		if r.EndsOn != nil && c.After(day(*r.EndsOn)) {
			return nil
		}
		if !r.matches(c, sched) {
			continue
		}
		at := time.Date(c.Year(), c.Month(), c.Day(), h, m, 0, 0, time.Local)
		if at.After(after) {
			return &at
		}
	}
	return nil
}

// AfterDays and AfterHours tell the deadline offset as the form shows it.
func (t Template) AfterDays() int {
	if t.DeadlineAfter == nil {
		return 0
	}
	return *t.DeadlineAfter / (24 * 60)
}

func (t Template) AfterHours() int {
	if t.DeadlineAfter == nil {
		return 0
	}
	return *t.DeadlineAfter % (24 * 60) / 60
}

// NewTask is what a task made from the template now looks like.
func (t Template) NewTask(now time.Time) NewTask {
	in := NewTask{Title: t.Title, Description: t.Description, ResponsibleID: t.ResponsibleID, Priority: t.Priority,
		RequiresControl: t.RequiresControl, AllowChangeDeadline: t.AllowChangeDeadline, MatchWorkTime: t.MatchWorkTime, Params: t.Params,
		Accomplices: t.Accomplices, Auditors: t.Auditors, Checklist: t.Checklist, TemplateID: t.ID}
	if t.DeadlineAfter != nil {
		d := now.Add(time.Duration(*t.DeadlineAfter) * time.Minute)
		in.Deadline = &d
	}
	return in
}

const templateSelect = `SELECT t.id, t.creator_id, cu.name, t.project_id, p.key, p.name, t.title, t.description,
		t.responsible_id, ru.name, t.priority, t.deadline_after, t.requires_control, t.allow_change_deadline, t.match_work_time,
		t.dates_from_subtasks, t.auto_complete, t.result_required, t.accomplices::text[], t.auditors::text[], t.checklist,
		t.replicate, t.period, t.every, t.workdays_only, t.week_days::int[], t.month_day, t.year_month, t.at_time,
		t.starts_on, t.ends_on, t.times, t.created_count, t.next_run, t.last_run, t.created_at,
		coalesce(t.parent_id::text, ''), coalesce(pt.title, ''), (SELECT count(*) FROM task_templates c WHERE c.parent_id = t.id),
		t.for_new_users
	FROM task_templates t JOIN users cu ON cu.id = t.creator_id JOIN users ru ON ru.id = t.responsible_id
	JOIN projects p ON p.id = t.project_id LEFT JOIN task_templates pt ON pt.id = t.parent_id`

func scanTemplate(row pgx.Row) (Template, error) {
	var t Template
	var r = &t.Repeat
	err := row.Scan(&t.ID, &t.CreatorID, &t.CreatorName, &t.ProjectID, &t.ProjectKey, &t.ProjectName, &t.Title, &t.Description,
		&t.ResponsibleID, &t.ResponsibleName, &t.Priority, &t.DeadlineAfter, &t.RequiresControl, &t.AllowChangeDeadline, &t.MatchWorkTime,
		&t.DatesFromSubtasks, &t.AutoComplete, &t.ResultRequired, &t.Accomplices, &t.Auditors, &t.Checklist,
		&r.On, &r.Period, &r.Every, &r.WorkdaysOnly, &r.WeekDays, &r.MonthDay, &r.YearMonth, &r.At,
		&r.StartsOn, &r.EndsOn, &r.Times, &t.CreatedCount, &t.NextRun, &t.LastRun, &t.CreatedAt,
		&t.ParentID, &t.ParentTitle, &t.SubCount, &t.ForNewUsers)
	return t, err
}

// Templates lists the user's templates and the ones shared with them (every template for an administrator in admin
// mode); subtask templates are seen in their base template.
func (s *Store) Templates(ctx context.Context, userID string) ([]Template, error) {
	shared, err := s.sharedTemplates(ctx, userID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for id := range shared {
		ids = append(ids, id)
	}
	rows, err := s.db.Query(ctx, templateSelect+` WHERE (t.creator_id = $1 OR $2 OR t.id::text = ANY($3)) AND t.parent_id IS NULL
		ORDER BY t.replicate DESC, lower(t.title)`, userID, IsAdmin(ctx), ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Template, error) { return scanTemplate(r) })
	for i, t := range list {
		if full, ok := shared[t.ID]; ok && t.CreatorID != userID && !IsAdmin(ctx) {
			list[i].Shared = map[bool]string{false: "read", true: "full"}[full]
		}
	}
	return list, err
}

// Template loads a template of the user or one shared with them (a subtask template — by its base's rights).
func (s *Store) Template(ctx context.Context, userID, id string) (Template, error) {
	t, err := scanTemplate(s.db.QueryRow(ctx, templateSelect+` WHERE t.id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, ErrNotFound // not a UUID
	}
	if t.CreatorID != userID && !IsAdmin(ctx) {
		base, err := s.baseTemplate(ctx, t.ID)
		if err != nil {
			return t, err
		}
		shared, err := s.sharedTemplates(ctx, userID)
		if err != nil {
			return t, err
		}
		full, ok := shared[base]
		if !ok {
			return t, ErrForbidden
		}
		t.Shared = map[bool]string{false: "read", true: "full"}[full]
	}
	t.Subtasks, err = s.subTemplates(ctx, t.ID)
	return t, err
}

func (s *Store) subTemplates(ctx context.Context, id string) ([]Template, error) {
	rows, err := s.db.Query(ctx, templateSelect+` WHERE t.parent_id = $1 ORDER BY t.created_at, t.id`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Template, error) { return scanTemplate(r) })
}

// SaveTemplate creates (empty ID) or changes a template and returns its ID. The project must be one where the
// user may set tasks; a regular template gets its next run. A new template with ParentID is a subtask template of
// that one (its author's, also when made by one with «Полный доступ»); a subtask template keeps its base and never
// repeats on its own.
func (s *Store) SaveTemplate(ctx context.Context, userID string, t Template) (string, error) {
	t.Title = strings.TrimSpace(t.Title)
	if t.Title == "" {
		return "", invalid("err.task_title_required")
	}
	if t.ParentID != "" && t.ID == "" {
		if _, err := s.templateToEdit(ctx, userID, t.ParentID); err != nil {
			return "", err
		}
	}
	p, err := s.ProjectByKey(ctx, userID, t.ProjectKey)
	if err != nil {
		return "", err
	}
	if !p.CanWork() {
		return "", ErrForbidden
	}
	if t.ResponsibleID == "" {
		t.ResponsibleID = userID
	}
	if t.DeadlineAfter != nil && *t.DeadlineAfter <= 0 {
		t.DeadlineAfter = nil
	}
	r := t.Repeat
	if r.Every == 0 {
		r.Every = 1
	}
	if r.MonthDay == 0 {
		r.MonthDay = 1
	}
	if r.YearMonth == 0 {
		r.YearMonth = 1
	}
	if r.At == "" {
		r.At = "09:00"
	}
	if r.StartsOn.IsZero() {
		r.StartsOn = day(time.Now())
	}
	if r.Period == "" {
		r.Period = "weekly"
	}
	if r.WeekDays == nil {
		r.WeekDays = []int{}
	}
	clean := func(l []string) []string {
		out := []string{}
		for _, v := range l {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	t.Accomplices, t.Auditors, t.Checklist = clean(t.Accomplices), clean(t.Auditors), clean(t.Checklist)
	sched, err := s.Schedule(ctx)
	if err != nil {
		return "", err
	}
	made := 0
	if t.ID != "" {
		cur, err := s.templateToEdit(ctx, userID, t.ID)
		if err != nil {
			return "", err
		}
		made = cur.CreatedCount
		t.ParentID = cur.ParentID
	}
	if t.ParentID != "" {
		r.On, t.ForNewUsers = false, false
	}
	if t.ForNewUsers {
		r.On = false
	}
	if r.On {
		if err := r.validate(); err != nil {
			return "", err
		}
	}
	next := r.Next(time.Now(), made, sched)
	args := []any{userID, p.ID, t.Title, strings.TrimSpace(t.Description), t.ResponsibleID, t.Priority, t.DeadlineAfter,
		t.RequiresControl, t.AllowChangeDeadline, t.MatchWorkTime, t.Accomplices, t.Auditors, t.Checklist,
		r.On, r.Period, r.Every, r.WorkdaysOnly, r.WeekDays, r.MonthDay, r.YearMonth, r.At, r.StartsOn, r.EndsOn, r.Times, next,
		t.DatesFromSubtasks, t.AutoComplete, t.ResultRequired, t.ForNewUsers}
	if t.ID == "" {
		var id string
		var parent *string
		if t.ParentID != "" {
			parent = &t.ParentID
		}
		err := s.db.QueryRow(ctx, `INSERT INTO task_templates (creator_id, project_id, title, description, responsible_id, priority,
			deadline_after, requires_control, allow_change_deadline, match_work_time, accomplices, auditors, checklist,
			replicate, period, every, workdays_only, week_days, month_day, year_month, at_time, starts_on, ends_on, times, next_run,
			dates_from_subtasks, auto_complete, result_required, for_new_users, new_users_since, parent_id)
			VALUES (coalesce((SELECT creator_id FROM task_templates WHERE id = $30), $1), $2, $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid[], $12::uuid[], $13, $14, $15, $16, $17, $18::smallint[], $19, $20, $21, $22, $23, $24, $25,
			$26, $27, $28, $29, CASE WHEN $29 THEN now() END, $30)
			RETURNING id`, append(args, parent)...).Scan(&id)
		return id, err
	}
	_, err = s.db.Exec(ctx, `UPDATE task_templates SET project_id = $2, title = $3, description = $4, responsible_id = $5,
		priority = $6, deadline_after = $7, requires_control = $8, allow_change_deadline = $9, match_work_time = $10,
		accomplices = $11::uuid[], auditors = $12::uuid[], checklist = $13, replicate = $14, period = $15, every = $16,
		workdays_only = $17, week_days = $18::smallint[], month_day = $19, year_month = $20, at_time = $21, starts_on = $22,
		ends_on = $23, times = $24, next_run = $25, dates_from_subtasks = $26, auto_complete = $27, result_required = $28,
		-- employees who came before the mark was put on do not get the task
		new_users_since = CASE WHEN NOT $29 THEN NULL WHEN for_new_users THEN new_users_since ELSE now() END, for_new_users = $29,
		updated_at = now() WHERE id = $1`, append([]any{t.ID}, args[1:]...)...)
	return t.ID, err
}

// DeleteTemplate removes a template with its subtask templates; tasks made from them stay.
func (s *Store) DeleteTemplate(ctx context.Context, userID, id string) error {
	if _, err := s.templateToEdit(ctx, userID, id); err != nil {
		return err
	}
	_, err := s.db.Exec(ctx, `DELETE FROM task_templates WHERE id = $1`, id)
	return err
}

// PauseTemplate stops (on = false) or resumes a regular template.
func (s *Store) PauseTemplate(ctx context.Context, userID, id string, on bool) error {
	t, err := s.templateToEdit(ctx, userID, id)
	if err != nil {
		return err
	}
	sched, err := s.Schedule(ctx)
	if err != nil {
		return err
	}
	if t.ForNewUsers || t.ParentID != "" {
		return invalid("err.bad_form")
	}
	t.Repeat.On = on
	_, err = s.db.Exec(ctx, `UPDATE task_templates SET replicate = $2, next_run = $3, updated_at = now() WHERE id = $1`,
		id, on, t.Repeat.Next(time.Now(), t.CreatedCount, sched))
	return err
}

// CreateFromTemplate makes one task from the template now, with the subtasks of its subtask templates, as the user
// (who must be able to set tasks in its project). A subtask that cannot be made is an error after the task is
// made: the key comes back too.
func (s *Store) CreateFromTemplate(ctx context.Context, userID string, t Template, now time.Time) (string, error) {
	p, err := s.ProjectByKey(ctx, userID, t.ProjectKey)
	if err != nil {
		return "", err
	}
	key, err := s.CreateTask(ctx, userID, p, t.NewTask(now))
	if err != nil {
		return "", err
	}
	return key, s.CreateTemplateSubtasks(ctx, userID, t.ID, key, now)
}

// CreateTemplateSubtasks makes the subtasks of the template's subtask templates (and theirs, deeper) under the task.
// The ones that can be made are made; the first error is returned.
func (s *Store) CreateTemplateSubtasks(ctx context.Context, userID, templateID, key string, now time.Time) error {
	var first error
	var walk func(id, parent string, depth int)
	walk = func(id, parent string, depth int) {
		subs, err := s.subTemplates(ctx, id)
		if err != nil || depth > 10 {
			first = cmp.Or(first, err)
			return
		}
		for _, sub := range subs {
			p, err := s.ProjectByKey(ctx, userID, sub.ProjectKey)
			if err != nil {
				first = cmp.Or(first, err)
				continue
			}
			in := sub.NewTask(now)
			in.ParentKey = parent
			k, err := s.CreateTask(ctx, userID, p, in)
			if err != nil {
				first = cmp.Or(first, err)
				continue
			}
			walk(sub.ID, k, depth+1)
		}
	}
	walk(templateID, key, 0)
	return first
}

// RunDue makes the tasks of regular templates whose time has come. A run missed while go24 was down makes one task,
// not a backlog of them. A template that cannot make its task (its author lost access, someone left) moves on to its
// next run; the error is reported.
func (s *Store) RunDue(ctx context.Context, now time.Time) (made []string, errs []error) {
	sched, err := s.Schedule(ctx)
	if err != nil {
		return nil, []error{err}
	}
	for range 1000 {
		var done bool
		err := s.inTx(ctx, func(tx pgx.Tx) error {
			// NO KEY UPDATE: the new task's link to the template (a foreign key) must not wait for this lock
			var id string
			err := tx.QueryRow(ctx, `SELECT id FROM task_templates WHERE replicate AND next_run <= $1
				ORDER BY next_run LIMIT 1 FOR NO KEY UPDATE SKIP LOCKED`, now).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			t, err := scanTemplate(tx.QueryRow(ctx, templateSelect+` WHERE t.id = $1`, id))
			if err != nil {
				return err
			}
			key, cerr := s.CreateFromTemplate(ctx, t.CreatorID, t, now)
			if key != "" {
				t.CreatedCount++
				made = append(made, key)
			}
			if cerr != nil {
				errs = append(errs, fmt.Errorf("template %s %q: %w", t.ID, t.Title, cerr))
			}
			_, err = tx.Exec(ctx, `UPDATE task_templates SET created_count = $2, last_run = $3, next_run = $4 WHERE id = $1`,
				id, t.CreatedCount, now, t.Repeat.Next(now, t.CreatedCount, sched))
			return err
		})
		if err != nil {
			return made, append(errs, err)
		}
		if done {
			break
		}
	}
	return made, errs
}

// RunNewUsers sets the tasks of templates «for new users»: each employee (not a guest, not dismissed, signed in at
// least once) who came to go24 after the template got the mark gets its task once, with its subtasks, as the
// template's author set it. A task that cannot be made (the author may not set tasks to the employee) is not tried
// again; the error is reported.
func (s *Store) RunNewUsers(ctx context.Context, now time.Time) (made []string, errs []error) {
	for range 1000 {
		var done bool
		err := s.inTx(ctx, func(tx pgx.Tx) error {
			var id, userID string
			err := tx.QueryRow(ctx, `SELECT t.id, u.id FROM task_templates t JOIN users u ON u.created_at >= t.new_users_since
				WHERE t.for_new_users AND u.active AND NOT u.guest AND u.oidc_sub NOT LIKE 'legacy:%'
				  AND NOT EXISTS (SELECT 1 FROM task_template_hires h WHERE h.template_id = t.id AND h.user_id = u.id)
				ORDER BY u.created_at, t.id LIMIT 1 FOR NO KEY UPDATE OF t SKIP LOCKED`).Scan(&id, &userID)
			if errors.Is(err, pgx.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO task_template_hires (template_id, user_id) VALUES ($1, $2)`, id, userID); err != nil {
				return err
			}
			t, err := scanTemplate(tx.QueryRow(ctx, templateSelect+` WHERE t.id = $1`, id))
			if err != nil {
				return err
			}
			t.ResponsibleID = userID
			key, cerr := s.CreateFromTemplate(ctx, t.CreatorID, t, now)
			if cerr != nil {
				errs = append(errs, fmt.Errorf("template %s %q for user %s: %w", t.ID, t.Title, userID, cerr))
			}
			if key == "" {
				return nil
			}
			made = append(made, key)
			taskID, err := s.TaskIDByKey(ctx, key)
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE task_template_hires SET task_id = $3 WHERE template_id = $1 AND user_id = $2`, id, userID, taskID)
			}
			if err == nil {
				_, err = tx.Exec(ctx, `UPDATE task_templates SET created_count = created_count + 1, last_run = $2 WHERE id = $1`, id, now)
			}
			return err
		})
		if err != nil {
			return made, append(errs, err)
		}
		if done {
			break
		}
	}
	return made, errs
}
