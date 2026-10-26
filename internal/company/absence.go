package company

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// «График отсутствий» and birthdays of the «Сотрудники».

var AbsenceKinds = []string{"vacation", "sick", "trip", "dayoff", "other"}

type Absence struct {
	ID, UserID, UserName, PhotoKey string
	Kind                           string
	From, To                       time.Time
	Comment                        string
	ActingID, ActingName           string   // «исполняющий обязанности»
	Rights                         []string // view | work | control | manage | notify
}

// ActingRights are the powers a person acting for someone may be given.
var ActingRights = []string{"view", "work", "control", "manage", "notify"}

const absenceSelect = `SELECT a.id::text, a.user_id::text, u.name, u.photo_key, a.kind, a.starts_on, a.ends_on, a.comment,
		coalesce(a.acting_id::text, ''), coalesce(au.name, ''), a.acting_rights
	FROM absences a JOIN users u ON u.id = a.user_id LEFT JOIN users au ON au.id = a.acting_id`

// Absences overlapping [from, to] (calendar days), by start.
func (s *Store) Absences(ctx context.Context, from, to time.Time) ([]Absence, error) {
	rows, err := s.db.Query(ctx, absenceSelect+`
		WHERE a.starts_on <= $2::date AND a.ends_on >= $1::date ORDER BY a.starts_on, u.name`, from, to)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Absence])
}

// AbsentNow is the person's absence today (nil: at work).
func (s *Store) AbsentNow(ctx context.Context, userID string) (*Absence, error) {
	today := time.Now()
	list, err := s.Absences(ctx, today, today)
	if err != nil {
		return nil, err
	}
	for _, a := range list {
		if a.UserID == userID {
			return &a, nil
		}
	}
	return nil, nil
}

// canManage: the person themself, an administrator, or anyone above them — heads and deputies of their
// departments and of the departments higher up.
func (s *Store) canManage(ctx context.Context, actor, userID string, admin bool) (bool, error) {
	if actor == userID || admin {
		return true, nil
	}
	managers, err := s.Superiors(ctx, userID)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(managers, func(p Person) bool { return p.ID == actor }), nil
}

// canAbsence: who may manage the person's absences — as canManage, and an HR officer with the right «absences»;
// hr tells the officer's action on another's absence (it is notified).
func (s *Store) canAbsence(ctx context.Context, actor, userID string, admin bool) (ok, hr bool, err error) {
	if actor != userID {
		h, err := s.HROf(ctx, actor)
		if err != nil {
			return false, false, err
		}
		if h.Can("absences") {
			return true, true, nil
		}
	}
	ok, err = s.canManage(ctx, actor, userID, admin)
	return ok, false, err
}

// checkAbsence: a known reason, dates in order and the actor may manage the person's absences (hr — as an HR officer).
func (s *Store) checkAbsence(ctx context.Context, actor, userID, kind string, from, to time.Time, admin bool) (hr bool, err error) {
	if !slices.Contains(AbsenceKinds, kind) {
		return false, i18n.E("err.bad_form")
	}
	if to.Before(from) {
		return false, i18n.E("err.absence_dates")
	}
	ok, hr, err := s.canAbsence(ctx, actor, userID, admin)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, ErrForbidden
	}
	return hr, nil
}

// actingOf: the person acting (nil — nobody) and their known rights, each once.
func actingOf(userID, actingID string, rights []string) (any, []string) {
	clean := []string{}
	if actingID == "" || actingID == userID {
		return nil, clean
	}
	for _, r := range rights {
		if slices.Contains(ActingRights, r) && !slices.Contains(clean, r) {
			clean = append(clean, r)
		}
	}
	return actingID, clean
}

// AddAbsence records an absence (own; a manager's, an HR officer's or an administrator's for others).
func (s *Store) AddAbsence(ctx context.Context, actor, userID, kind string, from, to time.Time, comment string, admin bool,
	actingID string, rights []string) error {
	hr, err := s.checkAbsence(ctx, actor, userID, kind, from, to, admin)
	if err != nil {
		return err
	}
	acting, clean := actingOf(userID, actingID, rights)
	var id string
	if err := s.db.QueryRow(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, comment, created_by, acting_id, acting_rights)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id::text`,
		userID, kind, from.Format("2006-01-02"), to.Format("2006-01-02"), strings.TrimSpace(comment), actor, acting, clean).Scan(&id); err != nil {
		return err
	}
	return s.notifyIfHR(ctx, hr, actor, "added", id)
}

// notifyIfHR: an HR officer's action — the absence (as it is now) to the officer, the employee and the managers.
func (s *Store) notifyIfHR(ctx context.Context, hr bool, actor, action, id string) error {
	if !hr {
		return nil
	}
	a, err := s.Absence(ctx, id)
	if err != nil {
		return err
	}
	return s.notifyHR(ctx, actor, action, *a)
}

// UpdateAbsence changes an absence — whose, the reason, dates, comment and the person acting — with the rights of
// adding it, for the person it was and the person it becomes.
func (s *Store) UpdateAbsence(ctx context.Context, actor, id, userID, kind string, from, to time.Time, comment string, admin bool,
	actingID string, rights []string) error {
	old, err := s.Absence(ctx, id)
	if err != nil {
		return err
	}
	ok, hrOld, err := s.canAbsence(ctx, actor, old.UserID, admin)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	if userID == "" {
		userID = old.UserID
	}
	hr, err := s.checkAbsence(ctx, actor, userID, kind, from, to, admin)
	if err != nil {
		return err
	}
	acting, clean := actingOf(userID, actingID, rights)
	if _, err = s.db.Exec(ctx, `UPDATE absences SET user_id = $2, kind = $3, starts_on = $4, ends_on = $5, comment = $6,
			acting_id = $7, acting_rights = $8 WHERE id::text = $1`,
		id, userID, kind, from.Format("2006-01-02"), to.Format("2006-01-02"), strings.TrimSpace(comment), acting, clean); err != nil {
		return err
	}
	if hrOld && old.UserID != userID { // moved to another person: the first one is told it is gone
		if err := s.notifyHR(ctx, actor, "deleted", *old); err != nil {
			return err
		}
		return s.notifyIfHR(ctx, hr, actor, "added", id)
	}
	return s.notifyIfHR(ctx, hr || hrOld, actor, "changed", id)
}

// DeleteAbsence removes an absence (with the same rights as adding it).
func (s *Store) DeleteAbsence(ctx context.Context, actor, id string, admin bool) error {
	a, err := s.Absence(ctx, id)
	if err != nil {
		return err
	}
	ok, hr, err := s.canAbsence(ctx, actor, a.UserID, admin)
	if err != nil {
		return err
	}
	if !ok {
		return ErrForbidden
	}
	if _, err = s.db.Exec(ctx, `DELETE FROM absences WHERE id::text = $1`, id); err != nil || !hr {
		return err
	}
	return s.notifyHR(ctx, actor, "deleted", *a)
}

// Birthday is an employee's next birthday.
type Birthday struct {
	Person
	Date  time.Time // this year's (or next year's) date
	Today bool
}

// Birthdays of active employees within the next days (today included), soonest first.
func (s *Store) Birthdays(ctx context.Context, days int) ([]Birthday, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, position, photo_key, birthday FROM users WHERE active AND NOT guest AND birthday IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	var out []Birthday
	for rows.Next() {
		var b Birthday
		var born time.Time
		if err := rows.Scan(&b.ID, &b.Name, &b.Position, &b.PhotoKey, &born); err != nil {
			return nil, err
		}
		d := time.Date(today.Year(), born.Month(), born.Day(), 0, 0, 0, 0, time.Local)
		if d.Before(today) {
			d = d.AddDate(1, 0, 0)
		}
		if d.Sub(today) <= time.Duration(days)*24*time.Hour {
			b.Date, b.Today = d, d.Equal(today)
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out, rows.Err()
}

// ActingFor are the people the user acts for today with the given power.
func (s *Store) ActingFor(ctx context.Context, userID, right string) ([]Person, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name, u.position, u.photo_key FROM absences a JOIN users u ON u.id = a.user_id
		WHERE a.acting_id = $1 AND $2 = ANY(a.acting_rights) AND current_date BETWEEN a.starts_on AND a.ends_on ORDER BY u.name`, userID, right)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
}

// Absence returns one absence.
func (s *Store) Absence(ctx context.Context, id string) (*Absence, error) {
	rows, err := s.db.Query(ctx, absenceSelect+` WHERE a.id::text = $1`, id)
	if err != nil {
		return nil, err
	}
	a, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[Absence])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

// ChartCell is a free day of a «График отсутствий» row (Span 1) or an absence across Span days.
type ChartCell struct {
	Day       time.Time
	Span      int
	Absence   *Absence
	Off       bool // a day off or a holiday
	Today     bool
	CutBefore bool // the absence began before the period
	CutAfter  bool // and lasts after it
}

// ChartRow is a person's line of the chart.
type ChartRow struct {
	Person
	Cells  []ChartCell
	Absent bool // has an absence in the period
}

// AbsenceChart lays the absences out on days [from, from+days) for each person «График отсутствий»:
// an absence is one bar over its days (cut at the edges of the period); where two of a person's absences overlap the
// earlier one is shown. off tells days off.
func AbsenceChart(people []Person, list []Absence, from time.Time, days int, off func(time.Time) bool, now time.Time) []ChartRow {
	from = time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.Local)
	last := from.AddDate(0, 0, days-1)
	today := now.Format(time.DateOnly)
	byUser := map[string][]*Absence{}
	for i := range list {
		byUser[list[i].UserID] = append(byUser[list[i].UserID], &list[i])
	}
	dayOf := func(t time.Time) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local) }
	rows := make([]ChartRow, 0, len(people))
	for _, p := range people {
		mine := byUser[p.ID]
		sort.SliceStable(mine, func(i, j int) bool { return mine[i].From.Before(mine[j].From) })
		row := ChartRow{Person: p}
		for i := 0; i < days; {
			d := from.AddDate(0, 0, i)
			var a *Absence
			for _, x := range mine {
				if !dayOf(x.From).After(d) && !dayOf(x.To).Before(d) {
					a = x
					break
				}
			}
			if a == nil {
				row.Cells = append(row.Cells, ChartCell{Day: d, Span: 1, Off: off(d), Today: d.Format(time.DateOnly) == today})
				i++
				continue
			}
			end := dayOf(a.To)
			if end.After(last) {
				end = last
			}
			span := int(end.Sub(d).Hours()/24+0.5) + 1
			row.Cells = append(row.Cells, ChartCell{Day: d, Span: span, Absence: a,
				CutBefore: dayOf(a.From).Before(d), CutAfter: dayOf(a.To).After(last)})
			row.Absent = true
			i += span
		}
		rows = append(rows, row)
	}
	return rows
}
