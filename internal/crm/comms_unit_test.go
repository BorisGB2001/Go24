package crm

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"go24/internal/i18n"
)

func TestCommKey_Unit(t *testing.T) {
	tests := []struct {
		kind, val, want string
	}{
		{"phone", "+7 (900) 123-45-67", "9001234567"},
		{"phone", "123", ""}, // too short
		{"email", "  User@Example.COM  ", "user@example.com"},
		{"email", "", ""},
	}

	for _, tt := range tests {
		got := CommKey(tt.kind, tt.val)
		if got != tt.want {
			t.Errorf("CommKey(%q, %q) = %q, want %q", tt.kind, tt.val, got, tt.want)
		}
	}
}

func TestCommsOf_Unit(t *testing.T) {
	list := []Comm{
		{Kind: "phone", Type: "mobile", Value: "+7900"},
		{Kind: "email", Type: "work", Value: "work@example.com"},
		{Kind: "phone", Type: "work", Value: "+7901"},
	}

	phones := CommsOf(list, "phone")
	if len(phones) != 2 || phones[0].Value != "+7900" || phones[1].Value != "+7901" {
		t.Errorf("CommsOf phones mismatch: %+v", phones)
	}

	emails := CommsOf(list, "email")
	if len(emails) != 1 || emails[0].Value != "work@example.com" {
		t.Errorf("CommsOf emails mismatch: %+v", emails)
	}

	other := CommsOf(list, "other")
	if len(other) != 0 {
		t.Errorf("CommsOf other = %+v, want empty", other)
	}
}

func TestCommsText_Unit(t *testing.T) {
	list := []Comm{
		{Kind: "phone", Value: "+79001112233"},
		{Kind: "phone", Value: "+79004445566"},
	}

	if got := commsText(list, "phone", "+7999"); got != "+79001112233, +79004445566" {
		t.Errorf("commsText() = %q, want joined phones", got)
	}

	// Falls back to main if no comms of kind
	if got := commsText(list, "email", "fallback@example.com"); got != "fallback@example.com" {
		t.Errorf("commsText() = %q, want fallback", got)
	}
}

func TestNormComms_Unit(t *testing.T) {
	// 1. Given nil uses main phone and email to populate comms
	p, e, comms, err := normComms(nil, "+7 (900) 123-45-67", "info@example.com", nil)
	if err != nil {
		t.Fatalf("normComms nil given failed: %v", err)
	}
	if p != "+7 (900) 123-45-67" || e != "info@example.com" {
		t.Errorf("main phone/email mismatch: p=%q, e=%q", p, e)
	}
	if len(comms) != 2 {
		t.Fatalf("comms len = %d, want 2", len(comms))
	}
	if comms[0].Type != "work" || comms[1].Type != "work" {
		t.Errorf("expected default type work, got %+v", comms)
	}

	// 2. Deduplication of phones matching by phoneKey
	given := []Comm{
		{Kind: "phone", Type: "mobile", Value: "+7 (900) 123-45-67"},
		{Kind: "phone", Type: "work", Value: "8-900-123-45-67"}, // duplicate phoneKey
		{Kind: "phone", Type: "home", Value: "+7 (901) 000-00-00"},
		{Kind: "email", Type: "invalid_type", Value: "USER@example.COM"},
		{Kind: "email", Type: "work", Value: "user@example.com"}, // duplicate email
	}
	p2, e2, comms2, err := normComms(nil, "", "", given)
	if err != nil {
		t.Fatalf("normComms deduplication failed: %v", err)
	}
	if p2 != "+7 (900) 123-45-67" || e2 != "USER@example.COM" {
		t.Errorf("mismatched first phone/email: p=%q, e=%q", p2, e2)
	}
	// Expected 2 phones and 1 email
	if len(comms2) != 3 {
		t.Fatalf("expected 3 comms, got %d: %+v", len(comms2), comms2)
	}
	// Invalid email type should default to CommTypes["email"][0] ("work")
	if comms2[2].Type != "work" {
		t.Errorf("expected type defaulted to work, got %q", comms2[2].Type)
	}

	// 3. Exceeding maxComms (10 of one kind)
	manyPhones := make([]Comm, 11)
	for i := range manyPhones {
		manyPhones[i] = Comm{
			Kind:  "phone",
			Type:  "mobile",
			Value: "+7 900 000 00 " + string(rune('0'+i)),
		}
	}
	_, _, _, err = normComms(nil, "", "", manyPhones)
	if err == nil {
		t.Fatal("expected error on > 10 phones, got nil")
	}
	var ie *i18n.Error
	if !errors.As(err, &ie) || ie.Key != "err.crm_comms_many" {
		t.Errorf("expected err.crm_comms_many, got %v", err)
	}

	// 4. Comm value exceeding 300 runes
	longComm := []Comm{
		{Kind: "email", Value: strings.Repeat("e", 305)},
	}
	_, _, _, err = normComms(nil, "", "", longComm)
	if err == nil {
		t.Fatal("expected error on long comm value, got nil")
	}
	if !errors.As(err, &ie) || ie.Key != "err.crm_field" {
		t.Errorf("expected err.crm_field, got %v", err)
	}
}

func TestCommTypes_Unit(t *testing.T) {
	wantPhoneTypes := []string{"work", "mobile", "home", "other"}
	if !reflect.DeepEqual(CommTypes["phone"], wantPhoneTypes) {
		t.Errorf("CommTypes[phone] = %v, want %v", CommTypes["phone"], wantPhoneTypes)
	}
	wantEmailTypes := []string{"work", "home", "other"}
	if !reflect.DeepEqual(CommTypes["email"], wantEmailTypes) {
		t.Errorf("CommTypes[email] = %v, want %v", CommTypes["email"], wantEmailTypes)
	}
}
