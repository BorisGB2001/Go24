package calendar

import (
	"context"
	"sort"
	"time"

	"go24/internal/tasks"
	"go24/internal/worktime"
)

// The planner of a meeting: when people are busy (their meetings, absences) and the nearest time
// all of them are free.

// Slot is a span of a person's busy time: busy | tentative (not answered yet or «maybe») | absent | booking (a
// client's booking of «Онлайн-запись»). Title and EventID
// are given only when the viewer may see that meeting.
type Slot struct {
	Start, End time.Time
	Kind       string
	Title      string
	EventID    string
}

// Person is one person of the planner and their busy time.
type Person struct {
	ID, Name string
	Slots    []Slot
}

// Busy is the busy time of the people between from and to, in the order asked; people the viewer may not know (see
// tasks.PeerSQL) are left out. The meeting being changed (except) does not make anyone busy.
func (s *Store) Busy(ctx context.Context, viewer string, people []string, from, to time.Time, except string) ([]Person, error) {
	rows, err := s.db.Query(ctx, `SELECT u.id::text, u.name FROM unnest($2::text[]) WITH ORDINALITY AS i(id, n)
		JOIN users u ON u.id::text = i.id WHERE u.active AND `+tasks.PeerSQL("$1", "u")+` ORDER BY i.n`, viewer, people)
	if err != nil {
		return nil, err
	}
	var list []Person
	at := map[string]int{}
	for rows.Next() {
		var p Person
		if err := rows.Scan(&p.ID, &p.Name); err != nil {
			rows.Close()
			return nil, err
		}
		if _, dup := at[p.ID]; !dup {
			at[p.ID] = len(list)
			list = append(list, p)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(list) == 0 {
		return list, err
	}
	ids := make([]string, len(list))
	for i, p := range list {
		ids[i] = p.ID
	}
	// the meetings of each person: organized, or invited and not declined; a free meeting keeps them free
	rows, err = s.db.Query(ctx, `SELECT x.uid::text, e.id::text, e.title, e.starts_at, e.ends_at, e.all_day,
			e.repeat, e.repeat_every, e.repeat_until, e.repeat_days, e.repeat_count, e.accessibility, x.status,
			(`+visible+` AND (NOT e.private OR e.owner_id = $1 OR EXISTS (SELECT 1 FROM event_attendees a WHERE a.event_id = e.id AND a.user_id = $1)))
		FROM (SELECT id AS event_id, owner_id AS uid, 'accepted' AS status FROM events
		      UNION ALL SELECT event_id, user_id, status FROM event_attendees WHERE status <> 'declined') x
		JOIN events e ON e.id = x.event_id
		WHERE x.uid::text = ANY($3::text[]) AND e.accessibility <> 'free' AND e.id::text <> $6 AND e.starts_at < $5
		  AND ((e.repeat = '' AND e.ends_at > $4) OR (e.repeat <> '' AND (e.repeat_until IS NULL OR e.repeat_until >= $4::date)))`,
		viewer, tasks.IsAdmin(ctx), ids, from, to, except)
	if err != nil {
		return nil, err
	}
	type row struct {
		uid, status string
		seen        bool
	}
	var found []row
	var events []Event
	for rows.Next() {
		var x row
		e := Event{}
		if err := rows.Scan(&x.uid, &e.ID, &e.Title, &e.Start, &e.End, &e.AllDay, &e.Repeat, &e.Every, &e.Until, &e.Days, &e.Count,
			&e.Accessibility, &x.status, &x.seen); err != nil {
			rows.Close()
			return nil, err
		}
		found, events = append(found, x), append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.skips(ctx, events); err != nil {
		return nil, err
	}
	for i, e := range events {
		x := found[i]
		kind := e.Accessibility
		if kind == "busy" && (x.status == "invited" || x.status == "tentative") {
			kind = "tentative"
		}
		for _, o := range e.expand(from, to) {
			sl := Slot{Start: o.Start, End: o.End, Kind: kind}
			if o.AllDay { // the whole day, as the calendar shows it
				sl.End = time.Date(o.End.Year(), o.End.Month(), o.End.Day(), 0, 0, 0, 0, o.End.Location()).AddDate(0, 0, 1)
			}
			if !sl.End.After(from) {
				continue
			}
			if x.seen {
				sl.Title, sl.EventID = o.Title, o.ID
			}
			p := &list[at[x.uid]]
			p.Slots = append(p.Slots, sl)
		}
	}
	// the bookings of «Онлайн-запись» on them as resources
	books, err := s.bookingsOf(ctx, ids, from, to)
	if err != nil {
		return nil, err
	}
	for _, b := range books {
		p := &list[at[b.OwnerID]]
		p.Slots = append(p.Slots, Slot{Start: b.Start, End: b.End, Kind: BookingKind})
	}
	// absences: whole days away
	rows, err = s.db.Query(ctx, `SELECT user_id::text, starts_on, ends_on FROM absences
		WHERE user_id::text = ANY($1::text[]) AND starts_on < $3::date AND ends_on >= $2::date`, ids, from, to)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var uid string
		var d0, d1 time.Time
		if err := rows.Scan(&uid, &d0, &d1); err != nil {
			rows.Close()
			return nil, err
		}
		p := &list[at[uid]]
		p.Slots = append(p.Slots, Slot{Kind: "absent",
			Start: time.Date(d0.Year(), d0.Month(), d0.Day(), 0, 0, 0, 0, time.Local),
			End:   time.Date(d1.Year(), d1.Month(), d1.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 1)})
	}
	rows.Close()
	for i := range list {
		sort.Slice(list[i].Slots, func(a, b int) bool { return list[i].Slots[a].Start.Before(list[i].Slots[b].Start) })
	}
	return list, rows.Err()
}

// FirstFree is the nearest start, not before from, of a meeting of the given length that falls into the working
// hours of a working day and overlaps none of the busy slots; ok is false when there is none within days.
func FirstFree(people []Person, from time.Time, length time.Duration, sched worktime.Schedule, days int) (time.Time, bool) {
	var busy []Slot
	for _, p := range people {
		busy = append(busy, p.Slots...)
	}
	sort.Slice(busy, func(a, b int) bool { return busy[a].Start.Before(busy[b].Start) })
	open, close := clockOf(sched.Start, 9*60), clockOf(sched.End, 18*60)
	day := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, from.Location())
	for d := 0; d < days; d, day = d+1, day.AddDate(0, 0, 1) {
		if !sched.WorkingDay(day) {
			continue
		}
		t, end := day.Add(time.Duration(open)*time.Minute), day.Add(time.Duration(close)*time.Minute)
		if t.Before(from) {
			t = from.Truncate(15 * time.Minute) // a quarter-hour grid
			if t.Before(from) {
				t = t.Add(15 * time.Minute)
			}
		}
		for !t.Add(length).After(end) {
			clash := false
			for _, b := range busy {
				if b.Start.Before(t.Add(length)) && b.End.After(t) {
					clash, t = true, b.End
					break
				}
			}
			if !clash {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// clockOf is minutes since midnight of "15:04" (def when it does not parse).
func clockOf(s string, def int) int {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return def
	}
	return t.Hour()*60 + t.Minute()
}
