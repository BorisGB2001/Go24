package calendar

import (
	"slices"
	"testing"
	"time"
)

func TestCalendarRepetitionValidation(t *testing.T) {
	base := time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local) // Monday

	cases := []struct {
		name    string
		event   Event
		wantErr bool
		check   func(t *testing.T, e Event)
	}{
		{
			name:    "invalid repeat period",
			event:   Event{Start: base, End: base.Add(time.Hour), Repeat: "hourly"},
			wantErr: true,
		},
		{
			name:    "negative count",
			event:   Event{Start: base, End: base.Add(time.Hour), Repeat: "daily", Count: -1},
			wantErr: true,
		},
		{
			name:    "count over 999",
			event:   Event{Start: base, End: base.Add(time.Hour), Repeat: "daily", Count: 1000},
			wantErr: true,
		},
		{
			name:  "every out of range is reset to 1",
			event: Event{Start: base, End: base.Add(time.Hour), Repeat: "daily", Every: 0},
			check: func(t *testing.T, e Event) {
				if e.Every != 1 {
					t.Errorf("expected Every=1, got %d", e.Every)
				}
			},
		},
		{
			name:  "every > 365 is reset to 1",
			event: Event{Start: base, End: base.Add(time.Hour), Repeat: "daily", Every: 500},
			check: func(t *testing.T, e Event) {
				if e.Every != 1 {
					t.Errorf("expected Every=1, got %d", e.Every)
				}
			},
		},
		{
			name: "single meeting clears until and count",
			event: Event{
				Start: base, End: base.Add(time.Hour), Repeat: "",
				Count: 5, Until: &base,
			},
			check: func(t *testing.T, e Event) {
				if e.Count != 0 || e.Until != nil {
					t.Errorf("expected Count=0 and Until=nil, got Count=%d, Until=%v", e.Count, e.Until)
				}
			},
		},
		{
			name:    "weekly with invalid day number",
			event:   Event{Start: base, End: base.Add(time.Hour), Repeat: "weekly", Days: []int{0}},
			wantErr: true,
		},
		{
			name:    "weekly with day 8",
			event:   Event{Start: base, End: base.Add(time.Hour), Repeat: "weekly", Days: []int{8}},
			wantErr: true,
		},
		{
			name:  "weekly single day equal to start day is cleared",
			event: Event{Start: base, End: base.Add(time.Hour), Repeat: "weekly", Days: []int{1}}, // Monday
			check: func(t *testing.T, e Event) {
				if e.Days != nil {
					t.Errorf("expected nil Days for single start day, got %v", e.Days)
				}
			},
		},
		{
			name:  "weekly start day automatically included and sorted",
			event: Event{Start: base, End: base.Add(time.Hour), Repeat: "weekly", Days: []int{5, 3}}, // Mon (1) not in list
			check: func(t *testing.T, e Event) {
				if !slices.Equal(e.Days, []int{1, 3, 5}) {
					t.Errorf("expected [1 3 5], got %v", e.Days)
				}
			},
		},
		{
			name:  "weekly duplicates in days removed",
			event: Event{Start: base, End: base.Add(time.Hour), Repeat: "weekly", Days: []int{3, 3, 5}},
			check: func(t *testing.T, e Event) {
				if !slices.Equal(e.Days, []int{1, 3, 5}) {
					t.Errorf("expected [1 3 5], got %v", e.Days)
				}
			},
		},
		{
			name:  "monthly count sets until",
			event: Event{Start: base, End: base.Add(time.Hour), Repeat: "monthly", Every: 1, Count: 3},
			check: func(t *testing.T, e Event) {
				if e.Until == nil {
					t.Fatal("expected Until to be set")
				}
				want := time.Date(2026, 12, 19, 0, 0, 0, 0, time.Local)
				if !e.Until.Equal(want) {
					t.Errorf("expected Until %v, got %v", want, *e.Until)
				}
			},
		},
		{
			name:  "yearly count sets until",
			event: Event{Start: base, End: base.Add(time.Hour), Repeat: "yearly", Every: 2, Count: 3},
			check: func(t *testing.T, e Event) {
				if e.Until == nil {
					t.Fatal("expected Until to be set")
				}
				// 2026 (1st), 2028 (2nd), 2030 (3rd)
				want := time.Date(2030, 10, 19, 0, 0, 0, 0, time.Local)
				if !e.Until.Equal(want) {
					t.Errorf("expected Until %v, got %v", want, *e.Until)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.event
			err := e.repetition()
			if (err != nil) != tc.wantErr {
				t.Fatalf("repetition() error = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.check != nil {
				tc.check(t, e)
			}
		})
	}
}

func TestCalendarExpandMonthly(t *testing.T) {
	// Monthly meeting on the 15th
	start := time.Date(2026, 1, 15, 14, 0, 0, 0, time.Local)
	e := Event{
		Start:  start,
		End:    start.Add(2 * time.Hour),
		Repeat: "monthly",
		Every:  1,
	}
	if err := e.repetition(); err != nil {
		t.Fatal(err)
	}

	// Expand across 4 months
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)
	to := time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
	occurrences := e.expand(from, to)

	if len(occurrences) != 4 {
		t.Fatalf("expected 4 occurrences, got %d", len(occurrences))
	}
	for i, occ := range occurrences {
		wantMonth := time.Month(i + 1)
		if occ.Start.Month() != wantMonth || occ.Start.Day() != 15 || occ.Start.Hour() != 14 {
			t.Errorf("occurrence %d: got %v, want month %v day 15", i, occ.Start, wantMonth)
		}
		if occ.End.Sub(occ.Start) != 2*time.Hour {
			t.Errorf("occurrence %d: duration = %v, want 2h", i, occ.End.Sub(occ.Start))
		}
	}

	// With Skip
	e.Skip = []time.Time{time.Date(2026, 3, 15, 14, 0, 0, 0, time.Local)}
	occurrences = e.expand(from, to)
	if len(occurrences) != 3 {
		t.Fatalf("expected 3 occurrences after skip, got %d", len(occurrences))
	}
	for _, occ := range occurrences {
		if occ.Start.Month() == time.March {
			t.Errorf("skipped March occurrence was returned: %v", occ.Start)
		}
	}
}

func TestCalendarExpandYearly(t *testing.T) {
	start := time.Date(2024, 6, 1, 9, 30, 0, 0, time.Local)
	e := Event{
		Start:  start,
		End:    start.Add(time.Hour),
		Repeat: "yearly",
		Every:  1,
	}
	if err := e.repetition(); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2024, 1, 1, 0, 0, 0, 0, time.Local)
	to := time.Date(2028, 1, 1, 0, 0, 0, 0, time.Local)
	occurrences := e.expand(from, to)

	if len(occurrences) != 4 {
		t.Fatalf("expected 4 occurrences (2024, 2025, 2026, 2027), got %d", len(occurrences))
	}
	for i, occ := range occurrences {
		wantYear := 2024 + i
		if occ.Start.Year() != wantYear || occ.Start.Month() != time.June || occ.Start.Day() != 1 {
			t.Errorf("occurrence %d: got %v, want year %d", i, occ.Start, wantYear)
		}
	}
}

func TestCalendarExpandNonRepeating(t *testing.T) {
	start := time.Date(2026, 10, 20, 10, 0, 0, 0, time.Local)
	end := start.Add(time.Hour)
	e := Event{Start: start, End: end}

	// 1. Touches window fully inside
	res := e.expand(start.Add(-time.Hour), end.Add(time.Hour))
	if len(res) != 1 {
		t.Errorf("expected 1 event, got %d", len(res))
	}

	// 2. Touches window overlapping start
	res = e.expand(start.Add(30*time.Minute), end.Add(time.Hour))
	if len(res) != 1 {
		t.Errorf("expected 1 event for overlapping start, got %d", len(res))
	}

	// 3. Touches window overlapping end
	res = e.expand(start.Add(-time.Hour), start.Add(30*time.Minute))
	if len(res) != 1 {
		t.Errorf("expected 1 event for overlapping end, got %d", len(res))
	}

	// 4. Window before event
	res = e.expand(start.Add(-2*time.Hour), start.Add(-time.Hour))
	if len(res) != 0 {
		t.Errorf("expected 0 events before window, got %d", len(res))
	}

	// 5. Window after event
	res = e.expand(end.Add(time.Hour), end.Add(2*time.Hour))
	if len(res) != 0 {
		t.Errorf("expected 0 events after window, got %d", len(res))
	}

	// 6. Zero duration event right at boundary
	zeroEv := Event{Start: start, End: start}
	res = zeroEv.expand(start, start.Add(time.Hour))
	if len(res) != 1 {
		t.Errorf("expected zero-duration event at boundary to be included, got %d", len(res))
	}
}

func TestCalendarOccurrence(t *testing.T) {
	start := time.Date(2026, 10, 19, 10, 0, 0, 0, time.Local) // Monday
	e := Event{
		Start:  start,
		End:    start.Add(time.Hour),
		Repeat: "daily",
		Every:  1,
	}
	if err := e.repetition(); err != nil {
		t.Fatal(err)
	}

	// Valid occurrence
	target := start.AddDate(0, 0, 3)
	occ := e.Occurrence(target)
	if occ == nil {
		t.Fatalf("expected occurrence at %v", target)
	}
	if !occ.Start.Equal(target) {
		t.Errorf("got start %v, want %v", occ.Start, target)
	}

	// Non-occurrence (off hours)
	if occ := e.Occurrence(target.Add(30 * time.Minute)); occ != nil {
		t.Errorf("expected nil for off-hour occurrence, got %v", occ)
	}

	// Skipped occurrence
	e.Skip = []time.Time{target}
	if occ := e.Occurrence(target); occ != nil {
		t.Errorf("expected nil for skipped occurrence, got %v", occ)
	}
}

func TestCalendarAllDayExpandAndOnDay(t *testing.T) {
	start := time.Date(2026, 10, 19, 0, 0, 0, 0, time.Local) // Monday
	end := time.Date(2026, 10, 19, 23, 59, 59, 0, time.Local)
	e := Event{
		Start:  start,
		End:    end,
		AllDay: true,
		Repeat: "weekly",
		Days:   []int{1, 4}, // Monday and Thursday
	}
	if err := e.repetition(); err != nil {
		t.Fatal(err)
	}

	if !e.OnDay(1) || !e.OnDay(4) {
		t.Errorf("OnDay(1)=%v, OnDay(4)=%v; both should be true", e.OnDay(1), e.OnDay(4))
	}
	if e.OnDay(2) || e.OnDay(5) {
		t.Errorf("OnDay(2)=%v, OnDay(5)=%v; both should be false", e.OnDay(2), e.OnDay(5))
	}

	// Default OnDay with no days specified
	singleAllDay := Event{Start: start, End: end, AllDay: true}
	if !singleAllDay.OnDay(1) || singleAllDay.OnDay(2) {
		t.Errorf("singleAllDay.OnDay(1)=%v, OnDay(2)=%v", singleAllDay.OnDay(1), singleAllDay.OnDay(2))
	}

	// OnDay for timed event in custom timezone that shifts weekday
	// Sunday 23:30 UTC is Monday in Tokyo (UTC+9)
	sunUTC := time.Date(2026, 10, 18, 23, 30, 0, 0, time.UTC)
	timedTokyo := Event{Start: sunUTC, End: sunUTC.Add(time.Hour), TZ: "Asia/Tokyo"}
	if !timedTokyo.OnDay(1) { // Monday in Tokyo
		t.Errorf("expected timedTokyo.OnDay(1)=true on Monday in Tokyo")
	}
	if timedTokyo.OnDay(7) { // Sunday
		t.Errorf("expected timedTokyo.OnDay(7)=false on Sunday in Tokyo")
	}

	// occurrence default branch (non-repeating)
	nonRepeating := Event{Start: start, End: end, SeriesStart: start}
	st, en, ok := nonRepeating.occurrence(0)
	if !ok || !st.Equal(start) || !en.Equal(end) {
		t.Errorf("occurrence for non-repeating = %v, %v, %v", st, en, ok)
	}

	occ := e.expand(start, start.AddDate(0, 0, 14))
	// In 2 weeks: Mon(19), Thu(22), Mon(26), Thu(29) = 4 occurrences
	if len(occ) != 4 {
		t.Fatalf("expected 4 all-day occurrences, got %d", len(occ))
	}
	for _, o := range occ {
		if !o.AllDay {
			t.Errorf("expected occurrence to preserve AllDay, got false: %v", o)
		}
	}
}

func TestTimeZones(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	// ValidTZ
	for _, valid := range []string{"", "UTC", "Europe/Moscow", "America/New_York", "Asia/Tokyo"} {
		if !ValidTZ(valid) {
			t.Errorf("ValidTZ(%q) must be true", valid)
		}
	}
	for _, invalid := range []string{"Local", "Mars/Colony", "Invalid/TZ", "Foo/Bar"} {
		if ValidTZ(invalid) {
			t.Errorf("ValidTZ(%q) must be false", invalid)
		}
	}

	// Event.Loc
	eDefault := Event{}
	if loc := eDefault.Loc(); loc != time.Local {
		t.Errorf("expected Loc() to be time.Local, got %v", loc)
	}
	eInvalid := Event{TZ: "Invalid/Zone"}
	if loc := eInvalid.Loc(); loc != time.Local {
		t.Errorf("expected invalid TZ to fallback to time.Local, got %v", loc)
	}
	eNY := Event{TZ: "America/New_York"}
	if loc := eNY.Loc(); loc.String() != "America/New_York" {
		t.Errorf("expected America/New_York, got %v", loc)
	}

	// Event.InZone
	utcTime := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	inZone := eNY.InZone(utcTime)
	if inZone.Location().String() != "America/New_York" {
		t.Errorf("expected InZone location America/New_York, got %v", inZone.Location())
	}

	// Zones
	zones := Zones(now, "America/Argentina/Buenos_Aires")
	if len(zones) != len(TimeZones)+1 {
		t.Fatalf("expected len(zones)=%d, got %d", len(TimeZones)+1, len(zones))
	}
	// Check sorting by offset
	for i := 0; i < len(zones)-1; i++ {
		_, offA := now.In(mustLoc(zones[i].Name)).Zone()
		_, offB := now.In(mustLoc(zones[i+1].Name)).Zone()
		if offA > offB {
			t.Errorf("zones not sorted by offset: %s (%d) > %s (%d)", zones[i].Name, offA, zones[i+1].Name, offB)
		}
	}
}

func TestCalendarEventHelpers(t *testing.T) {
	// Paint
	if got := (Event{Color: "#112233", CalColor: "#445566"}).Paint(); got != "#112233" {
		t.Errorf("Paint() with Color = %s, want #112233", got)
	}
	if got := (Event{CalColor: "#445566"}).Paint(); got != "#445566" {
		t.Errorf("Paint() fallback to CalColor = %s, want #445566", got)
	}
	if got := (Event{}).Paint(); got != "" {
		t.Errorf("Paint() empty = %s, want empty", got)
	}

	// Mine
	e := Event{OwnerID: "user-123"}
	if !e.Mine("user-123") {
		t.Error("Mine(user-123) should be true")
	}
	if e.Mine("user-456") {
		t.Error("Mine(user-456) should be false")
	}

	// WeekDays
	days := Event{}.WeekDays()
	if !slices.Equal(days, []int{1, 2, 3, 4, 5, 6, 7}) {
		t.Errorf("WeekDays() = %v", days)
	}

	// WeekDay
	// 2026-10-19 is Monday (1), 2026-10-25 is Sunday (7)
	for d := 1; d <= 7; d++ {
		date := time.Date(2026, 10, 18+d, 12, 0, 0, 0, time.Local)
		if got := WeekDay(date); got != d {
			t.Errorf("WeekDay(%v) = %d, want %d", date, got, d)
		}
	}

	// sortEvents: AllDay events first, then by Start
	t1 := time.Date(2026, 10, 20, 10, 0, 0, 0, time.Local)
	t2 := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	list := []Event{
		{ID: "normal-late", Start: t2, AllDay: false},
		{ID: "allday-late", Start: t2, AllDay: true},
		{ID: "normal-early", Start: t1, AllDay: false},
		{ID: "allday-early", Start: t1, AllDay: true},
	}
	sortEvents(list)
	wantOrder := []string{"allday-early", "allday-late", "normal-early", "normal-late"}
	for i, ev := range list {
		if ev.ID != wantOrder[i] {
			t.Errorf("sortEvents[%d] = %s, want %s", i, ev.ID, wantOrder[i])
		}
	}

	// dayBefore
	if got := dayBefore(time.Date(2026, 10, 15, 0, 0, 0, 0, time.Local)); got != "2026-10-14" {
		t.Errorf("dayBefore() = %s, want 2026-10-14", got)
	}
	if got := dayBefore(time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local)); got != "2025-12-31" {
		t.Errorf("dayBefore(year rollover) = %s, want 2025-12-31", got)
	}
	if got := dayBefore(time.Date(2024, 3, 1, 0, 0, 0, 0, time.Local)); got != "2024-02-29" {
		t.Errorf("dayBefore(leap year) = %s, want 2024-02-29", got)
	}

	// Reminds: ascending, deduplicated, negatives removed, >28d removed, max 10
	inReminds := []int{15, 0, -5, 10, 15, 60 * 24 * 30, 20, 30, 60, 120, 1440, 2880, 5, 25, 35}
	gotReminds := Reminds(inReminds)
	if len(gotReminds) > 10 {
		t.Errorf("Reminds() returned %d items, max is 10", len(gotReminds))
	}
	for i := 0; i < len(gotReminds)-1; i++ {
		if gotReminds[i] >= gotReminds[i+1] {
			t.Errorf("Reminds() not strictly ascending: %v", gotReminds)
		}
	}
	if slices.Contains(gotReminds, -5) || slices.Contains(gotReminds, 60*24*30) {
		t.Errorf("Reminds() contains invalid values: %v", gotReminds)
	}
}
