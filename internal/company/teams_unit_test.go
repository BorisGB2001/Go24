package company

import (
	"strings"
	"testing"

	"go24/internal/i18n"
)

// TestTeamTotal: Team.Total counts head (if any), deputies and members.
func TestTeamTotal(t *testing.T) {
	p1 := Person{ID: "u1", Name: "Alice"}
	p2 := Person{ID: "u2", Name: "Bob"}
	p3 := Person{ID: "u3", Name: "Charlie"}

	// Empty team
	tEmpty := &Team{}
	if got := tEmpty.Total(); got != 0 {
		t.Errorf("empty team Total() = %d, want 0", got)
	}

	// Team with deputies and members, no head
	tNoHead := &Team{
		Deputies: []Person{p1},
		Members:  []Person{p2, p3},
	}
	if got := tNoHead.Total(); got != 3 {
		t.Errorf("no head team Total() = %d, want 3", got)
	}

	// Team with head, deputies and members
	tFull := &Team{
		Head:     &p1,
		Deputies: []Person{p2},
		Members:  []Person{p3},
	}
	if got := tFull.Total(); got != 3 {
		t.Errorf("full team Total() = %d, want 3", got)
	}
}

// TestCleanTeam: cleanTeam validates name, description length and department requirement.
func TestCleanTeam(t *testing.T) {
	cases := []struct {
		name     string
		input    TeamInput
		wantErr  error
		wantName string
		wantDesc string
	}{
		{
			name:    "empty name",
			input:   TeamInput{DepartmentID: "d1", Name: "   "},
			wantErr: i18n.E("err.team_name"),
		},
		{
			name:    "name too long > 200 runes",
			input:   TeamInput{DepartmentID: "d1", Name: strings.Repeat("a", 201)},
			wantErr: i18n.E("err.team_name"),
		},
		{
			name:    "description too long > 2000 runes",
			input:   TeamInput{DepartmentID: "d1", Name: "Frontend", Description: strings.Repeat("b", 2001)},
			wantErr: i18n.E("err.team_description"),
		},
		{
			name:    "missing department",
			input:   TeamInput{DepartmentID: "", Name: "Frontend"},
			wantErr: i18n.E("err.bad_form"),
		},
		{
			name:     "valid input with trimming",
			input:    TeamInput{DepartmentID: "d1", Name: "  Core Dev  ", Description: "  Core engine  "},
			wantName: "Core Dev",
			wantDesc: "Core engine",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.input
			err := cleanTeam(&in)
			if tc.wantErr != nil {
				if err == nil || err.Error() != tc.wantErr.Error() {
					t.Fatalf("cleanTeam() err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("cleanTeam() unexpected err = %v", err)
			}
			if in.Name != tc.wantName || in.Description != tc.wantDesc {
				t.Errorf("cleanTeam() name=%q desc=%q, want name=%q desc=%q",
					in.Name, in.Description, tc.wantName, tc.wantDesc)
			}
		})
	}
}
