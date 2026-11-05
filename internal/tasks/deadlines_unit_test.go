package tasks

import (
	"testing"
	"time"

	"go24/internal/worktime"
)

func TestWeekEnd(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)

	tests := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{
			name: "Monday morning",
			in:   time.Date(2026, 10, 12, 9, 0, 0, 0, loc), // Monday
			want: time.Date(2026, 10, 19, 0, 0, 0, 0, loc),
		},
		{
			name: "Wednesday midday",
			in:   time.Date(2026, 10, 14, 12, 30, 0, 0, loc),
			want: time.Date(2026, 10, 19, 0, 0, 0, 0, loc),
		},
		{
			name: "Friday evening",
			in:   time.Date(2026, 10, 16, 23, 59, 59, 0, loc),
			want: time.Date(2026, 10, 19, 0, 0, 0, 0, loc),
		},
		{
			name: "Sunday night",
			in:   time.Date(2026, 10, 18, 23, 59, 59, 0, loc),
			want: time.Date(2026, 10, 19, 0, 0, 0, 0, loc),
		},
		{
			name: "Month transition late October",
			in:   time.Date(2026, 10, 28, 10, 0, 0, 0, loc), // Wednesday
			want: time.Date(2026, 11, 2, 0, 0, 0, 0, loc),   // Next Monday in November
		},
		{
			name: "Year transition late December",
			in:   time.Date(2026, 12, 30, 15, 0, 0, 0, loc), // Wednesday
			want: time.Date(2027, 1, 4, 0, 0, 0, 0, loc),    // Monday in January
		},
		{
			name: "Leap year February",
			in:   time.Date(2024, 2, 28, 11, 0, 0, 0, loc), // Wednesday
			want: time.Date(2024, 3, 4, 0, 0, 0, 0, loc),   // Monday in March (leap day Feb 29 passed)
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := weekEnd(tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("weekEnd(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestDeadlinePeriod_EdgeCases(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := func(day, h, m, s int) time.Time {
		return time.Date(2026, 10, day, h, m, s, 0, loc)
	}
	now := at(14, 12, 0, 0) // Wednesday 14.10.2026 12:00

	tests := []struct {
		name string
		d    *time.Time
		want string
	}{
		{name: "nil deadline", d: nil, want: "none"},
		{name: "1 second overdue", d: ptrTime(at(14, 11, 59, 59)), want: "overdue"},
		{name: "exact moment of now", d: ptrTime(now), want: "today"},
		{name: "today afternoon", d: ptrTime(at(14, 18, 0, 0)), want: "today"},
		{name: "today end of day", d: ptrTime(at(14, 23, 59, 59)), want: "today"},
		{name: "tomorrow midnight", d: ptrTime(at(15, 0, 0, 0)), want: "week"},
		{name: "this Sunday end of day", d: ptrTime(at(18, 23, 59, 59)), want: "week"},
		{name: "next Monday start of day", d: ptrTime(at(19, 0, 0, 0)), want: "next_week"},
		{name: "next Sunday end of day", d: ptrTime(at(25, 23, 59, 59)), want: "next_week"},
		{name: "week after next Monday start", d: ptrTime(at(26, 0, 0, 0)), want: "later"},
		{name: "far future", d: ptrTime(at(31, 18, 0, 0)), want: "later"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeadlinePeriod(tc.d, now)
			if got != tc.want {
				t.Errorf("DeadlinePeriod(%v, %v) = %q, want %q", tc.d, now, got, tc.want)
			}
		})
	}
}

func TestPeriodRange(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	now := time.Date(2026, 10, 14, 10, 0, 0, 0, loc) // Wednesday
	tomorrow := time.Date(2026, 10, 15, 0, 0, 0, 0, loc)
	week := time.Date(2026, 10, 19, 0, 0, 0, 0, loc)
	next := time.Date(2026, 10, 26, 0, 0, 0, 0, loc)

	tests := []struct {
		period   string
		wantFrom *time.Time
		wantTo   *time.Time
		wantErr  bool
	}{
		{period: "overdue", wantFrom: nil, wantTo: &now, wantErr: false},
		{period: "today", wantFrom: &now, wantTo: &tomorrow, wantErr: false},
		{period: "week", wantFrom: &tomorrow, wantTo: &week, wantErr: false},
		{period: "next_week", wantFrom: &week, wantTo: &next, wantErr: false},
		{period: "later", wantFrom: &next, wantTo: nil, wantErr: false},
		{period: "none", wantFrom: nil, wantTo: nil, wantErr: false},
		{period: "invalid_period", wantFrom: nil, wantTo: nil, wantErr: true},
		{period: "", wantFrom: nil, wantTo: nil, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.period, func(t *testing.T) {
			from, to, err := periodRange(tc.period, now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("periodRange(%q) error = %v, wantErr %v", tc.period, err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if (from == nil) != (tc.wantFrom == nil) || (from != nil && !from.Equal(*tc.wantFrom)) {
				t.Errorf("from = %v, want %v", from, tc.wantFrom)
			}
			if (to == nil) != (tc.wantTo == nil) || (to != nil && !to.Equal(*tc.wantTo)) {
				t.Errorf("to = %v, want %v", to, tc.wantTo)
			}
		})
	}
}

func TestPeriodDeadline_WorktimeAndErrors(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := func(day, h, m int) time.Time {
		return time.Date(2026, 10, day, h, m, 0, 0, loc)
	}

	sched := worktime.Default() // 09:00 - 18:00
	sched.End = "17:30"         // custom end

	now := at(14, 10, 0) // Wednesday 10:00
	got, err := PeriodDeadline("today", now, sched)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := at(14, 17, 30)
	if !got.Equal(want) {
		t.Errorf("PeriodDeadline(today) = %v, want %v", got, want)
	}

	// Schedule with unparseable End falls back to 18:00
	badSched := worktime.Default()
	badSched.End = "malformed"
	gotBad, err := PeriodDeadline("today", now, badSched)
	if err != nil {
		t.Fatalf("unexpected error with malformed End: %v", err)
	}
	wantBad := at(14, 18, 0)
	if !gotBad.Equal(wantBad) {
		t.Errorf("PeriodDeadline(today) with malformed End = %v, want %v", gotBad, wantBad)
	}

	// Invalid period returns error
	if _, err := PeriodDeadline("bogus", now, sched); err == nil {
		t.Error("expected error for bogus period, got nil")
	}

	// On Saturday (17.10), "week" deadline:
	// tomorrow is Sunday 18.10, weekEnd is Monday 19.10.
	// Last working day of week: Friday 16.10 is before tomorrow, so last falls back to day before week (Sunday 18.10)
	sat := at(17, 10, 0)
	gotSat, err := PeriodDeadline("week", sat, sched)
	if err != nil {
		t.Fatalf("unexpected error on Saturday: %v", err)
	}
	wantSat := at(18, 17, 30) // Sunday at 17:30
	if !gotSat.Equal(wantSat) {
		t.Errorf("PeriodDeadline(week) on Saturday = %v, want %v", gotSat, wantSat)
	}
}
