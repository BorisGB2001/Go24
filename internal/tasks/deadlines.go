package tasks

import (
	"context"
	"time"

	"go24/internal/worktime"
)

// «Сроки»: the person's tasks in columns by deadline; dragging a task to a column moves its deadline.

// DeadlinePeriods are the columns in their order.
var DeadlinePeriods = []string{"overdue", "today", "week", "next_week", "later", "none"}

// weekEnd is the start of the Monday after the week of t (weeks start on Monday).
func weekEnd(t time.Time) time.Time {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	return day.AddDate(0, 0, 7-(int(day.Weekday())+6)%7)
}

// DeadlinePeriod is the column of a deadline at the moment now (in now's location).
func DeadlinePeriod(d *time.Time, now time.Time) string {
	if d == nil {
		return "none"
	}
	t := d.In(now.Location())
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	switch week := weekEnd(now); {
	case t.Before(now):
		return "overdue"
	case t.Before(tomorrow):
		return "today"
	case t.Before(week):
		return "week"
	case t.Before(week.AddDate(0, 0, 7)):
		return "next_week"
	}
	return "later"
}

// periodRange is the deadlines of a column as [from, to) (nil — open end); «none» has neither.
func periodRange(period string, now time.Time) (from, to *time.Time, err error) {
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	week := weekEnd(now)
	next := week.AddDate(0, 0, 7)
	switch period {
	case "overdue":
		return nil, &now, nil
	case "today":
		return &now, &tomorrow, nil
	case "week":
		return &tomorrow, &week, nil
	case "next_week":
		return &week, &next, nil
	case "later":
		return &next, nil, nil
	case "none":
		return nil, nil, nil
	}
	return nil, nil, invalid("err.deadline_period")
}

// PeriodDeadline is the deadline a task gets when dropped into a column: the end of the working day — today, or
// the last working day of this week, the next one, the one after it. nil for «none»; overdue cannot be chosen.
func PeriodDeadline(period string, now time.Time, sched worktime.Schedule) (*time.Time, error) {
	h, m := 18, 0
	if end, err := time.Parse("15:04", sched.End); err == nil {
		h, m = end.Hour(), end.Minute()
	}
	at := func(day time.Time) *time.Time {
		t := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, now.Location())
		return &t
	}
	// last is the end of the last working day in [from, to), or of the day before to when none is.
	last := func(from, to time.Time) *time.Time {
		for day := to.AddDate(0, 0, -1); !day.Before(from); day = day.AddDate(0, 0, -1) {
			if sched.WorkingDay(day) {
				return at(day)
			}
		}
		return at(to.AddDate(0, 0, -1))
	}
	tomorrow := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	week := weekEnd(now)
	switch period {
	case "none":
		return nil, nil
	case "today":
		t := at(now)
		if !t.After(now) { // the working day is over: the end of the day
			e := tomorrow.Add(-time.Minute)
			t = &e
		}
		return t, nil
	case "week":
		if !tomorrow.Before(week) {
			return nil, invalid("err.deadline_period")
		}
		return last(tomorrow, week), nil
	case "next_week":
		return last(week, week.AddDate(0, 0, 7)), nil
	case "later":
		return last(week.AddDate(0, 0, 7), week.AddDate(0, 0, 14)), nil
	}
	return nil, invalid("err.deadline_period")
}

// MoveToPeriod gives the task the deadline of a column (with the rights of SetDeadline) and says the new deadline.
func (s *Store) MoveToPeriod(ctx context.Context, userID, taskID, period string, now time.Time) (*time.Time, error) {
	sched, err := s.Schedule(ctx)
	if err != nil {
		return nil, err
	}
	d, err := PeriodDeadline(period, now, sched)
	if err != nil {
		return nil, err
	}
	if err := s.SetDeadline(ctx, userID, taskID, d); err != nil {
		return nil, err
	}
	var cur *time.Time
	err = s.db.QueryRow(ctx, `SELECT deadline FROM tasks WHERE id = $1`, taskID).Scan(&cur)
	return cur, err
}
