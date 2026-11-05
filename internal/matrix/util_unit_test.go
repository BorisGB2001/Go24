package matrix

import (
	"encoding/hex"
	"testing"
)

func TestRandomHex(t *testing.T) {
	for _, n := range []int{0, 1, 4, 8, 16, 24, 32} {
		got := randomHex(n)
		if len(got) != 2*n {
			t.Errorf("randomHex(%d) length = %d, want %d", n, len(got), 2*n)
		}
		if _, err := hex.DecodeString(got); err != nil {
			t.Errorf("randomHex(%d) produced invalid hex %q: %v", n, got, err)
		}
	}

	// Distinct calls should produce distinct tokens.
	a := randomHex(16)
	b := randomHex(16)
	if a == b {
		t.Errorf("randomHex(16) produced identical values: %q", a)
	}
}
