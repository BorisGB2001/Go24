package booking

import (
	"context"
	"slices"
	"time"
)

// Stat is how a resource was booked in a period: its working minutes and those of them bookings take (not cancelled
// ones; overlapping bookings of an overbooked resource count once), and its bookings that start in the period by status.
type Stat struct {
	Resource          Resource
	Work, Busy        int // minutes
	Bookings          int // all of them but the cancelled ones
	Statuses          map[string]int
	Load, NoShowShare int // percent; NoShowShare is of those who came or did not, -1 when none of them yet
	Rated, Stars      int // the bookings the clients rated and their stars together
}

// AvgRating: the clients' average rating, 0 — none rated.
func (st Stat) AvgRating() float64 {
	if st.Rated == 0 {
		return 0
	}
	return float64(st.Stars) / float64(st.Rated)
}

// Stats: the load of the resources (typeID — of one type) for the days [from, to) in loc, and the total of them all
// (Resource of the total is empty; a booking of more resources is counted once).
func (s *Store) Stats(ctx context.Context, typeID string, from, to time.Time, loc *time.Location) ([]Stat, Stat, error) {
	res, err := s.Resources(ctx, typeID)
	if err != nil {
		return nil, Stat{}, err
	}
	list, err := s.Bookings(ctx, from, to)
	if err != nil {
		return nil, Stat{}, err
	}
	out := make([]Stat, 0, len(res))
	total := Stat{Statuses: map[string]int{}}
	seen := map[string]bool{}
	for _, r := range res {
		st := Stat{Resource: r, Statuses: map[string]int{}}
		st.Work, st.Busy = Load(r, list, from, to, loc)
		for _, b := range list {
			if !b.Takes(r.ID) || b.Start.Before(from) || !b.Start.Before(to) {
				continue
			}
			st.Statuses[b.Status]++
			if b.Rating > 0 {
				st.Rated, st.Stars = st.Rated+1, st.Stars+b.Rating
			}
			if !seen[b.ID] {
				seen[b.ID] = true
				total.Statuses[b.Status]++
				if b.Rating > 0 {
					total.Rated, total.Stars = total.Rated+1, total.Stars+b.Rating
				}
			}
		}
		st.finish()
		total.Work, total.Busy = total.Work+st.Work, total.Busy+st.Busy
		out = append(out, st)
	}
	total.finish()
	return out, total, nil
}

func (st *Stat) finish() {
	for status, n := range st.Statuses {
		if status != "cancelled" {
			st.Bookings += n
		}
	}
	st.Load, st.NoShowShare = 0, -1
	if st.Work > 0 {
		st.Load = (st.Busy*100 + st.Work/2) / st.Work
	}
	if n := st.Statuses["came"] + st.Statuses["no_show"]; n > 0 {
		st.NoShowShare = (st.Statuses["no_show"]*100 + n/2) / n
	}
}

// Load: the working minutes of a resource on the days [from, to) in loc and those of them the bookings of list that
// take it (not cancelled) cover.
func Load(r Resource, list []Booking, from, to time.Time, loc *time.Location) (work, busy int) {
	type span struct{ a, b time.Time }
	var spans []span
	for _, b := range list {
		if b.Status != "cancelled" && b.Takes(r.ID) {
			spans = append(spans, span{b.Start, b.End})
		}
	}
	slices.SortFunc(spans, func(x, y span) int { return x.a.Compare(y.a) })
	var merged []span
	for _, x := range spans {
		if n := len(merged); n > 0 && !x.a.After(merged[n-1].b) {
			if x.b.After(merged[n-1].b) {
				merged[n-1].b = x.b
			}
			continue
		}
		merged = append(merged, x)
	}
	from = from.In(loc)
	for d := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, loc); d.Before(to); d = d.AddDate(0, 0, 1) {
		if !r.WorksOn(d) {
			continue
		}
		h0, h1 := r.HoursOn(d)
		a := time.Date(d.Year(), d.Month(), d.Day(), 0, h0, 0, 0, loc)
		b := time.Date(d.Year(), d.Month(), d.Day(), 0, h1, 0, 0, loc)
		work += h1 - h0
		if r.InBreak(h0, h1) {
			work -= min(h1, r.BreakTo) - max(h0, r.BreakFrom)
		}
		for _, x := range merged {
			if x.a.Before(b) && x.b.After(a) {
				busy += int(minTime(x.b, b).Sub(maxTime(x.a, a)).Minutes())
			}
		}
	}
	return work, busy
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
