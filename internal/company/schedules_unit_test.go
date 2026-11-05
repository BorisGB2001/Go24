package company

import (
	"slices"
	"strings"
	"testing"
	"time"

	"go24/internal/i18n"
	"go24/internal/worktime"
)

// TestValidClock: validClock checks if a string is a valid hh:mm format.
func TestValidClock(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"00:00", true},
		{"09:30", true},
		{"9:00", true}, // time.Parse allows single digit hour
		{"18:00", true},
		{"23:59", true},
		{"24:00", false},
		{"12:60", false},
		{"-01:00", false},
		{"abc", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := validClock(tc.input); got != tc.want {
			t.Errorf("validClock(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// TestShiftLen: shiftLen calculates shift duration in minutes, crossing midnight when needed.
func TestShiftLen(t *testing.T) {
	cases := []struct {
		name string
		a, b int
		want int
	}{
		{"standard daytime", 540, 1080, 540},                // 09:00 to 18:00 -> 9 hours (540 min)
		{"overnight shift", 1320, 360, 480},                 // 22:00 to 06:00 -> 8 hours (480 min)
		{"midnight end", 1200, 0, 240},                      // 20:00 to 00:00 -> 4 hours (240 min)
		{"midnight start", 0, 480, 480},                     // 00:00 to 08:00 -> 8 hours (480 min)
		{"same start and end wraps 24h", 480, 480, 24 * 60}, // 08:00 to 08:00 -> 24 hours
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shiftLen(tc.a, tc.b); got != tc.want {
				t.Errorf("shiftLen(%d, %d) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestWorkScheduleClean: clean validates names, kinds, times, shifts and cleans unneeded fields.
func TestWorkScheduleClean(t *testing.T) {
	validUUID1 := "11111111-1111-1111-1111-111111111111"
	validUUID2 := "22222222-2222-2222-2222-222222222222"

	t.Run("invalid names", func(t *testing.T) {
		ws := WorkSchedule{Name: "   ", Kind: "flexible"}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_name").Error() {
			t.Errorf("empty name err = %v, want err.wschedule_name", err)
		}
		ws = WorkSchedule{Name: strings.Repeat("a", 201), Kind: "flexible"}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_name").Error() {
			t.Errorf("too long name err = %v, want err.wschedule_name", err)
		}
	})

	t.Run("invalid kind", func(t *testing.T) {
		ws := WorkSchedule{Name: "General", Kind: "unknown"}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.bad_form").Error() {
			t.Errorf("bad kind err = %v, want err.bad_form", err)
		}
	})

	t.Run("invalid clock fields and tolerance", func(t *testing.T) {
		ws := WorkSchedule{Name: "General", Kind: "flexible", LateAfter: "bad"}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_time").Error() {
			t.Errorf("bad LateAfter err = %v, want err.wschedule_time", err)
		}
		ws = WorkSchedule{Name: "General", Kind: "flexible", Tolerance: -1}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_time").Error() {
			t.Errorf("negative tolerance err = %v, want err.wschedule_time", err)
		}
		ws = WorkSchedule{Name: "General", Kind: "flexible", Tolerance: 721}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_time").Error() {
			t.Errorf("excessive tolerance err = %v, want err.wschedule_time", err)
		}
	})

	t.Run("fixed schedule validation", func(t *testing.T) {
		// Empty times
		ws := WorkSchedule{Name: "Office", Kind: "fixed"}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_days").Error() {
			t.Errorf("fixed empty times err = %v, want err.wschedule_days", err)
		}

		// Times with empty days
		ws = WorkSchedule{Name: "Office", Kind: "fixed", Times: []ScheduleTime{{Days: nil, Start: "09:00", End: "18:00"}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_days").Error() {
			t.Errorf("fixed empty days err = %v, want err.wschedule_days", err)
		}

		// End <= Start
		ws = WorkSchedule{Name: "Office", Kind: "fixed", Times: []ScheduleTime{{Days: []int{1}, Start: "18:00", End: "09:00"}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_time").Error() {
			t.Errorf("fixed End <= Start err = %v, want err.wschedule_time", err)
		}

		// Break negative or Break >= duration
		ws = WorkSchedule{Name: "Office", Kind: "fixed", Times: []ScheduleTime{{Days: []int{1}, Start: "09:00", End: "10:00", Break: -5}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_time").Error() {
			t.Errorf("fixed negative break err = %v, want err.wschedule_time", err)
		}
		ws = WorkSchedule{Name: "Office", Kind: "fixed", Times: []ScheduleTime{{Days: []int{1}, Start: "09:00", End: "10:00", Break: 60}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_time").Error() {
			t.Errorf("fixed break >= duration err = %v, want err.wschedule_time", err)
		}

		// Day out of range or duplicate day
		ws = WorkSchedule{Name: "Office", Kind: "fixed", Times: []ScheduleTime{{Days: []int{7}, Start: "09:00", End: "18:00"}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_days").Error() {
			t.Errorf("fixed day 7 err = %v, want err.wschedule_days", err)
		}
		ws = WorkSchedule{
			Name: "Office", Kind: "fixed",
			Times: []ScheduleTime{
				{Days: []int{1, 2}, Start: "09:00", End: "18:00"},
				{Days: []int{2, 3}, Start: "09:00", End: "18:00"},
			},
		}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_days").Error() {
			t.Errorf("fixed duplicate day 2 err = %v, want err.wschedule_days", err)
		}

		// Valid fixed schedule sorts days, clears shifts and resets tolerance
		ws = WorkSchedule{
			Name:      "Office Standard",
			Kind:      "fixed",
			Tolerance: 15,
			Shifts:    []Shift{{ID: 1, Name: "Ignored"}},
			Times: []ScheduleTime{
				{Days: []int{5, 1, 3}, Start: "09:00", End: "18:00", Break: 60},
			},
			NotifyTo: []string{validUUID1, "invalid", validUUID1, validUUID2},
		}
		if err := ws.clean(); err != nil {
			t.Fatalf("valid fixed clean err = %v", err)
		}
		if ws.Tolerance != 0 || len(ws.Shifts) != 0 {
			t.Errorf("fixed schedule should clear shifts and reset tolerance")
		}
		if !slices.Equal(ws.Times[0].Days, []int{1, 3, 5}) {
			t.Errorf("fixed schedule should sort days, got %v", ws.Times[0].Days)
		}
		if !slices.Equal(ws.NotifyTo, []string{validUUID1, validUUID2}) {
			t.Errorf("NotifyTo not cleaned: %v", ws.NotifyTo)
		}
	})

	t.Run("shift schedule validation", func(t *testing.T) {
		// Empty shifts
		ws := WorkSchedule{Name: "Warehouse", Kind: "shift"}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_shift").Error() {
			t.Errorf("shift empty shifts err = %v, want err.wschedule_shift", err)
		}

		// Only empty shift names
		ws = WorkSchedule{Name: "Warehouse", Kind: "shift", Shifts: []Shift{{Name: "   "}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_shift").Error() {
			t.Errorf("shift only blank names err = %v, want err.wschedule_shift", err)
		}

		// Shift name too long
		ws = WorkSchedule{Name: "Warehouse", Kind: "shift", Shifts: []Shift{{Name: strings.Repeat("s", 101), Start: "08:00", End: "20:00"}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_shift").Error() {
			t.Errorf("shift name too long err = %v, want err.wschedule_shift", err)
		}

		// Shift invalid times (Start == End)
		ws = WorkSchedule{Name: "Warehouse", Kind: "shift", Shifts: []Shift{{Name: "Day", Start: "08:00", End: "08:00"}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_shift").Error() {
			t.Errorf("shift Start == End err = %v, want err.wschedule_shift", err)
		}

		// Shift break >= shiftLen
		ws = WorkSchedule{Name: "Warehouse", Kind: "shift", Shifts: []Shift{{Name: "Night", Start: "22:00", End: "06:00", Break: 480}}}
		if err := ws.clean(); err == nil || err.Error() != i18n.E("err.wschedule_shift").Error() {
			t.Errorf("shift break >= shiftLen err = %v, want err.wschedule_shift", err)
		}

		// Valid shift schedule clears Times, LateAfter, LeaveBefore
		ws = WorkSchedule{
			Name:        "Warehouse Shifts",
			Kind:        "shift",
			LateAfter:   "09:15",
			LeaveBefore: "17:45",
			Times:       []ScheduleTime{{Days: []int{1}, Start: "09:00", End: "18:00"}},
			Tolerance:   10,
			Shifts: []Shift{
				{Name: "   "}, // skipped
				{Name: "Day Shift", Start: "08:00", End: "20:00", Break: 60},
				{Name: "Night Shift", Start: "20:00", End: "08:00", Break: 60},
			},
		}
		if err := ws.clean(); err != nil {
			t.Fatalf("valid shift clean err = %v", err)
		}
		if len(ws.Times) != 0 || ws.LateAfter != "" || ws.LeaveBefore != "" {
			t.Errorf("shift schedule should clear Times, LateAfter, LeaveBefore")
		}
		if len(ws.Shifts) != 2 {
			t.Errorf("shift schedule should have kept 2 valid shifts, got %d", len(ws.Shifts))
		}
	})

	t.Run("flexible schedule validation", func(t *testing.T) {
		ws := WorkSchedule{
			Name:        "Remote Freelance",
			Kind:        "flexible",
			LateAfter:   "10:00",
			LeaveBefore: "16:00",
			Tolerance:   30,
			Times:       []ScheduleTime{{Days: []int{1}, Start: "09:00", End: "18:00"}},
			Shifts:      []Shift{{Name: "S1", Start: "08:00", End: "16:00"}},
		}
		if err := ws.clean(); err != nil {
			t.Fatalf("flexible clean err = %v", err)
		}
		if len(ws.Times) != 0 || len(ws.Shifts) != 0 || ws.LateAfter != "" || ws.LeaveBefore != "" || ws.Tolerance != 0 {
			t.Errorf("flexible schedule should reset all times, shifts, tolerance, and limits")
		}
	})
}

// TestShiftByID: ShiftByID retrieves a shift pointer or nil.
func TestShiftByID(t *testing.T) {
	ws := WorkSchedule{
		Shifts: []Shift{
			{ID: 10, Name: "Morning"},
			{ID: 20, Name: "Evening"},
		},
	}
	if got := ws.ShiftByID(10); got == nil || got.Name != "Morning" {
		t.Errorf("ShiftByID(10) = %v, want Morning", got)
	}
	if got := ws.ShiftByID(20); got == nil || got.Name != "Evening" {
		t.Errorf("ShiftByID(20) = %v, want Evening", got)
	}
	if got := ws.ShiftByID(99); got != nil {
		t.Errorf("ShiftByID(99) = %v, want nil", got)
	}
}

// TestDayPlanMinutes: Minutes returns planned length without break.
func TestDayPlanMinutes(t *testing.T) {
	cases := []struct {
		name string
		plan DayPlan
		want int
	}{
		{"not working day", DayPlan{Work: false, Start: "09:00", End: "18:00"}, 0},
		{"invalid start", DayPlan{Work: true, Start: "invalid", End: "18:00"}, 0},
		{"invalid end", DayPlan{Work: true, Start: "09:00", End: "invalid"}, 0},
		{"standard daytime", DayPlan{Work: true, Start: "09:00", End: "18:00", Break: 60}, 480},
		{"overnight shift", DayPlan{Work: true, Start: "22:00", End: "06:00", Break: 30}, 450},
		{"break greater than duration", DayPlan{Work: true, Start: "09:00", End: "10:00", Break: 90}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.plan.Minutes(); got != tc.want {
				t.Errorf("DayPlan.Minutes() = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestNormPlan: Plan determines the plan for a day based on company or custom schedule.
func TestNormPlan(t *testing.T) {
	companySched := worktime.Default() // default Mon-Fri 09:00-18:00
	tue := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)
	sat := tue.AddDate(0, 0, 4)

	// No custom schedule (Norm.WS is nil)
	n := Norm{Company: companySched}
	pTue := n.Plan(tue)
	if !pTue.Work || pTue.Start != companySched.Start || pTue.End != companySched.End {
		t.Errorf("plan for Tuesday without WS = %+v, want work", pTue)
	}
	pSat := n.Plan(sat)
	if pSat.Work {
		t.Errorf("plan for Saturday without WS should not be a working day")
	}

	// Flexible schedule
	nFlex := Norm{WS: &WorkSchedule{Kind: "flexible"}}
	pFlex := nFlex.Plan(tue)
	if !pFlex.Free || pFlex.Work {
		t.Errorf("flexible plan = %+v, want Free: true, Work: false", pFlex)
	}

	// Shift schedule
	nShift := Norm{
		WS: &WorkSchedule{Kind: "shift"},
		Shifts: map[string]Shift{
			"2026-10-13": {ID: 1, Name: "Day Shift", Start: "08:00", End: "20:00", Break: 60},
		},
	}
	pShiftTue := nShift.Plan(tue)
	if !pShiftTue.Work || pShiftTue.Shift != "Day Shift" || pShiftTue.Start != "08:00" || pShiftTue.End != "20:00" {
		t.Errorf("shift plan for Tuesday = %+v", pShiftTue)
	}
	pShiftWed := nShift.Plan(tue.AddDate(0, 0, 1))
	if pShiftWed.Work {
		t.Errorf("unplanned shift day should not be work")
	}

	// Fixed schedule
	nFixed := Norm{
		Company: companySched,
		WS: &WorkSchedule{
			Kind: "fixed",
			Times: []ScheduleTime{
				{Days: []int{2}, Start: "10:00", End: "19:00", Break: 45}, // Tuesday only
			},
		},
	}
	pFixedTue := nFixed.Plan(tue)
	if !pFixedTue.Work || pFixedTue.Start != "10:00" || pFixedTue.End != "19:00" || pFixedTue.Break != 45 {
		t.Errorf("fixed plan Tuesday = %+v", pFixedTue)
	}
	pFixedWed := nFixed.Plan(tue.AddDate(0, 0, 1)) // Wednesday
	if pFixedWed.Work {
		t.Errorf("fixed plan Wednesday should be day off")
	}
}

// TestNormViolationsComprehensive: Violations detects late, early, short and open days across schedule kinds.
func TestNormViolationsComprehensive(t *testing.T) {
	companySched := worktime.Default()
	companySched.LateAfter = "09:15"
	companySched.LeaveBefore = "17:30"
	companySched.MinDuration = "08:00"

	day := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local) // Tuesday
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.Local)
	atTime := func(h, m int) time.Time {
		return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	ptr := func(t time.Time) *time.Time { return &t }

	t.Run("company schedule violations", func(t *testing.T) {
		n := Norm{Company: companySched}

		// On time: 09:10 to 18:00 (8h50m worked)
		d := Workday{Day: day, Started: atTime(9, 10), Ended: ptr(atTime(18, 0))}
		if got := n.Violations(d, now); len(got) != 0 {
			t.Errorf("on time workday violations = %v, want none", got)
		}

		// Late: started at 09:20 (> 09:15)
		d = Workday{Day: day, Started: atTime(9, 20), Ended: ptr(atTime(18, 0))}
		if got := n.Violations(d, now); !slices.Equal(got, []string{"late"}) {
			t.Errorf("late workday violations = %v, want [late]", got)
		}

		// Early and short: ended at 17:00 (< 17:30, worked 7h50m < 8h)
		d = Workday{Day: day, Started: atTime(9, 10), Ended: ptr(atTime(17, 0))}
		if got := n.Violations(d, now); !slices.Equal(got, []string{"early", "short"}) {
			t.Errorf("early/short workday violations = %v, want [early short]", got)
		}

		// Open past day
		d = Workday{Day: day, Started: atTime(9, 0), Ended: nil}
		if got := n.Violations(d, now); !slices.Equal(got, []string{"open"}) {
			t.Errorf("open past workday violations = %v, want [open]", got)
		}

		// Open today: not yet marked open
		todayWork := Workday{Day: now, Started: now.Add(-time.Hour), Ended: nil}
		if got := n.Violations(todayWork, now); slices.Contains(got, "open") {
			t.Errorf("open day today should not have open violation")
		}
	})

	t.Run("shift schedule with tolerance", func(t *testing.T) {
		n := Norm{
			WS: &WorkSchedule{
				Kind:        "shift",
				Tolerance:   15,
				MinDuration: "07:00",
			},
			Shifts: map[string]Shift{
				"2026-10-13": {Name: "Night", Start: "22:00", End: "06:00", Break: 0},
			},
		}

		// Late boundary: start 22:00 + tolerance 15m = 22:15
		// Start 22:10 -> ok
		d := Workday{Day: day, Started: atTime(22, 10), Ended: ptr(atTime(30, 0))} // 30:00 is next day 06:00
		if got := n.Violations(d, now); len(got) != 0 {
			t.Errorf("shift on time violations = %v, want none", got)
		}

		// Start 22:20 -> late
		d = Workday{Day: day, Started: atTime(22, 20), Ended: ptr(atTime(30, 0))}
		if got := n.Violations(d, now); !slices.Equal(got, []string{"late"}) {
			t.Errorf("shift late violations = %v, want [late]", got)
		}

		// Early boundary: end 06:00 (30:00) - tolerance 15m = 05:45 (29:45)
		// End 05:40 (29:40) -> early
		d = Workday{Day: day, Started: atTime(22, 0), Ended: ptr(atTime(29, 40))}
		if got := n.Violations(d, now); !slices.Contains(got, "early") {
			t.Errorf("shift early violations = %v, want to contain 'early'", got)
		}
	})

	t.Run("flexible schedule", func(t *testing.T) {
		n := Norm{
			WS: &WorkSchedule{
				Kind:        "flexible",
				MinDuration: "06:00",
			},
		}

		// Flexible has no late or early violations, only short and open
		d := Workday{Day: day, Started: atTime(15, 0), Ended: ptr(atTime(23, 0))} // worked 8h > 6h
		if got := n.Violations(d, now); len(got) != 0 {
			t.Errorf("flexible 8h violations = %v, want none", got)
		}

		d = Workday{Day: day, Started: atTime(15, 0), Ended: ptr(atTime(18, 0))} // worked 3h < 6h
		if got := n.Violations(d, now); !slices.Equal(got, []string{"short"}) {
			t.Errorf("flexible 3h violations = %v, want [short]", got)
		}
	})
}

// TestUUIDLikeAndCleanPeople: uuidLike checks format and cleanPeople deduplicates valid IDs.
func TestUUIDLikeAndCleanPeople(t *testing.T) {
	valid1 := "12345678-abcd-1234-abcd-1234567890ab"
	valid2 := "ABCDEF01-2345-6789-ABCD-EF0123456789"
	invalidCases := []string{
		"",
		"12345678-abcd-1234-abcd", // too short
		"12345678-abcd-1234-abcd-1234567890abcdef", // too long
		"12345678_abcd-1234-abcd-1234567890ab",     // underscore instead of dash
		"12345678-abcd-1234-abcd-1234567890ag",     // 'g' is not hex
		"123456789-abcd-123-abcd-1234567890ab",     // dash at wrong index
	}

	if !uuidLike(valid1) || !uuidLike(valid2) {
		t.Errorf("valid UUIDs failed uuidLike")
	}
	for _, inv := range invalidCases {
		if uuidLike(inv) {
			t.Errorf("uuidLike(%q) should be false", inv)
		}
	}

	cleaned := cleanPeople([]string{
		"  " + valid1 + "  ",
		"invalid",
		valid1, // duplicate
		"",
		valid2,
	})
	if want := []string{valid1, valid2}; !slices.Equal(cleaned, want) {
		t.Errorf("cleanPeople() = %v, want %v", cleaned, want)
	}
}

// TestNormNotify: Notify returns manager notification flag and people list from schedule.
func TestNormNotify(t *testing.T) {
	nNil := Norm{}
	head, people := nNil.Notify()
	if head || len(people) != 0 {
		t.Errorf("Norm without WS Notify = %v, %v, want false, nil", head, people)
	}

	ws := &WorkSchedule{NotifyHead: true, NotifyTo: []string{"u1", "u2"}}
	nWS := Norm{WS: ws}
	head, people = nWS.Notify()
	if !head || !slices.Equal(people, []string{"u1", "u2"}) {
		t.Errorf("Norm with WS Notify = %v, %v, want true, [u1 u2]", head, people)
	}
}
