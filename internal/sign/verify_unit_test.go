package sign

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNormCode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty string", "", ""},
		{"too short: 1 char", "A", ""},
		{"too short: 7 chars", "1234567", ""},
		{"too short: 7 chars with dashes", "123-456-7", ""},
		{"min length: exactly 8 chars", "12345678", "12345678"},
		{"8 chars with dash", "1234-5678", "12345678"},
		{"8 chars with spaces and tabs", "1234 \t 5678", "12345678"},
		{"lowercase to uppercase", "abcd-1234", "ABCD1234"},
		{"mixed case and symbols", "  ab-CD \t 12-34  ", "ABCD1234"},
		{"12 chars standard", "ABCD-EFGH-1234", "ABCDEFGH1234"},
		{"16 chars", "1234-5678-9012-3456", "1234567890123456"},
		{"max length: exactly 32 chars", "12345678901234567890123456789012", "12345678901234567890123456789012"},
		{"max length: 32 chars with dashes", "1234-5678-9012-3456-7890-1234-5678-9012", "12345678901234567890123456789012"},
		{"too long: 33 chars", "123456789012345678901234567890123", ""},
		{"too long: 33 chars with dashes", "1234-5678-9012-3456-7890-1234-5678-9012-3", ""},
		{"invalid char: exclamation", "ABCD-1234!", ""},
		{"invalid char: underscore", "ABCD_1234", ""},
		{"invalid char: dot", "ABCD.1234", ""},
		{"invalid char: slash", "ABCD/1234", ""},
		{"invalid char: newline", "ABCD\n1234", ""},
		{"invalid char: carriage return", "ABCD\r1234", ""},
		{"invalid char: cyrillic", "АБВГ-1234", ""},
		{"invalid char: emoji", "ABCD-1234👍", ""},
		{"invalid char: null byte", "ABCD1234\x00", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := NormCode(tc.in)
			if got != tc.want {
				t.Errorf("NormCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestShowCode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty string", "", ""},
		{"1 char", "A", "A"},
		{"2 chars", "AB", "AB"},
		{"3 chars", "ABC", "ABC"},
		{"4 chars", "ABCD", "ABCD"},
		{"5 chars", "ABCDE", "ABCD-E"},
		{"8 chars", "ABCDEFGH", "ABCD-EFGH"},
		{"9 chars", "ABCDEFGHI", "ABCD-EFGH-I"},
		{"12 chars", "123456789012", "1234-5678-9012"},
		{"16 chars", "1234567890123456", "1234-5678-9012-3456"},
		{"32 chars", strings.Repeat("A", 32), "AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA-AAAA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ShowCode(tc.in)
			if got != tc.want {
				t.Errorf("ShowCode(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestVerifyCodeRoundTrip(t *testing.T) {
	codes := []string{
		"12345678",
		"ABCDEFGH",
		"123456789012",
		"ABCDEFGH12345678",
		"12345678901234567890123456789012",
	}
	for _, c := range codes {
		shown := ShowCode(c)
		norm := NormCode(shown)
		if norm != c {
			t.Errorf("roundtrip failed: NormCode(ShowCode(%q)) = %q, want %q (shown was %q)", c, norm, c, shown)
		}
	}
}

func TestVerifyStore_NilDB(t *testing.T) {
	ctx := context.Background()
	s := NewStore(nil)

	// ByCode with invalid or empty code returns ErrNotFound without contacting database
	for _, invalid := range []string{"", "short", "invalid-char!", strings.Repeat("A", 33)} {
		if _, err := s.ByCode(ctx, invalid); !errors.Is(err, ErrNotFound) {
			t.Errorf("ByCode(%q) error = %v, want ErrNotFound", invalid, err)
		}
	}

	// ByHash with empty hash returns ErrNotFound without contacting database
	if _, _, err := s.ByHash(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByHash(\"\") error = %v, want ErrNotFound", err)
	}
}
