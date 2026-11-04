package calendar

import (
	"strings"
	"testing"
	"time"
)

func TestDavKeyAndSealing(t *testing.T) {
	key1 := DavKey("portal-secret-1")
	if len(key1) != 32 {
		t.Fatalf("expected 32-byte key, got %d", len(key1))
	}
	key2 := DavKey("portal-secret-1")
	if string(key1) != string(key2) {
		t.Error("DavKey must be deterministic for identical secret")
	}
	key3 := DavKey("portal-secret-2")
	if string(key1) == string(key3) {
		t.Error("DavKey must differ for different secrets")
	}

	s := &Store{Key: key1}

	// Seal and open roundtrip
	plain := "my-caldav-password-12345"
	sealed, err := s.seal(plain)
	if err != nil {
		t.Fatalf("seal failed: %v", err)
	}

	opened, err := s.open(sealed)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	if opened != plain {
		t.Errorf("open = %q, want %q", opened, plain)
	}

	// Tampered ciphertext fails
	corrupted := append([]byte(nil), sealed...)
	corrupted[len(corrupted)-1] ^= 0xff
	if _, err := s.open(corrupted); err == nil {
		t.Error("open on corrupted ciphertext must fail")
	}

	// Truncated secret (< NonceSize)
	if _, err := s.open([]byte("short")); err == nil {
		t.Error("open with short payload must fail")
	}

	// Empty key in store
	sNoKey := &Store{}
	if _, err := sNoKey.seal("pw"); err == nil {
		t.Error("seal without key must fail")
	}
	if _, err := sNoKey.open(sealed); err == nil {
		t.Error("open without key must fail")
	}

	// Bad key size in store
	sBadKey := &Store{Key: []byte("too-short")}
	if _, err := sBadKey.seal("pw"); err == nil {
		t.Error("seal with bad key length must fail")
	}
}

func TestSeriesICS(t *testing.T) {
	masterStart := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	masterEnd := masterStart.Add(time.Hour)
	skip1 := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	skip2 := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)

	master := Event{
		ID:     "master-1",
		UID:    "master-uid@example.com",
		Title:  "Recurring Standup",
		Start:  masterStart,
		End:    masterEnd,
		Repeat: "daily",
		Every:  1,
		Skip:   []time.Time{skip1, skip2},
	}

	// Exception 1 replaces skip1 (time changed to 11:00)
	kid1Start := time.Date(2026, 10, 21, 11, 0, 0, 0, time.UTC)
	kid1 := Event{
		Title:    "Moved Standup",
		Start:    kid1Start,
		End:      kid1Start.Add(time.Hour),
		SeriesAt: &skip1,
	}

	icsTemplate := "BEGIN:VCALENDAR\r\nDTSTAMP:20261019T080000Z\r\nEND:VCALENDAR"
	out := seriesICS(master, []Event{kid1}, icsTemplate)

	// Preserved DTSTAMP from template
	if !strings.Contains(out, "DTSTAMP:20261019T080000Z\r\n") {
		t.Errorf("seriesICS must preserve DTSTAMP from template:\n%s", out)
	}

	// Master event must have skip1 removed from EXDATE because kid1 represents it
	// Only skip2 should remain as EXDATE
	if strings.Contains(out, "EXDATE:20261021T100000Z\r\n") {
		t.Errorf("master EXDATE should not contain kid occurrence date 20261021:\n%s", out)
	}
	if !strings.Contains(out, "EXDATE:20261022T100000Z\r\n") {
		t.Errorf("master EXDATE must contain skip2:\n%s", out)
	}

	// Kid event must have RECURRENCE-ID matching original start (skip1)
	for _, want := range []string{
		"UID:master-uid@example.com\r\n",
		"RECURRENCE-ID:20261021T100000Z\r\n",
		"SUMMARY:Moved Standup\r\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in seriesICS output:\n%s", want, out)
		}
	}

	t.Run("all day master series", func(t *testing.T) {
		day1 := time.Date(2026, 10, 20, 0, 0, 0, 0, time.Local)
		day2 := time.Date(2026, 10, 21, 0, 0, 0, 0, time.Local)
		allDayMaster := Event{
			ID:     "all-day-master",
			Title:  "Conference",
			Start:  day1,
			End:    day1,
			AllDay: true,
			Repeat: "daily",
			Skip:   []time.Time{day2},
		}
		kid := Event{
			Title:    "Special Day",
			Start:    day2,
			End:      day2,
			AllDay:   true,
			SeriesAt: &day2,
		}

		outAllDay := seriesICS(allDayMaster, []Event{kid}, "")
		// Default UID generated
		if !strings.Contains(outAllDay, "UID:all-day-master@go24\r\n") {
			t.Errorf("expected generated UID, got:\n%s", outAllDay)
		}
		// RECURRENCE-ID;VALUE=DATE for all day
		if !strings.Contains(outAllDay, "RECURRENCE-ID;VALUE=DATE:20261021\r\n") {
			t.Errorf("expected all-day RECURRENCE-ID;VALUE=DATE, got:\n%s", outAllDay)
		}
	})
}
