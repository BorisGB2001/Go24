package calendar

import (
	"context"
	"testing"
)

func TestAccessDelegate(t *testing.T) {
	ctx := context.Background()
	if got := delegateOf(ctx); got != "" {
		t.Errorf("delegateOf(empty ctx) = %q, want empty", got)
	}

	ctx = AsDelegate(ctx, "user-secretary")
	if got := delegateOf(ctx); got != "user-secretary" {
		t.Errorf("delegateOf(ctx) = %q, want user-secretary", got)
	}
}

func TestEventAttends(t *testing.T) {
	e := Event{
		Attendees: []Attendee{
			{ID: "user-1", Name: "User 1"},
			{ID: "user-2", Name: "User 2"},
		},
	}

	if !e.attends("user-1") || !e.attends("user-2") {
		t.Error("attends should be true for user-1 and user-2")
	}
	if e.attends("user-3") || e.attends("") {
		t.Error("attends should be false for non-attendees")
	}
}

func TestEventHiddenFrom(t *testing.T) {
	ePublic := Event{Private: false, OwnerID: "user-boss"}
	if ePublic.hiddenFrom("user-secretary") {
		t.Error("public event should never be hidden from delegate")
	}

	ePrivate := Event{
		Private: true,
		OwnerID: "user-boss",
		Attendees: []Attendee{
			{ID: "user-invited"},
		},
	}

	// Delegate is empty (owner themselves) -> not hidden
	if ePrivate.hiddenFrom("") {
		t.Error("private event should not be hidden from empty delegate")
	}

	// Delegate is owner -> not hidden
	if ePrivate.hiddenFrom("user-boss") {
		t.Error("private event should not be hidden from owner as delegate")
	}

	// Delegate is in attendees list -> not hidden
	if ePrivate.hiddenFrom("user-invited") {
		t.Error("private event should not be hidden from invited delegate")
	}

	// Delegate is outside the meeting -> hidden!
	if !ePrivate.hiddenFrom("user-stranger") {
		t.Error("private event should be hidden from external delegate")
	}
}
