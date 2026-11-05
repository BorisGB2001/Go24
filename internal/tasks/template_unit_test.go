package tasks

import (
	"testing"
	"time"

	"go24/internal/worktime"
)

func TestRepeatValidate(t *testing.T) {
	now := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	later := now.Add(24 * time.Hour)
	earlier := now.Add(-24 * time.Hour)
	one := 1
	zero := 0

	validDaily := Repeat{
		Period:    "daily",
		Every:     1,
		MonthDay:  1,
		YearMonth: 1,
		At:        "09:00",
		StartsOn:  now,
	}

	tests := []struct {
		name    string
		r       Repeat
		wantErr bool
	}{
		{name: "valid daily", r: validDaily, wantErr: false},
		{
			name: "valid weekly",
			r: Repeat{
				Period:    "weekly",
				Every:     2,
				WeekDays:  []int{1, 3, 5},
				MonthDay:  1,
				YearMonth: 1,
				At:        "10:30",
				StartsOn:  now,
				EndsOn:    &later,
				Times:     &one,
			},
			wantErr: false,
		},
		{
			name: "valid monthly",
			r: Repeat{
				Period:    "monthly",
				Every:     1,
				MonthDay:  31,
				YearMonth: 1,
				At:        "18:00",
				StartsOn:  now,
			},
			wantErr: false,
		},
		{
			name: "valid yearly",
			r: Repeat{
				Period:    "yearly",
				Every:     1,
				MonthDay:  28,
				YearMonth: 2,
				At:        "12:00",
				StartsOn:  now,
			},
			wantErr: false,
		},
		{
			name:    "invalid period",
			r:       func() Repeat { r := validDaily; r.Period = "hourly"; return r }(),
			wantErr: true,
		},
		{
			name:    "every is 0",
			r:       func() Repeat { r := validDaily; r.Every = 0; return r }(),
			wantErr: true,
		},
		{
			name:    "every > 365",
			r:       func() Repeat { r := validDaily; r.Every = 366; return r }(),
			wantErr: true,
		},
		{
			name:    "weekly without weekdays",
			r:       Repeat{Period: "weekly", Every: 1, MonthDay: 1, YearMonth: 1, At: "09:00", StartsOn: now},
			wantErr: true,
		},
		{
			name:    "weekly with invalid weekday (0)",
			r:       Repeat{Period: "weekly", Every: 1, WeekDays: []int{0}, MonthDay: 1, YearMonth: 1, At: "09:00", StartsOn: now},
			wantErr: true,
		},
		{
			name:    "weekly with invalid weekday (8)",
			r:       Repeat{Period: "weekly", Every: 1, WeekDays: []int{8}, MonthDay: 1, YearMonth: 1, At: "09:00", StartsOn: now},
			wantErr: true,
		},
		{
			name:    "month day < 1",
			r:       func() Repeat { r := validDaily; r.MonthDay = 0; return r }(),
			wantErr: true,
		},
		{
			name:    "month day > 31",
			r:       func() Repeat { r := validDaily; r.MonthDay = 32; return r }(),
			wantErr: true,
		},
		{
			name:    "year month < 1",
			r:       func() Repeat { r := validDaily; r.YearMonth = 0; return r }(),
			wantErr: true,
		},
		{
			name:    "year month > 12",
			r:       func() Repeat { r := validDaily; r.YearMonth = 13; return r }(),
			wantErr: true,
		},
		{
			name:    "bad at time",
			r:       func() Repeat { r := validDaily; r.At = "bad_time"; return r }(),
			wantErr: true,
		},
		{
			name:    "ends on before starts on",
			r:       func() Repeat { r := validDaily; r.EndsOn = &earlier; return r }(),
			wantErr: true,
		},
		{
			name:    "times < 1",
			r:       func() Repeat { r := validDaily; r.Times = &zero; return r }(),
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.r.validate()
			if (err != nil) != tc.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestHHMM(t *testing.T) {
	tests := []struct {
		in      string
		wantH   int
		wantM   int
		wantErr bool
	}{
		{"00:00", 0, 0, false},
		{"09:30", 9, 30, false},
		{"23:59", 23, 59, false},
		{" 14:15 ", 14, 15, false},
		{"24:00", 0, 0, true},
		{"12:60", 0, 0, true},
		{"12", 0, 0, true},
		{"", 0, 0, true},
		{"invalid", 0, 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			h, m, err := hhmm(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("hhmm(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr && (h != tc.wantH || m != tc.wantM) {
				t.Errorf("hhmm(%q) = (%d, %d), want (%d, %d)", tc.in, h, m, tc.wantH, tc.wantM)
			}
		})
	}
}

func TestDayAndLastDay(t *testing.T) {
	// day helper truncates to UTC midnight
	loc := time.FixedZone("UTC+3", 3*3600)
	input := time.Date(2026, 10, 15, 14, 30, 45, 12345, loc)
	gotDay := day(input)
	wantDay := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	if !gotDay.Equal(wantDay) || gotDay.Location() != time.UTC {
		t.Errorf("day(%v) = %v, want %v", input, gotDay, wantDay)
	}

	// lastDay helper checks leap year February and different months
	tests := []struct {
		year  int
		month time.Month
		want  int
	}{
		{2024, time.February, 29}, // leap year
		{2000, time.February, 29}, // 400-year leap
		{2023, time.February, 28}, // regular year
		{1900, time.February, 28}, // 100-year non-leap
		{2026, time.January, 31},
		{2026, time.April, 30},
		{2026, time.July, 31},
		{2026, time.September, 30},
		{2026, time.December, 31},
	}

	for _, tc := range tests {
		got := lastDay(tc.year, tc.month)
		if got != tc.want {
			t.Errorf("lastDay(%d, %v) = %d, want %d", tc.year, tc.month, got, tc.want)
		}
	}
}

func TestRepeatMatches(t *testing.T) {
	sched := worktime.Default()
	sched.Holidays = []string{"2026-10-14"}                // Wednesday holiday
	start := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) // Monday

	// Daily with workdaysOnly
	rDaily := Repeat{
		Period:       "daily",
		Every:        1,
		WorkdaysOnly: true,
		StartsOn:     start,
	}
	// Monday 12.10 is work day
	if !rDaily.matches(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("Monday must match work day")
	}
	// Wednesday 14.10 is holiday
	if rDaily.matches(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("Holiday must not match work day")
	}
	// Saturday 17.10 is weekend
	if rDaily.matches(time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("Saturday must not match work day")
	}

	// Monthly on the 31st
	rMonthly := Repeat{
		Period:   "monthly",
		Every:    1,
		MonthDay: 31,
		StartsOn: start,
	}
	// October 31 matches (has 31 days)
	if !rMonthly.matches(time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("31 October must match")
	}
	// November 30 matches (clamped to last day 30)
	if !rMonthly.matches(time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("30 November must match clamped 31st")
	}
	// November 29 must not match
	if rMonthly.matches(time.Date(2026, 11, 29, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("29 November must not match")
	}

	// Yearly every 2 years
	rYearly := Repeat{
		Period:    "yearly",
		Every:     2,
		MonthDay:  15,
		YearMonth: 6,
		StartsOn:  start, // 2026
	}
	// 2026-06-15 matches
	if !rYearly.matches(time.Date(2026, 6, 15, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("2026-06-15 must match")
	}
	// 2027-06-15 does not match (every 2 years)
	if rYearly.matches(time.Date(2027, 6, 15, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("2027-06-15 must not match (every 2 years)")
	}
	// 2028-06-15 matches
	if !rYearly.matches(time.Date(2028, 6, 15, 0, 0, 0, 0, time.UTC), sched) {
		t.Error("2028-06-15 must match")
	}
}

func TestRepeatNext_OffAndLimits(t *testing.T) {
	sched := worktime.Default()
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.Local)

	// Repeat is off
	rOff := Repeat{
		On:       false,
		Period:   "daily",
		Every:    1,
		At:       "09:00",
		StartsOn: now,
	}
	if got := rOff.Next(now, 0, sched); got != nil {
		t.Errorf("expected nil for inactive repeat, got %v", got)
	}

	// Repeat reaches Times limit
	five := 5
	rLimit := Repeat{
		On:       true,
		Period:   "daily",
		Every:    1,
		At:       "09:00",
		StartsOn: now,
		Times:    &five,
	}
	if got := rLimit.Next(now, 5, sched); got != nil {
		t.Errorf("expected nil when made >= Times, got %v", got)
	}

	// Bad At time
	rBadTime := Repeat{
		On:       true,
		Period:   "daily",
		Every:    1,
		At:       "invalid",
		StartsOn: now,
	}
	if got := rBadTime.Next(now, 0, sched); got != nil {
		t.Errorf("expected nil for bad At time, got %v", got)
	}

	// Starts in future
	futureStart := now.AddDate(0, 0, 5)
	rFuture := Repeat{
		On:       true,
		Period:   "daily",
		Every:    1,
		At:       "09:00",
		StartsOn: futureStart,
	}
	gotFuture := rFuture.Next(now, 0, sched)
	if gotFuture == nil {
		t.Fatal("expected future next run, got nil")
	}
	wantDate := time.Date(futureStart.Year(), futureStart.Month(), futureStart.Day(), 9, 0, 0, 0, time.Local)
	if !gotFuture.Equal(wantDate) {
		t.Errorf("got %v, want %v", gotFuture, wantDate)
	}
}

func TestTemplateFieldsAndTask(t *testing.T) {
	// Template CanEdit
	tRead := Template{Shared: "read"}
	if tRead.CanEdit() {
		t.Error("shared=read must not be editable")
	}
	tFull := Template{Shared: "full"}
	if !tFull.CanEdit() {
		t.Error("shared=full must be editable")
	}
	tOwn := Template{}
	if !tOwn.CanEdit() {
		t.Error("own template must be editable")
	}

	// AfterDays and AfterHours
	tNoDeadline := Template{DeadlineAfter: nil}
	if tNoDeadline.AfterDays() != 0 || tNoDeadline.AfterHours() != 0 {
		t.Errorf("nil deadline after: got (%d, %d), want (0, 0)", tNoDeadline.AfterDays(), tNoDeadline.AfterHours())
	}

	mins := 1500 // 1 day (1440) + 1 hour (60)
	tDeadline := Template{DeadlineAfter: &mins}
	if tDeadline.AfterDays() != 1 || tDeadline.AfterHours() != 1 {
		t.Errorf("1500 mins: got (%d, %d), want (1, 1)", tDeadline.AfterDays(), tDeadline.AfterHours())
	}

	mins2 := 3000 // 2 days (2880) + 2 hours (120)
	tDeadline2 := Template{DeadlineAfter: &mins2}
	if tDeadline2.AfterDays() != 2 || tDeadline2.AfterHours() != 2 {
		t.Errorf("3000 mins: got (%d, %d), want (2, 2)", tDeadline2.AfterDays(), tDeadline2.AfterHours())
	}

	// NewTask creation
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.Local)
	tpl := Template{
		ID:                  "tpl-1",
		Title:               "Deploy",
		Description:         "Deploy to prod",
		ResponsibleID:       "u-2",
		Priority:            2,
		RequiresControl:     true,
		AllowChangeDeadline: true,
		MatchWorkTime:       true,
		DeadlineAfter:       &mins,
		Accomplices:         []string{"u-3"},
		Auditors:            []string{"u-4"},
		Checklist:           []string{"build", "test"},
		Params:              Params{AutoComplete: true},
	}

	task := tpl.NewTask(now)
	if task.Title != "Deploy" || task.TemplateID != "tpl-1" || task.Priority != 2 {
		t.Errorf("task fields mismatch: %+v", task)
	}
	if task.Deadline == nil {
		t.Fatal("expected non-nil deadline")
	}
	wantDL := now.Add(1500 * time.Minute)
	if !task.Deadline.Equal(wantDL) {
		t.Errorf("task deadline = %v, want %v", task.Deadline, wantDL)
	}
	if !task.AutoComplete || !task.RequiresControl || !task.AllowChangeDeadline {
		t.Errorf("task flags mismatch: %+v", task)
	}
}
