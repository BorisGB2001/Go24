// Package worktime is the company work schedule (the portal: Настройки → Рабочее время): working hours,
// weekend days and holidays. Tasks with «пропускать выходные» keep their dates inside working time.
package worktime

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Schedule is stored as JSON in company_settings ("worktime").
type Schedule struct {
	Start    string   `json:"start"`    // "09:00"
	End      string   `json:"end"`      // "18:00"
	Weekend  []int    `json:"weekend"`  // time.Weekday numbers: 0 Sunday … 6 Saturday
	Holidays []string `json:"holidays"` // "2006-01-02" or yearly "01-02"
	// violations of the working day (the portal: Учёт рабочего времени → нарушения), "" — not checked
	LateAfter   string `json:"late_after,omitempty"`   // a day started later is late ("" — the start of the working day)
	LeaveBefore string `json:"leave_before,omitempty"` // a day finished earlier is an early leave
	MinDuration string `json:"min_duration,omitempty"` // a shorter day (without pauses) is short, "08:00"
	FreeEdit    bool   `json:"free_edit,omitempty"`    // employees change their days without a manager's approval
}

// Default is the portal default: 9:00–18:00, Saturday and Sunday off, Russian public holidays.
func Default() Schedule {
	return Schedule{Start: "09:00", End: "18:00", Weekend: []int{0, 6},
		Holidays: []string{"01-01", "01-02", "01-03", "01-04", "01-05", "01-06", "01-07", "01-08", "02-23", "03-08", "05-01", "05-09", "06-12", "11-04"}}
}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return 0, err
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Validate checks the schedule and normalizes holidays; the error text is for logs, callers show their own.
func (s *Schedule) Validate() error {
	a, err := clock(s.Start)
	if err != nil {
		return fmt.Errorf("start: %w", err)
	}
	b, err := clock(s.End)
	if err != nil {
		return fmt.Errorf("end: %w", err)
	}
	if b <= a {
		return fmt.Errorf("end before start")
	}
	for name, v := range map[string]*string{"late after": &s.LateAfter, "leave before": &s.LeaveBefore, "min duration": &s.MinDuration} {
		if *v = strings.TrimSpace(*v); *v == "" {
			continue
		}
		if _, err := clock(*v); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for _, d := range s.Weekend {
		if d < 0 || d > 6 {
			return fmt.Errorf("weekday %d", d)
		}
	}
	if len(s.Weekend) == 7 {
		return fmt.Errorf("no working days")
	}
	var hs []string
	for _, h := range s.Holidays {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", h); err != nil {
			if _, err := time.Parse("01-02", h); err != nil {
				return fmt.Errorf("holiday %q", h)
			}
		}
		hs = append(hs, h)
	}
	slices.Sort(hs)
	s.Holidays = slices.Compact(hs)
	return nil
}

// LateAt is the time (hh:mm) after which a working day starts late.
func (s Schedule) LateAt() string {
	if s.LateAfter != "" {
		return s.LateAfter
	}
	return s.Start
}

// Minutes is a time of day or a duration "hh:mm" in minutes, -1 when empty or wrong.
func Minutes(hhmm string) int {
	m, err := clock(hhmm)
	if err != nil {
		return -1
	}
	return m
}

// WorkingDay reports whether the date (in its own location) is a working day.
func (s Schedule) WorkingDay(t time.Time) bool {
	return !slices.Contains(s.Weekend, int(t.Weekday())) && !s.Holiday(t)
}

// Holiday reports whether the date is one of the company's holidays.
func (s Schedule) Holiday(t time.Time) bool {
	return slices.Contains(s.Holidays, t.Format("2006-01-02")) || slices.Contains(s.Holidays, t.Format("01-02"))
}

// Deadline moves a deadline into working time with «пропускать выходные»: a deadline
// on a day off goes to the next working day; a time outside working hours goes to the end of the
// working day (before the start — the end of that day, after the end — the end of that day too).
func (s Schedule) Deadline(t time.Time) time.Time {
	end, err := clock(s.End)
	if err != nil {
		return t
	}
	start, _ := clock(s.Start)
	m := t.Hour()*60 + t.Minute()
	day := t
	for i := 0; i < 400 && !s.WorkingDay(day); i++ {
		day = day.AddDate(0, 0, 1)
	}
	if day.Equal(t) && m >= start && m <= end {
		return t
	}
	return time.Date(day.Year(), day.Month(), day.Day(), end/60, end%60, 0, 0, t.Location())
}

// Day moves a plan date (no time) to the nearest working day on or after it.
func (s Schedule) Day(t time.Time) time.Time {
	for i := 0; i < 400 && !s.WorkingDay(t); i++ {
		t = t.AddDate(0, 0, 1)
	}
	return t
}

// DefaultDeadline is the deadline a new task form proposes: a week from now at the end of the working day.
func (s Schedule) DefaultDeadline(now time.Time) time.Time {
	end, err := clock(s.End)
	if err != nil {
		end = 18 * 60
	}
	d := now.AddDate(0, 0, 7)
	return s.Deadline(time.Date(d.Year(), d.Month(), d.Day(), end/60, end%60, 0, 0, now.Location()))
}

// Adjust applies Deadline to an optional deadline (in local time).
func (s Schedule) Adjust(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := s.Deadline(t.Local())
	return &d
}

// AdjustDay applies Day to an optional plan date.
func (s Schedule) AdjustDay(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	d := s.Day(*t)
	return &d
}
