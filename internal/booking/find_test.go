package booking

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestFind: «Список записей» by its filter and the counter of the menu — the employee's bookings not confirmed yet to
// come or with a late client (made by them, of their resource, of the link they answer for).
func TestFind(t *testing.T) {
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
	var boss, emp string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('emp', 'Мастер', 'emp@go24.test') RETURNING id`).Scan(&emp))
	s := NewStore(pool)
	types, err := s.Types(ctx)
	must(err)
	all := []int{1, 2, 3, 4, 5, 6, 7}
	master, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Мастер", UserID: emp, Slot: 30, Days: all, From: 480, To: 1200, LateMinutes: 15})
	must(err)
	room, err := s.SaveResource(ctx, boss, Resource{TypeID: types[1].ID, Name: "Кабинет", Slot: 30, Days: all, From: 480, To: 1200})
	must(err)
	link, err := s.SaveLink(ctx, boss, Link{Name: "Запись", ResponsibleID: emp, Active: true})
	must(err)
	day := time.Now().AddDate(0, 0, 1)
	at := func(d, h, m int) time.Time { return time.Date(day.Year(), day.Month(), day.Day()+d, h, m, 0, 0, time.Local) }
	now := at(0, 12, 0)
	add := func(by string, b Booking) string {
		t.Helper()
		id, err := s.SaveBooking(ctx, by, b)
		must(err)
		return id
	}
	late := add(boss, Booking{ResourceID: master, Start: at(0, 10, 0), End: at(0, 11, 0), ClientName: "Иван", Status: "confirmed"})
	unconf := add(boss, Booking{ResourceID: master, Start: at(0, 14, 0), End: at(0, 15, 0), ClientName: "Пётр Петров", ClientPhone: "+7 (900) 111-22-33"})
	conf := add(boss, Booking{ResourceID: room, Start: at(0, 11, 30), End: at(0, 12, 0), ClientName: "Анна", Status: "confirmed", ClientEmail: "anna@client.test"})
	byLink := add("", Booking{ResourceID: room, LinkID: link, Start: at(0, 16, 0), End: at(0, 17, 0), ClientName: "Ольга"})
	add(boss, Booking{ResourceID: master, Start: at(0, 9, 0), End: at(0, 9, 30), ClientName: "Отменил", Status: "cancelled"})
	add(boss, Booking{ResourceID: master, Start: at(-1, 9, 0), End: at(-1, 9, 30), ClientName: "Вчера"}) // past, not confirmed: no more attention

	for _, c := range []struct {
		user string
		want int
	}{{emp, 3}, {boss, 2}} {
		n, err := s.Attention(ctx, c.user, now)
		must(err)
		if n != c.want {
			t.Errorf("attention of %s: %d, want %d", c.user, n, c.want)
		}
	}
	find := func(f Filter) []string {
		t.Helper()
		if f.From.IsZero() {
			f.From, f.To = at(0, 0, 0), at(1, 0, 0)
		}
		list, err := s.Find(ctx, f, now)
		must(err)
		ids := []string{}
		for _, b := range list {
			ids = append(ids, b.ID)
		}
		return ids
	}
	for name, c := range map[string]struct {
		f    Filter
		want []string
	}{
		"phone digits": {Filter{Query: "900 111"}, []string{unconf}},
		"name":         {Filter{Query: "Петров"}, []string{unconf}},
		"e-mail":       {Filter{Query: "anna@"}, []string{conf}},
		"late":         {Filter{Status: "late"}, []string{late}},
		"attention":    {Filter{Status: "attention", Mine: boss}, []string{late, unconf}},
		"mine of emp":  {Filter{Status: "attention", Mine: emp}, []string{late, unconf, byLink}},
		"confirmed":    {Filter{Status: "confirmed"}, []string{late, conf}},
		"resource":     {Filter{ResourceID: room}, []string{conf, byLink}},
		"by link":      {Filter{ByLink: true}, []string{byLink}},
		"percent":      {Filter{Query: "%"}, []string{}},
	} {
		if got := find(c.f); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
	if got := find(Filter{From: at(-1, 0, 0), To: at(0, 0, 0)}); len(got) != 1 {
		t.Errorf("yesterday: %v", got)
	}
}
