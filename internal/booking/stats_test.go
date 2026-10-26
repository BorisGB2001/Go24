package booking

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestLoad: the working minutes of the days and those bookings cover — overlapping ones once, cancelled ones not,
// the time out of the working hours not.
func TestLoad(t *testing.T) {
	loc := time.UTC
	mon := time.Date(2026, 10, 19, 0, 0, 0, 0, loc) // a Monday
	at := func(d, h, m int) time.Time { return mon.AddDate(0, 0, d).Add(time.Duration(h*60+m) * time.Minute) }
	r := Resource{ID: "r", Days: []int{1, 2, 3, 4, 5}, From: 9 * 60, To: 18 * 60}
	list := []Booking{
		{ID: "1", ResourceID: "r", Start: at(0, 10, 0), End: at(0, 11, 0), Status: "came"},
		{ID: "2", ResourceID: "r", Start: at(0, 10, 30), End: at(0, 11, 30), Status: "confirmed"},                   // overbooked: +30
		{ID: "3", ResourceID: "x", Extras: []string{"r"}, Start: at(1, 8, 0), End: at(1, 10, 0), Status: "no_show"}, // 9–10
		{ID: "4", ResourceID: "r", Start: at(2, 12, 0), End: at(2, 13, 0), Status: "cancelled"},
		{ID: "5", ResourceID: "x", Start: at(3, 12, 0), End: at(3, 13, 0), Status: "came"},
	}
	work, busy := Load(r, list, mon, mon.AddDate(0, 0, 7), loc)
	if work != 5*9*60 || busy != 90+60 {
		t.Fatalf("work %d busy %d", work, busy)
	}
	if work, busy = Load(r, list, mon.AddDate(0, 0, 5), mon.AddDate(0, 0, 7), loc); work != 0 || busy != 0 {
		t.Fatalf("weekend: work %d busy %d", work, busy)
	}
	st := Stat{Work: 100, Busy: 33, Statuses: map[string]int{"came": 2, "no_show": 1, "cancelled": 4, "confirmed": 1}}
	st.finish()
	if st.Load != 33 || st.NoShowShare != 33 || st.Bookings != 4 {
		t.Fatalf("finish: %+v", st)
	}
	st = Stat{Statuses: map[string]int{"confirmed": 1}}
	if st.finish(); st.Load != 0 || st.NoShowShare != -1 {
		t.Fatalf("empty: %+v", st)
	}
}

// TestStats: the load and the statuses by resources for a period, a booking of two resources once in the total.
func TestStats(t *testing.T) {
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
	a, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Анна", Slot: 60, Days: all, From: 600, To: 1200})
	must(err)
	b, err := s.SaveResource(ctx, boss, Resource{TypeID: types[1].ID, Name: "Кабинет", Slot: 60, Days: all, From: 600, To: 1100})
	must(err)
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local)
	at := func(h int) time.Time { return day.Add(time.Duration(h) * time.Hour) }
	for _, x := range []Booking{
		{ResourceID: a, Extras: []string{b}, Start: at(10), End: at(12), ClientName: "К1", Status: "came"},
		{ResourceID: a, Start: at(13), End: at(14), ClientName: "К2", Status: "no_show"},
		{ResourceID: b, Start: at(15), End: at(16), ClientName: "К3", Status: "cancelled"},
	} {
		_, err := s.SaveBooking(ctx, boss, x)
		must(err)
	}
	list, total, err := s.Stats(ctx, "", day, day.AddDate(0, 0, 2), time.Local)
	must(err)
	if len(list) != 2 || list[0].Resource.ID != a || list[0].Work != 2*600 || list[0].Busy != 180 || list[0].Load != 15 ||
		list[0].Bookings != 2 || list[0].NoShowShare != 50 {
		t.Fatalf("Анна: %+v", list[0])
	}
	if list[1].Work != 2*500 || list[1].Busy != 120 || list[1].Bookings != 1 || list[1].Statuses["cancelled"] != 1 || list[1].NoShowShare != 0 {
		t.Fatalf("Кабинет: %+v", list[1])
	}
	if total.Bookings != 2 || total.Statuses["cancelled"] != 1 || total.Work != 2200 || total.Busy != 300 || total.NoShowShare != 50 {
		t.Fatalf("total: %+v", total)
	}
	if only, _, err := s.Stats(ctx, types[1].ID, day, day.AddDate(0, 0, 1), time.Local); err != nil || len(only) != 1 || only[0].Resource.ID != b {
		t.Fatalf("of a type: %+v %v", only, err)
	}
}
