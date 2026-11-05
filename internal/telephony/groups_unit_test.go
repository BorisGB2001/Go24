package telephony

import (
	"strings"
	"testing"
)

func TestGroupKey(t *testing.T) {
	tests := []struct {
		id   string
		want string
	}{
		{"11111111-2222-3333-4444-555555555555", "go24-q-111111112222"},
		{"abcdefab-cdef-1234-5678-90abcdef0123", "go24-q-abcdefabcdef"},
		{"12345678-9012-3456-7890-123456789012", "go24-q-123456789012"},
	}
	for _, tc := range tests {
		if got := GroupKey(tc.id); got != tc.want {
			t.Errorf("GroupKey(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestRenderGroupsDetailed(t *testing.T) {
	gLinear := Group{
		ID:       "11111111-2222-3333-4444-555555555555",
		Name:     "Линейная группа",
		Users:    []string{"u1", "u_unknown", "u2"},
		Strategy: "linear",
		Ring:     10,
		Wait:     45,
		MusicSum: "musicsum1",
	}

	gEmpty := Group{
		ID:       "22222222-2222-3333-4444-555555555555",
		Name:     "Пустая группа без музыки",
		Users:    []string{},
		Strategy: "all",
		Ring:     15,
		Wait:     30,
		MusicSum: "",
	}

	numbers := map[string]string{
		"u1": "101",
		"u2": "102",
	}

	queues, moh, dialplan := RenderGroups([]Group{gLinear, gEmpty}, numbers)

	kLinear := GroupKey(gLinear.ID)
	kEmpty := GroupKey(gEmpty.ID)

	// Queues configuration
	for _, want := range []string{
		"[" + kLinear + "]\nstrategy = linear\ntimeout = 10\n",
		"musicclass = " + kLinear,
		"member => Local/101@go24-ring/n,0,101",
		"member => Local/102@go24-ring/n,0,102",
		"[" + kEmpty + "]\nstrategy = ringall\ntimeout = 15\n",
	} {
		if !strings.Contains(queues, want) {
			t.Errorf("queues missing %q in:\n%s", want, queues)
		}
	}
	// u_unknown has no number, must not be a member
	if strings.Contains(queues, "u_unknown") {
		t.Errorf("queues should not contain u_unknown in:\n%s", queues)
	}
	// gEmpty has no music -> musicclass should only appear once (for gLinear)
	if strings.Count(queues, "musicclass = ") != 1 {
		t.Errorf("queues should have exactly 1 musicclass, got:\n%s", queues)
	}

	// Music on hold configuration
	if !strings.Contains(moh, "["+kLinear+"]\nmode = playlist\nentry = "+AstDir+"/greet-musicsum1\n") {
		t.Errorf("moh missing entry for gLinear in:\n%s", moh)
	}
	if strings.Contains(moh, kEmpty) {
		t.Errorf("moh should not contain gEmpty (no music) in:\n%s", moh)
	}

	// Dialplan configuration
	for _, want := range []string{
		"[" + kLinear + "]\nexten => s,1,Set(GO24_BY=101)\n same => n,Set(__GO24_QCALLER=${CHANNEL})\n same => n,Queue(" + kLinear + ",c,,,45,,go24-q-who)\n",
		"[" + kEmpty + "]\nexten => s,1,Set(GO24_BY=)\n same => n,Set(__GO24_QCALLER=${CHANNEL})\n same => n,Queue(" + kEmpty + ",cr,,,30,,go24-q-who)\n",
		"Goto(${GO24_BACK})",
		"Gosub(go24-rate,s,1(${GO24_RATEP}))",
	} {
		if !strings.Contains(dialplan, want) {
			t.Errorf("dialplan missing %q in:\n%s", want, dialplan)
		}
	}
}
