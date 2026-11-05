package telephony

import (
	"strings"
	"testing"
	"time"
)

func TestCallbackKey(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"11111111-2222-3333-4444-555555555555", "go24-cb-111111112222"},
		{"abcdefab-cdef-1234-5678-90abcdef0123", "go24-cb-abcdefabcdef"},
		{"12345678-9012-3456-7890-123456789012", "go24-cb-123456789012"},
	}
	for _, tc := range tests {
		if got := CallbackKey(tc.id); got != tc.want {
			t.Errorf("CallbackKey(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestTrunkWorkingEdgeCases(t *testing.T) {
	// Line without working hours is always working
	always := Trunk{WorkOn: false}
	sundayNight := time.Date(2026, 10, 25, 23, 59, 59, 0, time.Local)
	if !always.Working(sundayNight) {
		t.Errorf("Trunk without WorkOn should always be working")
	}

	// Line with working hours Mon-Fri (1-5) from 09:00 to 18:00
	tr := Trunk{
		WorkOn:   true,
		WorkDays: []int{1, 2, 3, 4, 5},
		WorkFrom: "09:00",
		WorkTo:   "18:00",
	}

	tests := []struct {
		name string
		at   time.Time
		want bool
	}{
		// Monday = 1
		{"mon before work", time.Date(2026, 10, 26, 8, 59, 59, 0, time.Local), false},
		{"mon exact start", time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local), true},
		{"mon during work", time.Date(2026, 10, 26, 14, 30, 0, 0, time.Local), true},
		{"mon last minute", time.Date(2026, 10, 26, 17, 59, 59, 0, time.Local), true},
		{"mon exact end", time.Date(2026, 10, 26, 18, 0, 0, 0, time.Local), false},
		{"mon evening", time.Date(2026, 10, 26, 19, 0, 0, 0, time.Local), false},

		// Saturday = 6 (day 6 not in WorkDays)
		{"sat during work hours", time.Date(2026, 10, 31, 12, 0, 0, 0, time.Local), false},

		// Sunday = 0 -> converted to 7 (day 7 not in WorkDays)
		{"sun during work hours", time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tr.Working(tc.at); got != tc.want {
				t.Errorf("Trunk.Working(%v) = %v, want %v", tc.at, got, tc.want)
			}
		})
	}

	// Line with weekend work only: Sat (6) and Sun (7)
	weekendTrunk := Trunk{
		WorkOn:   true,
		WorkDays: []int{6, 7},
		WorkFrom: "10:00",
		WorkTo:   "16:00",
	}
	sundayAt12 := time.Date(2026, 10, 25, 12, 0, 0, 0, time.Local) // Sunday
	mondayAt12 := time.Date(2026, 10, 26, 12, 0, 0, 0, time.Local) // Monday
	if !weekendTrunk.Working(sundayAt12) {
		t.Errorf("weekendTrunk should be working on Sunday at 12:00")
	}
	if weekendTrunk.Working(mondayAt12) {
		t.Errorf("weekendTrunk should NOT be working on Monday at 12:00")
	}
}

func TestRenderCallbacksDialplan(t *testing.T) {
	tr := Trunk{
		ID:     "11111111-2222-3333-4444-555555555555",
		Number: "+74951234567",
	}
	trunks := map[string]Trunk{tr.ID: tr}
	numbers := map[string]string{
		"u1": "101",
		"u2": "102",
	}

	// 1. Missing trunk is skipped
	missingTrunkCB := Callback{
		ID:    "22222222-0000-0000-0000-000000000002",
		Name:  "Missing Trunk Form",
		Trunk: "non-existent-trunk-id",
		Ring:  []string{"u1"},
	}
	gotMissing := RenderCallbacks([]Callback{missingTrunkCB}, trunks, numbers, false)
	if strings.Contains(gotMissing, "Missing Trunk Form") {
		t.Errorf("callback with missing trunk should be skipped, got:\n%s", gotMissing)
	}

	// 2. Queue mode vs All mode, and record=false
	cbQueue := Callback{
		ID:       "aaaaaaaa-1111-2222-3333-444444444444",
		Name:     `Форма "Очередь" <site>`,
		Trunk:    tr.ID,
		Ring:     []string{"u1", "u2", "u_unknown"},
		RingMode: "queue",
	}
	planNoRec := RenderCallbacks([]Callback{cbQueue}, trunks, numbers, false)
	kQueue := CallbackKey(cbQueue.ID)

	for _, want := range []string{
		"; Форма Очередь site",
		"[" + kQueue + "]",
		"exten => s,1,Set(GO24_CB=${CUT(CHANNEL,\\;,1)}\\;1)",
		"Set(CALLERID(name)=Форма Очередь site)",
		"Dial(Local/101@go24-ring,20,U(go24-cb-who^${GO24_CB}))",
		"GotoIf($[\"${DIALSTATUS}\" = \"ANSWER\"]?done)",
		"Dial(Local/102@go24-ring,20,U(go24-cb-who^${GO24_CB}))",
		"Set(GO24_BY=101)",
		"Set(GO24_TO=+74951234567)",
		"Set(GO24_LINE=+74951234567)",
		"[" + kQueue + "-out]",
		"Dial(PJSIP/${GO24_TO}@" + tr.Key() + ",60)",
	} {
		if !strings.Contains(planNoRec, want) {
			t.Errorf("plan missing %q in:\n%s", want, planNoRec)
		}
	}
	// u_unknown has no number, must not be dialed
	if strings.Contains(planNoRec, "Local/u_unknown") {
		t.Errorf("unknown user should not be dialed in:\n%s", planNoRec)
	}
	// record=false -> no Set(GO24_RECORD=1)
	if strings.Contains(planNoRec, "GO24_RECORD") {
		t.Errorf("record=false should not set GO24_RECORD in:\n%s", planNoRec)
	}

	// 3. All mode with empty ring list
	cbEmptyRing := Callback{
		ID:       "bbbbbbbb-1111-2222-3333-444444444444",
		Name:     "Empty Ring Form",
		Trunk:    tr.ID,
		Ring:     []string{},
		RingMode: "all",
	}
	planEmptyRing := RenderCallbacks([]Callback{cbEmptyRing}, trunks, numbers, true)
	kEmpty := CallbackKey(cbEmptyRing.ID)
	if !strings.Contains(planEmptyRing, "["+kEmpty+"]") || strings.Contains(planEmptyRing, "Dial(Local/") {
		t.Errorf("empty ring form should not dial anyone, got:\n%s", planEmptyRing)
	}
	if !strings.Contains(planEmptyRing, "Set(GO24_BY=)") {
		t.Errorf("empty ring form should set GO24_BY to empty, got:\n%s", planEmptyRing)
	}
	// record=true -> Set(GO24_RECORD=1) present in out context
	if !strings.Contains(planEmptyRing, "same => n,Set(GO24_RECORD=1)\n") {
		t.Errorf("record=true should set GO24_RECORD in:\n%s", planEmptyRing)
	}
}
