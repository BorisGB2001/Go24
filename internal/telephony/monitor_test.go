package telephony

import (
	"encoding/json"
	"testing"
)

// TestActiveOf: the calls going on are the channels of the employees' phones and softphones that talk or ring — not
// the clients' or the lines', not of other people when only some are asked for; the one called talks to the caller,
// the one who called — to whom they called; the longest first.
func TestActiveOf(t *testing.T) {
	var chans []ariChannel
	raw := `[
	 {"name": "PJSIP/trunk1-00000001", "state": "Up", "caller": {"number": "+79001112233"}, "creationtime": "2026-10-28T12:00:00.000+0100", "dialplan": {"app_name": "Dial"}},
	 {"name": "PJSIP/w102-00000002", "state": "Up", "caller": {"number": "+79001112233", "name": "+79001112233"}, "connected": {"number": "102"}, "creationtime": "2026-10-28T12:00:05.000+0100", "dialplan": {"app_name": "AppDial"}},
	 {"name": "PJSIP/101-00000003", "state": "Up", "caller": {"number": "101"}, "connected": {"number": "+74950001122", "name": "ООО Ромашка"}, "creationtime": "2026-10-28T11:59:00.000+0100", "dialplan": {"app_name": "Dial"}},
	 {"name": "PJSIP/103-00000004", "state": "Ringing", "caller": {"number": "101"}, "connected": {"number": "101"}, "creationtime": "2026-10-28T12:01:00.000+0100", "dialplan": {"app_name": "AppDial"}},
	 {"name": "PJSIP/104-00000005", "state": "Down", "creationtime": "2026-10-28T12:01:00.000+0100"},
	 {"name": "Local/101@go24-ring-00000001;1", "state": "Up", "creationtime": "2026-10-28T12:01:00.000+0100"},
	 {"name": "PJSIP/1020-00000006", "state": "Up", "creationtime": "2026-10-28T12:01:00.000+0100"}
	]`
	if err := json.Unmarshal([]byte(raw), &chans); err != nil {
		t.Fatal(err)
	}
	lines := []Line{{UserID: "a", Name: "Анна", Number: "101"}, {UserID: "b", Name: "Борис", Number: "102"},
		{UserID: "e", Name: "Ева", Number: "103"}, {UserID: "x", Name: "Икс", Number: "104"}, {UserID: "d", Number: "103", Dup: true}}
	got := activeOf(chans, lines, nil)
	if len(got) != 3 {
		t.Fatalf("active: %+v", got)
	}
	a, b, e := got[0], got[1], got[2]
	if a.UserID != "a" || !a.Out || !a.Up || a.Peer != "+74950001122" || a.PeerName != "ООО Ромашка" || a.Channel != "PJSIP/101-00000003" {
		t.Errorf("anna: %+v", a)
	}
	if b.UserID != "b" || b.Out || b.Peer != "+79001112233" || b.PeerName != "" || b.Name != "Борис" {
		t.Errorf("boris: %+v", b)
	}
	if e.UserID != "e" || e.Up || e.Out || e.Peer != "101" {
		t.Errorf("eve: %+v", e)
	}
	if s := b.Since.Sub(a.Since).Seconds(); s != 65 {
		t.Errorf("since: %v", s)
	}
	if got := activeOf(chans, lines, []string{"b", "x"}); len(got) != 1 || got[0].UserID != "b" {
		t.Errorf("only b: %+v", got)
	}
}
