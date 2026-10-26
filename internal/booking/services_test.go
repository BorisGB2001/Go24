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

// TestServices: a resource provides services of the catalog (not products) with their minutes; a booking keeps its
// services as booked — a later price of the catalog does not change it, saved again it keeps them; the free slots
// of a service longer than a slot need the following ones free; by a link a resource with services is booked for one
// of them, for its minutes.
func TestServices(t *testing.T) {
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
	var boss, cut, color, comb string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	product := func(name, kind string, price float64) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO crm_products (name, kind, price, created_by) VALUES ($1, $2, $3, $4) RETURNING id`,
			name, kind, price, boss).Scan(&id))
		return id
	}
	cut, color, comb = product("Стрижка", "service", 1500), product("Окрашивание", "service", 3000), product("Расчёска", "product", 200)
	s := NewStore(pool)
	types, err := s.Types(ctx)
	must(err)
	all := []int{1, 2, 3, 4, 5, 6, 7}
	r := Resource{TypeID: types[0].ID, Name: "Мастер", Slot: 30, Days: all, From: 540, To: 720}
	if _, err := s.SaveResource(ctx, boss, Resource{TypeID: r.TypeID, Name: "Х", Slot: 30, Days: all, From: 540, To: 720,
		Services: []Service{{ProductID: comb}}}); !errors.Is(err, ErrService) {
		t.Fatalf("a product as a service: %v", err)
	}
	r.Services = []Service{{ProductID: color, Minutes: 90}, {ProductID: cut}, {ProductID: cut, Minutes: 45}}
	r.ID, err = s.SaveResource(ctx, boss, r)
	must(err)
	if r, err = s.Resource(ctx, r.ID); err != nil || len(r.Services) != 2 || r.Services[0].Name != "Окрашивание" ||
		r.Services[0].Minutes != 90 || r.Services[1].Minutes != 30 || r.Services[1].Price != 1500 {
		t.Fatalf("the resource's services: %+v %v", r.Services, err)
	}

	tomorrow := time.Now().AddDate(0, 0, 1)
	at := func(h, m int) time.Time {
		return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), h, m, 0, 0, time.Local)
	}
	b := Booking{ResourceID: r.ID, Start: at(10, 0), End: at(11, 30), ClientName: "Анна", Services: []Service{{ProductID: color}, {ProductID: cut}}}
	b.ID, err = s.SaveBooking(ctx, boss, b)
	must(err)
	must2 := func(id string) Booking {
		t.Helper()
		x, err := s.Booking(ctx, id)
		must(err)
		return x
	}
	if x := must2(b.ID); len(x.Services) != 2 || x.Services[0].Price != 3000 || x.Services[0].Minutes != 90 {
		t.Fatalf("the booking's services: %+v", x.Services)
	} else if sum, cur := x.Total(); sum != 4500 || cur != "RUB" {
		t.Fatalf("the total: %v %s", sum, cur)
	}
	if _, err := pool.Exec(ctx, `UPDATE crm_products SET price = 9999`); err != nil {
		t.Fatal(err)
	}
	x := must2(b.ID)
	x.Services = Keep([]string{cut}, x.Services)
	must(s.SetStatus(ctx, boss, b.ID, "confirmed"))
	if y := must2(b.ID); len(y.Services) != 2 || y.Services[1].Price != 1500 || y.Status != "confirmed" {
		t.Fatalf("saved again: %+v", y)
	}
	_, err = s.SaveBooking(ctx, boss, x)
	must(err)
	if y := must2(b.ID); len(y.Services) != 1 || y.Services[0].Price != 1500 {
		t.Fatalf("a service left: %+v", y.Services)
	}
	if _, err := s.SaveBooking(ctx, boss, Booking{ResourceID: r.ID, Start: at(9, 0), End: at(9, 30), ClientName: "Б",
		Services: []Service{{ProductID: comb}}}); !errors.Is(err, ErrService) {
		t.Fatalf("a product in a booking: %v", err)
	}

	// 10:00–11:30 is taken, the work ends at 12:00: no 90 minutes are free, 60 — only from 9:00
	free, err := s.FreeFor(ctx, r, tomorrow, time.Local, 90)
	must(err)
	if len(free) != 0 {
		t.Fatalf("90 minutes free: %v", free)
	}
	free, err = s.FreeFor(ctx, r, tomorrow, time.Local, 60)
	must(err)
	if len(free) != 1 || !free[0].Equal(at(9, 0)) {
		t.Fatalf("60 minutes free: %v", free)
	}

	id, err := s.SaveLink(ctx, boss, Link{Name: "Салон", DaysAhead: 3, Active: true})
	must(err)
	l, err := s.Link(ctx, id)
	must(err)
	client := Booking{ResourceID: r.ID, Start: at(11, 30), ClientName: "Ира", ClientPhone: "+7 900 000-00-01"}
	if _, err := s.BookByLink(ctx, l, client, time.Local); !errors.Is(err, ErrService) {
		t.Fatalf("by a link without a service: %v", err)
	}
	client.Services = []Service{{ProductID: color}}
	if _, err := s.BookByLink(ctx, l, client, time.Local); !errors.Is(err, ErrSlot) {
		t.Fatalf("90 minutes at 11:30: %v", err)
	}
	client.Services = []Service{{ProductID: cut, Name: "подделка", Price: 1}}
	got, err := s.BookByLink(ctx, l, client, time.Local)
	must(err)
	if y := must2(got.ID); !y.End.Equal(at(12, 0)) || len(y.Services) != 1 || y.Services[0].Name != "Стрижка" || y.Services[0].Price != 9999 {
		t.Fatalf("booked by a link: %+v", y)
	}
}
