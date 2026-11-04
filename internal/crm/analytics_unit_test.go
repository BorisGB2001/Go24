package crm

import (
	"math"
	"testing"
)

func TestAnalytics_ManagerWinRate_Unit(t *testing.T) {
	tests := []struct {
		name string
		m    Manager
		want int
	}{
		{
			name: "no closed deals",
			m:    Manager{Won: 0, Lost: 0},
			want: 0,
		},
		{
			name: "half won half lost",
			m:    Manager{Won: 5, Lost: 5},
			want: 50,
		},
		{
			name: "all won",
			m:    Manager{Won: 10, Lost: 0},
			want: 100,
		},
		{
			name: "all lost",
			m:    Manager{Won: 0, Lost: 10},
			want: 0,
		},
		{
			name: "integer division truncation (3 of 4 is 75%)",
			m:    Manager{Won: 3, Lost: 1},
			want: 75,
		},
		{
			name: "integer division truncation (1 of 3 is 33%)",
			m:    Manager{Won: 1, Lost: 2},
			want: 33,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.m.WinRate(); got != tt.want {
				t.Errorf("Manager.WinRate() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestAnalytics_Report_Unit(t *testing.T) {
	rep := Report{
		Won:       8,
		Lost:      2,
		WonSum:    15500.50,
		LeadsNew:  50,
		Converted: 15,
		Funnel: []StageCount{
			{Count: 12},
			{Count: 5},
		},
		Leads: []StageCount{
			{Count: 20},
			{Count: 30},
		},
	}

	// 1. WinRate: 8 / 10 = 80%
	if got := rep.WinRate(); got != 80 {
		t.Errorf("Report.WinRate() = %d, want 80", got)
	}

	// 2. AverageWon: 15500.50 / 8 = 1937.5625 -> round2 = 1937.56
	if got := rep.AverageWon(); math.Abs(got-1937.56) > 1e-6 {
		t.Errorf("Report.AverageWon() = %v, want 1937.56", got)
	}

	// 3. Conversion: 15 / 50 = 30%
	if got := rep.Conversion(); got != 30 {
		t.Errorf("Report.Conversion() = %d, want 30", got)
	}

	// 4. MaxCount: max across funnel (12, 5) and leads (20, 30) = 30
	if got := rep.MaxCount(); got != 30 {
		t.Errorf("Report.MaxCount() = %d, want 30", got)
	}

	// Zero/empty report edge cases
	zeroRep := Report{}
	if got := zeroRep.WinRate(); got != 0 {
		t.Errorf("zero Report.WinRate() = %d, want 0", got)
	}
	if got := zeroRep.AverageWon(); got != 0 {
		t.Errorf("zero Report.AverageWon() = %v, want 0", got)
	}
	if got := zeroRep.Conversion(); got != 0 {
		t.Errorf("zero Report.Conversion() = %d, want 0", got)
	}
	if got := zeroRep.MaxCount(); got != 1 {
		t.Errorf("zero Report.MaxCount() = %d, want minimum 1", got)
	}
}
