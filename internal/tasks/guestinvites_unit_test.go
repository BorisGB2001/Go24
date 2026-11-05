package tasks

import (
	"slices"
	"testing"
	"time"
)

func TestGuestInviteActive(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	later := now.Add(24 * time.Hour)
	earlier := now.Add(-24 * time.Hour)

	tests := []struct {
		name   string
		invite GuestInvite
		want   bool
	}{
		{
			name: "active link invite",
			invite: GuestInvite{
				Kind:    "link",
				Expires: later,
				Revoked: false,
				Uses:    0,
			},
			want: true,
		},
		{
			name: "active link invite with multiple uses",
			invite: GuestInvite{
				Kind:    "link",
				Expires: later,
				Revoked: false,
				Uses:    5,
			},
			want: true,
		},
		{
			name: "revoked invite",
			invite: GuestInvite{
				Kind:    "link",
				Expires: later,
				Revoked: true,
				Uses:    0,
			},
			want: false,
		},
		{
			name: "expired invite",
			invite: GuestInvite{
				Kind:    "link",
				Expires: earlier,
				Revoked: false,
				Uses:    0,
			},
			want: false,
		},
		{
			name: "personal invite unused",
			invite: GuestInvite{
				Kind:    "email",
				Expires: later,
				Revoked: false,
				Uses:    0,
			},
			want: true,
		},
		{
			name: "personal invite already used",
			invite: GuestInvite{
				Kind:    "email",
				Expires: later,
				Revoked: false,
				Uses:    1,
			},
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.invite.Active(now); got != tc.want {
				t.Errorf("Active() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCleanPhoneAndRegex(t *testing.T) {
	tests := []struct {
		in        string
		wantClean string
		wantValid bool
	}{
		{"+7 (999) 123-45-67", "+79991234567", true},
		{"8 (495) 123.45.67", "84951234567", true},
		{"+1 555-0199", "+15550199", true},
		{"1234567", "1234567", true},                    // 7 digits (min length)
		{"123456", "123456", false},                     // 6 digits (too short)
		{"1234567890123456", "1234567890123456", false}, // 16 digits (too long)
		{"abc", "abc", false},
	}

	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			cleaned := cleanPhone(tc.in)
			if cleaned != tc.wantClean {
				t.Errorf("cleanPhone(%q) = %q, want %q", tc.in, cleaned, tc.wantClean)
			}
			matched := phoneRe.MatchString(cleaned)
			if matched != tc.wantValid {
				t.Errorf("phoneRe.MatchString(%q) = %v, want %v", cleaned, matched, tc.wantValid)
			}
		})
	}
}

func TestSplitContacts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty string", "", nil},
		{
			name: "mixed newlines, commas, semicolons",
			in:   "anna@example.com\n+7 999 123-45-67; boris@example.com, anna@example.com",
			want: []string{"anna@example.com", "+7 999 123-45-67", "boris@example.com"},
		},
		{
			name: "whitespace and duplicates",
			in:   "  one  ,  two  ; one \n  three  ",
			want: []string{"one", "two", "three"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SplitContacts(tc.in)
			if !slices.Equal(got, tc.want) {
				t.Errorf("SplitContacts() = %v, want %v", got, tc.want)
			}
		})
	}
}
