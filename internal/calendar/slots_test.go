package calendar

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestSlots: the times of a link — two spans of a day on the meeting's grid, none on a day off of the rules, past
// ones and days beyond the link left out, an absence makes the day busy; two guests taking one time at once — one
// gets it; a guest's address makes SlotsPerHour meetings an hour; a meeting cancelled by the guest frees the time.
func TestSlots(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna := user("boss"), user("anna")
	s := NewStore(pool)
	links, err := s.SlotLinks(ctx, boss)
	must(err)
	if len(links) != 1 || links[0].Joint() || links[0].Length != 60 || len(links[0].Rules) != 1 || links[0].Rules[0].From != 9*60 {
		t.Fatalf("the personal link: %+v", links)
	}
	// a Monday a week or two ahead: spans 9–11 and 14:00–15:30 by 45 minutes on Mondays only
	now := time.Now()
	mon := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local).AddDate(0, 0, 7)
	for Weekday(mon) != 1 {
		mon = mon.AddDate(0, 0, 1)
	}
	l := links[0]
	l.Length, l.DaysAhead = 45, 30
	l.Rules = []SlotRule{{Days: []int{1}, From: 14 * 60, To: 15*60 + 30}, {Days: []int{1, 1}, From: 9 * 60, To: 11 * 60}}
	_, err = s.SaveSlotLink(ctx, boss, l)
	must(err)
	l, err = s.SlotLink(ctx, boss, l.ID)
	must(err)
	clock := func(list []time.Time) (out []string) {
		for _, t := range list {
			out = append(out, t.Format("15:04"))
		}
		return out
	}
	free, err := s.SlotFree(ctx, l, mon, now)
	must(err)
	if got := clock(free); len(got) != 4 || got[0] != "09:00" || got[1] != "09:45" || got[2] != "14:00" || got[3] != "14:45" {
		t.Fatalf("Monday: %v", got)
	}
	if free, _ := s.SlotFree(ctx, l, mon.AddDate(0, 0, 1), now); len(free) != 0 {
		t.Fatal("Tuesday is offered")
	}
	if free, _ := s.SlotFree(ctx, l, mon, mon.Add(10*time.Hour)); len(clock(free)) != 2 {
		t.Fatal("past times are offered")
	}
	if free, _ := s.SlotFree(ctx, l, mon.AddDate(0, 0, 35), now); len(free) != 0 {
		t.Fatal("a day beyond the link is offered")
	}
	// a joint link: Anna away that Monday
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', $2, $2, $1)`, anna, mon)
	must(err)
	j := SlotLink{Members: []string{anna}, Length: 45, Rules: l.Rules, DaysAhead: 30, Active: true}
	j.ID, err = s.SaveSlotLink(ctx, boss, j)
	must(err)
	j, err = s.SlotLink(ctx, boss, j.ID)
	must(err)
	if free, _ := s.SlotFree(ctx, j, mon, now); len(free) != 0 {
		t.Fatalf("an absent colleague is offered: %v", clock(free))
	}

	// two guests at 9:00 at once: one gets it
	at := mon.Add(9 * time.Hour)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.BookSlot(ctx, l, SlotBooking{Start: at, GuestName: "Гость", GuestContact: "+7 900", ClientIP: "10.0.0.1", Title: "Встреча"}, now)
		}()
	}
	wg.Wait()
	if (errs[0] == nil) == (errs[1] == nil) || !(errors.Is(errs[0], ErrSlotTaken) || errors.Is(errs[1], ErrSlotTaken)) {
		t.Fatalf("both or none: %v", errs)
	}
	if _, err := s.BookSlot(ctx, l, SlotBooking{Start: mon.Add(9*time.Hour + 45*time.Minute), GuestName: "Гость"}, now); !errors.Is(err, ErrSlotGuest) {
		t.Fatalf("no contact: %v", err)
	}
	if _, err := s.BookSlot(ctx, l, SlotBooking{Start: mon.Add(9*time.Hour + 10*time.Minute), GuestName: "Гость", GuestContact: "x"}, now); !errors.Is(err, ErrSlotTaken) {
		t.Fatalf("off the grid: %v", err)
	}
	// the address: SlotsPerHour an hour
	_, err = pool.Exec(ctx, `INSERT INTO calendar_slot_meetings (owner_id, token, guest_name, guest_contact, starts_at, ends_at, client_ip)
		SELECT $1, md5(random()::text), 'г', 'к', now(), now(), '10.0.0.1' FROM generate_series(1, $2)`, boss, SlotsPerHour-1)
	must(err)
	if _, err := s.BookSlot(ctx, l, SlotBooking{Start: mon.Add(14 * time.Hour), GuestName: "Гость", GuestContact: "x", ClientIP: "10.0.0.1"}, now); !errors.Is(err, ErrSlotTooMany) {
		t.Fatalf("too many: %v", err)
	}
	m, err := s.BookSlot(ctx, l, SlotBooking{Start: mon.Add(14 * time.Hour), GuestName: "Гость", GuestContact: "x", ClientIP: "10.0.0.2", Title: "Встреча"}, now)
	must(err)
	if free, _ := s.SlotFree(ctx, l, mon, now); len(clock(free)) != 2 {
		t.Fatalf("booked times are offered: %v", clock(free))
	}
	if _, err := s.CancelSlotMeeting(ctx, m.Token, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelSlotMeeting(ctx, m.Token, now); !errors.Is(err, ErrForbidden) {
		t.Fatalf("cancelled twice: %v", err)
	}
	if free, _ := s.SlotFree(ctx, l, mon, now); len(clock(free)) != 3 {
		t.Fatalf("the cancelled time is not offered again: %v", clock(free))
	}
	// the personal link stays
	if err := s.DeleteSlotLink(ctx, boss, l.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the personal link is deleted: %v", err)
	}
	must(s.DeleteSlotLink(ctx, boss, j.ID))
}
