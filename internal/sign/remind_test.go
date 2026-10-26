package sign

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRemind: the signer whose turn has lasted remind_days days (counted from midnight) is reminded, once a day at
// most, no more than RemindMax times; not when the document is not to be reminded of, nor after «Отправить ещё раз»
// that day; the next signer's turn counts from when it came.
func TestRemind(t *testing.T) {
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
	var anna, bob, file string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, content_type, size, uploaded_by) VALUES ('k', 'd.docx', 'x', 1, $1) RETURNING id`, anna).Scan(&file))
	s := NewStore(pool)
	if RemindOf(5) != 1 || RemindOf(0) != 0 || RemindOf(7) != 7 {
		t.Fatal("RemindOf")
	}

	now := time.Now()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	due := func(at time.Time) []Remind {
		t.Helper()
		day := at.Sub(now) / (24 * time.Hour)
		list, err := s.RemindDue(ctx, at, midnight.AddDate(0, 0, int(day)))
		must(err)
		return list
	}
	daily, err := s.Create(ctx, Doc{Title: "Договор", FileID: file, AuthorID: anna, RemindDays: 1},
		[]Party{{UserID: bob, Name: "Боб"}, {Name: "Иван", Email: "ivan@t"}})
	must(err)
	never, err := s.Create(ctx, Doc{Title: "Акт", FileID: file, AuthorID: anna}, []Party{{UserID: bob, Name: "Боб"}})
	must(err)
	weekly, err := s.Create(ctx, Doc{Title: "Счёт", FileID: file, AuthorID: anna, RemindDays: 7}, []Party{{Name: "Иван", Email: "ivan@t"}})
	must(err)
	if l := due(now); len(l) != 0 {
		t.Fatalf("the turn came today: %+v", l)
	}
	// the turns came two days ago
	_, err = pool.Exec(ctx, `UPDATE sign_parties SET ready_at = now() - interval '2 days' WHERE status = 'ready'`)
	must(err)
	l := due(now)
	if len(l) != 1 || l[0].DocID != daily || l[0].BatchSigner != "" {
		t.Fatalf("daily: %+v", l)
	}
	d, err := s.Get(ctx, daily)
	must(err)
	if c := d.Current(); c.ID != l[0].PartyID || c.Reminders != 1 || c.RemindedAt == nil || d.RemindDays != 1 {
		t.Fatalf("reminded: %+v", d)
	}
	if l := due(now.Add(time.Minute)); len(l) != 0 {
		t.Fatalf("twice a day: %+v", l)
	}
	if l := due(now.Add(24 * time.Hour)); len(l) != 1 || l[0].DocID != daily {
		t.Fatalf("the next day: %+v", l)
	}
	// «Отправить ещё раз» today: the next day is not due yet the day after
	must(s.Reminded(ctx, d.Current().ID))
	if l := due(now.Add(time.Hour)); len(l) != 0 {
		t.Fatalf("after resend: %+v", l)
	}
	// once a week: on the seventh day
	if l := due(now.Add(4 * 24 * time.Hour)); len(l) != 1 || l[0].DocID != daily {
		t.Fatalf("day 4: %+v", l)
	}
	if l := due(now.Add(5 * 24 * time.Hour)); len(l) != 2 || (l[0].DocID != weekly && l[1].DocID != weekly) {
		t.Fatalf("day 5 (7 since the turn): %+v", l)
	}
	// RemindMax reminders and no more
	for i := 6; i < 12; i++ {
		due(now.Add(time.Duration(i) * 24 * time.Hour))
	}
	d, _ = s.Get(ctx, daily)
	if c := d.Current(); c.Reminders != RemindMax {
		t.Fatalf("max: %d", c.Reminders)
	}
	// the employee signs: the client's turn counts from now
	next, _, err := s.Sign(ctx, daily, d.Current().ID, "", "", "")
	must(err)
	if l := due(now); len(l) != 0 {
		t.Fatalf("the client's turn came today: %+v", l)
	}
	_, err = pool.Exec(ctx, `UPDATE sign_parties SET ready_at = ready_at - interval '1 day' WHERE id::text = $1`, next.ID)
	must(err)
	l = due(now)
	if len(l) != 1 || l[0].PartyID != next.ID {
		t.Fatalf("the client: %+v", l)
	}
	for _, x := range l {
		if x.DocID == never {
			t.Fatal("never")
		}
	}
	must(s.SetRemind(ctx, daily, 0))
	if d, _ = s.Get(ctx, daily); d.RemindDays != 0 {
		t.Fatal("set remind")
	}
}
