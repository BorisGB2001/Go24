package calendar

import (
	"testing"
	"time"

	"go24/internal/worktime"
)

func TestClockOf(t *testing.T) {
	cases := []struct {
		input string
		def   int
		want  int
	}{
		{"00:00", 100, 0},
		{"09:00", 100, 540},
		{"09:30", 100, 570},
		{"18:00", 100, 1080},
		{"23:59", 100, 1439},
		{"", 540, 540},
		{"invalid", 1080, 1080},
		{"9:00", 540, 540},
		{"25:00", 540, 540},
		{"12:60", 540, 540},
	}

	for _, tc := range cases {
		if got := clockOf(tc.input, tc.def); got != tc.want {
			t.Errorf("clockOf(%q, %d) = %d, want %d", tc.input, tc.def, got, tc.want)
		}
	}
}

func TestFirstFreeEdgeCases(t *testing.T) {
	at := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.Local) } // 23.10 — Friday
	sched := worktime.Schedule{
		Start:   "09:00",
		End:     "18:00",
		Weekend: []int{0, 6}, // Sunday, Saturday
	}

	t.Run("empty people list returns earliest time on grid", func(t *testing.T) {
		// from before start -> 09:00
		got, ok := FirstFree(nil, at(23, 7, 30), time.Hour, sched, 7)
		if !ok || !got.Equal(at(23, 9, 0)) {
			t.Errorf("got %v %v, want %v", got, ok, at(23, 9, 0))
		}

		// from during day off-grid -> snaps to next quarter-hour (10:15)
		got, ok = FirstFree([]Person{}, at(23, 10, 7), 30*time.Minute, sched, 7)
		if !ok || !got.Equal(at(23, 10, 15)) {
			t.Errorf("got %v %v, want %v", got, ok, at(23, 10, 15))
		}

		// from exactly on quarter-hour grid -> 10:15
		got, ok = FirstFree(nil, at(23, 10, 15), 30*time.Minute, sched, 7)
		if !ok || !got.Equal(at(23, 10, 15)) {
			t.Errorf("got %v %v, want %v", got, ok, at(23, 10, 15))
		}
	})

	t.Run("overlapping busy slots between colleagues", func(t *testing.T) {
		people := []Person{
			{
				Slots: []Slot{
					{Start: at(23, 10, 0), End: at(23, 11, 0)},
				},
			},
			{
				Slots: []Slot{
					{Start: at(23, 10, 30), End: at(23, 11, 30)},
				},
			},
		}
		// A meeting requested at 10:00 must skip past both overlapping slots to 11:30
		got, ok := FirstFree(people, at(23, 10, 0), time.Hour, sched, 7)
		if !ok || !got.Equal(at(23, 11, 30)) {
			t.Errorf("got %v %v, want %v", got, ok, at(23, 11, 30))
		}
	})

	t.Run("skips holiday in schedule", func(t *testing.T) {
		// 23.10 is Friday. 24, 25 are weekend. Monday 26.10 is holiday. Next workday is Tuesday 27.10.
		holidaySched := sched
		holidaySched.Holidays = []string{"2026-10-26"}

		// Start late on Friday
		got, ok := FirstFree(nil, at(23, 17, 30), time.Hour, holidaySched, 7)
		if !ok || !got.Equal(at(27, 9, 0)) {
			t.Errorf("got %v %v, want Tuesday 27.10 09:00", got, ok)
		}
	})

	t.Run("multi-day absence slot", func(t *testing.T) {
		// Person is absent from Friday 23.10 to Tuesday 27.10 (inclusive)
		people := []Person{
			{
				Slots: []Slot{
					{Start: at(23, 0, 0), End: at(28, 0, 0), Kind: "absent"},
				},
			},
		}
		got, ok := FirstFree(people, at(23, 8, 0), time.Hour, sched, 14)
		if !ok || !got.Equal(at(28, 9, 0)) {
			t.Errorf("got %v %v, want Wednesday 28.10 09:00", got, ok)
		}
	})

	t.Run("days limit exceeded", func(t *testing.T) {
		// Schedule only searches 2 days ahead, Friday 23 and Saturday 24.
		// Whole Friday is busy, Saturday is weekend -> none found within 2 days.
		people := []Person{
			{
				Slots: []Slot{
					{Start: at(23, 9, 0), End: at(23, 18, 0)},
				},
			},
		}
		_, ok := FirstFree(people, at(23, 9, 0), time.Hour, sched, 2)
		if ok {
			t.Error("expected false when no free slot within days limit")
		}
	})

	t.Run("invalid schedule strings fallback to default 09:00-18:00", func(t *testing.T) {
		badSched := worktime.Schedule{
			Start: "unparseable",
			End:   "garbage",
		}
		got, ok := FirstFree(nil, at(23, 8, 0), time.Hour, badSched, 1)
		if !ok || !got.Equal(at(23, 9, 0)) {
			t.Errorf("got %v %v, want fallback default 09:00", got, ok)
		}
	})
}
