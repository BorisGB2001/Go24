package crm

import (
	"testing"
	"time"
)

func TestActivityOverdueAndPath_Unit(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)
	future := time.Now().Add(1 * time.Hour)
	done := time.Now().Add(-30 * time.Minute)

	tests := []struct {
		name        string
		act         Activity
		wantOverdue bool
		wantPath    string
	}{
		{
			name: "open past deadline is overdue",
			act: Activity{
				Entity:   "deal",
				EntityID: "d-101",
				Deadline: past,
				DoneAt:   nil,
			},
			wantOverdue: true,
			wantPath:    "/crm/deals/d-101",
		},
		{
			name: "open future deadline is not overdue",
			act: Activity{
				Entity:   "lead",
				EntityID: "l-202",
				Deadline: future,
				DoneAt:   nil,
			},
			wantOverdue: false,
			wantPath:    "/crm/leads/l-202",
		},
		{
			name: "completed past deadline is not overdue",
			act: Activity{
				Entity:   "contact",
				EntityID: "c-303",
				Deadline: past,
				DoneAt:   &done,
			},
			wantOverdue: false,
			wantPath:    "/crm/contacts/c-303",
		},
		{
			name: "company activity path with future deadline",
			act: Activity{
				Entity:   "company",
				EntityID: "co-404",
				Deadline: future,
			},
			wantOverdue: false,
			wantPath:    "/crm/companies/co-404",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.act.Overdue(); got != tt.wantOverdue {
				t.Errorf("Activity.Overdue() = %v, want %v", got, tt.wantOverdue)
			}
			if got := tt.act.Path(); got != tt.wantPath {
				t.Errorf("Activity.Path() = %q, want %q", got, tt.wantPath)
			}
		})
	}
}

func TestEndOfDay_Unit(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)
	tm := time.Date(2026, 10, 28, 14, 15, 30, 0, loc)
	got := endOfDay(tm)
	// endOfDay returns start of the next day (midnight) in the same location
	want := time.Date(2026, 10, 29, 0, 0, 0, 0, loc)

	if !got.Equal(want) || got.Location() != loc {
		t.Errorf("endOfDay() = %v (%v), want %v (%v)", got, got.Location(), want, loc)
	}
}
