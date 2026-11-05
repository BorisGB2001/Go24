package telephony

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestMenuKey(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"11111111-2222-3333-4444-555555555555", "go24-ivr-111111112222"},
		{"abcdefab-cdef-1234-5678-90abcdef0123", "go24-ivr-abcdefabcdef"},
		{"12345678-9012-3456-7890-123456789012", "go24-ivr-123456789012"},
	}
	for _, tc := range tests {
		if got := MenuKey(tc.id); got != tc.want {
			t.Errorf("MenuKey(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestMenuRows(t *testing.T) {
	m := Menu{
		Items: []MenuItem{
			{Digit: "1", Act: ActHangup},
			{Digit: "2", Act: MenuRepeat},
		},
	}
	rows := m.Rows()
	if len(rows) != len(m.Items)+2 {
		t.Fatalf("m.Rows() length = %d, want %d", len(rows), len(m.Items)+2)
	}
	// Verify last two rows are empty MenuItems
	if rows[2].Act != "" || rows[3].Act != "" {
		t.Errorf("last two rows of Menu.Rows() should be empty: %+v, %+v", rows[2], rows[3])
	}
	// Verify Rows() returns a clone and mutating it doesn't affect original
	rows[0].Digit = "9"
	if m.Items[0].Digit != "1" {
		t.Errorf("mutating Rows() modified original Menu.Items: %q", m.Items[0].Digit)
	}
}

func TestMenuItemValidation(t *testing.T) {
	menus := []string{"m1", "m2"}
	groups := []string{"g1", "g2"}
	self := "m1"

	var ie *i18n.Error

	// MenuRing valid & user deduplication
	itRing, err := menuItem(MenuItem{Act: MenuRing, Users: []string{"u1", "u1", "u2", ""}, Mode: "queue"}, menus, groups, self)
	if err != nil {
		t.Fatalf("menuItem(MenuRing) unexpected error: %v", err)
	}
	if len(itRing.Users) != 2 || itRing.Users[0] != "u1" || itRing.Users[1] != "u2" || itRing.Mode != "queue" {
		t.Errorf("menuItem(MenuRing) = %+v", itRing)
	}

	// MenuRing empty users error
	_, err = menuItem(MenuItem{Act: MenuRing, Users: []string{"", ""}}, menus, groups, self)
	if err == nil || !errors.As(err, &ie) || ie.Key != "err.menu_users" {
		t.Errorf("menuItem(MenuRing empty) expected err.menu_users, got %v", err)
	}

	// ActForward valid
	itFwd, err := menuItem(MenuItem{Act: ActForward, To: "+7 (900) 123-45-67"}, menus, groups, self)
	if err != nil || itFwd.To != "+79001234567" {
		t.Errorf("menuItem(ActForward) = %+v, %v", itFwd, err)
	}

	// ActForward invalid number
	_, err = menuItem(MenuItem{Act: ActForward, To: "abc"}, menus, groups, self)
	if err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_forward" {
		t.Errorf("menuItem(ActForward invalid) expected err.trunk_forward, got %v", err)
	}

	// MenuGoto self error
	_, err = menuItem(MenuItem{Act: MenuGoto, Menu: self}, menus, groups, self)
	if err == nil || !errors.As(err, &ie) || ie.Key != "err.menu_goto" {
		t.Errorf("menuItem(MenuGoto self) expected err.menu_goto, got %v", err)
	}

	// MenuGoto unknown menu error
	_, err = menuItem(MenuItem{Act: MenuGoto, Menu: "unknown"}, menus, groups, self)
	if err == nil || !errors.As(err, &ie) || ie.Key != "err.menu_goto" {
		t.Errorf("menuItem(MenuGoto unknown) expected err.menu_goto, got %v", err)
	}

	// MenuGoto valid
	itGoto, err := menuItem(MenuItem{Act: MenuGoto, Menu: "m2"}, menus, groups, self)
	if err != nil || itGoto.Menu != "m2" {
		t.Errorf("menuItem(MenuGoto m2) = %+v, %v", itGoto, err)
	}

	// MenuGroup unknown group error
	_, err = menuItem(MenuItem{Act: MenuGroup, Group: "unknown"}, menus, groups, self)
	if err == nil || !errors.As(err, &ie) || ie.Key != "err.menu_group" {
		t.Errorf("menuItem(MenuGroup unknown) expected err.menu_group, got %v", err)
	}

	// MenuGroup valid
	itGrp, err := menuItem(MenuItem{Act: MenuGroup, Group: "g1"}, menus, groups, self)
	if err != nil || itGrp.Group != "g1" {
		t.Errorf("menuItem(MenuGroup g1) = %+v, %v", itGrp, err)
	}

	// Simple actions: MenuRepeat, ActVoicemail, ActHangup
	for _, act := range []string{MenuRepeat, ActVoicemail, ActHangup} {
		it, err := menuItem(MenuItem{Act: act}, menus, groups, self)
		if err != nil || it.Act != act {
			t.Errorf("menuItem(%s) = %+v, %v", act, it, err)
		}
	}

	// Unknown action error
	_, err = menuItem(MenuItem{Act: "invalid_action"}, menus, groups, self)
	if err == nil || !errors.As(err, &ie) || ie.Key != "err.menu_digit" {
		t.Errorf("menuItem(unknown) expected err.menu_digit, got %v", err)
	}
}

func TestMenuActDetailed(t *testing.T) {
	keys := map[string]string{
		"m2": "go24-ivr-m2",
		"g1": "go24-q-g1",
	}
	numbers := map[string]string{
		"u1": "101",
		"u2": "102",
	}

	// 1. MenuRing with queue mode
	ringQueue := menuAct("1", MenuItem{Act: MenuRing, Users: []string{"u1", "u2"}, Mode: "queue"}, keys, numbers)
	for _, want := range []string{
		"exten => 1,1,Set(GO24_BY=101)\n",
		"same => n,Dial(Local/101@go24-ring,20,g)\n",
		"same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n",
		"same => n,Dial(Local/102@go24-ring,20,g)\n",
		"same => n,Goto(${GO24_BACK})\n",
		"same => n(done),Gosub(go24-rate,s,1(${GO24_RATEP}))\n",
		"same => n,Hangup()\n",
	} {
		if !strings.Contains(ringQueue, want) {
			t.Errorf("menuAct(MenuRing queue) missing %q in:\n%s", want, ringQueue)
		}
	}

	// 2. MenuRing with empty ring (users not in numbers map) -> fall back to GO24_BACK
	ringEmpty := menuAct("1", MenuItem{Act: MenuRing, Users: []string{"u_none"}}, keys, numbers)
	if ringEmpty != "exten => 1,1,Goto(${GO24_BACK})\n" {
		t.Errorf("menuAct(MenuRing empty) = %q, want Goto(${GO24_BACK})", ringEmpty)
	}

	// 3. ActForward to inner number (len <= 8, no "+")
	fwdInner := menuAct("2", MenuItem{Act: ActForward, To: "105"}, keys, numbers)
	for _, want := range []string{
		"exten => 2,1,Set(GO24_BY=105)\n",
		"same => n,Dial(Local/105@go24-ring,45,g)\n",
		"same => n,GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)\n",
		"same => n,Goto(${GO24_BACK})\n",
		"same => n(done),Gosub(go24-rate,s,1(${GO24_RATEP}))\n",
	} {
		if !strings.Contains(fwdInner, want) {
			t.Errorf("menuAct(Forward inner) missing %q in:\n%s", want, fwdInner)
		}
	}

	// 4. ActForward to external number
	fwdOut := menuAct("3", MenuItem{Act: ActForward, To: "+79001234567"}, keys, numbers)
	wantFwdOut := "exten => 3,1,Gosub(go24-forward,s,1(${GO24_TRUNK},${GO24_TNUM},+79001234567))\n same => n,Hangup()\n"
	if fwdOut != wantFwdOut {
		t.Errorf("menuAct(Forward out) = %q, want %q", fwdOut, wantFwdOut)
	}

	// 5. ActVoicemail
	vm := menuAct("4", MenuItem{Act: ActVoicemail}, keys, numbers)
	wantVM := "exten => 4,1,Gosub(go24-voicemail,s,1(${GO24_GREET}))\n same => n,Hangup()\n"
	if vm != wantVM {
		t.Errorf("menuAct(Voicemail) = %q, want %q", vm, wantVM)
	}

	// 6. MenuGroup known vs unknown
	grpKnown := menuAct("5", MenuItem{Act: MenuGroup, Group: "g1"}, keys, numbers)
	if grpKnown != "exten => 5,1,Goto(go24-q-g1,s,1)\n" {
		t.Errorf("menuAct(MenuGroup known) = %q", grpKnown)
	}
	grpUnknown := menuAct("5", MenuItem{Act: MenuGroup, Group: "g_missing"}, keys, numbers)
	if grpUnknown != "exten => 5,1,Goto(${GO24_BACK})\n" {
		t.Errorf("menuAct(MenuGroup unknown) = %q", grpUnknown)
	}

	// 7. MenuGoto known vs unknown
	gotoKnown := menuAct("6", MenuItem{Act: MenuGoto, Menu: "m2"}, keys, numbers)
	if gotoKnown != "exten => 6,1,Goto(go24-ivr-m2,s,1)\n" {
		t.Errorf("menuAct(MenuGoto known) = %q", gotoKnown)
	}
	gotoUnknown := menuAct("6", MenuItem{Act: MenuGoto, Menu: "m_missing"}, keys, numbers)
	if gotoUnknown != "exten => 6,1,Hangup()\n" {
		t.Errorf("menuAct(MenuGoto unknown) = %q", gotoUnknown)
	}

	// 8. MenuRepeat
	repeat := menuAct("*", MenuItem{Act: MenuRepeat}, keys, numbers)
	if repeat != "exten => *,1,Goto(s,1)\n" {
		t.Errorf("menuAct(MenuRepeat) = %q", repeat)
	}

	// 9. Default / Hangup
	hangup := menuAct("#", MenuItem{Act: ActHangup}, keys, numbers)
	if hangup != "exten => #,1,Hangup()\n" {
		t.Errorf("menuAct(ActHangup) = %q", hangup)
	}
}
