package tasks

import (
	"testing"
)

func TestParseDuration_EdgeCases(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		// Valid representations
		{"1:00", 3600, false},
		{"0:30", 1800, false},
		{" 2:15 ", 8100, false},
		{"0.5", 1800, false},
		{"0,25", 900, false},
		{"2.5h", 9000, false},
		{"15m", 900, false},
		{"1ч", 3600, false},
		{"30м", 1800, false},
		{"1ч15м", 4500, false},

		// Invalid inputs
		{"", 0, true},
		{"   ", 0, true},
		{"0", 0, true},
		{"-5", 0, true},
		{"-1h", 0, true},
		{"abc", 0, true},
		{"1:2:3", 0, true},
		{"1:xx", 0, true},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, err := ParseDuration(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParseDuration(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("ParseDuration(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestAccessCanLogTime(t *testing.T) {
	tests := []struct {
		name  string
		roles Roles
		want  bool
	}{
		{name: "creator can log time", roles: Roles{Creator: true}, want: true},
		{name: "responsible can log time", roles: Roles{Responsible: true}, want: true},
		{name: "accomplice can log time", roles: Roles{Accomplice: true}, want: true},
		{name: "manager can log time", roles: Roles{Manager: true}, want: true},
		{name: "auditor alone cannot log time", roles: Roles{Auditor: true}, want: false},
		{name: "stranger cannot log time", roles: Roles{}, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := access{Roles: tc.roles}
			if got := a.canLogTime(); got != tc.want {
				t.Errorf("canLogTime() = %v, want %v", got, tc.want)
			}
		})
	}
}
