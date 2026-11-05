package telephony

import (
	"testing"
	"time"
)

func TestActiveSeconds(t *testing.T) {
	t0 := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	a := Active{Since: t0}

	tests := []struct {
		now  time.Time
		want int
	}{
		{t0.Add(45 * time.Second), 45},
		{t0, 0},
		{t0.Add(-10 * time.Second), 0}, // before Since returns 0 (clamped by max(0, ...))
	}
	for _, tc := range tests {
		if got := a.Seconds(tc.now); got != tc.want {
			t.Errorf("Active.Seconds(%v) = %d, want %d", tc.now, got, tc.want)
		}
	}
}

func TestActiveOfAdvanced(t *testing.T) {
	lines := []Line{
		{UserID: "u1", Name: "Анна", Number: "101"},
		{UserID: "u2", Name: "Борис", Number: "102"},
		{UserID: "u3", Name: "Ева", Number: "103"},
		{UserID: "u4", Name: "Дубль", Number: "101", Dup: true},
	}

	chans := []ariChannel{
		// 1. Incoming via AppQueue, Caller.Number matches l.Number -> fallback to Connected
		{
			Name:  "PJSIP/101-00000001",
			State: "Up",
			Caller: struct {
				Name   string `json:"name"`
				Number string `json:"number"`
			}{Name: "101", Number: "101"},
			Connected: struct {
				Name   string `json:"name"`
				Number string `json:"number"`
			}{Name: "Client Org", Number: "+74950001122"},
			Created: "2026-10-28T12:00:10.000+0000",
			Dialplan: struct {
				App string `json:"app_name"`
			}{App: "AppQueue"},
		},
		// 2. Incoming via AppDial, Caller.Number is empty -> fallback to Connected
		{
			Name:  "PJSIP/w102-00000002",
			State: "Ring",
			Caller: struct {
				Name   string `json:"name"`
				Number string `json:"number"`
			}{Name: "", Number: ""},
			Connected: struct {
				Name   string `json:"name"`
				Number string `json:"number"`
			}{Name: "+79001112233", Number: "+79001112233"},
			Created: "2026-10-28T12:00:05.000+0000",
			Dialplan: struct {
				App string `json:"app_name"`
			}{App: "AppDial"},
		},
		// 3. State "Down" -> skipped
		{
			Name:    "PJSIP/103-00000003",
			State:   "Down",
			Created: "2026-10-28T12:00:00.000+0000",
		},
		// 4. Non-PJSIP technology -> skipped
		{
			Name:    "Local/101@go24-ring-00000001;1",
			State:   "Up",
			Created: "2026-10-28T12:00:00.000+0000",
		},
		// 5. Channel for non-existent line login -> skipped
		{
			Name:    "PJSIP/999-00000005",
			State:   "Up",
			Created: "2026-10-28T12:00:00.000+0000",
		},
	}

	got := activeOf(chans, lines, nil)
	if len(got) != 2 {
		t.Fatalf("activeOf returned %d calls, want 2: %+v", len(got), got)
	}

	// Stably sorted by Since ascending: u2 (12:00:05) before u1 (12:00:10)
	c1, c2 := got[0], got[1]

	// c1: u2 (Boris)
	if c1.UserID != "u2" || c1.Out || c1.Up { // State "Ring" means Up is false
		t.Errorf("c1 expected u2 incoming ringing: %+v", c1)
	}
	if c1.Peer != "+79001112233" || c1.PeerName != "" { // PeerName matching Peer is cleared to ""
		t.Errorf("c1 peer = (%q, %q), want (+79001112233, \"\")", c1.Peer, c1.PeerName)
	}

	// c2: u1 (Anna)
	if c2.UserID != "u1" || c2.Out || !c2.Up { // State "Up" means Up is true
		t.Errorf("c2 expected u1 incoming up: %+v", c2)
	}
	if c2.Peer != "+74950001122" || c2.PeerName != "Client Org" {
		t.Errorf("c2 peer fallback = (%q, %q), want (+74950001122, Client Org)", c2.Peer, c2.PeerName)
	}

	// Filter by users
	filtered := activeOf(chans, lines, []string{"u1"})
	if len(filtered) != 1 || filtered[0].UserID != "u1" {
		t.Errorf("activeOf filtered for u1 = %+v, want 1 entry", filtered)
	}
}
