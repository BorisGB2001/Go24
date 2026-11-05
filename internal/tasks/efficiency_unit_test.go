package tasks

import (
	"testing"
	"time"
)

func TestPercent(t *testing.T) {
	tests := []struct {
		total    int
		violated int
		want     int
	}{
		{0, 0, 100},
		{10, 0, 100},
		{10, 5, 50},
		{10, 10, 0},
		{3, 1, 66}, // integer division (3 - 1) * 100 / 3 = 66
		{3, 2, 33},
	}

	for _, tc := range tests {
		if got := percent(tc.total, tc.violated); got != tc.want {
			t.Errorf("percent(%d, %d) = %d, want %d", tc.total, tc.violated, got, tc.want)
		}
	}
}

func TestEffTaskInWork(t *testing.T) {
	d := func(day int) time.Time {
		return time.Date(2026, 10, day, 0, 0, 0, 0, time.UTC)
	}
	p := func(day int) *time.Time {
		val := d(day)
		return &val
	}

	from, end := d(10), d(20)

	tests := []struct {
		name    string
		task    effTask
		wantInW bool
	}{
		{
			name:    "created after end",
			task:    effTask{Created: d(21)},
			wantInW: false,
		},
		{
			name:    "closed before from",
			task:    effTask{Created: d(1), ClosedAt: p(9)},
			wantInW: false,
		},
		{
			name:    "open throughout period",
			task:    effTask{Created: d(5), ClosedAt: nil},
			wantInW: true,
		},
		{
			name:    "created during period, still open",
			task:    effTask{Created: d(15), ClosedAt: nil},
			wantInW: true,
		},
		{
			name:    "created before from, closed during period",
			task:    effTask{Created: d(5), ClosedAt: p(12)},
			wantInW: true,
		},
		{
			name:    "closed exactly at from",
			task:    effTask{Created: d(5), ClosedAt: p(10)},
			wantInW: true, // !ClosedAt.Before(from) is true
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.task.inWork(from, end); got != tc.wantInW {
				t.Errorf("inWork() = %v, want %v", got, tc.wantInW)
			}
		})
	}
}

func TestEffTaskOverdueAt(t *testing.T) {
	d := func(day, h int) time.Time {
		return time.Date(2026, 10, day, h, 0, 0, 0, time.UTC)
	}
	p := func(day, h int) *time.Time {
		val := d(day, h)
		return &val
	}

	from, end := d(10, 0), d(20, 0)

	tests := []struct {
		name   string
		task   effTask
		wantAt time.Time
		wantOk bool
	}{
		{
			name:   "no deadline",
			task:   effTask{Deadline: nil},
			wantAt: time.Time{},
			wantOk: false,
		},
		{
			name:   "deadline after end",
			task:   effTask{Deadline: p(25, 0)},
			wantAt: time.Time{},
			wantOk: false,
		},
		{
			name:   "closed before deadline",
			task:   effTask{Deadline: p(15, 18), ClosedAt: p(14, 12)},
			wantAt: time.Time{},
			wantOk: false,
		},
		{
			name:   "closed before from",
			task:   effTask{Deadline: p(8, 18), ClosedAt: p(9, 12)},
			wantAt: time.Time{},
			wantOk: false,
		},
		{
			name:   "deadline passed before from, still open",
			task:   effTask{Deadline: p(5, 18), ClosedAt: nil},
			wantAt: from, // counts from start of period
			wantOk: true,
		},
		{
			name:   "deadline passed during period, still open",
			task:   effTask{Deadline: p(15, 18), ClosedAt: nil},
			wantAt: d(15, 18),
			wantOk: true,
		},
		{
			name:   "deadline passed during period, closed after deadline",
			task:   effTask{Deadline: p(15, 18), ClosedAt: p(16, 12)},
			wantAt: d(15, 18),
			wantOk: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			at, ok := tc.task.overdueAt(from, end)
			if ok != tc.wantOk {
				t.Fatalf("overdueAt() ok = %v, wantOk %v", ok, tc.wantOk)
			}
			if ok && !at.Equal(tc.wantAt) {
				t.Errorf("overdueAt() at = %v, want %v", at, tc.wantAt)
			}
		})
	}
}

func TestEffTaskViolations(t *testing.T) {
	d := func(day, h int) time.Time {
		return time.Date(2026, 10, day, h, 0, 0, 0, time.UTC)
	}
	p := func(day, h int) *time.Time {
		val := d(day, h)
		return &val
	}

	from, end := d(10, 0), d(20, 0)

	// Task with both overdue and rework within period
	task := effTask{
		ID:       "task-1",
		Key:      "PRJ-1",
		Title:    "Issue",
		Deadline: p(12, 18),
		reworks: []time.Time{
			d(8, 10),  // before from -> ignored
			d(14, 15), // within [from, end) -> violation
			d(22, 10), // after end -> ignored
		},
	}

	vs := task.violations(from, end)
	if len(vs) != 2 {
		t.Fatalf("expected 2 violations, got %d: %+v", len(vs), vs)
	}

	if vs[0].Kind != ViolationOverdue || !vs[0].At.Equal(d(12, 18)) {
		t.Errorf("vs[0] = %+v, want overdue at 12th", vs[0])
	}
	if vs[1].Kind != ViolationRework || !vs[1].At.Equal(d(14, 15)) {
		t.Errorf("vs[1] = %+v, want rework at 14th", vs[1])
	}
}
