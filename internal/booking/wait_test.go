package booking

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestWait: a booking of more resources than its own (each one is taken), overbooking of a resource (not by a link),
// the waiting list: a client booked from it and put back on it.
func TestWait(t *testing.T) {
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
	types, err := s.Types(ctx)
	must(err)
	all := []int{1, 2, 3, 4, 5, 6, 7}
	room, err := s.SaveResource(ctx, boss, Resource{TypeID: types[1].ID, Name: "Кабинет", Slot: 30, Days: all, From: 540, To: 1080})
	must(err)
	master, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Мастер", Slot: 60, Days: all, From: 540, To: 1080, Overbooking: true})
	must(err)
	mr, err := s.Resource(ctx, master)
	must(err)
	if !mr.Overbooking {
		t.Fatalf("overbooking not kept: %+v", mr)
	}
	rr, err := s.Resource(ctx, room)
	must(err)
	day := time.Now().AddDate(0, 0, 1)
	at := func(h, m int) time.Time { return time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, time.Local) }

	// the master with the room: the room is taken too
	b1, err := s.SaveBooking(ctx, boss, Booking{ResourceID: master, Extras: []string{room, master, room}, Start: at(10, 0), End: at(11, 0), ClientName: "Иван"})
	must(err)
	b, err := s.Booking(ctx, b1)
	must(err)
	if !slices.Equal(b.Extras, []string{room}) || !slices.Equal(b.ExtraNames, []string{"Кабинет"}) || !b.Takes(room) || !b.Takes(master) {
		t.Fatalf("extras: %+v", b)
	}
	if _, err := s.SaveBooking(ctx, boss, Booking{ResourceID: room, Start: at(10, 30), End: at(11, 0), ClientName: "Пётр"}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("the room taken as an extra booked again: %v", err)
	}
	free, err := s.Free(ctx, rr, day, time.Local)
	must(err)
	if slices.ContainsFunc(free, func(x time.Time) bool { return x.Equal(at(10, 0)) || x.Equal(at(10, 30)) }) || !slices.ContainsFunc(free, at(11, 0).Equal) {
		t.Fatalf("free slots of the room: %v", free)
	}
	// the master allows overbooking: another client at the same time, but not with the room, and not by a link
	b2, err := s.SaveBooking(ctx, boss, Booking{ResourceID: master, Start: at(10, 0), End: at(11, 0), ClientName: "Анна"})
	must(err)
	if _, err := s.SaveBooking(ctx, boss, Booking{ResourceID: master, Extras: []string{room}, Start: at(10, 0), End: at(11, 0), ClientName: "Ольга"}); !errors.Is(err, ErrOverlap) {
		t.Fatalf("overbooked with a taken room: %v", err)
	}
	if _, err := s.save(ctx, "", Booking{ResourceID: master, Start: at(10, 0), End: at(11, 0), ClientName: "Ольга"}, true); !errors.Is(err, ErrOverlap) {
		t.Fatalf("overbooked by a link: %v", err)
	}
	if free, _ := s.Free(ctx, mr, day, time.Local); slices.ContainsFunc(free, at(10, 0).Equal) {
		t.Fatalf("the overbooked time offered by a link: %v", free)
	}
	if _, err := s.SaveBooking(ctx, boss, Booking{ResourceID: master, Extras: []string{"00000000-0000-0000-0000-000000000000"}, Start: at(12, 0), End: at(13, 0), ClientName: "Ольга"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown extra: %v", err)
	}
	// the room taken off the booking — it is free again
	b.Extras = nil
	_, err = s.SaveBooking(ctx, boss, b)
	must(err)
	if b, _ = s.Booking(ctx, b1); len(b.Extras) != 0 {
		t.Fatalf("extras left: %+v", b.Extras)
	}
	if free, _ := s.Free(ctx, rr, day, time.Local); !slices.ContainsFunc(free, at(10, 0).Equal) {
		t.Fatalf("the room not freed: %v", free)
	}

	// the waiting list: a client with no time, booked on the room by a drag — the entry goes off the list
	if _, err := s.SaveWait(ctx, boss, Wait{Note: "любое утро"}); !errors.Is(err, ErrClient) {
		t.Fatalf("a waiting entry with no client: %v", err)
	}
	w1, err := s.SaveWait(ctx, boss, Wait{ResourceID: master, ClientName: " Мария ", ClientPhone: "+7 901", Note: "любое утро"})
	must(err)
	_, err = s.SaveWait(ctx, boss, Wait{ClientName: "Глеб"})
	must(err)
	list, err := s.Waitlist(ctx)
	must(err)
	if len(list) != 2 || list[0].ClientName != "Мария" || list[0].ResourceName != "Мастер" || list[0].CreatedByName != "Босс" {
		t.Fatalf("waitlist: %+v", list)
	}
	_, err = s.SaveBooking(ctx, boss, Booking{ResourceID: room, Start: at(14, 0), End: at(14, 30), ClientName: "Пётр"})
	must(err)
	if _, err := s.BookWait(ctx, boss, w1, room, at(14, 0)); !errors.Is(err, ErrOverlap) {
		t.Fatalf("booked from the list on a taken time: %v", err)
	}
	if list, _ := s.Waitlist(ctx); len(list) != 2 {
		t.Fatalf("an entry gone with a refused booking: %d", len(list))
	}
	nb, err := s.BookWait(ctx, boss, w1, room, at(12, 0))
	must(err)
	got, err := s.Booking(ctx, nb.ID)
	must(err)
	if got.ResourceID != room || !got.End.Equal(at(12, 30)) || got.ClientName != "Мария" || got.Note != "любое утро" || got.Status != "unconfirmed" {
		t.Fatalf("booked from the list: %+v", got)
	}
	if list, _ := s.Waitlist(ctx); len(list) != 1 || list[0].ClientName != "Глеб" {
		t.Fatalf("waitlist after booking: %+v", list)
	}
	if _, err := s.BookWait(ctx, boss, w1, room, at(15, 0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("booked twice from the list: %v", err)
	}
	// back on the list: the booking is gone, the entry keeps its resource
	w3, err := s.ToWait(ctx, boss, b2)
	must(err)
	w, err := s.Wait(ctx, w3)
	must(err)
	if w.ClientName != "Анна" || w.ResourceID != master {
		t.Fatalf("back on the list: %+v", w)
	}
	if _, err := s.Booking(ctx, b2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the booking put on the list is left: %v", err)
	}
	must(s.DeleteWait(ctx, w3))
	if err := s.DeleteWait(ctx, w3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
