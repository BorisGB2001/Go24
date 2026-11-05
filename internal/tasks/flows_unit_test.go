package tasks

import "testing"

func TestFlowModeratorAndMember(t *testing.T) {
	// Moderator()
	fExplicitMod := Flow{
		OwnerID:     "owner-1",
		ModeratorID: "mod-1",
	}
	if got := fExplicitMod.Moderator(); got != "mod-1" {
		t.Errorf("Moderator() = %q, want 'mod-1'", got)
	}

	fOwnerFallback := Flow{
		OwnerID:     "owner-1",
		ModeratorID: "",
	}
	if got := fOwnerFallback.Moderator(); got != "owner-1" {
		t.Errorf("Moderator() fallback = %q, want 'owner-1'", got)
	}

	// Member()
	fWithMembers := Flow{
		Members: []User{
			{ID: "user-1", Name: "User One"},
			{ID: "user-2", Name: "User Two"},
		},
	}
	if !fWithMembers.Member("user-1") {
		t.Error("user-1 must be recognized as member")
	}
	if !fWithMembers.Member("user-2") {
		t.Error("user-2 must be recognized as member")
	}
	if fWithMembers.Member("user-3") {
		t.Error("user-3 is not a member")
	}
}
