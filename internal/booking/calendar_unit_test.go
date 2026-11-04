package booking

import (
	"context"
	"testing"
	"time"
)

// TestCalendarBusyInactiveResource tests that resources without a user or without calendar sync return nil.
func TestCalendarBusyInactiveResource(t *testing.T) {
	ctx := context.Background()
	s := &Store{} // nil db; must return early without touching database

	loc := time.UTC
	from := time.Date(2026, 10, 19, 9, 0, 0, 0, loc)
	to := time.Date(2026, 10, 19, 18, 0, 0, 0, loc)

	tests := []struct {
		name string
		r    Resource
	}{
		{"no user and calendar busy false", Resource{UserID: "", CalendarBusy: false}},
		{"no user and calendar busy true", Resource{UserID: "", CalendarBusy: true}},
		{"user set and calendar busy false", Resource{UserID: "usr-1", CalendarBusy: false}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spans, err := s.CalendarBusy(ctx, tc.r, from, to)
			if err != nil || spans != nil {
				t.Fatalf("CalendarBusy(%+v) = %v, %v; want nil, nil", tc.r, spans, err)
			}
		})
	}
}
