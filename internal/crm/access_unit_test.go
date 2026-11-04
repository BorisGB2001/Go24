package crm

import (
	"testing"
)

func TestRank_Unit(t *testing.T) {
	tests := []struct {
		level string
		want  int
	}{
		{"none", 0},
		{"own", 1},
		{"dept", 2},
		{"all", 3},
		{"unknown", 0},
		{"", 0},
	}

	for _, tt := range tests {
		if got := rank(tt.level); got != tt.want {
			t.Errorf("rank(%q) = %d, want %d", tt.level, got, tt.want)
		}
	}
}

func TestPermsLevel_Unit(t *testing.T) {
	perms := Perms{
		"deal": {
			"read":  "all",
			"write": "dept",
		},
		"lead": {
			"read": "own",
		},
	}

	if got := perms.Level("deal", "read"); got != "all" {
		t.Errorf("perms.Level(deal, read) = %q, want \"all\"", got)
	}
	if got := perms.Level("deal", "write"); got != "dept" {
		t.Errorf("perms.Level(deal, write) = %q, want \"dept\"", got)
	}
	if got := perms.Level("deal", "delete"); got != "none" {
		t.Errorf("perms.Level(deal, delete) = %q, want \"none\"", got)
	}
	if got := perms.Level("contact", "read"); got != "none" {
		t.Errorf("perms.Level(contact, read) = %q, want \"none\"", got)
	}
	if got := (Perms(nil)).Level("deal", "read"); got != "none" {
		t.Errorf("nil Perms.Level() = %q, want \"none\"", got)
	}
}
