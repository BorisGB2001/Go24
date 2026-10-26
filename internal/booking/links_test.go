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

// TestLinks: a link for clients offers its resources (none — all); a client books a free slot of the schedule within
// the link's days with a name and a phone — «не подтверждена», with a page of its own; a taken, an off-schedule or a
// too far slot, a switched off link and too many bookings from one address are refused.
func TestLinks(t *testing.T) {
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
	doc, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Врач", Slot: 30, Days: all, From: 540, To: 660})
	must(err)
	room, err := s.SaveResource(ctx, boss, Resource{TypeID: types[1].ID, Name: "Кабинет", Slot: 60, Days: all, From: 540, To: 660})
	must(err)

	if _, err := s.SaveLink(ctx, boss, Link{Name: " "}); !errors.Is(err, ErrName) {
		t.Fatalf("a link without a name: %v", err)
	}
	id, err := s.SaveLink(ctx, boss, Link{Name: "Запись к врачу", Resources: []string{doc, doc}, DaysAhead: 3, Active: true})
	must(err)
	l, err := s.Link(ctx, id)
	must(err)
	if len(l.Token) != 32 || len(l.Resources) != 1 || l.DaysAhead != 3 || l.CreatedBy != boss {
		t.Fatalf("link: %+v", l)
	}
	if res, _ := s.LinkResources(ctx, l); len(res) != 1 || res[0].ID != doc {
		t.Fatalf("the link's resources: %+v", res)
	}
	if res, _ := s.LinkResources(ctx, Link{}); len(res) != 2 {
		t.Fatalf("a link of all resources: %+v", res)
	}

	tomorrow := time.Now().AddDate(0, 0, 1)
	at := func(d time.Time, h, m int) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), h, m, 0, 0, time.Local)
	}
	client := Booking{ResourceID: doc, Start: at(tomorrow, 9, 30), ClientName: "Иван", ClientPhone: "+7 900 123-45-67", ClientIP: "10.0.0.1"}
	if _, err := s.BookByLink(ctx, l, Booking{ResourceID: doc, Start: at(tomorrow, 9, 30), ClientName: "Иван", ClientPhone: "12"}, time.Local); !errors.Is(err, ErrPhone) {
		t.Fatalf("no phone: %v", err)
	}
	if _, err := s.BookByLink(ctx, l, Booking{ResourceID: room, Start: at(tomorrow, 9, 0), ClientName: "Иван", ClientPhone: "+7 900 123"}, time.Local); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a resource not of the link: %v", err)
	}
	off := client
	off.Start = at(tomorrow, 9, 10)
	if _, err := s.BookByLink(ctx, l, off, time.Local); !errors.Is(err, ErrSlot) {
		t.Fatalf("not a slot of the schedule: %v", err)
	}
	far := client
	far.Start = at(time.Now().AddDate(0, 0, 3), 9, 0)
	if _, err := s.BookByLink(ctx, l, far, time.Local); !errors.Is(err, ErrSlot) {
		t.Fatalf("beyond the link's days: %v", err)
	}
	b, err := s.BookByLink(ctx, l, client, time.Local)
	must(err)
	got, err := s.ByClientToken(ctx, b.ClientToken)
	must(err)
	if got.ID != b.ID || got.Status != "unconfirmed" || got.LinkName != "Запись к врачу" || got.CreatedBy != "" ||
		!got.End.Equal(at(tomorrow, 10, 0)) || got.ClientIP != "10.0.0.1" {
		t.Fatalf("the booking by the link: %+v", got)
	}
	if _, err := s.BookByLink(ctx, l, client, time.Local); !errors.Is(err, ErrSlot) {
		t.Fatalf("the same slot twice: %v", err)
	}
	if _, err := s.ByClientToken(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an empty token: %v", err)
	}
	// from one address — PerHour bookings an hour
	for m := 10 * 60; m < 11*60; m += 30 {
		x := client
		x.Start = at(tomorrow, m/60, m%60)
		_, err := s.BookByLink(ctx, l, x, time.Local)
		must(err)
	}
	day2 := time.Now().AddDate(0, 0, 2)
	for i := 0; i < PerHour-3; i++ {
		x := client
		x.Start = at(day2, 9, 30*i)
		_, err := s.BookByLink(ctx, l, x, time.Local)
		must(err)
	}
	x := client
	x.Start = at(day2, 10, 30)
	if _, err := s.BookByLink(ctx, l, x, time.Local); !errors.Is(err, ErrTooMany) {
		t.Fatalf("too many from one address: %v", err)
	}
	x.ClientIP = "10.0.0.2"
	_, err = s.BookByLink(ctx, l, x, time.Local)
	must(err)

	// switched off — the page is gone; deleted — the bookings stay
	l.Active = false
	_, err = s.SaveLink(ctx, boss, l)
	must(err)
	if _, err := s.LinkByToken(ctx, l.Token); !errors.Is(err, ErrLinkOff) {
		t.Fatalf("a switched off link: %v", err)
	}
	if links, _ := s.Links(ctx); len(links) != 1 || links[0].Bookings != PerHour+1 {
		t.Fatalf("links: %+v", links)
	}
	must(s.DeleteLink(ctx, id))
	if got, err := s.Booking(ctx, b.ID); err != nil || got.LinkID != "" {
		t.Fatalf("a booking after its link is deleted: %+v %v", got, err)
	}
}
