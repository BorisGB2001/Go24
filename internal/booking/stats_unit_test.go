package booking

import (
	"math"
	"testing"
	"time"
)

// TestLoadComprehensive tests Load with working hours, custom day hours, breaks, off-dates,
// booking overlap merges, and timezone conversions.
func TestLoadComprehensive(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600) // UTC+3

	// A Monday: 2026-10-19 00:00 MSK
	mon := time.Date(2026, 10, 19, 0, 0, 0, 0, loc)

	r := Resource{
		ID:        "res-1",
		Days:      []int{1, 2, 3, 4, 5}, // Mon-Fri
		From:      9 * 60,               // 09:00
		To:        18 * 60,              // 18:00 (9 hours = 540 min)
		BreakFrom: 13 * 60,              // 13:00
		BreakTo:   14 * 60,              // 14:00 (1 hour break = 60 min)
		DayHours: map[int][2]int{
			5: {9 * 60, 15 * 60}, // Friday short day: 9:00 - 15:00 (6h - 1h break = 300 min)
		},
		OffDates: []time.Time{
			// Wednesday is an off-day
			time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC),
		},
	}

	at := func(dayOffset, hour, min int) time.Time {
		return time.Date(2026, 10, 19+dayOffset, hour, min, 0, 0, loc)
	}

	bookings := []Booking{
		// Monday (day 0):
		// 10:00 - 11:00 (60 min)
		{ID: "b1", ResourceID: "res-1", Start: at(0, 10, 0), End: at(0, 11, 0), Status: "confirmed"},
		// 10:30 - 12:00 (overlapping with b1, extends by 60 min -> 10:00-12:00 = 120 min)
		{ID: "b2", ResourceID: "res-1", Start: at(0, 10, 30), End: at(0, 12, 0), Status: "unconfirmed"},
		// 10:45 - 11:15 (fully contained inside 10:00-12:00 -> 0 extra min)
		{ID: "b3", ResourceID: "res-1", Start: at(0, 10, 45), End: at(0, 11, 15), Status: "came"},
		// Cancelled booking on Monday (should be ignored)
		{ID: "b4", ResourceID: "res-1", Start: at(0, 15, 0), End: at(0, 16, 0), Status: "cancelled"},
		// Booking for another resource (not in Extras -> ignored)
		{ID: "b5", ResourceID: "res-other", Start: at(0, 16, 0), End: at(0, 17, 0), Status: "confirmed"},

		// Tuesday (day 1):
		// Booking partially before working hours: 08:00 - 10:00 -> clamped to 09:00 - 10:00 (60 min)
		{ID: "b6", ResourceID: "res-1", Start: at(1, 8, 0), End: at(1, 10, 0), Status: "confirmed"},
		// Booking taking res-1 as an extra resource: 15:00 - 17:00 (120 min)
		{ID: "b7", ResourceID: "res-other", Extras: []string{"res-1"}, Start: at(1, 15, 0), End: at(1, 17, 0), Status: "came"},
		// Booking extending beyond working hours: 17:30 - 19:00 -> clamped to 17:30 - 18:00 (30 min)
		{ID: "b8", ResourceID: "res-1", Start: at(1, 17, 30), End: at(1, 19, 0), Status: "no_show"},

		// Wednesday (day 2): off-date, so any bookings here should yield 0 busy and 0 work
		{ID: "b9", ResourceID: "res-1", Start: at(2, 10, 0), End: at(2, 11, 0), Status: "came"},

		// Thursday (day 3): no bookings

		// Friday (day 4): short day (09:00 - 15:00, break 13:00 - 14:00)
		// Booking extending past Friday's 15:00 closing: 14:00 - 16:00 -> clamped to 14:00 - 15:00 (60 min)
		{ID: "b10", ResourceID: "res-1", Start: at(4, 14, 0), End: at(4, 16, 0), Status: "came"},

		// Saturday (day 5) / Sunday (day 6): weekend bookings (should contribute 0)
		{ID: "b11", ResourceID: "res-1", Start: at(5, 10, 0), End: at(5, 12, 0), Status: "came"},
	}

	from := mon
	to := mon.AddDate(0, 0, 7) // full 7 days

	work, busy := Load(r, bookings, from, to, loc)

	// Expected work:
	// Mon (day 0): 540 - 60 = 480 min
	// Tue (day 1): 540 - 60 = 480 min
	// Wed (day 2): 0 min (OffDate)
	// Thu (day 3): 540 - 60 = 480 min
	// Fri (day 4): 360 - 60 = 300 min (DayHours: 9:00-15:00 minus 1h break)
	// Sat (day 5): 0 min (weekend)
	// Sun (day 6): 0 min (weekend)
	// Total work = 480 + 480 + 0 + 480 + 300 = 1740 min
	wantWork := 1740

	// Expected busy:
	// Mon: 10:00 - 12:00 = 120 min
	// Tue: (09:00 - 10:00: 60 min) + (15:00 - 17:00: 120 min) + (17:30 - 18:00: 30 min) = 210 min
	// Wed: 0 (OffDate)
	// Thu: 0
	// Fri: 14:00 - 15:00 = 60 min
	// Sat/Sun: 0
	// Total busy = 120 + 210 + 60 = 390 min
	wantBusy := 390

	if work != wantWork || busy != wantBusy {
		t.Fatalf("Load() = work:%d, busy:%d; want work:%d, busy:%d", work, busy, wantWork, wantBusy)
	}

	// Single day check: Monday only
	workMon, busyMon := Load(r, bookings, mon, mon.AddDate(0, 0, 1), loc)
	if workMon != 480 || busyMon != 120 {
		t.Errorf("Monday Load() = work:%d, busy:%d; want 480, 120", workMon, busyMon)
	}

	// Single day check: Wednesday (off-date)
	workWed, busyWed := Load(r, bookings, mon.AddDate(0, 0, 2), mon.AddDate(0, 0, 3), loc)
	if workWed != 0 || busyWed != 0 {
		t.Errorf("Wednesday Load() = work:%d, busy:%d; want 0, 0", workWed, busyWed)
	}

	// Weekend only check
	workWknd, busyWknd := Load(r, bookings, mon.AddDate(0, 0, 5), mon.AddDate(0, 0, 7), loc)
	if workWknd != 0 || busyWknd != 0 {
		t.Errorf("Weekend Load() = work:%d, busy:%d; want 0, 0", workWknd, busyWknd)
	}

	// Empty date range: from == to
	if w, b := Load(r, bookings, mon, mon, loc); w != 0 || b != 0 {
		t.Errorf("Empty range Load() = %d, %d; want 0, 0", w, b)
	}

	// Inverted date range: from > to
	if w, b := Load(r, bookings, mon.AddDate(0, 0, 1), mon, loc); w != 0 || b != 0 {
		t.Errorf("Inverted range Load() = %d, %d; want 0, 0", w, b)
	}
}

// TestLoadBreakBoundaries tests break deduction when break edges touch or exceed working hours.
func TestLoadBreakBoundaries(t *testing.T) {
	loc := time.UTC
	day := time.Date(2026, 10, 19, 0, 0, 0, 0, loc) // Monday
	from, to := day, day.AddDate(0, 0, 1)

	tests := []struct {
		name      string
		breakFrom int
		breakTo   int
		wantWork  int
	}{
		{
			name:      "break entirely before working hours",
			breakFrom: 7 * 60,
			breakTo:   8 * 60,
			wantWork:  540, // 09:00 - 18:00 = 540, no reduction
		},
		{
			name:      "break starts before and ends inside working hours",
			breakFrom: 8 * 60,
			breakTo:   10 * 60, // 08:00 - 10:00 touches 09:00 - 10:00 (60 min)
			wantWork:  480,
		},
		{
			name:      "break starts inside and ends after working hours",
			breakFrom: 17 * 60,
			breakTo:   19 * 60, // 17:00 - 19:00 touches 17:00 - 18:00 (60 min)
			wantWork:  480,
		},
		{
			name:      "break entirely after working hours",
			breakFrom: 18 * 60,
			breakTo:   19 * 60,
			wantWork:  540, // 09:00 - 18:00 = 540, no reduction
		},
		{
			name:      "break encompasses all working hours",
			breakFrom: 8 * 60,
			breakTo:   19 * 60,
			wantWork:  0, // min(1080, 1140) - max(540, 480) = 1080 - 540 = 540 -> 540 - 540 = 0
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := Resource{
				ID:        "r",
				Days:      []int{1},
				From:      9 * 60,
				To:        18 * 60,
				BreakFrom: tc.breakFrom,
				BreakTo:   tc.breakTo,
			}
			work, _ := Load(r, nil, from, to, loc)
			if work != tc.wantWork {
				t.Fatalf("Load() work = %d; want %d", work, tc.wantWork)
			}
		})
	}
}

// TestLoadNonUTCLocations tests load calculation across non-UTC and half-hour offset time zones.
func TestLoadNonUTCLocations(t *testing.T) {
	// Kathmandu UTC+5:45
	locKathmandu := time.FixedZone("NPT", 5*3600+45*60)
	// New York UTC-5
	locNY := time.FixedZone("EST", -5*3600)

	for _, loc := range []*time.Location{locKathmandu, locNY} {
		mon := time.Date(2026, 10, 19, 0, 0, 0, 0, loc)
		r := Resource{
			ID:   "r",
			Days: []int{1},
			From: 9 * 60,
			To:   17 * 60,
		}
		// Booking exactly during working hours
		b := Booking{
			ResourceID: "r",
			Start:      time.Date(2026, 10, 19, 10, 0, 0, 0, loc),
			End:        time.Date(2026, 10, 19, 12, 0, 0, 0, loc),
			Status:     "confirmed",
		}
		work, busy := Load(r, []Booking{b}, mon, mon.AddDate(0, 0, 1), loc)
		if work != 8*60 || busy != 120 {
			t.Errorf("Load in %v = work:%d, busy:%d; want 480, 120", loc, work, busy)
		}
	}
}

// TestStatAvgRating tests calculation of client average rating.
func TestStatAvgRating(t *testing.T) {
	tests := []struct {
		name string
		st   Stat
		want float64
	}{
		{"no ratings", Stat{Rated: 0, Stars: 0}, 0},
		{"single 5 star rating", Stat{Rated: 1, Stars: 5}, 5.0},
		{"mixed ratings", Stat{Rated: 4, Stars: 15}, 3.75},
		{"fractional average", Stat{Rated: 3, Stars: 14}, 14.0 / 3.0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.st.AvgRating()
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("AvgRating() = %v; want %v", got, tc.want)
			}
		})
	}
}

// TestStatFinish tests percentage calculation, integer rounding and no-show share.
func TestStatFinish(t *testing.T) {
	tests := []struct {
		name         string
		st           Stat
		wantLoad     int
		wantNoShow   int
		wantBookings int
	}{
		{
			name: "exact 50 percent load",
			st: Stat{
				Work:     100,
				Busy:     50,
				Statuses: map[string]int{"confirmed": 2},
			},
			wantLoad:     50,
			wantNoShow:   -1,
			wantBookings: 2,
		},
		{
			name: "rounding up 33.5 percent to 34",
			st: Stat{
				Work:     200,
				Busy:     67, // (67*100 + 100) / 200 = 6800 / 200 = 34
				Statuses: map[string]int{"came": 1},
			},
			wantLoad:     34,
			wantNoShow:   0,
			wantBookings: 1,
		},
		{
			name: "rounding down 33.33 percent to 33",
			st: Stat{
				Work:     300,
				Busy:     100, // (10000 + 150) / 300 = 10150 / 300 = 33
				Statuses: map[string]int{"came": 1},
			},
			wantLoad:     33,
			wantNoShow:   0,
			wantBookings: 1,
		},
		{
			name: "no show share rounding half up",
			st: Stat{
				Statuses: map[string]int{"came": 1, "no_show": 2}, // 2 / 3 = 66.67% -> (200 + 1)/3 = 67
			},
			wantLoad:     0,
			wantNoShow:   67,
			wantBookings: 3,
		},
		{
			name: "100 percent no show",
			st: Stat{
				Statuses: map[string]int{"no_show": 4},
			},
			wantLoad:     0,
			wantNoShow:   100,
			wantBookings: 4,
		},
		{
			name: "cancelled excluded from bookings count",
			st: Stat{
				Statuses: map[string]int{"came": 2, "cancelled": 5, "unconfirmed": 1},
			},
			wantLoad:     0,
			wantNoShow:   0,
			wantBookings: 3,
		},
		{
			name: "zero work handles division by zero safely",
			st: Stat{
				Work: 0,
				Busy: 10,
			},
			wantLoad:     0,
			wantNoShow:   -1,
			wantBookings: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.st
			s.finish()
			if s.Load != tc.wantLoad {
				t.Errorf("s.Load = %d; want %d", s.Load, tc.wantLoad)
			}
			if s.NoShowShare != tc.wantNoShow {
				t.Errorf("s.NoShowShare = %d; want %d", s.NoShowShare, tc.wantNoShow)
			}
			if s.Bookings != tc.wantBookings {
				t.Errorf("s.Bookings = %d; want %d", s.Bookings, tc.wantBookings)
			}
		})
	}
}

// TestMinMaxTime tests helper functions for time clamping.
func TestMinMaxTime(t *testing.T) {
	t1 := time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	if got := minTime(t1, t2); !got.Equal(t1) {
		t.Errorf("minTime(t1, t2) = %v; want %v", got, t1)
	}
	if got := minTime(t2, t1); !got.Equal(t1) {
		t.Errorf("minTime(t2, t1) = %v; want %v", got, t1)
	}
	if got := maxTime(t1, t2); !got.Equal(t2) {
		t.Errorf("maxTime(t1, t2) = %v; want %v", got, t2)
	}
	if got := maxTime(t2, t1); !got.Equal(t2) {
		t.Errorf("maxTime(t2, t1) = %v; want %v", got, t2)
	}

	// Identical timestamps
	if got := minTime(t1, t1); !got.Equal(t1) {
		t.Errorf("minTime(t1, t1) = %v; want %v", got, t1)
	}
	if got := maxTime(t1, t1); !got.Equal(t1) {
		t.Errorf("maxTime(t1, t1) = %v; want %v", got, t1)
	}
}
