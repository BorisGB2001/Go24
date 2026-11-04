package booking

import (
	"testing"
	"time"
)

// TestBookingLate tests lateness detection logic under various statuses, offsets and time zones.
func TestBookingLate(t *testing.T) {
	locUTC := time.UTC
	locMSK := time.FixedZone("MSK", 3*3600)

	start := time.Date(2026, 10, 19, 14, 0, 0, 0, locUTC)

	tests := []struct {
		name   string
		status string
		lateM  int
		now    time.Time
		want   bool
	}{
		{
			name:   "unconfirmed before late threshold",
			status: "unconfirmed",
			lateM:  15,
			now:    start.Add(14 * time.Minute),
			want:   false,
		},
		{
			name:   "unconfirmed exactly at late threshold",
			status: "unconfirmed",
			lateM:  15,
			now:    start.Add(15 * time.Minute),
			want:   false, // strictly After required
		},
		{
			name:   "unconfirmed after late threshold",
			status: "unconfirmed",
			lateM:  15,
			now:    start.Add(15*time.Minute + time.Second),
			want:   true,
		},
		{
			name:   "confirmed after late threshold",
			status: "confirmed",
			lateM:  15,
			now:    start.Add(20 * time.Minute),
			want:   true,
		},
		{
			name:   "client came after threshold is not late",
			status: "came",
			lateM:  15,
			now:    start.Add(30 * time.Minute),
			want:   false,
		},
		{
			name:   "no_show status is not considered late",
			status: "no_show",
			lateM:  15,
			now:    start.Add(30 * time.Minute),
			want:   false,
		},
		{
			name:   "cancelled status is not late",
			status: "cancelled",
			lateM:  15,
			now:    start.Add(30 * time.Minute),
			want:   false,
		},
		{
			name:   "empty status is not late",
			status: "",
			lateM:  15,
			now:    start.Add(30 * time.Minute),
			want:   false,
		},
		{
			name:   "resource with 0 late minutes is never late",
			status: "unconfirmed",
			lateM:  0,
			now:    start.Add(2 * time.Hour),
			want:   false,
		},
		{
			name:   "resource with negative late minutes is never late",
			status: "confirmed",
			lateM:  -10,
			now:    start.Add(2 * time.Hour),
			want:   false,
		},
		{
			name:   "cross-timezone comparison equivalent time",
			status: "confirmed",
			lateM:  15,
			// 14:16 UTC is 17:16 MSK
			now:  time.Date(2026, 10, 19, 17, 16, 0, 0, locMSK),
			want: true,
		},
		{
			name:   "cross-timezone comparison before threshold",
			status: "confirmed",
			lateM:  15,
			// 14:14 UTC is 17:14 MSK
			now:  time.Date(2026, 10, 19, 17, 14, 0, 0, locMSK),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := Booking{Start: start, Status: tc.status}
			res := Resource{LateMinutes: tc.lateM}
			if got := b.Late(res, tc.now); got != tc.want {
				t.Errorf("Booking.Late() = %v; want %v (status=%s, lateM=%d, now=%v)", got, tc.want, tc.status, tc.lateM, tc.now)
			}
		})
	}
}
