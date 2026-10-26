package company

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
	"go24/internal/worktime"
)

// «Таблица рабочего времени»: everyone's days of a month with the violations, and changes of a day
// (start, end, pauses) with the reason — an employee's change waits for the manager's approval.

// Violations of the day by the company's working time: late, early (left before the time), short (worked less than
// the minimum), open (a past day not finished); see Norm.Violations.
func Violations(d Workday, s worktime.Schedule, now time.Time) []string {
	return Norm{Company: s}.Violations(d, now)
}

// SheetCell is one person's day in the table.
type SheetCell struct {
	Day     time.Time
	Kind    string // work | off (weekend, holiday) | absence | none (a working day without a working day) | future
	Absence string // the kind of the absence
	Worked  int    // seconds; an open past day counts nothing
	Flags   []string
	Pending bool // a change of the day waits for approval
	Open    bool // the day is going on (today)
	Plan    DayPlan
}

// SheetRow is a person's month.
type SheetRow struct {
	Person
	Cells      []SheetCell
	Days       int // days worked
	Worked     int // seconds
	Violations int // days with violations
}

// BuildSheet lays out the month of the people: their days, absences and pending changes ("user|2006-01-02"), by
// their rules (norms; a person without — by the company's working time s). A day off of the schedule is "off", a
// day of a flexible schedule without work — "free".
func BuildSheet(people []Person, days []Workday, absences []Absence, pending map[string]bool, norms map[string]Norm,
	s worktime.Schedule, month, now time.Time) []SheetRow {
	byKey := map[string]Workday{}
	for _, d := range days {
		byKey[d.UserID+"|"+d.Day.Format(time.DateOnly)] = d
	}
	today := now.Format(time.DateOnly)
	end := month.AddDate(0, 1, 0)
	rows := make([]SheetRow, 0, len(people))
	for _, p := range people {
		row := SheetRow{Person: p}
		n, ok := norms[p.ID]
		if !ok {
			n = Norm{Company: s}
		}
		for t := month; t.Before(end); t = t.AddDate(0, 0, 1) {
			key := t.Format(time.DateOnly)
			c := SheetCell{Day: t, Pending: pending[p.ID+"|"+key], Plan: n.Plan(t)}
			for _, a := range absences {
				if a.UserID == p.ID && key >= a.From.Format(time.DateOnly) && key <= a.To.Format(time.DateOnly) {
					c.Kind, c.Absence = "absence", a.Kind
				}
			}
			if d, ok := byKey[p.ID+"|"+key]; ok {
				c.Kind, c.Flags = "work", n.Violations(d, now)
				c.Open = d.Ended == nil && key == today
				if d.Ended != nil || c.Open {
					c.Worked = d.Worked(now)
				}
				row.Days++
				row.Worked += c.Worked
				if len(c.Flags) > 0 {
					row.Violations++
				}
			} else if c.Kind == "" {
				switch {
				case c.Plan.Free:
					c.Kind = "free"
				case !c.Plan.Work:
					c.Kind = "off"
				case key > today:
					c.Kind = "future"
				default:
					c.Kind = "none"
				}
			}
			row.Cells = append(row.Cells, c)
		}
		rows = append(rows, row)
	}
	return rows
}

// SheetDays are the days of the people in [from, to).
func (s *Store) SheetDays(ctx context.Context, people []string, from, to time.Time) ([]Workday, error) {
	if len(people) == 0 {
		return nil, nil
	}
	return scanWorkdays(s.db.Query(ctx, workdaySQL+` WHERE w.user_id::text = ANY($1) AND w.day >= $2 AND w.day < $3`,
		people, from.Format(time.DateOnly), to.Format(time.DateOnly)))
}

// Day is one working day of the person (nil: there is none).
func (s *Store) Day(ctx context.Context, userID string, day time.Time) (*Workday, error) {
	list, err := scanWorkdays(s.db.Query(ctx, workdaySQL+` WHERE w.user_id = $1 AND w.day = $2`, userID, day.Format(time.DateOnly)))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

// Under are the people a manager stands above: employees of the departments they head or are a deputy of, with
// all departments below, and the heads of those departments.
func (s *Store) Under(ctx context.Context, userID string) ([]Person, error) {
	rows, err := s.db.Query(ctx, `WITH RECURSIVE down AS (
			SELECT id FROM departments WHERE head_id = $1
			UNION SELECT department_id FROM department_deputies WHERE user_id = $1
			UNION SELECT d.id FROM departments d JOIN down ON d.parent_id = down.id)
		SELECT u.id, u.name, u.position, u.photo_key FROM users u WHERE u.id <> $1 AND u.active AND NOT u.guest AND (
			EXISTS (SELECT 1 FROM user_departments ud JOIN down ON down.id = ud.department_id WHERE ud.user_id = u.id)
			OR EXISTS (SELECT 1 FROM departments d JOIN down ON down.id = d.id WHERE d.head_id = u.id))
		ORDER BY u.name`, userID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
}

// Manages: the actor is an administrator or stands above the person (the person themself is not).
func (s *Store) Manages(ctx context.Context, actor, userID string, admin bool) (bool, error) {
	if admin {
		return true, nil
	}
	if actor == userID {
		return false, nil
	}
	return s.canManage(ctx, actor, userID, false)
}

// DayChange is a change of a working day; nil fields are not changed.
type DayChange struct {
	Started, Ended *time.Time
	PauseSecs      *int
	Reason         string
}

// WorkdayChange is a change in the history of a day, or one waiting for approval.
type WorkdayChange struct {
	ID                   int64
	UserID, UserName     string
	Day                  time.Time
	AuthorID, AuthorName string
	Started, Ended       *time.Time
	PauseSecs            *int
	OldStarted, OldEnded *time.Time
	OldPause             *int
	Reason, Status       string // pending | approved | declined
	DeciderName          string
	DecidedAt            *time.Time
	CreatedAt            time.Time
}

// apply returns the day with the change, checking it: a new day needs its start and end, the start is on the
// day, the end after the start (within a day), pauses shorter than the day, nothing in the future.
func (c DayChange) apply(d *Workday, userID string, day, now time.Time) (Workday, error) {
	var out Workday
	if d != nil {
		out = *d
	} else {
		if c.Started == nil || c.Ended == nil {
			return out, i18n.E("err.workday_new_day")
		}
		out = Workday{UserID: userID, Day: day}
	}
	if c.Started != nil {
		out.Started = *c.Started
	}
	if c.Ended != nil {
		e := *c.Ended
		if out.PausedAt != nil && c.PauseSecs == nil && e.After(*out.PausedAt) { // a pause going on ends with the day
			out.PauseSecs += int(e.Sub(*out.PausedAt).Seconds())
		}
		out.Ended, out.PausedAt = &e, nil
	}
	if c.PauseSecs != nil {
		out.PauseSecs = *c.PauseSecs
	}
	switch {
	case out.Started.Local().Format(time.DateOnly) != day.Format(time.DateOnly), out.Started.After(now):
		return out, i18n.E("err.workday_start_day")
	case out.Ended != nil && (!out.Ended.After(out.Started) || out.Ended.Sub(out.Started) > 24*time.Hour || out.Ended.After(now)):
		return out, i18n.E("err.workday_end")
	case out.PauseSecs < 0 || out.Ended != nil && out.PauseSecs >= int(out.Ended.Sub(out.Started).Seconds()):
		return out, i18n.E("err.workday_pause")
	}
	return out, nil
}

// ChangeDay changes a person's day. The person's own change waits for a manager's approval (applied false) unless
// the schedule lets employees change their days freely or they are an administrator; a manager's change is applied
// at once. A new pending change replaces the previous one of the day. Managers of the person are notified of a
// change to approve, the person — of a change made by somebody else.
func (s *Store) ChangeDay(ctx context.Context, actor, userID string, day time.Time, c DayChange, admin, free bool,
	now time.Time) (applied bool, err error) {
	c.Reason = strings.TrimSpace(c.Reason)
	if c.Reason == "" || len([]rune(c.Reason)) > 1000 {
		return false, i18n.E("err.workday_reason")
	}
	if c.Started == nil && c.Ended == nil && c.PauseSecs == nil {
		return false, i18n.E("err.bad_form")
	}
	manager, err := s.Manages(ctx, actor, userID, admin)
	if err != nil {
		return false, err
	}
	if actor != userID && !manager {
		return false, i18n.E("err.forbidden")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	old, err := s.lockDay(ctx, tx, userID, day)
	if err != nil {
		return false, err
	}
	next, err := c.apply(old, userID, day, now)
	if err != nil {
		return false, err
	}
	applied = manager || free
	var oldStart, oldEnd *time.Time
	var oldPause *int
	if old != nil {
		oldStart, oldEnd, oldPause = &old.Started, old.Ended, &old.PauseSecs
	}
	status := "pending"
	if applied {
		status = "approved"
		if err := writeDay(ctx, tx, next); err != nil {
			return false, err
		}
	} else if _, err := tx.Exec(ctx, `DELETE FROM workday_changes WHERE user_id = $1 AND day = $2 AND status = 'pending'`,
		userID, day.Format(time.DateOnly)); err != nil {
		return false, err
	}
	var decider any
	if applied {
		decider = actor
	}
	if _, err := tx.Exec(ctx, `INSERT INTO workday_changes (user_id, day, author_id, started_at, ended_at, pause_secs,
			old_started, old_ended, old_pause, reason, status, decided_by, decided_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, CASE WHEN $12::uuid IS NULL THEN NULL ELSE $13::timestamptz END)`,
		userID, day.Format(time.DateOnly), actor, c.Started, c.Ended, c.PauseSecs, oldStart, oldEnd, oldPause, c.Reason, status,
		decider, now); err != nil {
		return false, err
	}
	link := dayLink(userID, day)
	switch {
	case !applied:
		managers, err := s.Managers(ctx, userID)
		if err != nil {
			return false, err
		}
		for _, m := range managers {
			if _, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'workday_change', $3, $4)`,
				m.ID, actor, day.Format("02.01.2006"), link); err != nil {
				return false, err
			}
		}
	case actor != userID:
		if err := notifyDecided(ctx, tx, userID, actor, "changed", day, link); err != nil {
			return false, err
		}
	}
	return applied, tx.Commit(ctx)
}

// DecideChange approves (applies) or declines a pending change; a manager of the person or an administrator decides.
func (s *Store) DecideChange(ctx context.Context, actor string, id int64, approve, admin bool, now time.Time) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID string
	var day time.Time
	var c DayChange
	err = tx.QueryRow(ctx, `SELECT user_id::text, day, started_at, ended_at, pause_secs, reason FROM workday_changes
		WHERE id = $1 AND status = 'pending' FOR UPDATE`, id).Scan(&userID, &day, &c.Started, &c.Ended, &c.PauseSecs, &c.Reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return i18n.E("err.workday_change_gone")
	}
	if err != nil {
		return err
	}
	day = time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local)
	if ok, err := s.Manages(ctx, actor, userID, admin); err != nil {
		return err
	} else if !ok {
		return i18n.E("err.forbidden")
	}
	status := "declined"
	if approve {
		status = "approved"
		old, err := s.lockDay(ctx, tx, userID, day)
		if err != nil {
			return err
		}
		next, err := c.apply(old, userID, day, now)
		if err != nil {
			return err
		}
		if err := writeDay(ctx, tx, next); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE workday_changes SET status = $2, decided_by = $3, decided_at = $4 WHERE id = $1`,
		id, status, actor, now); err != nil {
		return err
	}
	if err := notifyDecided(ctx, tx, userID, actor, status, day, dayLink(userID, day)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func dayLink(userID string, day time.Time) string {
	return "/worktime/d/" + userID + "/" + day.Format(time.DateOnly)
}

// notifyDecided tells the person their day was changed, or their change approved or declined: body "status|dd.mm.yyyy".
func notifyDecided(ctx context.Context, tx pgx.Tx, userID, actor, status string, day time.Time, link string) error {
	_, err := tx.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body, url) VALUES ($1, $2, 'workday_decided', $3, $4)`,
		userID, actor, status+"|"+day.Format("02.01.2006"), link)
	return err
}

func (s *Store) lockDay(ctx context.Context, tx pgx.Tx, userID string, day time.Time) (*Workday, error) {
	list, err := scanWorkdays(tx.Query(ctx, workdaySQL+` WHERE w.user_id = $1 AND w.day = $2 FOR UPDATE OF w`, userID, day.Format(time.DateOnly)))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}

func writeDay(ctx context.Context, tx pgx.Tx, d Workday) error {
	_, err := tx.Exec(ctx, `INSERT INTO workdays (user_id, day, started_at, ended_at, paused_at, pause_secs) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, day) DO UPDATE SET started_at = $3, ended_at = $4, paused_at = $5, pause_secs = $6`,
		d.UserID, d.Day.Format(time.DateOnly), d.Started, d.Ended, d.PausedAt, d.PauseSecs)
	return err
}

const changeSQL = `SELECT c.id, c.user_id::text, u.name, c.day, coalesce(c.author_id::text, ''), coalesce(a.name, ''),
		c.started_at, c.ended_at, c.pause_secs, c.old_started, c.old_ended, c.old_pause, c.reason, c.status,
		coalesce(d.name, ''), c.decided_at, c.created_at
	FROM workday_changes c JOIN users u ON u.id = c.user_id LEFT JOIN users a ON a.id = c.author_id LEFT JOIN users d ON d.id = c.decided_by`

func scanChanges(rows pgx.Rows, err error) ([]WorkdayChange, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (WorkdayChange, error) {
		var c WorkdayChange
		err := r.Scan(&c.ID, &c.UserID, &c.UserName, &c.Day, &c.AuthorID, &c.AuthorName, &c.Started, &c.Ended, &c.PauseSecs,
			&c.OldStarted, &c.OldEnded, &c.OldPause, &c.Reason, &c.Status, &c.DeciderName, &c.DecidedAt, &c.CreatedAt)
		return c, err
	})
}

// DayChanges are the changes of the person's day, newest first.
func (s *Store) DayChanges(ctx context.Context, userID string, day time.Time) ([]WorkdayChange, error) {
	return scanChanges(s.db.Query(ctx, changeSQL+` WHERE c.user_id = $1 AND c.day = $2 ORDER BY c.id DESC`, userID, day.Format(time.DateOnly)))
}

// PendingChanges are the changes of the people waiting for approval, oldest first (people nil: everyone's).
func (s *Store) PendingChanges(ctx context.Context, people []string, all bool) ([]WorkdayChange, error) {
	if !all && len(people) == 0 {
		return nil, nil
	}
	return scanChanges(s.db.Query(ctx, changeSQL+` WHERE c.status = 'pending' AND ($1 OR c.user_id::text = ANY($2)) ORDER BY c.id`, all, people))
}

// PendingDays are the "user|2006-01-02" of the people's days with a change waiting, in [from, to).
func (s *Store) PendingDays(ctx context.Context, people []string, from, to time.Time) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT user_id::text || '|' || to_char(day, 'YYYY-MM-DD') FROM workday_changes
		WHERE status = 'pending' AND user_id::text = ANY($1) AND day >= $2 AND day < $3`, people, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	out := map[string]bool{}
	for _, k := range keys {
		out[k] = true
	}
	return out, err
}

// ForgottenDay is the person's last past day left unfinished, without a change waiting for it (nil: none), like
// «Вы не завершили рабочий день».
func (s *Store) ForgottenDay(ctx context.Context, userID string, now time.Time) (*Workday, error) {
	list, err := scanWorkdays(s.db.Query(ctx, workdaySQL+` WHERE w.user_id = $1 AND w.day < $2 AND w.ended_at IS NULL
		AND w.day > $2::date - 31 AND NOT EXISTS (SELECT 1 FROM workday_changes c WHERE c.user_id = w.user_id AND c.day = w.day AND c.status = 'pending')
		ORDER BY w.day DESC LIMIT 1`, userID, now.Format(time.DateOnly)))
	if err != nil || len(list) == 0 {
		return nil, err
	}
	return &list[0], nil
}
