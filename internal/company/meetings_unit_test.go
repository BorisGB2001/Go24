package company

import (
	"slices"
	"testing"
	"time"
)

// TestMeetingMethods: Meeting End, CanEdit, MemberIDs, People and MeetingTask Open.
func TestMeetingMethods(t *testing.T) {
	start := time.Date(2026, 10, 15, 14, 0, 0, 0, time.Local)
	m := Meeting{
		Start:   start,
		Minutes: 45,
		Owner:   Person{ID: "u1", Name: "Alice (Owner)"},
		Keeper:  Person{ID: "u2", Name: "Bob (Secretary)"},
		Members: []Person{
			{ID: "u2", Name: "Bob (Duplicate)"},
			{ID: "u3", Name: "Charlie"},
		},
	}

	// End
	if want := start.Add(45 * time.Minute); m.End() != want {
		t.Errorf("Meeting.End() = %v, want %v", m.End(), want)
	}

	// CanEdit
	if !m.CanEdit("u1", false) {
		t.Errorf("owner should be able to edit meeting")
	}
	if !m.CanEdit("u2", false) {
		t.Errorf("secretary (keeper) should be able to edit meeting")
	}
	if !m.CanEdit("u99", true) {
		t.Errorf("admin should be able to edit meeting")
	}
	if m.CanEdit("u3", false) {
		t.Errorf("ordinary member should not be able to edit meeting")
	}
	if m.CanEdit("u99", false) {
		t.Errorf("unrelated user should not be able to edit meeting")
	}

	// MemberIDs
	if got := m.MemberIDs(); !slices.Equal(got, []string{"u2", "u3"}) {
		t.Errorf("MemberIDs() = %v, want [u2 u3]", got)
	}

	// People (owner + keeper + members deduplicated)
	if got := m.People(); !slices.Equal(got, []string{"u1", "u2", "u3"}) {
		t.Errorf("People() = %v, want [u1 u2 u3]", got)
	}

	// MeetingTask Open
	tOpen := MeetingTask{Status: "in_progress"}
	if !tOpen.Open() {
		t.Errorf("in_progress task should be Open()")
	}
	tCompleted := MeetingTask{Status: "completed"}
	if tCompleted.Open() {
		t.Errorf("completed task should not be Open()")
	}
}
