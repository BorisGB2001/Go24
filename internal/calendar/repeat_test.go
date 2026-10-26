package calendar

import (
	"strings"
	"testing"
	"time"
)

// TestRepeatDays: a weekly meeting on chosen days (every other week as well), its start's day always among them, and
// a series that ends after so many meetings — Until is the day of the last one, deleted meetings count.
func TestRepeatDays(t *testing.T) {
	d := func(n, h int) time.Time { return time.Date(2026, 10, 19+n, h, 0, 0, 0, time.Local) } // 19.10 — Monday
	days := func(list []Event) string {
		var out []string
		for _, o := range list {
			out = append(out, o.Start.Format("02.01"))
		}
		return strings.Join(out, " ")
	}
	// from Wednesday on Mon, Wed, Fri: Monday of the first week is before the start
	e := Event{Start: d(2, 10), End: d(2, 11), Repeat: "weekly", Days: []int{5, 1}}
	if err := e.repetition(); err != nil || len(e.Days) != 3 || e.Days[0] != 1 || e.Days[1] != 3 || e.Days[2] != 5 {
		t.Fatalf("days %v, %v", e.Days, err)
	}
	if got := days(e.expand(d(0, 0), d(14, 0))); got != "21.10 23.10 26.10 28.10 30.10" {
		t.Fatalf("weekly on days: %s", got)
	}
	if !e.OnDay(5) || e.OnDay(2) {
		t.Fatal("OnDay")
	}
	// every other week, seven meetings: the last on 18.11, a deleted one counts
	e.Every, e.Count = 2, 7
	if err := e.repetition(); err != nil || e.Until == nil || e.Until.Format("02.01") != "18.11" {
		t.Fatalf("until %v, %v", e.Until, err)
	}
	e.Skip = []time.Time{d(4, 10)}
	if got := days(e.expand(d(0, 0), d(60, 0))); got != "21.10 02.11 04.11 06.11 16.11 18.11" {
		t.Fatalf("every other week, 7 meetings: %s", got)
	}
	// its start's day alone is no list; days are of a weekly one only; a day out of the week is refused
	e = Event{Start: d(0, 10), End: d(0, 11), Repeat: "weekly", Days: []int{1}}
	if err := e.repetition(); err != nil || e.Days != nil {
		t.Fatalf("start's day alone %v", e.Days)
	}
	e = Event{Start: d(0, 10), End: d(0, 11), Repeat: "daily", Days: []int{2, 4}, Count: 3}
	if err := e.repetition(); err != nil || e.Days != nil || e.Until.Format("02.01") != "21.10" {
		t.Fatalf("daily, 3 meetings: %v %v", e.Days, e.Until)
	}
	if got := days(e.expand(d(0, 0), d(14, 0))); got != "19.10 20.10 21.10" {
		t.Fatalf("daily, 3 meetings: %s", got)
	}
	e = Event{Start: d(0, 10), End: d(0, 11), Repeat: "weekly", Days: []int{8}}
	if err := e.repetition(); err == nil {
		t.Fatal("day 8 taken")
	}
	e = Event{Start: d(0, 10), End: d(0, 11), Count: 5}
	if err := e.repetition(); err != nil || e.Count != 0 || e.Until != nil {
		t.Fatal("a single meeting has no count")
	}
	if ICSDays([]int{1, 3, 7}) != "MO,WE,SU" {
		t.Fatal("ICSDays")
	}
}
