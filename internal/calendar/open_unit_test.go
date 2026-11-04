package calendar

import "testing"

func TestEventComing(t *testing.T) {
	// Organizer always counts as 1
	eNoAttendees := Event{}
	if got := eNoAttendees.Coming(); got != 1 {
		t.Errorf("Coming() with no attendees = %d, want 1 (organizer)", got)
	}

	e := Event{
		Attendees: []Attendee{
			{ID: "u1", Status: "accepted"},
			{ID: "u2", Status: "invited"},
			{ID: "u3", Status: "tentative"},
			{ID: "u4", Status: "declined"},
		},
	}
	// 1 organizer + 3 non-declined attendees = 4
	if got := e.Coming(); got != 4 {
		t.Errorf("Coming() = %d, want 4", got)
	}
}
