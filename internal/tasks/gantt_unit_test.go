package tasks

import (
	"testing"
	"time"
)

func TestValidLinkType(t *testing.T) {
	valid := []string{"finish_start", "start_start", "finish_finish", "start_finish"}
	for _, v := range valid {
		if !validLinkType(v) {
			t.Errorf("validLinkType(%q) must be true", v)
		}
	}

	invalid := []string{"", "finish", "start", "start_to_finish", "finish_to_start", "unknown"}
	for _, inv := range invalid {
		if validLinkType(inv) {
			t.Errorf("validLinkType(%q) must be false", inv)
		}
	}
}

func TestBarDates(t *testing.T) {
	d := func(day int) time.Time {
		return time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC)
	}
	p := func(day int) *time.Time {
		val := d(day)
		return &val
	}

	created := time.Date(2026, 11, 5, 14, 30, 0, 0, time.UTC)

	tests := []struct {
		name      string
		startPlan *time.Time
		endPlan   *time.Time
		deadline  *time.Time
		closed    *time.Time
		wantStart time.Time
		wantEnd   time.Time
	}{
		{
			name:      "both plan dates set",
			startPlan: p(10),
			endPlan:   p(15),
			wantStart: d(10),
			wantEnd:   d(15),
		},
		{
			name:      "only start plan set, end falls back to deadline",
			startPlan: p(8),
			deadline:  p(12),
			wantStart: d(8),
			wantEnd:   d(12),
		},
		{
			name:      "no plan dates, start from created, end from closed",
			closed:    p(7),
			wantStart: d(5), // day(created)
			wantEnd:   d(7),
		},
		{
			name:      "no plan dates, no deadline, no closed -> end equals start",
			wantStart: d(5),
			wantEnd:   d(5),
		},
		{
			name:      "end before start clamps end to start",
			startPlan: p(10),
			endPlan:   p(5),
			wantStart: d(10),
			wantEnd:   d(10),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, end := barDates(tc.startPlan, tc.endPlan, created, tc.deadline, tc.closed)
			if !start.Equal(tc.wantStart) || !end.Equal(tc.wantEnd) {
				t.Errorf("barDates() = (%v, %v), want (%v, %v)", start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestLinkShift_EdgeCases(t *testing.T) {
	d := func(day int) time.Time {
		return time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC)
	}

	tests := []struct {
		name      string
		typ       string
		ps, pe    int
		ss, se    int
		wantShift int
	}{
		{
			name: "finish_start: successor starts exactly the day after -> 0 shift",
			typ:  "finish_start",
			ps:   1, pe: 5,
			ss: 6, se: 10,
			wantShift: 0,
		},
		{
			name: "finish_start: successor starts on same day predecessor ends -> 1 day shift",
			typ:  "finish_start",
			ps:   1, pe: 5,
			ss: 5, se: 9,
			wantShift: 1,
		},
		{
			name: "finish_start: successor starts 3 days before predecessor ends -> 4 days shift",
			typ:  "finish_start",
			ps:   1, pe: 5,
			ss: 2, se: 6,
			wantShift: 4,
		},
		{
			name: "start_start: successor starts before predecessor -> shift needed",
			typ:  "start_start",
			ps:   5, pe: 10,
			ss: 3, se: 7,
			wantShift: 2,
		},
		{
			name: "start_start: successor starts same day or later -> 0 shift",
			typ:  "start_start",
			ps:   5, pe: 10,
			ss: 5, se: 8,
			wantShift: 0,
		},
		{
			name: "finish_finish: successor ends before predecessor ends -> shift needed",
			typ:  "finish_finish",
			ps:   1, pe: 10,
			ss: 1, se: 8,
			wantShift: 2,
		},
		{
			name: "finish_finish: successor ends same day or later -> 0 shift",
			typ:  "finish_finish",
			ps:   1, pe: 10,
			ss: 1, se: 12,
			wantShift: 0,
		},
		{
			name: "start_finish: successor ends before predecessor starts -> shift needed",
			typ:  "start_finish",
			ps:   8, pe: 12,
			ss: 1, se: 5,
			wantShift: 3,
		},
		{
			name: "start_finish: successor ends on or after predecessor starts -> 0 shift",
			typ:  "start_finish",
			ps:   8, pe: 12,
			ss: 1, se: 8,
			wantShift: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := linkShift(tc.typ, d(tc.ps), d(tc.pe), d(tc.ss), d(tc.se))
			if got != tc.wantShift {
				t.Errorf("linkShift(%s) = %d, want %d", tc.typ, got, tc.wantShift)
			}
		})
	}
}
