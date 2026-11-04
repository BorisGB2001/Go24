package booking

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"
)

// TestToken verifies generation of valid 32-character hex tokens.
func TestToken(t *testing.T) {
	t1 := token()
	t2 := token()

	if len(t1) != 32 {
		t.Fatalf("len(token()) = %d; want 32", len(t1))
	}
	if _, err := hex.DecodeString(t1); err != nil {
		t.Fatalf("token() returned invalid hex: %v", err)
	}
	if t1 == t2 {
		t.Fatalf("consecutive calls to token() produced identical values: %s", t1)
	}
}

// TestSaveLinkValidation tests link name validation before database interaction.
func TestSaveLinkValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{}

	for _, bad := range []string{"", "   ", "\t\n"} {
		l := Link{Name: bad}
		if _, err := s.SaveLink(ctx, "user", l); !errors.Is(err, ErrName) {
			t.Errorf("SaveLink(%q) error = %v; want ErrName", bad, err)
		}
	}
}

// TestByClientTokenEmpty tests immediate ErrNotFound on empty client token.
func TestByClientTokenEmpty(t *testing.T) {
	ctx := context.Background()
	s := &Store{}

	if _, err := s.ByClientToken(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ByClientToken(\"\") error = %v; want ErrNotFound", err)
	}
}

// TestBookByLinkValidation tests name, phone digit count and resource membership validation.
func TestBookByLinkValidation(t *testing.T) {
	ctx := context.Background()
	s := &Store{}
	loc := time.UTC

	l := Link{
		Resources: []string{"res-allowed-1", "res-allowed-2"},
		DaysAhead: 14,
	}

	tests := []struct {
		name    string
		b       Booking
		wantErr error
	}{
		{
			name:    "empty client name",
			b:       Booking{ClientName: "", ClientPhone: "+7 999 123-45-67", ResourceID: "res-allowed-1"},
			wantErr: ErrClient,
		},
		{
			name:    "whitespace client name",
			b:       Booking{ClientName: "   ", ClientPhone: "+7 999 123-45-67", ResourceID: "res-allowed-1"},
			wantErr: ErrClient,
		},
		{
			name:    "empty phone",
			b:       Booking{ClientName: "Guest", ClientPhone: "", ResourceID: "res-allowed-1"},
			wantErr: ErrPhone,
		},
		{
			name:    "phone with fewer than 5 digits",
			b:       Booking{ClientName: "Guest", ClientPhone: "1234", ResourceID: "res-allowed-1"},
			wantErr: ErrPhone,
		},
		{
			name:    "phone formatted with 4 digits",
			b:       Booking{ClientName: "Guest", ClientPhone: "+1 (23) 4", ResourceID: "res-allowed-1"},
			wantErr: ErrPhone,
		},
		{
			name:    "phone letters only",
			b:       Booking{ClientName: "Guest", ClientPhone: "no-digits", ResourceID: "res-allowed-1"},
			wantErr: ErrPhone,
		},
		{
			name:    "resource not in link resources",
			b:       Booking{ClientName: "Guest", ClientPhone: "+7 999 000-00-00", ResourceID: "res-forbidden"},
			wantErr: ErrNotFound,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.BookByLink(ctx, l, tc.b, loc)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("BookByLink() error = %v; want %v", err, tc.wantErr)
			}
		})
	}
}
