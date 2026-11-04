package crm

import (
	"testing"
	"time"
)

func TestRecurrenceNth_Unit(t *testing.T) {
	start := time.Date(2026, 1, 31, 10, 30, 0, 0, time.UTC)

	// 1. Day recurrence
	rDay := Recurrence{
		Unit:  "day",
		Every: 3,
		Start: start,
	}
	if got := rDay.nth(0); !got.Equal(start) {
		t.Errorf("rDay.nth(0) = %v, want %v", got, start)
	}
	wantDay1 := time.Date(2026, 2, 3, 10, 30, 0, 0, time.UTC)
	if got := rDay.nth(1); !got.Equal(wantDay1) {
		t.Errorf("rDay.nth(1) = %v, want %v", got, wantDay1)
	}

	// 2. Week recurrence
	rWeek := Recurrence{
		Unit:  "week",
		Every: 2,
		Start: start,
	}
	wantWeek1 := time.Date(2026, 2, 14, 10, 30, 0, 0, time.UTC)
	if got := rWeek.nth(1); !got.Equal(wantWeek1) {
		t.Errorf("rWeek.nth(1) = %v, want %v", got, wantWeek1)
	}

	// 3. Month recurrence with day clamping on shorter months (Jan 31 -> Feb 28 in 2026, non-leap year)
	rMonth := Recurrence{
		Unit:  "month",
		Every: 1,
		Start: start,
	}
	wantFeb := time.Date(2026, 2, 28, 0, 0, 0, 0, time.UTC)
	if got := rMonth.nth(1); !got.Equal(wantFeb) {
		t.Errorf("rMonth.nth(1) = %v, want %v", got, wantFeb)
	}
	wantMar := time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC)
	if got := rMonth.nth(2); !got.Equal(wantMar) {
		t.Errorf("rMonth.nth(2) = %v, want %v", got, wantMar)
	}
	wantApr := time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)
	if got := rMonth.nth(3); !got.Equal(wantApr) {
		t.Errorf("rMonth.nth(3) = %v, want %v", got, wantApr)
	}

	// Leap year test (2028 is a leap year)
	startLeap := time.Date(2028, 1, 31, 0, 0, 0, 0, time.UTC)
	rLeap := Recurrence{
		Unit:  "month",
		Every: 1,
		Start: startLeap,
	}
	wantFebLeap := time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)
	if got := rLeap.nth(1); !got.Equal(wantFebLeap) {
		t.Errorf("rLeap.nth(1) = %v, want %v", got, wantFebLeap)
	}
}

func TestDateOnly_Unit(t *testing.T) {
	in := time.Date(2026, 10, 28, 18, 45, 30, 500, time.FixedZone("MSK", 3*3600))
	got := dateOnly(in)
	want := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("dateOnly(%v) = %v (%v), want %v (UTC)", in, got, got.Location(), want)
	}
}

func TestValidUnit_Unit(t *testing.T) {
	valid := []string{"day", "week", "month"}
	for _, u := range valid {
		if !validUnit(u) {
			t.Errorf("validUnit(%q) = false, want true", u)
		}
	}

	invalid := []string{"hour", "minute", "year", "", "days"}
	for _, u := range invalid {
		if validUnit(u) {
			t.Errorf("validUnit(%q) = true, want false", u)
		}
	}
}
