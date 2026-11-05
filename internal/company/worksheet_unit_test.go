package company

import (
	"slices"
	"testing"
	"time"

	"go24/internal/i18n"
	"go24/internal/worktime"
)

// TestWorksheetViolationsWrapper: Violations delegates to Norm.Violations.
func TestWorksheetViolationsWrapper(t *testing.T) {
	sched := worktime.Default()
	sched.LateAfter = "09:00"
	day := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)
	now := day.Add(24 * time.Hour)
	lateStart := day.Add(9*time.Hour + 30*time.Minute)
	okEnd := day.Add(18 * time.Hour)

	d := Workday{Day: day, Started: lateStart, Ended: &okEnd}
	got := Violations(d, sched, now)
	if !slices.Equal(got, []string{"late"}) {
		t.Errorf("Violations() = %v, want [late]", got)
	}
}

// TestBuildSheet: BuildSheet lays out an entire month for employees with cell classifications and totals.
func TestBuildSheet(t *testing.T) {
	sched := worktime.Default()                             // Mon-Fri 09:00-18:00 (9h - 0 = 540m plan)
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local) // 31 days (Oct 1 is Thursday)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.Local)  // Monday noon

	p1 := Person{ID: "u1", Name: "Alice"}
	p2 := Person{ID: "u2", Name: "Bob"}

	at := func(d int, h, m int) time.Time {
		return month.AddDate(0, 0, d-1).Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
	}
	ptr := func(t time.Time) *time.Time { return &t }

	office := "HQ"
	outside := ""

	days := []Workday{
		// u1 worked on Oct 1 (Thu): 09:00 to 18:00 at HQ
		{
			UserID:  "u1",
			Day:     month.AddDate(0, 0, 0),
			Started: at(1, 9, 0),
			Ended:   ptr(at(1, 18, 0)),
			Start:   Place{Office: &office},
			End:     Place{Office: &office},
		},
		// u1 worked on Oct 5 (Mon): started 09:00, not ended (open today!), started away
		{
			UserID:  "u1",
			Day:     month.AddDate(0, 0, 4),
			Started: at(5, 9, 0),
			Ended:   nil,
			Start:   Place{Office: &outside},
		},
	}

	absences := []Absence{
		// u2 is on vacation Oct 1 to Oct 2 (Thu-Fri)
		{
			UserID: "u2",
			Kind:   "vacation",
			From:   month.AddDate(0, 0, 0),
			To:     month.AddDate(0, 0, 1),
		},
	}

	pending := map[string]bool{
		"u1|2026-10-01": true,
	}

	norms := map[string]Norm{
		"u1": {Company: sched},
		"u2": {
			Company: sched,
			WS:      &WorkSchedule{Kind: "flexible"}, // flexible schedule for u2
		},
	}

	rows := BuildSheet([]Person{p1, p2}, days, absences, pending, norms, sched, month, now)
	if len(rows) != 2 {
		t.Fatalf("BuildSheet rows count = %d, want 2", len(rows))
	}

	// Verify u1
	r1 := rows[0]
	if len(r1.Cells) != 31 {
		t.Fatalf("u1 cells count = %d, want 31", len(r1.Cells))
	}
	if r1.Days != 2 {
		t.Errorf("u1 days worked = %d, want 2", r1.Days)
	}

	// Oct 1 for u1: work, pending=true, away=false
	cOct1 := r1.Cells[0]
	if cOct1.Kind != "work" || !cOct1.Pending || cOct1.Away || cOct1.Worked != 9*3600 {
		t.Errorf("u1 Oct 1 cell = %+v, want work pending 9h", cOct1)
	}

	// Oct 3 for u1 (Saturday): off
	cOct3 := r1.Cells[2]
	if cOct3.Kind != "off" {
		t.Errorf("u1 Oct 3 cell kind = %q, want off", cOct3.Kind)
	}

	// Oct 5 for u1 (Monday, today): work, open=true, away=true (started outside)
	cOct5 := r1.Cells[4]
	if cOct5.Kind != "work" || !cOct5.Open || !cOct5.Away {
		t.Errorf("u1 Oct 5 cell = %+v, want work open away", cOct5)
	}

	// Oct 6 for u1 (Tuesday, tomorrow): future
	cOct6 := r1.Cells[5]
	if cOct6.Kind != "future" {
		t.Errorf("u1 Oct 6 cell kind = %q, want future", cOct6.Kind)
	}

	// Oct 2 for u1 (Friday, in past, unworked working day): none
	cOct2 := r1.Cells[1]
	if cOct2.Kind != "none" {
		t.Errorf("u1 Oct 2 cell kind = %q, want none", cOct2.Kind)
	}

	// Verify u2
	r2 := rows[1]
	// Oct 1 for u2: absence "vacation"
	c2Oct1 := r2.Cells[0]
	if c2Oct1.Kind != "absence" || c2Oct1.Absence != "vacation" {
		t.Errorf("u2 Oct 1 cell = %+v, want absence vacation", c2Oct1)
	}
	// Oct 3 for u2 (Saturday, flexible schedule has Free: true): free
	c2Oct3 := r2.Cells[2]
	if c2Oct3.Kind != "free" {
		t.Errorf("u2 Oct 3 cell kind = %q, want free", c2Oct3.Kind)
	}
}

// TestDayChangeApply: DayChange.apply validates and applies workday modifications.
func TestDayChangeApply(t *testing.T) {
	day := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)
	now := day.Add(18 * time.Hour)
	ptr := func(t time.Time) *time.Time { return &t }
	ptrInt := func(i int) *int { return &i }

	t.Run("creating new day (d is nil)", func(t *testing.T) {
		// Missing start or end
		c := DayChange{Started: ptr(day.Add(9 * time.Hour)), Ended: nil}
		if _, err := c.apply(nil, "u1", day, now); err == nil || err.Error() != i18n.E("err.workday_new_day").Error() {
			t.Errorf("missing ended err = %v, want err.workday_new_day", err)
		}

		// Start on different day
		c = DayChange{
			Started: ptr(day.AddDate(0, 0, 1).Add(9 * time.Hour)),
			Ended:   ptr(day.AddDate(0, 0, 1).Add(18 * time.Hour)),
		}
		if _, err := c.apply(nil, "u1", day, now); err == nil || err.Error() != i18n.E("err.workday_start_day").Error() {
			t.Errorf("start on wrong day err = %v, want err.workday_start_day", err)
		}

		// Start in future
		futureDay := now.Add(24 * time.Hour)
		c = DayChange{
			Started: ptr(futureDay.Add(9 * time.Hour)),
			Ended:   ptr(futureDay.Add(18 * time.Hour)),
		}
		if _, err := c.apply(nil, "u1", futureDay, now); err == nil || err.Error() != i18n.E("err.workday_start_day").Error() {
			t.Errorf("start in future err = %v, want err.workday_start_day", err)
		}

		// Ended before or equal to started
		c = DayChange{
			Started: ptr(day.Add(10 * time.Hour)),
			Ended:   ptr(day.Add(9 * time.Hour)),
		}
		if _, err := c.apply(nil, "u1", day, now); err == nil || err.Error() != i18n.E("err.workday_end").Error() {
			t.Errorf("ended before started err = %v, want err.workday_end", err)
		}

		// Duration > 24h
		c = DayChange{
			Started: ptr(day.Add(9 * time.Hour)),
			Ended:   ptr(day.Add(34 * time.Hour)),
		}
		if _, err := c.apply(nil, "u1", day, now.Add(48*time.Hour)); err == nil || err.Error() != i18n.E("err.workday_end").Error() {
			t.Errorf("duration > 24h err = %v, want err.workday_end", err)
		}

		// Negative pause
		c = DayChange{
			Started:   ptr(day.Add(9 * time.Hour)),
			Ended:     ptr(day.Add(18 * time.Hour)),
			PauseSecs: ptrInt(-10),
		}
		if _, err := c.apply(nil, "u1", day, now); err == nil || err.Error() != i18n.E("err.workday_pause").Error() {
			t.Errorf("negative pause err = %v, want err.workday_pause", err)
		}

		// Pause >= duration
		c = DayChange{
			Started:   ptr(day.Add(9 * time.Hour)),
			Ended:     ptr(day.Add(18 * time.Hour)),
			PauseSecs: ptrInt(9 * 3600),
		}
		if _, err := c.apply(nil, "u1", day, now); err == nil || err.Error() != i18n.E("err.workday_pause").Error() {
			t.Errorf("pause >= duration err = %v, want err.workday_pause", err)
		}

		// Valid new day
		c = DayChange{
			Started:   ptr(day.Add(9 * time.Hour)),
			Ended:     ptr(day.Add(18 * time.Hour)),
			PauseSecs: ptrInt(3600),
		}
		got, err := c.apply(nil, "u1", day, now)
		if err != nil {
			t.Fatalf("valid new day apply err = %v", err)
		}
		if got.UserID != "u1" || got.Day != day || got.Started != day.Add(9*time.Hour) || *got.Ended != day.Add(18*time.Hour) || got.PauseSecs != 3600 {
			t.Errorf("applied workday mismatch: %+v", got)
		}
	})

	t.Run("updating existing day", func(t *testing.T) {
		start := day.Add(9 * time.Hour)
		paused := day.Add(12 * time.Hour)
		end := day.Add(18 * time.Hour)

		// Existing day was on pause; change sets Ended without PauseSecs -> pause added to PauseSecs
		existing := Workday{
			UserID:    "u1",
			Day:       day,
			Started:   start,
			PausedAt:  &paused,
			PauseSecs: 600,
		}
		c := DayChange{Ended: &end}
		got, err := c.apply(&existing, "u1", day, now)
		if err != nil {
			t.Fatalf("update existing err = %v", err)
		}
		// Pause between paused (12:00) and end (18:00) = 6 hours (21600s), plus existing 600s = 22200s
		wantPause := 600 + int(end.Sub(paused).Seconds())
		if got.PauseSecs != wantPause || got.PausedAt != nil || *got.Ended != end {
			t.Errorf("ongoing pause resolution mismatch: PauseSecs=%d (want %d), PausedAt=%v", got.PauseSecs, wantPause, got.PausedAt)
		}
	})
}

// TestDayLink: dayLink formats the URL for the user's working day.
func TestDayLink(t *testing.T) {
	day := time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local)
	got := dayLink("u123", day)
	want := "/worktime/d/u123/2026-10-13"
	if got != want {
		t.Errorf("dayLink() = %q, want %q", got, want)
	}
}

// TestWorkdayStateAndWorked: Workday state detection and worked seconds calculation.
func TestWorkdayStateAndWorked(t *testing.T) {
	start := time.Date(2026, 10, 13, 9, 0, 0, 0, time.Local)
	paused := start.Add(2 * time.Hour)
	end := start.Add(8 * time.Hour)
	now := start.Add(5 * time.Hour)

	// Paused
	dPaused := Workday{Started: start, PausedAt: &paused, PauseSecs: 300}
	// Worked from start to paused = 2h = 7200s - 300s = 6900s
	if dPaused.State() != "paused" || dPaused.Worked(now) != 6900 {
		t.Errorf("paused workday: state=%s, worked=%d (want 6900)", dPaused.State(), dPaused.Worked(now))
	}

	// Done
	dDone := Workday{Started: start, Ended: &end, PauseSecs: 3600}
	// Worked from start to end = 8h = 28800s - 3600s = 25200s (7h)
	if dDone.State() != "done" || dDone.Worked(now) != 7*3600 {
		t.Errorf("done workday: state=%s, worked=%d", dDone.State(), dDone.Worked(now))
	}

	// Running (ongoing)
	dRunning := Workday{Started: start, PauseSecs: 600}
	// Worked from start to now = 5h = 18000s - 600s = 17400s
	if dRunning.State() != "running" || dRunning.Worked(now) != 17400 {
		t.Errorf("running workday: state=%s, worked=%d (want 17400)", dRunning.State(), dRunning.Worked(now))
	}

	// Late check
	if !dDone.Late("08:45") {
		t.Errorf("09:00 should be late for 08:45")
	}
	if dDone.Late("09:15") {
		t.Errorf("09:00 should not be late for 09:15")
	}
	if dDone.Late("invalid") {
		t.Errorf("invalid clock should return false for Late()")
	}
}

// TestPlaceAway: Place.Away and Place.OfficeName methods.
func TestPlaceAway(t *testing.T) {
	hq := "Main Office"
	empty := ""

	pNil := Place{Office: nil}
	if pNil.Away() || pNil.OfficeName() != "" {
		t.Errorf("Place with nil office: away=%v, name=%q", pNil.Away(), pNil.OfficeName())
	}

	pAway := Place{Office: &empty}
	if !pAway.Away() || pAway.OfficeName() != "" {
		t.Errorf("Place with empty office: away=%v, name=%q", pAway.Away(), pAway.OfficeName())
	}

	pHQ := Place{Office: &hq}
	if pHQ.Away() || pHQ.OfficeName() != "Main Office" {
		t.Errorf("Place with HQ office: away=%v, name=%q", pHQ.Away(), pHQ.OfficeName())
	}
}
