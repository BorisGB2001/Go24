package company

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"go24/internal/i18n"
	"go24/internal/worktime"
)

// «Графики работы»: a schedule is fixed (working days and hours), by shifts (a «График смен» says who
// works which shift on a day) or flexible (no fixed days or hours). Departments (with their sub-departments) and
// people are its members; the violations of a person's working day are counted by their schedule, and without
// one — by the company's working time.

// ScheduleKinds are the kinds of work schedules.
var ScheduleKinds = []string{"fixed", "shift", "flexible"}

// ScheduleTime is a set of working days of a fixed schedule with their hours.
type ScheduleTime struct {
	Days  []int  `json:"days"` // time.Weekday numbers
	Start string `json:"start"`
	End   string `json:"end"`
	Break int    `json:"break"` // minutes
}

// Shift is a shift of a shift schedule; an end before the start is on the next day.
type Shift struct {
	ID         int64
	Name       string
	Start, End string
	Break      int // minutes
}

// WorkSchedule is a work schedule. Members are people's ids and departments as "dept:ID", Excluded — people left
// out of the departments; MemberNames name both.
type WorkSchedule struct {
	ID          int64
	Name, Kind  string
	Times       []ScheduleTime
	Shifts      []Shift
	LateAfter   string
	LeaveBefore string
	MinDuration string
	Tolerance   int // shift schedules: minutes allowed late or early
	Members     []string
	Excluded    []string
	MemberNames map[string]string
}

// ShiftByID is the shift of the schedule (nil: none).
func (ws WorkSchedule) ShiftByID(id int64) *Shift {
	for i := range ws.Shifts {
		if ws.Shifts[i].ID == id {
			return &ws.Shifts[i]
		}
	}
	return nil
}

func validClock(s string) bool { return worktime.Minutes(s) >= 0 }

// clean checks the schedule and drops what its kind does not use.
func (ws *WorkSchedule) clean() error {
	ws.Name = strings.TrimSpace(ws.Name)
	if ws.Name == "" || len([]rune(ws.Name)) > 200 {
		return i18n.E("err.wschedule_name")
	}
	if !slices.Contains(ScheduleKinds, ws.Kind) {
		return i18n.E("err.bad_form")
	}
	for _, v := range []*string{&ws.LateAfter, &ws.LeaveBefore, &ws.MinDuration} {
		if *v = strings.TrimSpace(*v); *v != "" && !validClock(*v) {
			return i18n.E("err.wschedule_time")
		}
	}
	if ws.Tolerance < 0 || ws.Tolerance > 720 {
		return i18n.E("err.wschedule_time")
	}
	switch ws.Kind {
	case "fixed":
		ws.Shifts, ws.Tolerance = nil, 0
		var times []ScheduleTime
		seen := map[int]bool{}
		for _, t := range ws.Times {
			if len(t.Days) == 0 {
				continue
			}
			a, b := worktime.Minutes(t.Start), worktime.Minutes(t.End)
			if a < 0 || b <= a || t.Break < 0 || t.Break >= b-a {
				return i18n.E("err.wschedule_time")
			}
			for _, d := range t.Days {
				if d < 0 || d > 6 || seen[d] {
					return i18n.E("err.wschedule_days")
				}
				seen[d] = true
			}
			slices.Sort(t.Days)
			times = append(times, t)
		}
		if len(times) == 0 {
			return i18n.E("err.wschedule_days")
		}
		ws.Times = times
	case "shift":
		ws.Times, ws.LateAfter, ws.LeaveBefore = nil, "", ""
		var shifts []Shift
		for _, sh := range ws.Shifts {
			if sh.Name = strings.TrimSpace(sh.Name); sh.Name == "" {
				continue
			}
			a, b := worktime.Minutes(sh.Start), worktime.Minutes(sh.End)
			if len([]rune(sh.Name)) > 100 || a < 0 || b < 0 || a == b || sh.Break < 0 || sh.Break >= shiftLen(a, b) {
				return i18n.E("err.wschedule_shift")
			}
			shifts = append(shifts, sh)
		}
		if len(shifts) == 0 {
			return i18n.E("err.wschedule_shift")
		}
		ws.Shifts = shifts
	default:
		ws.Times, ws.Shifts, ws.LateAfter, ws.LeaveBefore, ws.Tolerance = nil, nil, "", "", 0
	}
	return nil
}

// shiftLen is the length of a shift from a to b (minutes of the day), across midnight when b is before a.
func shiftLen(a, b int) int {
	if b <= a {
		b += 24 * 60
	}
	return b - a
}

// DayPlan is what the schedule wants of a person on a day.
type DayPlan struct {
	Work       bool // a working day: missing it is a gap
	Free       bool // a flexible schedule: no fixed days
	Start, End string
	Break      int
	Shift      string // the shift's name
}

// Minutes is the planned length of the day without its break (0: not a working day or no fixed hours).
func (p DayPlan) Minutes() int {
	a, b := worktime.Minutes(p.Start), worktime.Minutes(p.End)
	if !p.Work || a < 0 || b < 0 {
		return 0
	}
	return max(shiftLen(a, b)-p.Break, 0)
}

// Norm is a person's working-time rules: the company's working time and, if they have one, their work schedule
// with their planned shifts ("2006-01-02" → the shift).
type Norm struct {
	Company worktime.Schedule
	WS      *WorkSchedule
	Shifts  map[string]Shift
}

// Plan is the person's day by their schedule.
func (n Norm) Plan(day time.Time) DayPlan {
	if n.WS == nil {
		if !n.Company.WorkingDay(day) {
			return DayPlan{}
		}
		return DayPlan{Work: true, Start: n.Company.Start, End: n.Company.End}
	}
	switch n.WS.Kind {
	case "shift":
		if sh, ok := n.Shifts[day.Format(time.DateOnly)]; ok {
			return DayPlan{Work: true, Start: sh.Start, End: sh.End, Break: sh.Break, Shift: sh.Name}
		}
		return DayPlan{}
	case "flexible":
		return DayPlan{Free: true}
	}
	if n.Company.Holiday(day) {
		return DayPlan{}
	}
	for _, t := range n.WS.Times {
		if slices.Contains(t.Days, int(day.Weekday())) {
			return DayPlan{Work: true, Start: t.Start, End: t.End, Break: t.Break}
		}
	}
	return DayPlan{}
}

// at is the time hh:mm (minutes) of the day.
func at(day time.Time, m int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local).Add(time.Duration(m) * time.Minute)
}

// Violations of the day: late, early (left before the time), short (worked less than the minimum), open (a past
// day not finished). By a fixed schedule a day is late after its start (or «late after»), early before «leave
// before»; by a shift schedule — later than the shift's start or earlier than its end by more than the tolerance;
// a flexible schedule has only the minimum length. Days off of a schedule have no violations but «open».
func (n Norm) Violations(d Workday, now time.Time) []string {
	late, early, minDur := -1, time.Time{}, worktime.Minutes(n.Company.MinDuration)
	day := time.Date(d.Day.Year(), d.Day.Month(), d.Day.Day(), 0, 0, 0, 0, time.Local)
	if n.WS == nil {
		late = worktime.Minutes(n.Company.LateAt())
		if m := worktime.Minutes(n.Company.LeaveBefore); m >= 0 {
			early = at(day, m)
		}
	} else {
		minDur = worktime.Minutes(n.WS.MinDuration)
		p := n.Plan(day)
		switch {
		case n.WS.Kind == "flexible":
		case !p.Work:
			minDur = -1
		case n.WS.Kind == "shift":
			a, b := worktime.Minutes(p.Start), worktime.Minutes(p.End)
			late = a + n.WS.Tolerance
			early = at(day, a+shiftLen(a, b)-n.WS.Tolerance)
		default:
			late = worktime.Minutes(p.Start)
			if n.WS.LateAfter != "" {
				late = worktime.Minutes(n.WS.LateAfter)
			}
			if m := worktime.Minutes(n.WS.LeaveBefore); m >= 0 {
				early = at(day, m)
			}
		}
	}
	var out []string
	if s := d.Started.Local(); late >= 0 && s.Hour()*60+s.Minute() > late {
		out = append(out, "late")
	}
	if d.Ended == nil {
		if d.Day.Format(time.DateOnly) < now.Format(time.DateOnly) {
			out = append(out, "open")
		}
		return out
	}
	if !early.IsZero() && d.Ended.Before(early) {
		out = append(out, "early")
	}
	if minDur > 0 && d.Worked(now) < minDur*60 {
		out = append(out, "short")
	}
	return out
}

// ScheduleRef is a work schedule with its members counted, for the list.
type ScheduleRef struct {
	ID         int64
	Name, Kind string
	Depts      int
	People     int
}

// WorkSchedules are all the work schedules by name.
func (s *Store) WorkSchedules(ctx context.Context) ([]ScheduleRef, error) {
	rows, err := s.db.Query(ctx, `SELECT w.id, w.name, w.kind,
			(SELECT count(*) FROM work_schedule_members m WHERE m.schedule_id = w.id AND m.department_id IS NOT NULL),
			(SELECT count(*) FROM work_schedule_members m WHERE m.schedule_id = w.id AND m.user_id IS NOT NULL AND NOT m.excluded)
		FROM work_schedules w ORDER BY lower(w.name), w.id`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[ScheduleRef])
}

// WorkSchedule is the schedule with its shifts and members.
func (s *Store) WorkSchedule(ctx context.Context, id int64) (*WorkSchedule, error) {
	list, err := s.workSchedules(ctx, []int64{id})
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, i18n.E("err.not_found")
	}
	return &list[0], nil
}

func (s *Store) workSchedules(ctx context.Context, ids []int64) ([]WorkSchedule, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, kind, times, late_after, leave_before, min_duration, tolerance
		FROM work_schedules WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (WorkSchedule, error) {
		var ws WorkSchedule
		var times []byte
		if err := r.Scan(&ws.ID, &ws.Name, &ws.Kind, &times, &ws.LateAfter, &ws.LeaveBefore, &ws.MinDuration, &ws.Tolerance); err != nil {
			return ws, err
		}
		ws.MemberNames = map[string]string{}
		return ws, json.Unmarshal(times, &ws.Times)
	})
	if err != nil || len(list) == 0 {
		return list, err
	}
	byID := map[int64]*WorkSchedule{}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	rows, err = s.db.Query(ctx, `SELECT schedule_id, id, name, start_at, end_at, break_min FROM work_shifts
		WHERE schedule_id = ANY($1) ORDER BY sort, id`, ids)
	if err != nil {
		return nil, err
	}
	var sid int64
	var sh Shift
	if _, err := pgx.ForEachRow(rows, []any{&sid, &sh.ID, &sh.Name, &sh.Start, &sh.End, &sh.Break}, func() error {
		byID[sid].Shifts = append(byID[sid].Shifts, sh)
		return nil
	}); err != nil {
		return nil, err
	}
	rows, err = s.db.Query(ctx, `SELECT m.schedule_id, coalesce(m.user_id::text, 'dept:' || m.department_id), m.excluded,
			coalesce(u.name, d.name, '')
		FROM work_schedule_members m LEFT JOIN users u ON u.id = m.user_id LEFT JOIN departments d ON d.id = m.department_id
		WHERE m.schedule_id = ANY($1) ORDER BY m.department_id IS NULL, coalesce(d.name, u.name)`, ids)
	if err != nil {
		return nil, err
	}
	var ref, name string
	var excluded bool
	if _, err := pgx.ForEachRow(rows, []any{&sid, &ref, &excluded, &name}, func() error {
		ws := byID[sid]
		if excluded {
			ws.Excluded = append(ws.Excluded, ref)
		} else {
			ws.Members = append(ws.Members, ref)
		}
		ws.MemberNames[ref] = name
		return nil
	}); err != nil {
		return nil, err
	}
	return list, nil
}

// SaveWorkSchedule creates (ID 0) or changes a schedule with its shifts and members (an administrator — checked by
// the caller). Shifts keep their ids (and the plan) when changed; a removed shift leaves the plan.
func (s *Store) SaveWorkSchedule(ctx context.Context, actor string, ws *WorkSchedule) error {
	if err := ws.clean(); err != nil {
		return err
	}
	times, err := json.Marshal(ws.Times)
	if err != nil {
		return err
	}
	if ws.Times == nil {
		times = []byte("[]")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if ws.ID == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO work_schedules (name, kind, times, late_after, leave_before, min_duration, tolerance, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
			ws.Name, ws.Kind, times, ws.LateAfter, ws.LeaveBefore, ws.MinDuration, ws.Tolerance, actor).Scan(&ws.ID)
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx, `UPDATE work_schedules SET name = $2, kind = $3, times = $4, late_after = $5, leave_before = $6,
			min_duration = $7, tolerance = $8 WHERE id = $1`, ws.ID, ws.Name, ws.Kind, times, ws.LateAfter, ws.LeaveBefore, ws.MinDuration, ws.Tolerance)
		if err == nil && tag.RowsAffected() == 0 {
			err = i18n.E("err.not_found")
		}
	}
	if err != nil {
		return err
	}
	var keep []int64
	for i := range ws.Shifts {
		sh := &ws.Shifts[i]
		if sh.ID != 0 {
			tag, err := tx.Exec(ctx, `UPDATE work_shifts SET name = $3, start_at = $4, end_at = $5, break_min = $6, sort = $7
				WHERE id = $1 AND schedule_id = $2`, sh.ID, ws.ID, sh.Name, sh.Start, sh.End, sh.Break, i)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 1 {
				keep = append(keep, sh.ID)
				continue
			}
		}
		if err := tx.QueryRow(ctx, `INSERT INTO work_shifts (schedule_id, name, start_at, end_at, break_min, sort)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`, ws.ID, sh.Name, sh.Start, sh.End, sh.Break, i).Scan(&sh.ID); err != nil {
			return err
		}
		keep = append(keep, sh.ID)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM work_shifts WHERE schedule_id = $1 AND NOT id = ANY($2)`, ws.ID, keep); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM work_schedule_members WHERE schedule_id = $1`, ws.ID); err != nil {
		return err
	}
	add := func(ref string, excluded bool) error {
		col, id := "user_id", ref
		if d, ok := strings.CutPrefix(ref, "dept:"); ok {
			if excluded {
				return nil
			}
			col, id = "department_id", d
		}
		if !uuidLike(id) {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO work_schedule_members (schedule_id, `+col+`, excluded)
			SELECT $1, $2::uuid, $3 WHERE EXISTS (SELECT 1 FROM `+map[string]string{"user_id": "users", "department_id": "departments"}[col]+` WHERE id = $2::uuid)
			ON CONFLICT DO NOTHING`, ws.ID, id, excluded)
		return err
	}
	for _, ref := range ws.Members {
		if err := add(ref, false); err != nil {
			return err
		}
	}
	for _, ref := range ws.Excluded {
		if err := add(ref, true); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func uuidLike(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !strings.ContainsRune("0123456789abcdefABCDEF", c) {
			return false
		}
	}
	return true
}

// DeleteWorkSchedule removes a schedule; its members go back to the company's working time.
func (s *Store) DeleteWorkSchedule(ctx context.Context, id int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM work_schedules WHERE id = $1`, id)
	return err
}

// ScheduleIDs are the schedules of the people (people without one are not in the map): the schedule they are
// members of themselves, else the one of their nearest department (or its parent) that does not exclude them; of
// two at one level — the older.
func (s *Store) ScheduleIDs(ctx context.Context, people []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(people) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `WITH RECURSIVE up AS (
			SELECT ud.user_id, d.id, d.parent_id, 0 AS lvl FROM user_departments ud JOIN departments d ON d.id = ud.department_id
			WHERE ud.user_id::text = ANY($1)
			UNION ALL SELECT up.user_id, p.id, p.parent_id, up.lvl + 1 FROM departments p JOIN up ON p.id = up.parent_id WHERE up.lvl < 50),
		cand AS (
			SELECT m.user_id, m.schedule_id, -1 AS lvl FROM work_schedule_members m WHERE m.user_id::text = ANY($1) AND NOT m.excluded
			UNION ALL SELECT up.user_id, m.schedule_id, up.lvl FROM up JOIN work_schedule_members m ON m.department_id = up.id)
		SELECT DISTINCT ON (c.user_id) c.user_id::text, c.schedule_id FROM cand c
		WHERE NOT EXISTS (SELECT 1 FROM work_schedule_members x WHERE x.schedule_id = c.schedule_id AND x.excluded AND x.user_id = c.user_id)
		ORDER BY c.user_id, c.lvl, c.schedule_id`, people)
	if err != nil {
		return nil, err
	}
	var uid string
	var sid int64
	_, err = pgx.ForEachRow(rows, []any{&uid, &sid}, func() error {
		out[uid] = sid
		return nil
	})
	return out, err
}

// Norms are the working-time rules of the people for the days [from, to) — their schedules and planned shifts;
// people without a schedule follow the company's working time.
func (s *Store) Norms(ctx context.Context, people []string, sched worktime.Schedule, from, to time.Time) (map[string]Norm, error) {
	ids, err := s.ScheduleIDs(ctx, people)
	if err != nil {
		return nil, err
	}
	var sids []int64
	for _, id := range ids {
		if !slices.Contains(sids, id) {
			sids = append(sids, id)
		}
	}
	schedules := map[int64]*WorkSchedule{}
	if len(sids) > 0 {
		list, err := s.workSchedules(ctx, sids)
		if err != nil {
			return nil, err
		}
		for i := range list {
			schedules[list[i].ID] = &list[i]
		}
	}
	out := make(map[string]Norm, len(people))
	for _, p := range people {
		n := Norm{Company: sched}
		if ws := schedules[ids[p]]; ws != nil {
			n.WS = ws
			if ws.Kind == "shift" {
				n.Shifts = map[string]Shift{}
			}
		}
		out[p] = n
	}
	plan, err := s.ShiftPlan(ctx, people, from, to)
	if err != nil {
		return nil, err
	}
	for key, shiftID := range plan {
		uid, day, _ := strings.Cut(key, "|")
		if n := out[uid]; n.WS != nil && n.Shifts != nil {
			if sh := n.WS.ShiftByID(shiftID); sh != nil {
				n.Shifts[day] = *sh
			}
		}
	}
	return out, nil
}

// NormOf is the person's rules for the days [from, to).
func (s *Store) NormOf(ctx context.Context, userID string, sched worktime.Schedule, from, to time.Time) (Norm, error) {
	m, err := s.Norms(ctx, []string{userID}, sched, from, to)
	if err != nil {
		return Norm{Company: sched}, err
	}
	return m[userID], nil
}

// SchedulePeople are the active employees whose schedule is this one, by name.
func (s *Store) SchedulePeople(ctx context.Context, id int64) ([]Person, error) {
	rows, err := s.db.Query(ctx, `SELECT id, name, position, photo_key FROM users WHERE active AND NOT guest ORDER BY name`)
	if err != nil {
		return nil, err
	}
	all, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Person])
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(all))
	for i, p := range all {
		ids[i] = p.ID
	}
	of, err := s.ScheduleIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(all, func(p Person) bool { return of[p.ID] != id }), nil
}

// ShiftPlan is the planned shifts of the people in [from, to): "user|2006-01-02" → the shift id.
func (s *Store) ShiftPlan(ctx context.Context, people []string, from, to time.Time) (map[string]int64, error) {
	out := map[string]int64{}
	if len(people) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT user_id::text || '|' || to_char(day, 'YYYY-MM-DD'), shift_id FROM work_shift_plan
		WHERE user_id::text = ANY($1) AND day >= $2 AND day < $3`, people, from.Format(time.DateOnly), to.Format(time.DateOnly))
	if err != nil {
		return nil, err
	}
	var key string
	var id int64
	_, err = pgx.ForEachRow(rows, []any{&key, &id}, func() error {
		out[key] = id
		return nil
	})
	return out, err
}

// SetShiftPlan replaces the plan of the people for the days [from, to) with cells ("user|2006-01-02" → a shift of
// the schedule; others are ignored). A day of a person has one shift: planning it here moves it from another schedule.
func (s *Store) SetShiftPlan(ctx context.Context, scheduleID int64, people []string, from, to time.Time, cells map[string]int64) error {
	ws, err := s.WorkSchedule(ctx, scheduleID)
	if err != nil {
		return err
	}
	if ws.Kind != "shift" {
		return i18n.E("err.bad_form")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM work_shift_plan p USING work_shifts sh WHERE sh.id = p.shift_id AND sh.schedule_id = $1
		AND p.user_id::text = ANY($2) AND p.day >= $3 AND p.day < $4`, scheduleID, people, from.Format(time.DateOnly), to.Format(time.DateOnly)); err != nil {
		return err
	}
	for key, shiftID := range cells {
		uid, d, _ := strings.Cut(key, "|")
		day, err := time.ParseInLocation(time.DateOnly, d, time.Local)
		if err != nil || !slices.Contains(people, uid) || day.Before(from) || !day.Before(to) || ws.ShiftByID(shiftID) == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO work_shift_plan (user_id, day, shift_id) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, day) DO UPDATE SET shift_id = $3`, uid, d, shiftID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
