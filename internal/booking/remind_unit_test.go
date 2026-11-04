package booking

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestBookingMovable tests whether a booking can be moved by client.
func TestBookingMovable(t *testing.T) {
	future := time.Now().Add(2 * time.Hour)
	past := time.Now().Add(-2 * time.Hour)

	tests := []struct {
		name   string
		status string
		start  time.Time
		want   bool
	}{
		{"future unconfirmed", "unconfirmed", future, true},
		{"future confirmed", "confirmed", future, true},
		{"future came", "came", future, false},
		{"future no_show", "no_show", future, false},
		{"future cancelled", "cancelled", future, false},
		{"future empty status", "", future, false},
		{"past unconfirmed", "unconfirmed", past, false},
		{"past confirmed", "confirmed", past, false},
		{"past came", "came", past, false},
		{"past cancelled", "cancelled", past, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Booking{Status: tc.status, Start: tc.start}
			if got := b.Movable(); got != tc.want {
				t.Fatalf("Booking.Movable() = %v; want %v (status=%s, start=%v)", got, tc.want, tc.status, tc.start)
			}
		})
	}
}

// TestDaysAheadNoLink verifies default MoveDays when a booking has no link.
func TestDaysAheadNoLink(t *testing.T) {
	ctx := context.Background()
	s := &Store{}

	b := Booking{LinkID: ""}
	if got := s.DaysAhead(ctx, b); got != MoveDays {
		t.Fatalf("DaysAhead() = %d; want %d (MoveDays)", got, MoveDays)
	}
}

// TestClaimUnknownKind verifies ErrNotFound when claiming an unrecognized due kind.
func TestClaimUnknownKind(t *testing.T) {
	ctx := context.Background()
	s := &Store{}

	for _, badKind := range []string{"", "unknown", "invalid", "reminder"} {
		d := Due{Kind: badKind, B: Booking{ID: "b1"}}
		claimed, err := s.Claim(ctx, d)
		if claimed || !errors.Is(err, ErrNotFound) {
			t.Errorf("Claim(%q) = %v, %v; want false, ErrNotFound", badKind, claimed, err)
		}
	}
}
