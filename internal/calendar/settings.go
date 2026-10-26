package calendar

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"go24/internal/i18n"
)

// Settings of one's calendar, as «Настройки календаря».
type Settings struct {
	WeekStart    int   `json:"week_start"` // 1 Monday … 7 Sunday
	WorkFrom     int   `json:"work_from"`  // the working hours, [from, to)
	WorkTo       int   `json:"work_to"`
	DaysOff      []int `json:"days_off"` // 1 Monday … 7 Sunday
	ShowDeclined bool  `json:"show_declined"`
	ShowTasks    bool  `json:"show_tasks"`
	Remind       int   `json:"remind"` // the reminder of a new meeting, minutes before; -1 — none
}

// DefaultSettings are those: the week from Monday, 9 to 18, Saturday and Sunday off, everything shown,
// reminded 15 minutes before.
func DefaultSettings() Settings {
	return Settings{WeekStart: 1, WorkFrom: 9, WorkTo: 18, DaysOff: []int{6, 7}, ShowDeclined: true, ShowTasks: true, Remind: 15}
}

// Weekday is 1 Monday … 7 Sunday.
func Weekday(t time.Time) int { return (int(t.Weekday())+6)%7 + 1 }

// WeekOf is the first day of the week of day.
func (x Settings) WeekOf(day time.Time) time.Time {
	return day.AddDate(0, 0, -((Weekday(day) - x.WeekStart + 7) % 7))
}

// Off tells a day off.
func (x Settings) Off(day time.Time) bool { return slices.Contains(x.DaysOff, Weekday(day)) }

// Settings are the viewer's calendar settings.
func (s *Store) Settings(ctx context.Context, viewer string) (Settings, error) {
	x := DefaultSettings()
	var off []int16
	err := s.db.QueryRow(ctx, `SELECT week_start, work_from, work_to, days_off, show_declined, show_tasks, remind
		FROM calendar_settings WHERE user_id = $1`, viewer).Scan(&x.WeekStart, &x.WorkFrom, &x.WorkTo, &off, &x.ShowDeclined, &x.ShowTasks, &x.Remind)
	if errors.Is(err, pgx.ErrNoRows) {
		return x, nil
	}
	x.DaysOff = []int{}
	for _, d := range off {
		x.DaysOff = append(x.DaysOff, int(d))
	}
	return x, err
}

// SaveSettings keeps the viewer's calendar settings.
func (s *Store) SaveSettings(ctx context.Context, viewer string, x Settings) error {
	if x.WeekStart < 1 || x.WeekStart > 7 || x.WorkFrom < 0 || x.WorkTo > 24 || (x.Remind != -1 && !slices.Contains(RemindPresets, x.Remind)) {
		return i18n.E("err.bad_form")
	}
	if x.WorkFrom >= x.WorkTo {
		return i18n.E("err.calendar_hours")
	}
	off := []int{}
	for _, d := range x.DaysOff {
		if d < 1 || d > 7 {
			return i18n.E("err.bad_form")
		}
		if !slices.Contains(off, d) {
			off = append(off, d)
		}
	}
	slices.Sort(off)
	_, err := s.db.Exec(ctx, `INSERT INTO calendar_settings (user_id, week_start, work_from, work_to, days_off, show_declined, show_tasks, remind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) ON CONFLICT (user_id) DO UPDATE SET week_start = $2, work_from = $3, work_to = $4,
		days_off = $5, show_declined = $6, show_tasks = $7, remind = $8`,
		viewer, x.WeekStart, x.WorkFrom, x.WorkTo, off, x.ShowDeclined, x.ShowTasks, x.Remind)
	return err
}
