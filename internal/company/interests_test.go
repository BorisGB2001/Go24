package company

import (
	"slices"
	"strings"
	"testing"
)

// TestNormInterests: «#» and spaces go, the same interest in another case is kept once, punctuation around is cut,
// too long ones are cut, at most MaxInterests.
func TestNormInterests(t *testing.T) {
	got := NormInterests(SplitInterests("#Футбол, футбол ;  горные   лыжи #C++ #C# ,,  «кино»,  ...  "))
	if want := []string{"Футбол", "горные лыжи", "C++", "C#", "кино"}; !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	long := strings.Repeat("я", MaxInterestLen+10)
	if got := NormInterests([]string{long}); len([]rune(got[0])) != MaxInterestLen {
		t.Fatalf("not cut: %d runes", len([]rune(got[0])))
	}
	var many []string
	for i := range MaxInterests + 5 {
		many = append(many, "t"+strings.Repeat("x", i))
	}
	if got := NormInterests(many); len(got) != MaxInterests {
		t.Fatalf("%d interests kept", len(got))
	}
}
