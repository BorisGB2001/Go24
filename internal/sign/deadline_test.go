package sign

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestParseDeadline: a date or a time's day, not before today; empty — none.
func TestParseDeadline(t *testing.T) {
	today := time.Date(2026, 10, 28, 23, 30, 0, 0, time.Local)
	for v, want := range map[string]string{"": "", "2026-10-28": "2026-10-28", "2026-11-05T10:00:00+01:00": "2026-11-05"} {
		d, err := ParseDeadline(v, today)
		if err != nil || (want == "") != (d == nil) || (d != nil && d.Format(time.DateOnly) != want) {
			t.Fatalf("%q: %v %v", v, d, err)
		}
	}
	for _, v := range []string{"2026-10-27", "28.10.2026", "x"} {
		if _, err := ParseDeadline(v, today); err != ErrDeadline {
			t.Fatalf("%q: %v", v, err)
		}
	}
	if d := DeadlineIn(today, 3); d == nil || d.Format(time.DateOnly) != "2026-10-31" || Days(today, d) != 3 {
		t.Fatalf("in 3 days: %v", d)
	}
	if DeadlineIn(today, 0) != nil || Days(today, nil) != 0 {
		t.Fatal("none")
	}
}

// TestExpire: a document being signed whose deadline day has passed is expired, with the event in its history; one
// due today, one with no deadline and one signed are not; the deadline is changed only while it is being signed.
func TestExpire(t *testing.T) {
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
	now := time.Now()
	mk := func(title string, deadline *time.Time) string {
		id, err := s.Create(ctx, Doc{Title: title, FileID: file, AuthorID: anna, Deadline: deadline}, []Party{{UserID: bob, Name: "Боб"}})
		must(err)
		return id
	}
	day := Day(now)
	today := mk("Сегодня", &day)
	soon := mk("Через 2 дня", DeadlineIn(now, 2))
	none := mk("Без срока", nil)
	signed := mk("Подписан", DeadlineIn(now, 1))
	d, err := s.Get(ctx, signed)
	must(err)
	_, done, err := s.Sign(ctx, signed, d.Current().ID, "", "", "")
	if err != nil || !done {
		t.Fatal("sign", err)
	}
	if ids, err := s.Expire(ctx, now); err != nil || len(ids) != 0 {
		t.Fatalf("today: %v %v", ids, err)
	}
	if d, _ = s.Get(ctx, soon); d.Deadline == nil || Days(now, d.Deadline) != 2 {
		t.Fatalf("deadline kept: %v", d.Deadline)
	}
	ids, err := s.Expire(ctx, now.AddDate(0, 0, 1))
	if err != nil || len(ids) != 1 || ids[0] != today {
		t.Fatalf("tomorrow: %v %v", ids, err)
	}
	d, _ = s.Get(ctx, today)
	ev, err := s.Events(ctx, today)
	must(err)
	if d.Status != "expired" || d.DoneAt == nil || ev[len(ev)-1].Kind != EvExpired {
		t.Fatalf("expired: %+v %+v", d, ev)
	}
	if err := s.SetDeadline(ctx, today, nil); err != ErrTurn {
		t.Fatalf("expired deadline changed: %v", err)
	}
	must(s.SetDeadline(ctx, soon, DeadlineIn(now, 10))) // extended
	if ids, _ := s.Expire(ctx, now.AddDate(0, 0, 5)); len(ids) != 0 {
		t.Fatalf("extended: %v", ids)
	}
	if ids, _ := s.Expire(ctx, now.AddDate(0, 0, 400)); len(ids) != 1 || ids[0] != soon {
		t.Fatalf("none never expires: %v", ids)
	}
	if d, _ = s.Get(ctx, none); d.Status != "signing" {
		t.Fatal("none", d.Status)
	}
	if d, _ = s.Get(ctx, signed); d.Status != "done" {
		t.Fatal("signed", d.Status)
	}
	if l, err := s.List(ctx, anna, "closed", false); err != nil || len(l) != 2 {
		t.Fatalf("closed: %d %v", len(l), err)
	}
	id, err := s.NewTemplate(ctx, Template{Title: "Т", FileID: file, AuthorID: anna, DeadlineDays: 5})
	must(err)
	if tp, err := s.Template(ctx, id); err != nil || tp.DeadlineDays != 5 {
		t.Fatalf("template: %+v %v", tp, err)
	}
}
