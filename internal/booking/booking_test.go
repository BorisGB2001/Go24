package booking

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestBooking: resource types and resources with their schedule, bookings kept from overlapping (a cancelled one
// frees its time, bringing it back needs the time free), the free slots of a day.
func TestBooking(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	s := NewStore(pool)

	// the standard types come with the migration; one of its own is added, renamed, and kept while it has resources
	types, err := s.Types(ctx)
	must(err)
	if len(types) != 3 || types[0].Code != "employee" || types[1].Code != "room" || types[2].Code != "equipment" {
		t.Fatalf("standard types: %+v", types)
	}
	if _, err := s.SaveType(ctx, types[0].ID, "Мастер"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a standard type renamed: %v", err)
	}
	own, err := s.SaveType(ctx, "", "Стол")
	must(err)
	_, err = s.SaveType(ctx, own, "Столик")
	must(err)

	// a resource: the schedule is checked
	if _, err := s.SaveResource(ctx, boss, Resource{TypeID: own, Name: "Стол 1", Slot: 30, Days: []int{1}, From: 600, To: 540}); !errors.Is(err, ErrSchedule) {
		t.Fatalf("«с» after «до»: %v", err)
	}
	if _, err := s.SaveResource(ctx, boss, Resource{TypeID: own, Name: "Стол 1", Slot: 30, Days: nil, From: 540, To: 600}); !errors.Is(err, ErrSchedule) {
		t.Fatalf("no working days: %v", err)
	}
	table, err := s.SaveResource(ctx, boss, Resource{TypeID: own, Name: "Стол 1", Slot: 30, Days: []int{5, 1, 1, 9}, From: 540, To: 660})
	must(err)
	if err := s.DeleteType(ctx, own); !errors.Is(err, ErrTypeInUse) {
		t.Fatalf("a type with a resource deleted: %v", err)
	}
	res, err := s.Resource(ctx, table)
	must(err)
	if res.TypeName != "Столик" || len(res.Days) != 2 || res.Days[0] != 1 || res.Days[1] != 5 {
		t.Fatalf("resource: %+v", res)
	}
	doc, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Врач", UserID: boss, Slot: 60, Days: []int{1, 2, 3, 4, 5, 6, 7}, From: 0, To: 1440})
	must(err)

	// bookings: next Monday 9:00–10:00 at the table; 9:30 is taken, 10:00 is free, another resource is free
	now := time.Now()
	mon := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.Local)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	at := func(h, m int) time.Time { return time.Date(mon.Year(), mon.Month(), mon.Day(), h, m, 0, 0, time.Local) }
	if _, err := s.SaveBooking(ctx, boss, Booking{ResourceID: table, Start: at(9, 0), End: at(10, 0)}); !errors.Is(err, ErrClient) {
		t.Fatalf("no client: %v", err)
	}
	b1, err := s.SaveBooking(ctx, boss, Booking{ResourceID: table, Start: at(9, 0), End: at(10, 0), ClientName: "Иван", ClientPhone: "+7 900"})
	must(err)
	if _, err := s.SaveBooking(ctx, boss, Booking{ResourceID: table, Start: at(9, 30), End: at(10, 30), ClientName: "Пётр"}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("an overlap: %v", err)
	}
	b2, err := s.SaveBooking(ctx, boss, Booking{ResourceID: table, Start: at(10, 0), End: at(10, 30), ClientName: "Пётр"})
	must(err)
	_, err = s.SaveBooking(ctx, boss, Booking{ResourceID: doc, Start: at(9, 30), End: at(10, 30), ClientName: "Пётр"})
	must(err)
	b, err := s.Booking(ctx, b1)
	must(err)
	if b.Status != "unconfirmed" || b.CreatedByName != "Босс" {
		t.Fatalf("a new booking: %+v", b)
	}
	// moving a booking onto itself is no overlap; onto the next one is
	b.End = at(9, 45)
	_, err = s.SaveBooking(ctx, boss, b)
	must(err)
	b.End = at(10, 15)
	if _, err := s.SaveBooking(ctx, boss, b); !errors.Is(err, ErrOverlap) {
		t.Fatalf("moved onto the next booking: %v", err)
	}

	// the free slots: 9:00–11:00 by 30 minutes, without the booked 9:00–9:45 and 10:00–10:30
	free, err := s.Free(ctx, res, mon, time.Local)
	must(err)
	if len(free) != 1 || !free[0].Equal(at(10, 30)) {
		t.Fatalf("free slots: %v", free)
	}
	if free, _ := s.Free(ctx, res, mon.AddDate(0, 0, 1), time.Local); len(free) != 0 {
		t.Fatalf("free slots on a day off: %v", free)
	}
	// cancelled — its time is free again; brought back while the time is taken — refused
	must(s.SetStatus(ctx, boss, b2, "cancelled"))
	if free, _ := s.Free(ctx, res, mon, time.Local); len(free) != 2 {
		t.Fatalf("free slots after a cancel: %v", free)
	}
	_, err = s.SaveBooking(ctx, boss, Booking{ResourceID: table, Start: at(10, 0), End: at(10, 30), ClientName: "Анна"})
	must(err)
	if err := s.SetStatus(ctx, boss, b2, "confirmed"); !errors.Is(err, ErrOverlap) {
		t.Fatalf("a cancelled booking brought back over another: %v", err)
	}
	if err := s.SetStatus(ctx, boss, b1, "bogus"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown status: %v", err)
	}
	list, err := s.Bookings(ctx, mon, mon.AddDate(0, 0, 1))
	must(err)
	if len(list) != 4 {
		t.Fatalf("bookings of the day: %d", len(list))
	}
	// a resource deleted — with its bookings
	must(s.DeleteResource(ctx, table))
	if list, _ := s.Bookings(ctx, mon, mon.AddDate(0, 0, 1)); len(list) != 1 {
		t.Fatalf("bookings after the resource is deleted: %d", len(list))
	}
	must(s.DeleteType(ctx, own))
}
