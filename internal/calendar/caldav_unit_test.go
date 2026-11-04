package calendar

import (
	"slices"
	"testing"
	"time"
)

func TestDavCTag(t *testing.T) {
	it1 := DavItem{Name: "event1.ics", ETag: `"abc"`}
	it2 := DavItem{Name: "event2.ics", ETag: `"def"`}

	tag1 := DavCTag([]DavItem{it1, it2})
	if len(tag1) != 20 { // 10 bytes hex-encoded
		t.Errorf("expected 20 hex chars, got %d (%s)", len(tag1), tag1)
	}

	// Order dependency
	tag2 := DavCTag([]DavItem{it2, it1})
	if tag1 == tag2 {
		t.Errorf("DavCTag should be order dependent, got same tag %s", tag1)
	}

	// Modifying ETag changes CTag
	it1Mod := DavItem{Name: "event1.ics", ETag: `"xyz"`}
	tag3 := DavCTag([]DavItem{it1Mod, it2})
	if tag1 == tag3 {
		t.Errorf("DavCTag should change when ETag changes, got %s", tag1)
	}

	// Empty list produces deterministic tag
	tagEmpty := DavCTag(nil)
	if tagEmpty != DavCTag([]DavItem{}) || len(tagEmpty) != 20 {
		t.Errorf("expected deterministic 20 hex chars for empty list, got %s", tagEmpty)
	}
}

func TestSynced(t *testing.T) {
	t.Run("nil old event creates new imported event with cleaned fields", func(t *testing.T) {
		in := Event{
			ID:        "dirty-id",
			ProjectID: "dirty-proj",
			Company:   true,
			RoomID:    "dirty-room",
			Video:     true,
			Title:     "New Meeting",
			Start:     time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC),
			End:       time.Date(2026, 10, 28, 11, 0, 0, 0, time.UTC),
		}

		got, invite := synced(nil, in)
		if invite != nil {
			t.Errorf("expected nil invite list, got %v", invite)
		}
		if got.ID != "" || got.ProjectID != "" || got.Company || got.RoomID != "" || got.Video {
			t.Errorf("synced(nil, in) should clear ID, ProjectID, Company, RoomID, Video; got %+v", got)
		}
		if got.Title != "New Meeting" {
			t.Errorf("expected title preserved, got %s", got.Title)
		}
	})

	t.Run("existing old event merges fields and extracts attendees", func(t *testing.T) {
		oldStart := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
		old := &Event{
			ID:            "ev-1",
			OwnerID:       "user-boss",
			ProjectID:     "proj-x",
			RoomID:        "room-1",
			RoomName:      "Conference Hall",
			Color:         "#112233",
			Video:         true,
			TZ:            "Europe/Moscow",
			Accessibility: "busy",
			Where:         "Conference Hall",
			Attendees: []Attendee{
				{ID: "user-anna", Name: "Anna", Status: "accepted"},
				{ID: "user-bob", Name: "Bob", Status: "invited"},
			},
			Start: oldStart,
			End:   oldStart.Add(time.Hour),
		}

		newStart := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)
		in := Event{
			Title:         "Updated Title",
			Description:   "Updated Description",
			Start:         newStart,
			End:           newStart.Add(2 * time.Hour),
			Where:         "Conference Hall Floor 2",
			Repeat:        "daily",
			Every:         2,
			Reminds:       []int{30},
			Private:       true,
			Accessibility: "free", // in gives free
		}

		got, invite := synced(old, in)

		// Invite list from old attendees
		if !slices.Equal(invite, []string{"user-anna", "user-bob"}) {
			t.Errorf("invite list = %v, want [user-anna user-bob]", invite)
		}

		// Preserved from old
		if got.ID != "ev-1" || got.OwnerID != "user-boss" || got.ProjectID != "proj-x" || got.RoomID != "room-1" || !got.Video || got.Color != "#112233" {
			t.Errorf("preserved fields modified: %+v", got)
		}

		// Updated from in
		if got.Title != "Updated Title" || got.Description != "Updated Description" || got.Repeat != "daily" || got.Every != 2 || !got.Private {
			t.Errorf("fields not updated from in: %+v", got)
		}

		// RoomName stripped from start of Where
		if got.Where != "Floor 2" {
			t.Errorf("Where = %q, want %q (RoomName stripped)", got.Where, "Floor 2")
		}

		// Since in.TZ was empty and !in.AllDay, old.TZ must be retained
		if got.TZ != "Europe/Moscow" {
			t.Errorf("TZ = %q, want preserved %q", got.TZ, "Europe/Moscow")
		}

		// in.Accessibility was "free", so accessibility becomes "free"
		if got.Accessibility != "free" {
			t.Errorf("Accessibility = %q, want free", got.Accessibility)
		}
	})

	t.Run("accessibility not free retains old accessibility", func(t *testing.T) {
		old := &Event{Accessibility: "absent"}
		in := Event{Accessibility: "busy"}
		got, _ := synced(old, in)
		if got.Accessibility != "absent" {
			t.Errorf("Accessibility = %q, want absent", got.Accessibility)
		}
	})

	t.Run("explicit in TZ or all-day updates timezone", func(t *testing.T) {
		old := &Event{TZ: "Europe/Moscow"}
		in := Event{TZ: "America/New_York"}
		got, _ := synced(old, in)
		if got.TZ != "America/New_York" {
			t.Errorf("TZ = %q, want America/New_York", got.TZ)
		}

		// AllDay updates even if empty
		old2 := &Event{TZ: "Europe/Moscow"}
		in2 := Event{AllDay: true, TZ: ""}
		got2, _ := synced(old2, in2)
		if got2.TZ != "" {
			t.Errorf("AllDay TZ = %q, want empty", got2.TZ)
		}
	})
}
