package calendar

import (
	"slices"
	"testing"
	"time"

	"go24/internal/worktime"
)

func TestSettingsDefaultsAndWeekday(t *testing.T) {
	s := DefaultSettings()
	if s.WeekStart != 1 || s.WorkFrom != 9 || s.WorkTo != 18 || s.Remind != 15 {
		t.Errorf("unexpected DefaultSettings: %+v", s)
	}
	if !slices.Equal(s.DaysOff, []int{6, 7}) {
		t.Errorf("unexpected DaysOff: %v", s.DaysOff)
	}
	if !s.ShowDeclined || !s.ShowTasks {
		t.Error("expected ShowDeclined and ShowTasks to be true by default")
	}

	// Weekday: 1 Monday … 7 Sunday
	// 2026-10-19 is Monday, 2026-10-25 is Sunday
	for d := 1; d <= 7; d++ {
		date := time.Date(2026, 10, 18+d, 10, 0, 0, 0, time.Local)
		if got := Weekday(date); got != d {
			t.Errorf("Weekday(%v) = %d, want %d", date, got, d)
		}
	}
}

func TestSettingsWeekOf(t *testing.T) {
	// 2026-10-18: Sunday
	// 2026-10-19: Monday
	// 2026-10-21: Wednesday
	// 2026-10-24: Saturday
	// 2026-10-25: Sunday
	wed := time.Date(2026, 10, 21, 15, 30, 0, 0, time.Local)

	t.Run("week starts on Monday (1)", func(t *testing.T) {
		s := Settings{WeekStart: 1}
		wantMonday := time.Date(2026, 10, 19, 15, 30, 0, 0, time.Local)
		if got := s.WeekOf(wed); !got.Equal(wantMonday) {
			t.Errorf("WeekOf(Wed) = %v, want %v", got, wantMonday)
		}

		sun := time.Date(2026, 10, 25, 10, 0, 0, 0, time.Local)
		if got := s.WeekOf(sun); !got.Equal(time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local)) {
			t.Errorf("WeekOf(Sun) = %v, want Monday 19.10", got)
		}
	})

	t.Run("week starts on Sunday (7)", func(t *testing.T) {
		s := Settings{WeekStart: 7}
		wantSunday := time.Date(2026, 10, 18, 15, 30, 0, 0, time.Local)
		if got := s.WeekOf(wed); !got.Equal(wantSunday) {
			t.Errorf("WeekOf(Wed) = %v, want Sunday 18.10: got %v", got, wantSunday)
		}

		sat := time.Date(2026, 10, 24, 10, 0, 0, 0, time.Local)
		if got := s.WeekOf(sat); !got.Equal(time.Date(2026, 10, 18, 10, 0, 0, 0, time.Local)) {
			t.Errorf("WeekOf(Sat) = %v, want Sunday 18.10", got)
		}
	})
}

func TestSettingsOffAndHoliday(t *testing.T) {
	s := Settings{
		DaysOff:  []int{6, 7}, // Sat, Sun
		Holidays: []string{"2026-05-01", "01-01"},
	}

	// Normal workday
	wed := time.Date(2026, 10, 21, 10, 0, 0, 0, time.Local)
	if s.Off(wed) {
		t.Errorf("Wednesday should not be off")
	}

	// Weekend
	sat := time.Date(2026, 10, 24, 10, 0, 0, 0, time.Local)
	sun := time.Date(2026, 10, 25, 10, 0, 0, 0, time.Local)
	if !s.Off(sat) || !s.Off(sun) {
		t.Errorf("Saturday and Sunday must be off")
	}

	// Specific holiday (Friday 2026-05-01)
	mayDay := time.Date(2026, 5, 1, 10, 0, 0, 0, time.Local)
	if !s.Holiday(mayDay) || !s.Off(mayDay) {
		t.Errorf("May Day 2026-05-01 must be holiday and off")
	}

	// Annual holiday (Thursday 2026-01-01)
	newYear := time.Date(2026, 1, 1, 10, 0, 0, 0, time.Local)
	if !s.Holiday(newYear) || !s.Off(newYear) {
		t.Errorf("New Year 2026-01-01 must be holiday and off")
	}
}

func TestCompanySettings(t *testing.T) {
	sched := worktime.Schedule{
		Start:    "08:30",
		End:      "17:15",
		Weekend:  []int{0, 6}, // Sun, Sat
		Holidays: []string{"2026-12-31"},
	}

	s := companySettings(sched)
	// 08:30 -> 510/60 = 8
	// 17:15 -> (1035 + 59)/60 = 18
	if s.WorkFrom != 8 || s.WorkTo != 18 {
		t.Errorf("WorkFrom=%d, WorkTo=%d; want 8 and 18", s.WorkFrom, s.WorkTo)
	}
	if !slices.Equal(s.DaysOff, []int{6, 7}) {
		t.Errorf("DaysOff = %v, want [6 7]", s.DaysOff)
	}
	if !slices.Equal(s.Holidays, []string{"2026-12-31"}) {
		t.Errorf("Holidays = %v, want [2026-12-31]", s.Holidays)
	}

	// Empty schedule retains default hours
	emptySched := worktime.Schedule{}
	sEmpty := companySettings(emptySched)
	if sEmpty.WorkFrom != 9 || sEmpty.WorkTo != 18 {
		t.Errorf("expected default 9-18, got %d-%d", sEmpty.WorkFrom, sEmpty.WorkTo)
	}
}
