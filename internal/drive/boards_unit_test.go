package drive

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestIsBoard(t *testing.T) {
	for _, c := range []struct {
		name string
		want bool
	}{
		{"diagram.board", true},
		{"SCHEMA.BOARD", true},
		{"plan.Board", true},
		{".board", true},
		{"/path/to/nested/canvas.board", true},
		{"diagram.board.bak", false},
		{"diagram.board.json", false},
		{"board", false},
		{"", false},
		{"board.txt", false},
		{"diagram.board/", false},
	} {
		if got := IsBoard(c.name); got != c.want {
			t.Errorf("IsBoard(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestBoardStamp(t *testing.T) {
	// BoardStamp returns the first 8 bytes of the sha256 hash formatted as a 16-character hex string.
	for _, key := range []string{
		"",
		"go24/2026/10/deadbeef/canvas.board",
		"simple-key",
	} {
		got := BoardStamp(key)
		if len(got) != 16 {
			t.Errorf("BoardStamp(%q) length = %d, want 16", key, len(got))
		}
		sum := sha256.Sum256([]byte(key))
		want := hex.EncodeToString(sum[:8])
		if got != want {
			t.Errorf("BoardStamp(%q) = %q, want %q", key, got, want)
		}
	}

	// Determinism: same input always produces identical stamp.
	k := "go24/storage/key"
	if s1, s2 := BoardStamp(k), BoardStamp(k); s1 != s2 {
		t.Errorf("BoardStamp non-deterministic: %q != %q", s1, s2)
	}
}
