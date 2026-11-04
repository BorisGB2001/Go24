package crm

import (
	"testing"
	"time"
)

func TestPlanStartAndEnd_Unit(t *testing.T) {
	// 2026-10-28 (October = month index 9, 4th quarter, 2nd half)
	ref := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)

	tests := []struct {
		period     string
		wantStart  time.Time
		wantEnd    time.Time
		wantMonths int
		wantOK     bool
	}{
		{
			period:     "month",
			wantStart:  time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:    time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
			wantMonths: 1,
			wantOK:     true,
		},
		{
			period:     "quarter",
			wantStart:  time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:    time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
			wantMonths: 3,
			wantOK:     true,
		},
		{
			period:     "half",
			wantStart:  time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:    time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
			wantMonths: 6,
			wantOK:     true,
		},
		{
			period:     "year",
			wantStart:  time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:    time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC),
			wantMonths: 12,
			wantOK:     true,
		},
		{
			period: "decade",
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.period, func(t *testing.T) {
			start, ok := PlanStart(tt.period, ref)
			if ok != tt.wantOK {
				t.Fatalf("PlanStart() ok = %v, wantOK %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if !start.Equal(tt.wantStart) {
				t.Errorf("PlanStart() = %v, want %v", start, tt.wantStart)
			}
			if m := planMonths(tt.period); m != tt.wantMonths {
				t.Errorf("planMonths() = %d, want %d", m, tt.wantMonths)
			}
			end := PlanEnd(tt.period, start)
			if !end.Equal(tt.wantEnd) {
				t.Errorf("PlanEnd() = %v, want %v", end, tt.wantEnd)
			}
		})
	}
}

func TestPlanRow_Unit(t *testing.T) {
	// 1. Metric: sum
	rowSum := PlanRow{
		Metric:    "sum",
		Target:    100000,
		Won:       5,
		WonSum:    75000,
		InWork:    10,
		InWorkSum: 50000,
	}

	if rowSum.Fact() != 75000 {
		t.Errorf("Fact() = %v, want 75000", rowSum.Fact())
	}
	if rowSum.Forecast() != 50000 {
		t.Errorf("Forecast() = %v, want 50000", rowSum.Forecast())
	}
	if rowSum.Percent() != 75 {
		t.Errorf("Percent() = %d, want 75", rowSum.Percent())
	}
	if rowSum.Bar() != 75 {
		t.Errorf("Bar() = %d, want 75", rowSum.Bar())
	}
	if rowSum.Left() != 25000 {
		t.Errorf("Left() = %v, want 25000", rowSum.Left())
	}

	// Over-exceeded target
	rowOver := PlanRow{
		Metric: "sum",
		Target: 1000,
		WonSum: 1500,
	}
	if rowOver.Percent() != 150 {
		t.Errorf("Percent() = %d, want 150", rowOver.Percent())
	}
	if rowOver.Bar() != 100 {
		t.Errorf("Bar() = %d, want 100 (capped at 100)", rowOver.Bar())
	}
	if rowOver.Left() != 0 {
		t.Errorf("Left() = %v, want 0", rowOver.Left())
	}

	// Target 0
	rowZero := PlanRow{
		Metric: "sum",
		Target: 0,
		WonSum: 500,
	}
	if rowZero.Percent() != 0 || rowZero.Bar() != 0 {
		t.Errorf("Zero target: Percent=%d, Bar=%d", rowZero.Percent(), rowZero.Bar())
	}

	// 2. Metric: count
	rowCount := PlanRow{
		Metric: "count",
		Target: 10,
		Won:    6,
		WonSum: 99999, // ignored
		InWork: 8,
	}
	if rowCount.Fact() != 6 {
		t.Errorf("Fact() count = %v, want 6", rowCount.Fact())
	}
	if rowCount.Forecast() != 8 {
		t.Errorf("Forecast() count = %v, want 8", rowCount.Forecast())
	}
	if rowCount.Percent() != 60 {
		t.Errorf("Percent() = %d, want 60", rowCount.Percent())
	}
	if rowCount.Left() != 4 {
		t.Errorf("Left() = %v, want 4", rowCount.Left())
	}
}

func TestPlanIDRe_Unit(t *testing.T) {
	validUUID := "12345678-1234-1234-1234-123456789abc"
	if !planIDRe.MatchString(validUUID) {
		t.Errorf("planIDRe failed to match valid UUID %s", validUUID)
	}

	invalidUUIDs := []string{
		"not-a-uuid",
		"12345678-1234-1234-1234-123456789abg", // 'g' is not hex
		"12345678-1234-1234-1234-123456789ab",  // too short
		"",
	}
	for _, inv := range invalidUUIDs {
		if planIDRe.MatchString(inv) {
			t.Errorf("planIDRe unexpectedly matched invalid UUID %s", inv)
		}
	}
}
