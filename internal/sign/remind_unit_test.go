package sign

import (
	"slices"
	"testing"
)

func TestRemindOf(t *testing.T) {
	tests := []struct {
		name string
		days int
		want int
	}{
		{"never (0)", 0, 0},
		{"daily (1)", 1, 1},
		{"every 2 days (2)", 2, 2},
		{"every 3 days (3)", 3, 3},
		{"weekly (7)", 7, 7},
		{"negative (-1)", -1, 1},
		{"negative large (-100)", -100, 1},
		{"4 days (unsupported, defaults to daily)", 4, 1},
		{"5 days (unsupported, defaults to daily)", 5, 1},
		{"6 days (unsupported, defaults to daily)", 6, 1},
		{"8 days (unsupported, defaults to daily)", 8, 1},
		{"14 days (unsupported, defaults to daily)", 14, 1},
		{"30 days (unsupported, defaults to daily)", 30, 1},
		{"100 days (unsupported, defaults to daily)", 100, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := RemindOf(tc.days)
			if got != tc.want {
				t.Errorf("RemindOf(%d) = %d, want %d", tc.days, got, tc.want)
			}
		})
	}
}

func TestRemindConstants(t *testing.T) {
	wantChoices := []int{0, 1, 2, 3, 7}
	if !slices.Equal(RemindDays, wantChoices) {
		t.Errorf("RemindDays = %v, want %v", RemindDays, wantChoices)
	}
	if RemindMax != 5 {
		t.Errorf("RemindMax = %d, want 5", RemindMax)
	}
}

func TestRemindStruct(t *testing.T) {
	r := Remind{
		DocID:       "d-1",
		PartyID:     "p-1",
		BatchSigner: "u-1",
		BatchTitle:  "Batch Title",
	}
	if r.DocID != "d-1" || r.PartyID != "p-1" || r.BatchSigner != "u-1" || r.BatchTitle != "Batch Title" {
		t.Errorf("Remind struct fields mismatch: %+v", r)
	}
}
