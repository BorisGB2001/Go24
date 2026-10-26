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

// TestSchedule: the schedule by days — a weekday's own hours, the break and the dates off — gives the free slots and
// the working time of the statistics; hours of a weekday not worked or the common ones are not kept.
func TestSchedule(t *testing.T) {
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
	// the next Monday and Saturday after a week, the Tuesday after them — off
	mon := time.Now().AddDate(0, 0, 7)
	for mon.Weekday() != time.Monday {
		mon = mon.AddDate(0, 0, 1)
	}
	mon = time.Date(mon.Year(), mon.Month(), mon.Day(), 0, 0, 0, 0, time.Local)
	sat, tue := mon.AddDate(0, 0, 5), mon.AddDate(0, 0, 8)
	r := Resource{TypeID: types[0].ID, Name: "Мастер", Slot: 60, Days: []int{1, 2, 6}, From: 9 * 60, To: 13 * 60,
		DayHours:  map[int][2]int{6: {10 * 60, 12 * 60}, 1: {9 * 60, 13 * 60}, 3: {8 * 60, 9 * 60}},
		BreakFrom: 11 * 60, BreakTo: 12 * 60, OffDates: []time.Time{tue, tue}}
	if _, err := s.SaveResource(ctx, boss, Resource{TypeID: r.TypeID, Name: "x", Slot: 60, Days: []int{1}, From: 540, To: 600,
		BreakFrom: 600, BreakTo: 540}); !errors.Is(err, ErrSchedule) {
		t.Fatalf("a break backwards: %v", err)
	}
	if _, err := s.SaveResource(ctx, boss, Resource{TypeID: r.TypeID, Name: "x", Slot: 60, Days: []int{1}, From: 540, To: 600,
		DayHours: map[int][2]int{1: {600, 540}}}); !errors.Is(err, ErrSchedule) {
		t.Fatalf("a day's hours backwards: %v", err)
	}
	id, err := s.SaveResource(ctx, boss, r)
	must(err)
	if r, err = s.Resource(ctx, id); err != nil {
		t.Fatal(err)
	}
	if len(r.DayHours) != 1 || r.DayHours[6] != [2]int{600, 720} || len(r.OffDates) != 1 || r.OwnFrom(6) != "10:00" || r.OwnTo(1) != "" {
		t.Fatalf("kept: %+v", r)
	}
	slots := func(day time.Time) (out []string) {
		t.Helper()
		free, err := s.Free(ctx, r, day, time.Local)
		must(err)
		for _, f := range free {
			out = append(out, f.Format("15:04"))
		}
		return out
	}
	if got := slots(mon); len(got) != 3 || got[0] != "09:00" || got[1] != "10:00" || got[2] != "12:00" {
		t.Fatalf("Monday: %v", got)
	}
	if got := slots(sat); len(got) != 1 || got[0] != "10:00" {
		t.Fatalf("Saturday: %v", got)
	}
	if got := slots(tue); len(got) != 0 || r.WorksOn(tue) || !r.WorksOn(tue.AddDate(0, 0, 7)) {
		t.Fatalf("the date off: %v", got)
	}
	// a longer service does not span the break
	if free, err := s.FreeFor(ctx, r, mon, time.Local, 120); err != nil || len(free) != 1 || free[0].Hour() != 9 {
		t.Fatalf("two hours on Monday: %v %v", free, err)
	}
	// the working time: Monday and Tuesday 4−1, Saturday 2−1; the next Monday 3, the Tuesday after it off
	work, _ := Load(r, nil, mon, mon.AddDate(0, 0, 9), time.Local)
	if work != 10*60 {
		t.Fatalf("work: %d", work)
	}
	// cleared
	r.DayHours, r.BreakFrom, r.BreakTo, r.OffDates = nil, 0, 0, nil
	_, err = s.SaveResource(ctx, boss, r)
	must(err)
	if r, err = s.Resource(ctx, id); err != nil || len(r.DayHours) != 0 || r.BreakTo != 0 || len(r.OffDates) != 0 {
		t.Fatalf("cleared: %+v %v", r, err)
	}
}
