package telephony

import (
	"strings"
	"testing"
)

func TestBlackKey(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"+7 (900) 123-45-67", "9001234567"},
		{"89001234567", "9001234567"},
		{"79001234567", "9001234567"},
		{"9001234567", "9001234567"},
		{"123456789012345", "6789012345"}, // last 10 digits
		{"555", "555"},
		{"12", "12"},
		{"0", "0"},
		{"", ""},
		{"abc", ""},
		{"+--() . ", ""},
		{"tel:+7 (495) 000-11-22", "4950001122"},
		{"8 (800) 555-35-35", "8005553535"},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := BlackKey(tc.input)
			if got != tc.want {
				t.Errorf("BlackKey(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestRenderBlacklist(t *testing.T) {
	// Empty blacklist
	empty := RenderBlacklist(map[string]bool{})
	wantEmpty := "\n; the black list (Settings → Telephony → Black list)\n[go24-black]\n"
	if empty != wantEmpty {
		t.Errorf("RenderBlacklist(empty) = %q, want %q", empty, wantEmpty)
	}

	// Multiple keys: verify sorted order and deterministic rendering
	keys := map[string]bool{
		"9001112233": true,
		"555":        true,
		"8005553535": true,
		"100":        true,
	}
	got := RenderBlacklist(keys)
	wantLines := []string{
		"[go24-black]",
		"exten => 100,1,Return()",
		"exten => 555,1,Return()",
		"exten => 8005553535,1,Return()",
		"exten => 9001112233,1,Return()",
	}
	for _, l := range wantLines {
		if !strings.Contains(got, l) {
			t.Errorf("RenderBlacklist missing line %q in:\n%s", l, got)
		}
	}
	// Verify strict ascending order
	i100 := strings.Index(got, "exten => 100,1,Return()")
	i555 := strings.Index(got, "exten => 555,1,Return()")
	i800 := strings.Index(got, "exten => 8005553535,1,Return()")
	i900 := strings.Index(got, "exten => 9001112233,1,Return()")
	if !(i100 < i555 && i555 < i800 && i800 < i900) {
		t.Errorf("RenderBlacklist keys are not sorted: indices 100=%d, 555=%d, 800=%d, 900=%d in:\n%s",
			i100, i555, i800, i900, got)
	}
}
