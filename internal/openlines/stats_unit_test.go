package openlines

import (
	"testing"
	"time"
)

func TestAvg(t *testing.T) {
	cases := []struct {
		name string
		sum  time.Duration
		n    int
		want time.Duration
	}{
		{"zero count", 10 * time.Second, 0, 0},
		{"zero sum", 0, 5, 0},
		{"exact division", 10 * time.Second, 2, 5 * time.Second},
		{"rounds down", 10 * time.Second, 3, 3 * time.Second},
		{"rounds up", 11 * time.Second, 3, 4 * time.Second},
		{"one count", 42 * time.Second, 1, 42 * time.Second},
		{"subsecond rounding", 1500 * time.Millisecond, 1, 2 * time.Second},
		{"subsecond rounds down", 1400 * time.Millisecond, 1, 1 * time.Second},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := avg(tc.sum, tc.n); got != tc.want {
				t.Errorf("avg(%v, %d) = %v; want %v", tc.sum, tc.n, got, tc.want)
			}
		})
	}
}

func TestStatsRowAverages(t *testing.T) {
	t.Run("FirstAnswer", func(t *testing.T) {
		emptyRow := StatsRow{}
		if got := emptyRow.FirstAnswer(); got != 0 {
			t.Errorf("FirstAnswer() on empty row = %v; want 0", got)
		}

		row := StatsRow{
			answered: 4,
			answer:   100 * time.Second,
		}
		if got := row.FirstAnswer(); got != 25*time.Second {
			t.Errorf("FirstAnswer() = %v; want 25s", got)
		}
	})

	t.Run("ToClose", func(t *testing.T) {
		emptyRow := StatsRow{}
		if got := emptyRow.ToClose(); got != 0 {
			t.Errorf("ToClose() on empty row = %v; want 0", got)
		}

		row := StatsRow{
			closedN: 3,
			close:   360 * time.Second,
		}
		if got := row.ToClose(); got != 120*time.Second {
			t.Errorf("ToClose() = %v; want 120s", got)
		}
	})
}
