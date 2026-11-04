package backup

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

// TestSettingsSlotAndNext tests schedule slot calculation and next occurrence for daily, weekly,
// and unscheduled configurations, including edge cases like month/year rollovers and invalid times.
func TestSettingsSlotAndNext(t *testing.T) {
	loc := time.FixedZone("UTC+3", 3*3600)
	parseTime := func(s string) time.Time {
		v, err := time.ParseInLocation("2006-01-02 15:04:05", s, loc)
		if err != nil {
			t.Fatalf("parse time %q: %v", s, err)
		}
		return v
	}

	tests := []struct {
		name     string
		st       Settings
		now      time.Time
		wantSlot time.Time
		wantNext time.Time
	}{
		{
			name:     "no schedule empty",
			st:       Settings{},
			now:      parseTime("2026-10-28 10:00:00"),
			wantSlot: time.Time{},
			wantNext: time.Time{},
		},
		{
			name:     "unsupported schedule",
			st:       Settings{Schedule: "monthly", At: "03:00"},
			now:      parseTime("2026-10-28 10:00:00"),
			wantSlot: time.Time{},
			wantNext: time.Time{},
		},
		{
			name:     "daily invalid At defaults to 03:00",
			st:       Settings{Schedule: "daily", At: "invalid"},
			now:      parseTime("2026-10-28 04:00:00"),
			wantSlot: parseTime("2026-10-28 03:00:00"),
			wantNext: parseTime("2026-10-29 03:00:00"),
		},
		{
			name:     "daily invalid At hour out of range defaults to 03:00",
			st:       Settings{Schedule: "daily", At: "25:00"},
			now:      parseTime("2026-10-28 04:00:00"),
			wantSlot: parseTime("2026-10-28 03:00:00"),
			wantNext: parseTime("2026-10-29 03:00:00"),
		},
		{
			name:     "daily now before At rolls back to previous day",
			st:       Settings{Schedule: "daily", At: "12:00"},
			now:      parseTime("2026-10-28 11:59:59"),
			wantSlot: parseTime("2026-10-27 12:00:00"),
			wantNext: parseTime("2026-10-28 12:00:00"),
		},
		{
			name:     "daily now exact At matches today",
			st:       Settings{Schedule: "daily", At: "12:00"},
			now:      parseTime("2026-10-28 12:00:00"),
			wantSlot: parseTime("2026-10-28 12:00:00"),
			wantNext: parseTime("2026-10-29 12:00:00"),
		},
		{
			name:     "daily month boundary rollover",
			st:       Settings{Schedule: "daily", At: "04:00"},
			now:      parseTime("2026-11-01 02:00:00"),
			wantSlot: parseTime("2026-10-31 04:00:00"),
			wantNext: parseTime("2026-11-01 04:00:00"),
		},
		{
			name:     "daily year boundary rollover",
			st:       Settings{Schedule: "daily", At: "05:00"},
			now:      parseTime("2027-01-01 01:00:00"),
			wantSlot: parseTime("2026-12-31 05:00:00"),
			wantNext: parseTime("2027-01-01 05:00:00"),
		},
		{
			name:     "weekly sunday weekday 0",
			st:       Settings{Schedule: "weekly", At: "02:00", Weekday: 0},
			now:      parseTime("2026-10-28 12:00:00"), // Wednesday
			wantSlot: parseTime("2026-10-25 02:00:00"), // Previous Sunday
			wantNext: parseTime("2026-11-01 02:00:00"), // Next Sunday
		},
		{
			name:     "weekly sunday weekday 7 modulo",
			st:       Settings{Schedule: "weekly", At: "02:00", Weekday: 7},
			now:      parseTime("2026-10-28 12:00:00"), // Wednesday
			wantSlot: parseTime("2026-10-25 02:00:00"), // Previous Sunday
			wantNext: parseTime("2026-11-01 02:00:00"), // Next Sunday
		},
		{
			name:     "weekly weekday greater than 7 modulos correctly",
			st:       Settings{Schedule: "weekly", At: "04:00", Weekday: 8}, // 8 % 7 = 1 (Monday)
			now:      parseTime("2026-10-28 12:00:00"),                      // Wednesday
			wantSlot: parseTime("2026-10-26 04:00:00"),                      // Monday
			wantNext: parseTime("2026-11-02 04:00:00"),                      // Next Monday
		},
		{
			name:     "weekly on target weekday before At rolls back 7 days",
			st:       Settings{Schedule: "weekly", At: "15:00", Weekday: 3}, // Wednesday
			now:      parseTime("2026-10-28 14:00:00"),                      // Wednesday before 15:00
			wantSlot: parseTime("2026-10-21 15:00:00"),                      // Previous Wednesday
			wantNext: parseTime("2026-10-28 15:00:00"),                      // Today
		},
		{
			name:     "weekly on target weekday after At matches today",
			st:       Settings{Schedule: "weekly", At: "15:00", Weekday: 3}, // Wednesday
			now:      parseTime("2026-10-28 16:00:00"),                      // Wednesday after 15:00
			wantSlot: parseTime("2026-10-28 15:00:00"),                      // Today
			wantNext: parseTime("2026-11-04 15:00:00"),                      // Next Wednesday
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slot := tt.st.Slot(tt.now)
			if !slot.Equal(tt.wantSlot) {
				t.Errorf("Slot(%v) = %v, want %v", tt.now, slot, tt.wantSlot)
			}
			next := tt.st.Next(tt.now)
			if !next.Equal(tt.wantNext) {
				t.Errorf("Next(%v) = %v, want %v", tt.now, next, tt.wantNext)
			}
		})
	}
}

// TestSettingsHelpers tests Has and HasPassword helper methods on Settings.
func TestSettingsHelpers(t *testing.T) {
	st := Settings{
		Parts:  []string{"go24", "config"},
		Secret: "sealed-key",
	}
	if !st.HasPassword() {
		t.Error("expected HasPassword to be true with non-empty secret")
	}
	if !st.Has("go24") {
		t.Error("expected Has('go24') to be true")
	}
	if !st.Has("config") {
		t.Error("expected Has('config') to be true")
	}
	if st.Has("synapse") {
		t.Error("expected Has('synapse') to be false")
	}

	empty := Settings{}
	if empty.HasPassword() {
		t.Error("expected HasPassword to be false when secret is empty")
	}
	if empty.Has("go24") {
		t.Error("expected Has to be false on empty parts")
	}
}

// TestCleanDirUnit tests directory path sanitization and rejection of dangerous or malformed paths.
func TestCleanDirUnit(t *testing.T) {
	valid := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"backups", "backups"},
		{"/backups/", "backups"},
		{"a/b/c", "a/b/c"},
		{"a/./b", "a/b"},
		{"a/b/../c", "a/c"},
		{"etc/..", "."},
		{`nested\win\dir`, "nested/win/dir"},
		{` \lead\trail\ `, "lead/trail"},
	}
	for _, tc := range valid {
		got, err := CleanDir(tc.in)
		if err != nil {
			t.Errorf("CleanDir(%q) unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("CleanDir(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	invalid := []string{
		"..",
		"../",
		"../etc",
		"a/../..",
		"a/../../b",
		"dir\x00null",
		"c:drive",
		":colon",
		"foo:bar",
	}
	for _, tc := range invalid {
		if got, err := CleanDir(tc); err == nil {
			t.Errorf("CleanDir(%q) expected error, got %q", tc, got)
		}
	}
}

// TestCutMsg tests journal message truncation and UTF-8 validity preservation.
func TestCutMsg(t *testing.T) {
	if got := cutMsg(""); got != "" {
		t.Errorf("cutMsg empty string = %q, want empty", got)
	}

	short := "short error message"
	if got := cutMsg(short); got != short {
		t.Errorf("cutMsg(%q) = %q, want %q", short, got, short)
	}

	exact2000 := strings.Repeat("a", 2000)
	if got := cutMsg(exact2000); got != exact2000 {
		t.Errorf("cutMsg(2000 chars) length = %d, want 2000", len(got))
	}

	over2000 := strings.Repeat("b", 2500)
	gotOver := cutMsg(over2000)
	if len(gotOver) != 2000 {
		t.Errorf("cutMsg(2500 chars) length = %d, want 2000", len(gotOver))
	}
	if gotOver != strings.Repeat("b", 2000) {
		t.Errorf("cutMsg content mismatch for truncated ascii string")
	}

	// Rune boundary truncation: fill 1999 ASCII bytes, then a 3-byte Cyrillic/CJK rune.
	// Byte 2000 falls inside the 3-byte rune; cutMsg must cleanly remove the split rune.
	rune3Byte := "界" // 3 bytes in UTF-8
	raw := strings.Repeat("x", 1999) + rune3Byte
	cut := cutMsg(raw)
	if len(cut) != 1999 {
		t.Errorf("cutMsg with split rune length = %d, want 1999", len(cut))
	}
	if cut != strings.Repeat("x", 1999) {
		t.Errorf("cutMsg with split rune contains unexpected bytes")
	}
}

// TestStoreSealAndOpen tests symmetric password sealing and opening in Store using the server secret key.
func TestStoreSealAndOpen(t *testing.T) {
	store := NewStore(nil, "server-secret-alpha")

	// Empty secret string should open to empty string without error.
	if opened, err := store.openSecret(""); err != nil || opened != "" {
		t.Fatalf("openSecret empty = (%q, %v), want ('', nil)", opened, err)
	}

	sealed, err := store.seal("super-secret-backup-pass")
	if err != nil {
		t.Fatalf("seal failed: %v", err)
	}
	if sealed == "" || sealed == "super-secret-backup-pass" {
		t.Fatalf("unexpected sealed output: %q", sealed)
	}

	opened, err := store.openSecret(sealed)
	if err != nil {
		t.Fatalf("openSecret failed: %v", err)
	}
	if opened != "super-secret-backup-pass" {
		t.Fatalf("openSecret = %q, want %q", opened, "super-secret-backup-pass")
	}

	pass, err := store.Password(Settings{Secret: sealed})
	if err != nil {
		t.Fatalf("Password failed: %v", err)
	}
	if pass != "super-secret-backup-pass" {
		t.Fatalf("Password = %q, want %q", pass, "super-secret-backup-pass")
	}

	// Invalid base64.
	if _, err := store.openSecret("not-valid-base64!!!"); err == nil {
		t.Error("expected error opening invalid base64")
	}

	// Valid base64 but shorter than AES-GCM nonce size (12 bytes).
	shortB64 := "YWJj" // "abc" (3 bytes)
	if _, err := store.openSecret(shortB64); err == nil {
		t.Error("expected error opening short payload")
	}

	// Opening with a different server secret key must fail.
	otherStore := NewStore(nil, "different-server-secret")
	if _, err := otherStore.openSecret(sealed); err == nil {
		t.Error("expected error opening with different store key")
	}

	// Corrupted ciphertext payload.
	corrupted := sealed[:len(sealed)-4] + "AAAA"
	if _, err := store.openSecret(corrupted); err == nil {
		t.Error("expected error opening tampered ciphertext")
	}
}

// TestEncryptAndDecryptEdgeCases tests encryption edge cases: corrupt magic, short data, wrong password.
func TestEncryptAndDecryptEdgeCases(t *testing.T) {
	data := []byte("configuration secret data")
	enc, err := Encrypt("passphrase", data)
	if err != nil {
		t.Fatalf("Encrypt failed: %v", err)
	}

	dec, err := Decrypt("passphrase", enc)
	if err != nil {
		t.Fatalf("Decrypt failed: %v", err)
	}
	if !bytes.Equal(dec, data) {
		t.Fatalf("Decrypt = %q, want %q", dec, data)
	}

	// Corrupt magic header.
	corruptMagic := make([]byte, len(enc))
	copy(corruptMagic, enc)
	copy(corruptMagic[:8], []byte("NOTGO24!"))
	if _, err := Decrypt("passphrase", corruptMagic); err == nil {
		t.Error("expected error for corrupted magic header")
	}

	// Truncated payload shorter than magic + salt + nonce (8 + 16 + 12 = 36 bytes).
	for _, size := range []int{0, 1, 8, 24, 35} {
		short := make([]byte, size)
		_, _ = rand.Read(short)
		if _, err := Decrypt("passphrase", short); err == nil {
			t.Errorf("expected error for short encrypted data of size %d", size)
		}
	}

	// Empty data encryption/decryption round-trip.
	encEmpty, err := Encrypt("empty-pass", []byte{})
	if err != nil {
		t.Fatalf("Encrypt empty failed: %v", err)
	}
	decEmpty, err := Decrypt("empty-pass", encEmpty)
	if err != nil {
		t.Fatalf("Decrypt empty failed: %v", err)
	}
	if len(decEmpty) != 0 {
		t.Fatalf("expected empty decrypted slice, got %v", decEmpty)
	}

	// Wrong password.
	if _, err := Decrypt("wrong-pass", enc); err == nil {
		t.Error("expected error decrypting with wrong password")
	}
}
