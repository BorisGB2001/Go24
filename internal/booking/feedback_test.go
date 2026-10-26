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

// TestFeedback: «Обратная связь» — a resource asking for ratings: the letter is due once after a visit the client came
// to (not before its end, not of one not come, not of a resource not asking, not of a week ago); the client rates it
// 1–5 with a comment and may change it; the statistics give the average.
func TestFeedback(t *testing.T) {
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
	asks, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Мастер", Slot: 30, Days: all, From: 0, To: 1440, NotifyMail: true, Feedback: true})
	must(err)
	silent, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Другой", Slot: 30, Days: all, From: 0, To: 1440, NotifyMail: true})
	must(err)
	if r, err := s.Resource(ctx, asks); err != nil || !r.Feedback {
		t.Fatalf("feedback not kept: %+v %v", r, err)
	}
	now := time.Now().Truncate(time.Minute)
	add := func(res, status string, start time.Time) Booking {
		t.Helper()
		id, err := s.SaveBooking(ctx, boss, Booking{ResourceID: res, Start: start, End: start.Add(time.Hour), ClientName: "Клиент",
			ClientEmail: "c@client.test", Status: status})
		must(err)
		b, err := s.Booking(ctx, id)
		must(err)
		return b
	}
	came := add(asks, "came", now.Add(-2*time.Hour))
	add(asks, "came", now.Add(-30*time.Minute))                      // not over yet
	add(asks, "no_show", now.Add(-3*time.Hour))                      // did not come
	add(silent, "came", now.Add(-3*time.Hour))                       // the resource does not ask
	add(asks, "came", now.Add(-8*24*time.Hour))                      // too long ago
	_, err = pool.Exec(ctx, `UPDATE bookings SET mailed_at = now()`) // the letters of new bookings are not the point here
	must(err)
	rates := func() []Due {
		t.Helper()
		list, err := s.DueAt(ctx, now)
		must(err)
		var out []Due
		for _, d := range list {
			if d.Kind == DueRate {
				out = append(out, d)
			}
		}
		return out
	}
	due := rates()
	if len(due) != 1 || due[0].B.ID != came.ID || !due[0].Mail {
		t.Fatalf("due to rate: %+v", due)
	}
	ok, err := s.Claim(ctx, due[0])
	must(err)
	if !ok || len(rates()) != 0 {
		t.Fatalf("the request is due again")
	}

	// the client rates the visit from its page
	if _, _, err := s.Rate(ctx, came.ClientToken, 0, ""); !errors.Is(err, ErrRating) {
		t.Fatalf("no stars: %v", err)
	}
	future := add(asks, "confirmed", now.Add(24*time.Hour))
	if _, _, err := s.Rate(ctx, future.ClientToken, 5, ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("a visit yet to come rated: %v", err)
	}
	b, first, err := s.Rate(ctx, came.ClientToken, 4, "  Хорошо  ")
	must(err)
	if !first || b.Rating != 4 {
		t.Fatalf("first rating: %v %+v", first, b)
	}
	if _, first, err = s.Rate(ctx, came.ClientToken, 5, "Отлично"); err != nil || first {
		t.Fatalf("the rating changed: %v %v", first, err)
	}
	if b, err = s.Booking(ctx, came.ID); err != nil || b.Rating != 5 || b.RatingNote != "Отлично" {
		t.Fatalf("rated booking: %+v %v", b, err)
	}
	other := add(silent, "came", now.Add(-5*time.Hour))
	_, _, err = s.Rate(ctx, other.ClientToken, 2, "")
	must(err)
	list, total, err := s.Stats(ctx, "", now.AddDate(0, 0, -1), now.AddDate(0, 0, 1), time.Local)
	must(err)
	for _, st := range list {
		if st.Resource.ID == asks && (st.Rated != 1 || st.AvgRating() != 5) || st.Resource.ID == silent && st.AvgRating() != 2 {
			t.Fatalf("stats of %s: %+v", st.Resource.Name, st)
		}
	}
	if total.Rated != 2 || total.AvgRating() != 3.5 {
		t.Fatalf("total: %+v", total)
	}
}
