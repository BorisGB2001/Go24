package crm

import (
	"testing"
	"time"
)

func TestNumeratorPeriod_Unit(t *testing.T) {
	tm := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		reset string
		want  string
	}{
		{"day", "2026-10-28"},
		{"month", "2026-10"},
		{"year", "2026"},
		{"none", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.reset, func(t *testing.T) {
			if got := numeratorPeriod(tt.reset, tm); got != tt.want {
				t.Errorf("numeratorPeriod(%q) = %q, want %q", tt.reset, got, tt.want)
			}
		})
	}
}

func TestNumeratorNextAndPreview_Unit(t *testing.T) {
	tm := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	lastVal := int64(42)

	// 1. Fresh numerator without previous number
	numFresh := Numerator{
		Format: "Д-{NUMBER}",
		Start:  1,
		Step:   1,
		Pad:    3,
		Reset:  "year",
	}
	if got := numFresh.next(tm); got != 1 {
		t.Errorf("fresh next() = %d, want 1", got)
	}
	if got := numFresh.Preview(tm); got != "Д-001" {
		t.Errorf("fresh Preview() = %q, want \"Д-001\"", got)
	}

	// 2. Existing number within current period
	numCurrent := Numerator{
		Format: "Д-{NUMBER}",
		Start:  1,
		Step:   5,
		Pad:    3,
		Reset:  "year",
		Period: "2026",
		Last:   &lastVal,
	}
	if got := numCurrent.next(tm); got != 47 {
		t.Errorf("current next() = %d, want 47", got)
	}
	if got := numCurrent.Preview(tm); got != "Д-047" {
		t.Errorf("current Preview() = %q, want \"Д-047\"", got)
	}

	// 3. Existing number in previous period restarts from Start
	numOldPeriod := Numerator{
		Format: "Д-{NUMBER}",
		Start:  1,
		Step:   1,
		Pad:    3,
		Reset:  "year",
		Period: "2025",
		Last:   &lastVal,
	}
	if got := numOldPeriod.next(tm); got != 1 {
		t.Errorf("new period next() = %d, want 1", got)
	}
}

func TestFormatDocNumber_Unit(t *testing.T) {
	tm := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		format string
		n      int64
		pad    int
		want   string
	}{
		{
			name:   "all placeholders",
			format: "INV-{YEAR}/{MONTH}/{DAY}-{NUMBER}",
			n:      7,
			pad:    4,
			want:   "INV-2026/05/09-0007",
		},
		{
			name:   "number with zero pad",
			format: "№{NUMBER}",
			n:      150,
			pad:    0,
			want:   "№150",
		},
		{
			name:   "pad smaller than number digits preserves full number",
			format: "DOC-{NUMBER}",
			n:      12345,
			pad:    2,
			want:   "DOC-12345",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatDocNumber(tt.format, tt.n, tt.pad, tm); got != tt.want {
				t.Errorf("FormatDocNumber() = %q, want %q", got, tt.want)
			}
		})
	}
}
