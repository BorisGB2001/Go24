package tasks

import (
	"strings"
	"testing"
	"time"
)

func TestSprintPercent(t *testing.T) {
	tests := []struct {
		name   string
		sprint Sprint
		want   int
	}{
		{
			name: "estimated sprint with points",
			sprint: Sprint{
				Points: 50,
				Done:   25,
			},
			want: 50,
		},
		{
			name: "unestimated sprint with task counts",
			sprint: Sprint{
				Points:    0,
				DoneCount: 3,
				Tasks:     make([]Task, 4),
			},
			want: 75,
		},
		{
			name: "empty unestimated sprint",
			sprint: Sprint{
				Points:    0,
				DoneCount: 0,
				Tasks:     nil,
			},
			want: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sprint.Percent(); got != tc.want {
				t.Errorf("Percent() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSprintOverdue(t *testing.T) {
	today := time.Now().Truncate(24 * time.Hour)
	yesterday := today.AddDate(0, 0, -1)
	tomorrow := today.AddDate(0, 0, 1)

	tests := []struct {
		name   string
		sprint Sprint
		want   bool
	}{
		{
			name:   "planned sprint past end date is not overdue",
			sprint: Sprint{Status: "planned", DateEnd: &yesterday},
			want:   false,
		},
		{
			name:   "completed sprint past end date is not overdue",
			sprint: Sprint{Status: "completed", DateEnd: &yesterday},
			want:   false,
		},
		{
			name:   "active sprint without end date is not overdue",
			sprint: Sprint{Status: "active", DateEnd: nil},
			want:   false,
		},
		{
			name:   "active sprint with past end date is overdue",
			sprint: Sprint{Status: "active", DateEnd: &yesterday},
			want:   true,
		},
		{
			name:   "active sprint ending today is not overdue",
			sprint: Sprint{Status: "active", DateEnd: &today},
			want:   false,
		},
		{
			name:   "active sprint ending tomorrow is not overdue",
			sprint: Sprint{Status: "active", DateEnd: &tomorrow},
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.sprint.Overdue(); got != tc.want {
				t.Errorf("Overdue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPointsHelpers(t *testing.T) {
	// points
	if got := points(nil); got != 0 {
		t.Errorf("points(nil) = %v, want 0", got)
	}
	f := 3.5
	if got := points(&f); got != 3.5 {
		t.Errorf("points(&3.5) = %v, want 3.5", got)
	}

	// fmtPoints
	if got := fmtPoints(nil); got != "" {
		t.Errorf("fmtPoints(nil) = %q, want empty", got)
	}
	if got := fmtPoints(&f); got != "3.5" {
		t.Errorf("fmtPoints(&3.5) = %q, want '3.5'", got)
	}
	fInt := 5.0
	if got := fmtPoints(&fInt); got != "5" {
		t.Errorf("fmtPoints(&5.0) = %q, want '5'", got)
	}
}

func TestCleanEpic(t *testing.T) {
	tests := []struct {
		name      string
		epicName  string
		color     string
		wantName  string
		wantColor string
		wantErr   bool
	}{
		{
			name:      "valid epic with hex color without hash",
			epicName:  "Core refactoring",
			color:     "FF5752",
			wantName:  "Core refactoring",
			wantColor: "FF5752",
			wantErr:   false,
		},
		{
			name:      "valid epic with hash and lowercase color",
			epicName:  "  Frontend redesign  ",
			color:     "  #47d1e2  ",
			wantName:  "Frontend redesign",
			wantColor: "47D1E2",
			wantErr:   false,
		},
		{
			name:      "empty color defaults to 69DAFF",
			epicName:  "Default color epic",
			color:     "   ",
			wantName:  "Default color epic",
			wantColor: "69DAFF",
			wantErr:   false,
		},
		{
			name:     "empty epic name returns error",
			epicName: "   ",
			color:    "#123456",
			wantErr:  true,
		},
		{
			name:     "epic name too long (> 200 runes)",
			epicName: strings.Repeat("a", 201),
			color:    "#123456",
			wantErr:  true,
		},
		{
			name:     "invalid color (not hex)",
			epicName: "Valid name",
			color:    "ZZZZZZ",
			wantErr:  true,
		},
		{
			name:     "invalid color length",
			epicName: "Valid name",
			color:    "#FFF",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, color, err := cleanEpic(tc.epicName, tc.color)
			if (err != nil) != tc.wantErr {
				t.Fatalf("cleanEpic() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if name != tc.wantName || color != tc.wantColor {
					t.Errorf("cleanEpic() = (%q, %q), want (%q, %q)", name, color, tc.wantName, tc.wantColor)
				}
			}
		})
	}
}

func TestCanPlan(t *testing.T) {
	pMember := Project{MyRole: "member"}
	if !canPlan(pMember) {
		t.Error("member must be able to plan")
	}

	pArchived := Project{MyRole: "member", Archived: true}
	if canPlan(pArchived) {
		t.Error("archived project cannot be planned")
	}

	pViewer := Project{MyRole: "viewer"}
	if canPlan(pViewer) {
		t.Error("viewer cannot plan")
	}
}
