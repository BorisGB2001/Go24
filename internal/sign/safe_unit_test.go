package sign

import "testing"

func TestLikeEscape(t *testing.T) {
	for in, want := range map[string]string{"Приказ": "Приказ", "50%": `50\%`, "a_b": `a\_b`, `c:\d`: `c:\\d`} {
		if got := likeEscape(in); got != want {
			t.Errorf("likeEscape(%q) = %q, want %q", in, got, want)
		}
	}
}
