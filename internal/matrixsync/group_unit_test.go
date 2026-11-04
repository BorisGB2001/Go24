package matrixsync

import (
	"testing"

	"go24/internal/matrix"
)

// TestRightOf verifies mapping from power levels to Right strings.
func TestRightOf(t *testing.T) {
	tests := []struct {
		level int
		want  string
	}{
		{level: -10, want: RightAll},
		{level: 0, want: RightAll},
		{level: 1, want: RightAdmins},
		{level: 50, want: RightAdmins},
		{level: 51, want: RightOwner},
		{level: 100, want: RightOwner},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := rightOf(tt.level); got != tt.want {
				t.Errorf("rightOf(%d) = %q, want %q", tt.level, got, tt.want)
			}
		})
	}
}

// TestPLLevel verifies extracting power level integers from matrix.PowerLevels.
func TestPLLevel(t *testing.T) {
	pl := matrix.PowerLevels{
		"int_val":   int(25),
		"float_val": float64(75),
		"str_val":   "not a number",
	}

	if got := plLevel(pl, "int_val", 0); got != 25 {
		t.Errorf("plLevel(int_val) = %d, want 25", got)
	}
	if got := plLevel(pl, "float_val", 0); got != 75 {
		t.Errorf("plLevel(float_val) = %d, want 75", got)
	}
	if got := plLevel(pl, "str_val", 10); got != 10 {
		t.Errorf("plLevel(str_val) = %d, want default 10", got)
	}
	if got := plLevel(pl, "missing", 5); got != 5 {
		t.Errorf("plLevel(missing) = %d, want default 5", got)
	}
}

// TestRightsOfAndApplyRights verifies setting and reading group chat permissions.
func TestRightsOfAndApplyRights(t *testing.T) {
	pl := matrix.PowerLevels{}

	// Scenario 1: Add: all (0), Edit: admins (50), Write: owner (100)
	r1 := Rights{Add: RightAll, Edit: RightAdmins, Write: RightOwner}
	applyRights(pl, r1)

	// invite should be 0, kick should be clamped to at least adminLevel (50)
	if pl["invite"] != 0 {
		t.Errorf("pl['invite'] = %v, want 0", pl["invite"])
	}
	if pl["kick"] != 50 {
		t.Errorf("pl['kick'] = %v, want 50 (max(0, 50))", pl["kick"])
	}

	events := pl["events"].(map[string]any)
	for _, ev := range editEvents {
		if events[ev] != 50 {
			t.Errorf("events[%q] = %v, want 50", ev, events[ev])
		}
	}
	if pl["events_default"] != 100 {
		t.Errorf("pl['events_default'] = %v, want 100", pl["events_default"])
	}
	if events["m.room.pinned_events"] != 100 {
		t.Errorf("pinned_events = %v, want 100", events["m.room.pinned_events"])
	}
	// Reactions, redactions, polls must remain 0
	if events["m.reaction"] != 0 || events["m.room.redaction"] != 0 {
		t.Errorf("reaction / redaction level not 0: %+v", events)
	}
	if events["m.room.power_levels"] != 100 {
		t.Errorf("power_levels level = %v, want 100", events["m.room.power_levels"])
	}

	// Scenario 2: Add: owner (100) -> kick is 100
	r2 := Rights{Add: RightOwner, Edit: RightAll, Write: RightAdmins}
	applyRights(pl, r2)
	if pl["invite"] != 100 || pl["kick"] != 100 {
		t.Errorf("r2 invite/kick = %v/%v, want 100/100", pl["invite"], pl["kick"])
	}
}

// TestChannelRulesAndIsChannel tests channel power level rules.
func TestChannelRulesAndIsChannel(t *testing.T) {
	pl := matrix.PowerLevels{}

	// Turn channel rules on: events_default becomes 50
	channelRules(pl, true)
	if pl["events_default"] != 50 {
		t.Errorf("channelRules(true) events_default = %v, want 50", pl["events_default"])
	}
	if !isChannel(pl) {
		t.Error("isChannel(pl) should be true when events_default is 50")
	}

	// Turn channel rules off: events_default becomes 0
	channelRules(pl, false)
	if pl["events_default"] != 0 {
		t.Errorf("channelRules(false) events_default = %v, want 0", pl["events_default"])
	}
	if isChannel(pl) {
		t.Error("isChannel(pl) should be false when events_default is 0")
	}
}

// TestCanRemove verifies moderator permission logic for kicking users.
func TestCanRemove(t *testing.T) {
	pl := matrix.PowerLevels{
		"kick": float64(50),
		"users": map[string]any{
			"@owner:ex": float64(100),
			"@admin:ex": float64(50),
			"@user1:ex": float64(0),
			"@user2:ex": float64(0),
		},
	}

	// Owner (100) can remove admin (50) and user (0)
	if !canRemove(pl, "@owner:ex", "@admin:ex") {
		t.Error("owner should be able to remove admin")
	}
	if !canRemove(pl, "@owner:ex", "@user1:ex") {
		t.Error("owner should be able to remove user")
	}

	// Admin (50) can remove regular user (0)
	if !canRemove(pl, "@admin:ex", "@user1:ex") {
		t.Error("admin should be able to remove regular user")
	}

	// Admin cannot remove owner (100)
	if canRemove(pl, "@admin:ex", "@owner:ex") {
		t.Error("admin should NOT be able to remove owner")
	}

	// Admin cannot remove another admin (50)
	if canRemove(pl, "@admin:ex", "@admin:ex") {
		t.Error("admin should NOT be able to remove same-level admin")
	}

	// Regular user (0) cannot remove anyone
	if canRemove(pl, "@user1:ex", "@user2:ex") {
		t.Error("regular user should NOT be able to remove another user (below kick level)")
	}
}
