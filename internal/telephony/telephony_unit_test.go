package telephony

import (
	"slices"
	"strings"
	"testing"
)

func TestDialableTable(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"101", "101"},
		{"tel:+7 (900) 123-45-67", "+79001234567"},
		{" 8.800.555 ", "8800555"},
		{"*72#", "*72#"},
		{"#123*", "#123*"},
		{"+1-800-555-0199", "+18005550199"},
		{"+7(495)123-45-67", "+74951234567"},
		{"+1234567890123456789", "+1234567890123456789"}, // exactly 20 chars
		{"(812) 555-00-11", "8125550011"},
		{"tel:101", "101"},
		{"101 202 303 404 505 606", "101202303404505606"}, // spaces stripped, 18 digits <= 20

		// Invalid cases
		{"", ""},
		{"   ", ""},
		{"tel:", ""},
		{"tel: ", ""},
		{"12+3", ""},
		{"+7+900", ""},
		{"abc", ""},
		{"101;drop", ""},
		{"123456789012345678901", ""},       // 21 chars (> 20)
		{"+12345678901234567890", ""},       // 21 chars (> 20)
		{"101 202 303 404 505 606 707", ""}, // 21 digits (> 20)
		{"tel: +79001234567", ""},           // + at index 1 after space
		{"@123", ""},
		{"123!456", ""},
		{"---", ""},
		{"( ) . -", ""},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := Dialable(tc.input)
			if got != tc.want {
				t.Errorf("Dialable(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestPasswordGeneration(t *testing.T) {
	const allowed = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for _, n := range []int{0, 8, 12, 16, 24, 64} {
		p := Password(n)
		if len(p) != n {
			t.Errorf("Password(%d) length = %d, want %d", n, len(p), n)
		}
		for _, r := range p {
			if !strings.ContainsRune(allowed, r) {
				t.Errorf("Password(%d) contains disallowed character %q", n, r)
			}
			if strings.ContainsRune("0O1lI", r) {
				t.Errorf("Password(%d) contains ambiguous character %q", n, r)
			}
		}
	}
	p1 := Password(16)
	p2 := Password(16)
	if p1 == p2 {
		t.Errorf("Password(16) generated identical passwords: %q", p1)
	}
}

func TestLineLogin(t *testing.T) {
	l := Line{Number: "205"}
	if got := l.Login(); got != "205" {
		t.Errorf("Line.Login() = %q, want %q", got, "205")
	}
	if got := l.WebLogin(); got != "w205" {
		t.Errorf("Line.WebLogin() = %q, want %q", got, "w205")
	}
}

func TestStoreSealOpen(t *testing.T) {
	s := NewStore(nil, "test-super-secret-key-12345")
	plain := "my-secret-sip-password!@#"

	sealed, err := s.seal(plain)
	if err != nil {
		t.Fatalf("seal failed: %v", err)
	}
	if len(sealed) == 0 || string(sealed) == plain {
		t.Fatalf("seal returned empty or unencrypted data")
	}

	opened, err := s.open(sealed)
	if err != nil {
		t.Fatalf("open failed: %v", err)
	}
	if opened != plain {
		t.Fatalf("open = %q, want %q", opened, plain)
	}

	// Bad secret length (< GCM nonce size 12)
	if _, err := s.open([]byte("short")); err == nil {
		t.Errorf("open(short) expected error, got nil")
	}

	// Tampered ciphertext
	tampered := slices.Clone(sealed)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := s.open(tampered); err == nil {
		t.Errorf("open(tampered) expected error, got nil")
	}

	// Decrypting with different store secret
	s2 := NewStore(nil, "another-secret-key")
	if _, err := s2.open(sealed); err == nil {
		t.Errorf("open with wrong key expected error, got nil")
	}
}
