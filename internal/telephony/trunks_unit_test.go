package telephony

import (
	"errors"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestTrunkKey(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want string
	}{
		{"11111111-2222-3333-4444-555555555555", "trunk-111111112222"},
		{"abcdefab-cdef-1234-5678-90abcdef0123", "trunk-abcdefabcdef"},
		{"12345678-9012-3456-7890-123456789012", "trunk-123456789012"},
	} {
		tr := Trunk{ID: tc.id}
		if got := tr.Key(); got != tc.want {
			t.Errorf("Trunk{ID: %q}.Key() = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestWeekdays(t *testing.T) {
	tests := []struct {
		days []int
		want string
	}{
		{[]int{1, 2, 5}, "mon&tue&fri"},
		{[]int{1, 2, 3, 4, 5, 6, 7}, "mon&tue&wed&thu&fri&sat&sun"},
		{[]int{7}, "sun"},
		{[]int{3, 4}, "wed&thu"},
		{[]int{}, "*"},
		{nil, "*"},
		{[]int{0, 8, -1}, "*"},         // values outside 1..7 ignored
		{[]int{2, 0, 4, 9}, "tue&thu"}, // filter out invalid days
	}
	for _, tc := range tests {
		if got := weekdays(tc.days); got != tc.want {
			t.Errorf("weekdays(%v) = %q, want %q", tc.days, got, tc.want)
		}
	}
}

func TestAction(t *testing.T) {
	// Voicemail
	act, to, err := action(ActVoicemail, "anything")
	if err != nil || act != ActVoicemail || to != "" {
		t.Errorf("action(ActVoicemail) = (%q, %q, %v), want (%q, %q, nil)", act, to, err, ActVoicemail, "")
	}

	// Forward valid numbers
	for _, num := range []string{"+7 (900) 123-45-67", "101", "+18005550199", "88005553535"} {
		wantClean := Dialable(num)
		act, to, err := action(ActForward, num)
		if err != nil || act != ActForward || to != wantClean {
			t.Errorf("action(ActForward, %q) = (%q, %q, %v), want (%q, %q, nil)", num, act, to, err, ActForward, wantClean)
		}
	}

	// Forward invalid numbers
	var ie *i18n.Error
	for _, badNum := range []string{"", "   ", "abc", "*72#", "#123", "+123456789012345678901"} {
		_, _, err := action(ActForward, badNum)
		if err == nil || !errors.As(err, &ie) || ie.Key != "err.trunk_forward" {
			t.Errorf("action(ActForward, %q) expected err.trunk_forward, got %v", badNum, err)
		}
	}

	// Default action is hangup
	for _, otherAct := range []string{ActHangup, "unknown", "", "busy"} {
		act, to, err := action(otherAct, "123")
		if err != nil || act != ActHangup || to != "" {
			t.Errorf("action(%q) = (%q, %q, %v), want (%q, %q, nil)", otherAct, act, to, err, ActHangup, "")
		}
	}
}

func TestHandle(t *testing.T) {
	tr := Trunk{ID: "11111111-2222-3333-4444-555555555555", Number: "+74950000000"}

	// Voicemail
	got := handle(tr, "(noans)", ActVoicemail, "", false, "/etc/asterisk/greet-1")
	wantVM := " same => n(noans),Gosub(go24-voicemail,s,1(/etc/asterisk/greet-1))\n same => n,Hangup()\n"
	if got != wantVM {
		t.Errorf("handle(Voicemail) = %q, want %q", got, wantVM)
	}

	// Forward to inner number (<= 8 chars, no "+")
	gotInner := handle(tr, "(off)", ActForward, "101", false, "")
	wantInner := " same => n(off),Dial(Local/101@go24-ring,45)\n same => n,Hangup()\n"
	if gotInner != wantInner {
		t.Errorf("handle(Forward inner) = %q, want %q", gotInner, wantInner)
	}

	// Forward to outside number (+ prefix)
	gotOutPlus := handle(tr, "(noans)", ActForward, "+79001234567", false, "")
	wantOutPlus := " same => n(noans),Gosub(go24-forward,s,1(" + tr.Key() + ",+74950000000,+79001234567))\n same => n,Hangup()\n"
	if gotOutPlus != wantOutPlus {
		t.Errorf("handle(Forward out plus) = %q, want %q", gotOutPlus, wantOutPlus)
	}

	// Forward to outside number (> 8 chars, no "+")
	gotOutLong := handle(tr, "(noans)", ActForward, "88005553535", false, "")
	wantOutLong := " same => n(noans),Gosub(go24-forward,s,1(" + tr.Key() + ",+74950000000,88005553535))\n same => n,Hangup()\n"
	if gotOutLong != wantOutLong {
		t.Errorf("handle(Forward out long) = %q, want %q", gotOutLong, wantOutLong)
	}

	// Busy hangup
	gotBusy := handle(tr, "(noans)", ActHangup, "", true, "")
	wantBusy := " same => n(noans),Busy(5)\n same => n,Hangup()\n"
	if gotBusy != wantBusy {
		t.Errorf("handle(Hangup busy) = %q, want %q", gotBusy, wantBusy)
	}

	// Normal hangup
	gotHangup := handle(tr, "(off)", ActHangup, "", false, "")
	wantHangup := " same => n(off),Hangup()\n"
	if gotHangup != wantHangup {
		t.Errorf("handle(Hangup normal) = %q, want %q", gotHangup, wantHangup)
	}
}

func TestOutLinesDetailed(t *testing.T) {
	t1 := Trunk{ID: "11111111-0000-0000-0000-000000000001", Name: "T1", OutAll: true}
	t2 := Trunk{ID: "22222222-0000-0000-0000-000000000002", Name: "T2", Out: []string{"u2", "u3"}}
	t3 := Trunk{ID: "33333333-0000-0000-0000-000000000003", Name: "T3", Out: []string{"u3"}}
	trunks := []Trunk{t1, t2, t3}

	numbers := map[string]string{
		"u1": "101",
		"u2": "102",
		"u3": "103",
		"u4": "104",
		"u5": "105",
	}

	limits := map[string]string{
		"u4": "none", // outgoing forbidden
	}

	prefs := map[string]Prefs{
		"u2": {OutTrunk: t2.ID}, // prefers t2 (allowed)
		"u3": {OutTrunk: t3.ID}, // prefers t3 (allowed)
		"u5": {OutTrunk: t3.ID}, // prefers t3 but only allowed t1 (fallback)
	}

	out := OutLines(trunks, numbers, limits, prefs)

	// u1: no pref, allowed t1 (OutAll) -> t1
	if out["101"].ID != t1.ID {
		t.Errorf("u1 trunk = %q, want %q", out["101"].ID, t1.ID)
	}
	// u2: prefers t2, allowed t1 and t2 -> t2
	if out["102"].ID != t2.ID {
		t.Errorf("u2 trunk = %q, want %q", out["102"].ID, t2.ID)
	}
	// u3: prefers t3, allowed t1, t2, t3 -> t3
	if out["103"].ID != t3.ID {
		t.Errorf("u3 trunk = %q, want %q", out["103"].ID, t3.ID)
	}
	// u4: limit "none" -> not in map
	if _, ok := out["104"]; ok {
		t.Errorf("u4 with limit none should not have trunk, got %+v", out["104"])
	}
	// u5: prefers t3 (not allowed), falls back to t1
	if out["105"].ID != t1.ID {
		t.Errorf("u5 trunk fallback = %q, want %q", out["105"].ID, t1.ID)
	}

	// If no trunks available for a user with OutAll false
	restrictedTrunks := []Trunk{t2}
	outRestricted := OutLines(restrictedTrunks, map[string]string{"u1": "101"}, nil, nil)
	if _, ok := outRestricted["101"]; ok {
		t.Errorf("u1 should not have trunk when not allowed on any, got %+v", outRestricted["101"])
	}
}

func TestRenderTrunksDetailed(t *testing.T) {
	tr := Trunk{
		ID:          "11111111-2222-3333-4444-555555555555",
		Name:        `Основная "Линия"`,
		Host:        "sip.provider.com:5060",
		Username:    "prov_user",
		AuthUser:    "auth_prov_user",
		Password:    "sec;ret;pass",
		Number:      "+74951234567",
		Register:    true,
		OutAll:      true,
		RingMode:    "queue",
		Ring:        []string{"u1", "u2"},
		WorkOn:      true,
		WorkDays:    []int{1, 2, 3, 4, 5},
		WorkFrom:    "09:00",
		WorkTo:      "18:00",
		RateOn:      true,
		RateSum:     "ratesum123",
		GreetingSum: "greetsum456",
		NoAnswer:    ActVoicemail,
		OffHours:    ActForward,
		OffHoursTo:  "+79001112233",
		ToOwner:     true,
	}

	numbers := map[string]string{"u1": "101", "u2": "102", "u3": "103"}
	limits := map[string]string{"u1": "crm", "u3": "none"}
	prefs := map[string]Prefs{"u1": {OutTrunk: tr.ID}}

	pjsip, dialplan := RenderTrunks([]Trunk{tr}, numbers, nil, limits, prefs, true, "Europe/Moscow")

	// PJSIP checks
	k := tr.Key()
	for _, want := range []string{
		"[" + k + "-auth]\ntype = auth\nauth_type = userpass\nusername = auth_prov_user\npassword = sec\\;ret\\;pass\n",
		"[" + k + "]\ntype = aor\ncontact = sip:sip.provider.com:5060\n",
		"[" + k + "]\ntype = endpoint\ncontext = go24-in-" + k + "\n",
		"from_user = prov_user\nfrom_domain = sip.provider.com\n",
		"set_var = GO24_RECORD=1\n",
		"[" + k + "]\ntype = identify\nendpoint = " + k + "\nmatch = sip.provider.com\n",
		"[" + k + "]\ntype = registration\n",
		"contact_user = 74951234567\n",
	} {
		if !strings.Contains(pjsip, want) {
			t.Errorf("RenderTrunks pjsip missing %q in:\n%s", want, pjsip)
		}
	}

	// Dialplan checks
	for _, want := range []string{
		"[go24-in-" + k + "]",
		"GotoIfTime(09:00-18:00,mon&tue&wed&thu&fri,*,*,Europe/Moscow?work:off)",
		"Gosub(go24-owner,s,1(20,g))",
		"Dial(Local/101@go24-ring,20,g)",
		"Dial(Local/102@go24-ring,20,g)",
		"same => n(noans),Gosub(go24-voicemail,s,1(" + AstDir + "/greet-greetsum456))",
		"same => n(off),Gosub(go24-forward,s,1(" + k + ",+74951234567,+79001112233))",
		"same => n(rate),Gosub(go24-rate,s,1(" + AstDir + "/greet-ratesum123))",
		"exten => _X./101,1,Gosub(go24-out-crm,s,1(" + k + ",+74951234567,${EXTEN}))",
		"exten => _+X./101,1,Gosub(go24-out-crm,s,1(" + k + ",+74951234567,${EXTEN}))",
		"exten => _X.,1,Gosub(go24-no-line,s,1)",
	} {
		if !strings.Contains(dialplan, want) {
			t.Errorf("RenderTrunks dialplan missing %q in:\n%s", want, dialplan)
		}
	}

	// 103 with limit "none" should NOT have dial-out pattern
	if strings.Contains(dialplan, "/103,") {
		t.Errorf("dialplan should not contain outbound routes for 103 (limit none):\n%s", dialplan)
	}

	// Verify fallback when AuthUser is empty -> uses Username
	trNoAuth := tr
	trNoAuth.AuthUser = ""
	pjsipNoAuth, _ := RenderTrunks([]Trunk{trNoAuth}, nil, nil, nil, nil, false, "")
	if !strings.Contains(pjsipNoAuth, "username = prov_user\n") {
		t.Errorf("pjsip with empty AuthUser should fall back to Username:\n%s", pjsipNoAuth)
	}

	// Verify non-registered trunk
	trNoReg := tr
	trNoReg.Register = false
	pjsipNoReg, _ := RenderTrunks([]Trunk{trNoReg}, nil, nil, nil, nil, false, "")
	if strings.Contains(pjsipNoReg, "type = registration") {
		t.Errorf("pjsip with Register=false should not have registration block:\n%s", pjsipNoReg)
	}
}
