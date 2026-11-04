package calendar

import (
	"strings"
	"testing"
	"time"
)

func TestEventICSExport(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	start := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	end := start.Add(time.Hour)

	t.Run("basic single event", func(t *testing.T) {
		e := Event{
			ID:          "ev-1",
			Title:       "Meeting with team",
			Description: "Discuss roadmaps\nand plans",
			Where:       "Main Office",
			Start:       start,
			End:         end,
			Reminds:     []int{15},
		}

		ics := EventICS(e, now)
		for _, want := range []string{
			"BEGIN:VCALENDAR\r\n",
			"VERSION:2.0\r\n",
			"PRODID:-//go24//calendar//RU\r\n",
			"CALSCALE:GREGORIAN\r\n",
			"METHOD:PUBLISH\r\n",
			"BEGIN:VEVENT\r\n",
			"UID:ev-1@go24\r\n",
			"DTSTAMP:20261028T120000Z\r\n",
			"DTSTART:20261029T100000Z\r\n",
			"DTEND:20261029T110000Z\r\n",
			"SUMMARY:Meeting with team\r\n",
			`DESCRIPTION:Discuss roadmaps\nand plans` + "\r\n",
			"LOCATION:Main Office\r\n",
			"STATUS:CONFIRMED\r\n",
			"BEGIN:VALARM\r\n",
			"ACTION:DISPLAY\r\n",
			"TRIGGER:-PT15M\r\n",
			"END:VALARM\r\n",
			"END:VEVENT\r\n",
			"END:VCALENDAR\r\n",
		} {
			if !strings.Contains(ics, want) {
				t.Errorf("missing expected substring %q in ICS:\n%s", want, ics)
			}
		}
	})

	t.Run("private and hidden event", func(t *testing.T) {
		e := Event{
			ID:        "ev-private",
			Title:     "Confidential",
			Start:     start,
			End:       end,
			Private:   true,
			Hidden:    true,
			Important: true,
			Color:     "#de2b24",
		}

		ics := EventICS(e, now)
		if !strings.Contains(ics, "SUMMARY:—\r\n") {
			t.Errorf("hidden event should have SUMMARY:—, got:\n%s", ics)
		}
		if !strings.Contains(ics, "CLASS:PRIVATE\r\n") {
			t.Errorf("private event should have CLASS:PRIVATE, got:\n%s", ics)
		}
		if strings.Contains(ics, "Confidential") {
			t.Errorf("hidden event must not leak title:\n%s", ics)
		}
		if strings.Contains(ics, "PRIORITY:") || strings.Contains(ics, "COLOR:") {
			t.Errorf("hidden event must not output PRIORITY or COLOR:\n%s", ics)
		}
	})

	t.Run("important and colored event with room", func(t *testing.T) {
		e := Event{
			ID:        "ev-room",
			Title:     "Quarterly review",
			Start:     start,
			End:       end,
			Important: true,
			Color:     "#de2b24", // crimson
			RoomName:  "Room 101",
			Where:     "Floor 1",
		}

		ics := EventICS(e, now)
		for _, want := range []string{
			"PRIORITY:1\r\n",
			"X-MICROSOFT-CDO-IMPORTANCE:2\r\n",
			"COLOR:crimson\r\n",
			"LOCATION:Room 101 Floor 1\r\n",
		} {
			if !strings.Contains(ics, want) {
				t.Errorf("missing substring %q in ICS:\n%s", want, ics)
			}
		}
	})

	t.Run("tentative status and transparent accessibility", func(t *testing.T) {
		e := Event{
			ID:            "ev-tentative",
			Title:         "Sync",
			Start:         start,
			End:           end,
			MyStatus:      "tentative",
			Accessibility: "free",
		}

		ics := EventICS(e, now)
		if !strings.Contains(ics, "STATUS:TENTATIVE\r\n") {
			t.Errorf("expected STATUS:TENTATIVE, got:\n%s", ics)
		}
		if !strings.Contains(ics, "TRANSP:TRANSPARENT\r\n") {
			t.Errorf("expected TRANSP:TRANSPARENT, got:\n%s", ics)
		}
	})

	t.Run("recurring event with skip and until", func(t *testing.T) {
		untilDate := time.Date(2026, 12, 31, 0, 0, 0, 0, time.Local)
		skipDate := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
		e := Event{
			ID:     "ev-repeat",
			Title:  "Weekly Standup",
			Start:  start,
			End:    end,
			Repeat: "weekly",
			Every:  2,
			Days:   []int{1, 3}, // Mon, Wed
			Until:  &untilDate,
			Skip:   []time.Time{skipDate},
		}

		ics := EventICS(e, now)
		if !strings.Contains(ics, "RRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE;UNTIL=") {
			t.Errorf("missing RRULE in ICS:\n%s", ics)
		}
		if !strings.Contains(ics, "EXDATE:20261105T100000Z\r\n") {
			t.Errorf("missing EXDATE in ICS:\n%s", ics)
		}
	})

	t.Run("recurring all-day event with skip and count", func(t *testing.T) {
		allDayStart := time.Date(2026, 10, 29, 0, 0, 0, 0, time.Local)
		allDayEnd := time.Date(2026, 10, 29, 23, 59, 59, 0, time.Local)
		skipDate := time.Date(2026, 11, 2, 0, 0, 0, 0, time.Local)
		e := Event{
			ID:     "ev-allday-repeat",
			Title:  "Conference",
			Start:  allDayStart,
			End:    allDayEnd,
			AllDay: true,
			Repeat: "daily",
			Every:  1,
			Count:  10,
			Skip:   []time.Time{skipDate},
		}

		ics := EventICS(e, now)
		if !strings.Contains(ics, "RRULE:FREQ=DAILY;INTERVAL=1;COUNT=10\r\n") {
			t.Errorf("missing RRULE in ICS:\n%s", ics)
		}
		if !strings.Contains(ics, "EXDATE;VALUE=DATE:20261102\r\n") {
			t.Errorf("expected EXDATE;VALUE=DATE for all-day skip, got:\n%s", ics)
		}
	})
}
