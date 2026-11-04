package booking

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIsoWeekday verifies ISO weekday mapping (Monday = 1, ..., Sunday = 7).
func TestIsoWeekday(t *testing.T) {
	tests := []struct {
		year  int
		month time.Month
		day   int
		want  int
	}{
		{2026, time.October, 19, 1},  // Monday
		{2026, time.October, 20, 2},  // Tuesday
		{2026, time.October, 21, 3},  // Wednesday
		{2026, time.October, 22, 4},  // Thursday
		{2026, time.October, 23, 5},  // Friday
		{2026, time.October, 24, 6},  // Saturday
		{2026, time.October, 25, 7},  // Sunday
		{2024, time.February, 29, 4}, // Leap day Thursday
	}
	for _, tc := range tests {
		tm := time.Date(tc.year, tc.month, tc.day, 12, 0, 0, 0, time.UTC)
		if got := isoWeekday(tm); got != tc.want {
			t.Errorf("isoWeekday(%v) = %d; want %d", tm, got, tc.want)
		}
	}
}

// TestResourceWorksOn tests weekday matching and date-off exclusion.
func TestResourceWorksOn(t *testing.T) {
	loc := time.UTC
	mon := time.Date(2026, 10, 19, 10, 0, 0, 0, loc) // Monday
	tue := mon.AddDate(0, 0, 1)                      // Tuesday
	wed := mon.AddDate(0, 0, 2)                      // Wednesday
	sat := mon.AddDate(0, 0, 5)                      // Saturday
	sun := mon.AddDate(0, 0, 6)                      // Sunday

	offWed := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	r := Resource{
		Days:     []int{1, 2, 3, 4, 5},
		OffDates: []time.Time{offWed},
	}

	if !r.WorksOn(mon) {
		t.Errorf("expected WorksOn Monday to be true")
	}
	if !r.WorksOn(tue) {
		t.Errorf("expected WorksOn Tuesday to be true")
	}
	if r.WorksOn(wed) {
		t.Errorf("expected WorksOn Wednesday to be false (off date)")
	}
	if r.WorksOn(sat) {
		t.Errorf("expected WorksOn Saturday to be false (weekend)")
	}
	if r.WorksOn(sun) {
		t.Errorf("expected WorksOn Sunday to be false (weekend)")
	}
}

// TestResourceIsOff tests matching dates regardless of time-of-day.
func TestResourceIsOff(t *testing.T) {
	off := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	r := Resource{OffDates: []time.Time{off}}

	tests := []struct {
		time time.Time
		want bool
	}{
		{time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC), true},
		{time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC), true},
		{time.Date(2026, 11, 4, 23, 59, 59, 0, time.UTC), true},
		{time.Date(2026, 11, 3, 23, 59, 59, 0, time.UTC), false},
		{time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC), false},
		{time.Date(2025, 11, 4, 12, 0, 0, 0, time.UTC), false},
		{time.Date(2026, 12, 4, 12, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range tests {
		if got := r.IsOff(tc.time); got != tc.want {
			t.Errorf("r.IsOff(%v) = %v; want %v", tc.time, got, tc.want)
		}
	}
}

// TestResourceHoursOn tests standard fallback vs day-specific hours.
func TestResourceHoursOn(t *testing.T) {
	mon := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) // Monday (1)
	fri := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC) // Friday (5)
	sat := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC) // Saturday (6)

	r := Resource{
		From:     9 * 60,
		To:       18 * 60,
		DayHours: map[int][2]int{5: {10 * 60, 16 * 60}},
	}

	from, to := r.HoursOn(mon)
	if from != 9*60 || to != 18*60 {
		t.Errorf("Monday hours = %d, %d; want %d, %d", from, to, 9*60, 18*60)
	}

	from, to = r.HoursOn(fri)
	if from != 10*60 || to != 16*60 {
		t.Errorf("Friday hours = %d, %d; want %d, %d", from, to, 10*60, 16*60)
	}

	from, to = r.HoursOn(sat)
	if from != 9*60 || to != 18*60 {
		t.Errorf("Saturday hours = %d, %d; want %d, %d", from, to, 9*60, 18*60)
	}
}

// TestResourceInBreak tests boundary and overlap conditions with breaks.
func TestResourceInBreak(t *testing.T) {
	r := Resource{BreakFrom: 13 * 60, BreakTo: 14 * 60} // 780 to 840

	tests := []struct {
		name string
		a, b int
		want bool
	}{
		{"well before", 10 * 60, 11 * 60, false},
		{"abutting break start", 12 * 60, 13 * 60, false},
		{"overlapping break start", 12*60 + 30, 13*60 + 30, true},
		{"inside break", 13*60 + 15, 13*60 + 45, true},
		{"exact match", 13 * 60, 14 * 60, true},
		{"encompassing break", 12 * 60, 15 * 60, true},
		{"overlapping break end", 13*60 + 30, 14*60 + 30, true},
		{"abutting break end", 14 * 60, 15 * 60, false},
		{"well after", 15 * 60, 16 * 60, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := r.InBreak(tc.a, tc.b); got != tc.want {
				t.Errorf("r.InBreak(%d, %d) = %v; want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}

	// No break configured: BreakFrom == BreakTo
	rNoBreak := Resource{BreakFrom: 0, BreakTo: 0}
	if rNoBreak.InBreak(0, 60) {
		t.Errorf("InBreak with 0..0 break should be false")
	}

	// Inverted break: BreakFrom > BreakTo
	rInverted := Resource{BreakFrom: 840, BreakTo: 780}
	if rInverted.InBreak(750, 850) {
		t.Errorf("InBreak with inverted break should be false")
	}
}

// TestResourceOwnHours tests string formatting of weekday own hours.
func TestResourceOwnHours(t *testing.T) {
	r := Resource{
		DayHours: map[int][2]int{
			1: {9 * 60, 18 * 60},
			5: {8*60 + 30, 16*60 + 45},
			6: {0, 23*60 + 59},
		},
	}

	if got := r.OwnFrom(1); got != "09:00" {
		t.Errorf("OwnFrom(1) = %q; want %q", got, "09:00")
	}
	if got := r.OwnTo(1); got != "18:00" {
		t.Errorf("OwnTo(1) = %q; want %q", got, "18:00")
	}

	if got := r.OwnFrom(5); got != "08:30" {
		t.Errorf("OwnFrom(5) = %q; want %q", got, "08:30")
	}
	if got := r.OwnTo(5); got != "16:45" {
		t.Errorf("OwnTo(5) = %q; want %q", got, "16:45")
	}

	if got := r.OwnFrom(6); got != "00:00" {
		t.Errorf("OwnFrom(6) = %q; want %q", got, "00:00")
	}
	if got := r.OwnTo(6); got != "23:59" {
		t.Errorf("OwnTo(6) = %q; want %q", got, "23:59")
	}

	// Weekday without custom hours
	if got := r.OwnFrom(2); got != "" {
		t.Errorf("OwnFrom(2) = %q; want %q", got, "")
	}
	if got := r.OwnTo(2); got != "" {
		t.Errorf("OwnTo(2) = %q; want %q", got, "")
	}
}

// TestBookingTakes tests resource membership check in primary and extras.
func TestBookingTakes(t *testing.T) {
	b := Booking{
		ResourceID: "res-primary",
		Extras:     []string{"res-extra1", "res-extra2"},
	}

	if !b.Takes("res-primary") {
		t.Errorf("expected Takes(res-primary) to be true")
	}
	if !b.Takes("res-extra1") {
		t.Errorf("expected Takes(res-extra1) to be true")
	}
	if !b.Takes("res-extra2") {
		t.Errorf("expected Takes(res-extra2) to be true")
	}
	if b.Takes("res-other") {
		t.Errorf("expected Takes(res-other) to be false")
	}
	if b.Takes("") {
		t.Errorf("expected Takes(\"\") to be false")
	}

	bNoExtras := Booking{ResourceID: "res-only"}
	if !bNoExtras.Takes("res-only") {
		t.Errorf("expected Takes(res-only) to be true")
	}
	if bNoExtras.Takes("res-other") {
		t.Errorf("expected Takes(res-other) to be false")
	}
}

// TestNullable tests conversion of empty string to nil.
func TestNullable(t *testing.T) {
	if got := nullable(""); got != nil {
		t.Errorf("nullable(\"\") = %v; want nil", got)
	}
	if got := nullable("abc"); got != "abc" {
		t.Errorf("nullable(\"abc\") = %v; want \"abc\"", got)
	}
}

// TestSaveResourceValidation tests pre-database validation in SaveResource.
func TestSaveResourceValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{} // nil db; validation fails before any db access

	valid := Resource{
		Name: "Doctor",
		Slot: 30,
		Days: []int{1, 2, 3, 4, 5},
		From: 9 * 60,
		To:   18 * 60,
	}

	tests := []struct {
		name    string
		modify  func(r *Resource)
		wantErr error
	}{
		{"empty name", func(r *Resource) { r.Name = "" }, ErrName},
		{"whitespace name", func(r *Resource) { r.Name = "   \t\n" }, ErrName},
		{"empty days", func(r *Resource) { r.Days = nil }, ErrSchedule},
		{"invalid days only", func(r *Resource) { r.Days = []int{0, 8, -1} }, ErrSchedule},
		{"slot too small", func(r *Resource) { r.Slot = 4 }, ErrSchedule},
		{"slot zero", func(r *Resource) { r.Slot = 0 }, ErrSchedule},
		{"slot negative", func(r *Resource) { r.Slot = -10 }, ErrSchedule},
		{"slot too big", func(r *Resource) { r.Slot = 481 }, ErrSchedule},
		{"from negative", func(r *Resource) { r.From = -1 }, ErrSchedule},
		{"to beyond 1440", func(r *Resource) { r.To = 1441 }, ErrSchedule},
		{"from equal to", func(r *Resource) { r.From = 600; r.To = 600 }, ErrSchedule},
		{"from greater than to", func(r *Resource) { r.From = 700; r.To = 600 }, ErrSchedule},
		{"remind hours negative", func(r *Resource) { r.RemindHours = -1 }, ErrSchedule},
		{"remind hours beyond 336", func(r *Resource) { r.RemindHours = 337 }, ErrSchedule},
		{"late minutes negative", func(r *Resource) { r.LateMinutes = -1 }, ErrSchedule},
		{"late minutes beyond 240", func(r *Resource) { r.LateMinutes = 241 }, ErrSchedule},
		{"break from negative", func(r *Resource) { r.BreakFrom = -1; r.BreakTo = 60 }, ErrSchedule},
		{"break to beyond 1440", func(r *Resource) { r.BreakFrom = 60; r.BreakTo = 1441 }, ErrSchedule},
		{"break from greater than break to", func(r *Resource) { r.BreakFrom = 840; r.BreakTo = 780 }, ErrSchedule},
		{"confirm hours negative", func(r *Resource) { r.ConfirmHours = -1 }, ErrConfirm},
		{"confirm hours beyond 336", func(r *Resource) { r.ConfirmHours = 337 }, ErrConfirm},
		{"confirm alert negative", func(r *Resource) { r.ConfirmAlert = -1 }, ErrConfirm},
		{"confirm alert equal to confirm hours", func(r *Resource) { r.ConfirmHours = 24; r.ConfirmAlert = 24 }, ErrConfirm},
		{"confirm alert greater than confirm hours", func(r *Resource) { r.ConfirmHours = 24; r.ConfirmAlert = 48 }, ErrConfirm},
		{"day hours from negative", func(r *Resource) {
			r.DayHours = map[int][2]int{1: {-1, 18 * 60}}
		}, ErrSchedule},
		{"day hours to beyond 1440", func(r *Resource) {
			r.DayHours = map[int][2]int{1: {9 * 60, 1441}}
		}, ErrSchedule},
		{"day hours from equal to", func(r *Resource) {
			r.DayHours = map[int][2]int{1: {600, 600}}
		}, ErrSchedule},
		{"day hours from greater than to", func(r *Resource) {
			r.DayHours = map[int][2]int{1: {700, 600}}
		}, ErrSchedule},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := valid
			tc.modify(&r)
			_, err := s.SaveResource(ctx, "user", r)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("SaveResource() error = %v; want %v", err, tc.wantErr)
			}
		})
	}
}

// TestSaveTypeValidation tests name validation in SaveType.
func TestSaveTypeValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{}

	for _, bad := range []string{"", "   ", "\t\n"} {
		if _, err := s.SaveType(ctx, "", bad); !errors.Is(err, ErrName) {
			t.Errorf("SaveType(empty) = %v; want ErrName", err)
		}
		if _, err := s.SaveType(ctx, "type-1", bad); !errors.Is(err, ErrName) {
			t.Errorf("SaveType(id, empty) = %v; want ErrName", err)
		}
	}
}

// TestOfCardValidation tests early return on unknown entity names.
func TestOfCardValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{}

	for _, entity := range []string{"", "lead", "task", "invoice", "unknown"} {
		bookings, err := s.OfCard(ctx, entity, "123")
		if err != nil || bookings != nil {
			t.Errorf("OfCard(%q) = %v, %v; want nil, nil", entity, bookings, err)
		}
	}
}

// TestStoreFreeNonWorkingDay verifies Free, FreeFor, and FreeMoving return nil on non-working days.
func TestStoreFreeNonWorkingDay(t *testing.T) {
	ctx := context.Background()
	s := &Store{} // nil db; returns before query if !WorksOn

	loc := time.UTC
	r := Resource{
		Slot:     30,
		Days:     []int{1, 2, 3, 4, 5}, // Mon-Fri
		From:     9 * 60,
		To:       18 * 60,
		OffDates: []time.Time{time.Date(2026, 10, 21, 0, 0, 0, 0, loc)}, // Wed off
	}

	sat := time.Date(2026, 10, 24, 0, 0, 0, 0, loc) // Saturday
	wedOff := time.Date(2026, 10, 21, 0, 0, 0, 0, loc)

	// Free on Saturday
	free, err := s.Free(ctx, r, sat, loc)
	if err != nil || free != nil {
		t.Errorf("Free(Saturday) = %v, %v; want nil, nil", free, err)
	}

	// FreeFor on OffDate
	free, err = s.FreeFor(ctx, r, wedOff, loc, 60)
	if err != nil || free != nil {
		t.Errorf("FreeFor(OffDate) = %v, %v; want nil, nil", free, err)
	}

	// FreeMoving on Saturday
	b := Booking{
		ID:    "b1",
		Start: time.Date(2026, 10, 20, 10, 0, 0, 0, loc),
		End:   time.Date(2026, 10, 20, 11, 0, 0, 0, loc),
	}
	free, err = s.FreeMoving(ctx, b, r, sat, loc)
	if err != nil || free != nil {
		t.Errorf("FreeMoving(Saturday) = %v, %v; want nil, nil", free, err)
	}
}
