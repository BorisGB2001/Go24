package tasks

import (
	"testing"
	"time"

	"go24/internal/worktime"
)

func TestDeadlinePeriods(t *testing.T) {
	loc := time.FixedZone("CET", 3600)
	at := func(day, h int) *time.Time { d := time.Date(2026, 10, day, h, 0, 0, 0, loc); return &d }
	now := *at(14, 10) // Wednesday 14.10.2026 10:00
	for _, c := range []struct {
		d    *time.Time
		want string
	}{
		{nil, "none"}, {at(14, 9), "overdue"}, {at(14, 23), "today"}, {at(15, 9), "week"}, {at(18, 23), "week"},
		{at(19, 0), "next_week"}, {at(25, 23), "next_week"}, {at(26, 0), "later"},
	} {
		if got := DeadlinePeriod(c.d, now); got != c.want {
			t.Errorf("%v: %s, want %s", c.d, got, c.want)
		}
	}

	sched := worktime.Default() // 09:00–18:00, Saturday and Sunday off
	sched.Holidays = []string{"2026-10-30"}
	for _, c := range []struct {
		period string
		now    time.Time
		want   *time.Time
	}{
		{"today", now, at(14, 18)},
		{"today", *at(14, 19), func() *time.Time { d := time.Date(2026, 10, 14, 23, 59, 0, 0, loc); return &d }()},
		{"week", now, at(16, 18)},         // Friday
		{"next_week", now, at(23, 18)},    // next Friday
		{"later", now, at(29, 18)},        // Friday 30.10 is a holiday: Thursday
		{"week", *at(16, 10), at(18, 18)}, // on Friday no working day is left: Sunday
		{"none", now, nil},
	} {
		got, err := PeriodDeadline(c.period, c.now, sched)
		if err != nil || (got == nil) != (c.want == nil) || (got != nil && !got.Equal(*c.want)) {
			t.Errorf("%s at %v: %v %v, want %v", c.period, c.now, got, err, c.want)
		}
		if got != nil && DeadlinePeriod(got, c.now) != c.period {
			t.Errorf("%s: the new deadline %v falls into %s", c.period, got, DeadlinePeriod(got, c.now))
		}
	}
	if _, err := PeriodDeadline("overdue", now, sched); err == nil {
		t.Error("overdue cannot be chosen")
	}
	if _, err := PeriodDeadline("week", *at(18, 10), sched); err == nil {
		t.Error("on Sunday this week has no day left")
	}
}
