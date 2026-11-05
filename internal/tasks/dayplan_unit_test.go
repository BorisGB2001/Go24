package tasks

import (
	"testing"
	"time"
)

func TestDayStart(t *testing.T) {
	now := time.Now()
	start := dayStart(now)

	y, m, d := now.Local().Date()
	want := time.Date(y, m, d, 0, 0, 0, 0, time.Local)

	if !start.Equal(want) {
		t.Errorf("dayStart(%v) = %v, want %v", now, start, want)
	}

	// Verify time is midnight
	if start.Hour() != 0 || start.Minute() != 0 || start.Second() != 0 || start.Nanosecond() != 0 {
		t.Errorf("dayStart returned non-midnight time: %v", start)
	}
}
