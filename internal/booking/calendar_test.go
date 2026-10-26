package booking

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/calendar"
	"go24/internal/db"
)

// TestCalendarBusy: an employee's meetings (not «free», not declined) and absences take the free slots of their
// resource unless it does not go by the calendar; their bookings show in their calendar and planner, of the resource
// or one of the others, and are not counted twice.
func TestCalendarBusy(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	var boss, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	s := NewStore(pool)
	types, err := s.Types(ctx)
	must(err)
	mon := time.Now().AddDate(0, 0, 7)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	mon = time.Date(mon.Year(), mon.Month(), mon.Day(), 0, 0, 0, 0, time.Local)
	tue := mon.AddDate(0, 0, 1)
	at := func(d time.Time, h int) time.Time { return d.Add(time.Duration(h) * time.Hour) }
	r := Resource{TypeID: types[0].ID, Name: "Мастер", UserID: boss, Slot: 60, Days: []int{1, 2}, From: 9 * 60, To: 14 * 60,
		CalendarBusy: true}
	r.ID, err = s.SaveResource(ctx, boss, r)
	must(err)
	room, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Кабинет Анны", UserID: anna, Slot: 60,
		Days: []int{1, 2}, From: 9 * 60, To: 14 * 60, CalendarBusy: true})
	must(err)
	got, err := s.Resource(ctx, r.ID)
	must(err)
	if !got.CalendarBusy {
		t.Fatal("calendar_busy is not kept")
	}
	// a busy meeting 10–11, a free one 11–12, Anna's 12–13 the boss declined, Anna's 13–14 the boss has not answered
	ev := func(owner string, h int, access string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO events (owner_id, title, starts_at, ends_at, accessibility) VALUES ($1, 'Встреча', $2, $3, $4)
			RETURNING id`, owner, at(mon, h), at(mon, h+1), access).Scan(&id))
		return id
	}
	ev(boss, 10, "busy")
	ev(boss, 11, "free")
	_, err = pool.Exec(ctx, `INSERT INTO event_attendees (event_id, user_id, status) VALUES ($1, $2, 'declined'), ($3, $2, 'invited')`,
		ev(anna, 12, "busy"), boss, ev(anna, 13, "busy"))
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', $2, $2, $1)`, boss, tue)
	must(err)
	free, err := s.Free(ctx, got, mon, time.Local)
	must(err)
	if want := []time.Time{at(mon, 9), at(mon, 11), at(mon, 12)}; !slices.EqualFunc(free, want, time.Time.Equal) {
		t.Fatalf("free on Monday: %v, want %v", free, want)
	}
	if free, err = s.Free(ctx, got, tue, time.Local); err != nil || len(free) != 0 {
		t.Fatalf("free in the absence: %v %v", free, err)
	}
	// a booking of 9–10 on the boss with Anna's room: in both calendars and planners, taken once
	b, err := s.SaveBooking(ctx, anna, Booking{ResourceID: r.ID, Extras: []string{room}, Start: at(mon, 9), End: at(mon, 10), ClientName: "Иван"})
	must(err)
	if free, err = s.Free(ctx, got, mon, time.Local); err != nil || len(free) != 2 {
		t.Fatalf("free with the booking: %v %v", free, err)
	}
	cal := calendar.NewStore(pool)
	for user, where := range map[string]string{boss: "Мастер", anna: "Кабинет Анны"} {
		list, err := cal.Range(ctx, user, mon, mon.AddDate(0, 0, 1))
		must(err)
		i := slices.IndexFunc(list, func(e calendar.Event) bool { return e.Kind == calendar.BookingKind })
		if i < 0 || list[i].ID != b || list[i].Title != "Иван" || list[i].Where != where || list[i].Status != "unconfirmed" ||
			!list[i].Start.Equal(at(mon, 9)) {
			t.Fatalf("the booking in the calendar of %s: %+v", where, list)
		}
	}
	people, err := cal.Busy(ctx, anna, []string{anna}, mon, mon.AddDate(0, 0, 1), "")
	must(err)
	if !slices.ContainsFunc(people[0].Slots, func(x calendar.Slot) bool { return x.Kind == calendar.BookingKind && x.Start.Equal(at(mon, 9)) }) {
		t.Fatalf("the booking in the planner: %+v", people)
	}
	if spans, err := s.CalendarBusy(ctx, got, mon, mon.AddDate(0, 0, 1)); err != nil || len(spans) != 2 {
		t.Fatalf("the calendar busy time (the meetings 10 and 13, not the booking): %v %v", spans, err)
	}
	// a cancelled booking is gone from the calendar; the resource that does not go by the calendar is free at 10
	must(s.SetStatus(ctx, anna, b, "cancelled"))
	list, err := cal.Range(ctx, boss, mon, mon.AddDate(0, 0, 1))
	must(err)
	if slices.ContainsFunc(list, func(e calendar.Event) bool { return e.Kind == calendar.BookingKind }) {
		t.Fatal("a cancelled booking in the calendar")
	}
	got.CalendarBusy = false
	_, err = s.SaveResource(ctx, boss, got)
	must(err)
	got, err = s.Resource(ctx, r.ID)
	must(err)
	if free, err = s.Free(ctx, got, mon, time.Local); err != nil || len(free) != 5 {
		t.Fatalf("free without the calendar: %v %v", free, err)
	}
}
