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

// TestDue: the letter of a new booking with an e-mail, the reminder within its hours (not of a booking made inside
// them), the lateness of one not come; each due once, again after the booking is moved; the client confirms and
// cancels a booking yet to come, not one gone.
func TestDue(t *testing.T) {
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
	var boss, ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@go24.test') RETURNING id`).Scan(&ann))
	s := NewStore(pool)
	types, err := s.Types(ctx)
	must(err)
	all := []int{1, 2, 3, 4, 5, 6, 7}
	if _, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Врач", Slot: 30, Days: all, From: 0, To: 1440, RemindHours: 999}); !errors.Is(err, ErrSchedule) {
		t.Fatalf("a reminder too far: %v", err)
	}
	doc, err := s.SaveResource(ctx, boss, Resource{TypeID: types[0].ID, Name: "Врач", UserID: ann, Slot: 30, Days: all, From: 0, To: 1440,
		NotifyMail: true, RemindHours: 24, LateMinutes: 15})
	must(err)
	quiet, err := s.SaveResource(ctx, boss, Resource{TypeID: types[1].ID, Name: "Кабинет", Slot: 30, Days: all, From: 0, To: 1440, LateMinutes: 10})
	must(err)
	r, err := s.Resource(ctx, doc)
	must(err)
	if !r.NotifyMail || r.RemindHours != 24 || r.LateMinutes != 15 {
		t.Fatalf("resource: %+v", r)
	}

	now := time.Now().Truncate(time.Minute)
	book := func(res string, start time.Time, email string) string {
		t.Helper()
		id, err := s.SaveBooking(ctx, boss, Booking{ResourceID: res, Start: start, End: start.Add(30 * time.Minute), ClientName: "Пётр", ClientEmail: email})
		must(err)
		return id
	}
	soon := book(doc, now.Add(2*time.Hour), "p@go24.test")     // made inside the reminder's hours: the first letter only
	later := book(doc, now.Add(10*time.Hour), "p@go24.test")   // made a day before: the reminder too
	late := book(doc, now.Add(-30*time.Minute), "p@go24.test") // gone: lateness, no first letter
	noMail := book(doc, now.Add(5*time.Hour), "")              // no e-mail: nothing
	room := book(quiet, now.Add(-20*time.Minute), "r@go24.test")
	must(exec(ctx, pool, `UPDATE bookings SET created_at = now() - interval '2 days' WHERE id = $1`, later))
	_ = noMail

	due := func() map[string]string {
		t.Helper()
		list, err := s.DueAt(ctx, time.Now())
		must(err)
		m := map[string]string{}
		for _, d := range list {
			m[d.B.ID] += d.Kind + " "
			if d.B.ID == late && (d.Sender != ann || d.Notify != ann || !d.Mail || d.Resource != "Врач") {
				t.Fatalf("lateness: %+v", d)
			}
			if d.B.ID == room && (d.Mail || d.Notify != boss) {
				t.Fatalf("a resource without letters: %+v", d)
			}
		}
		return m
	}
	got := due()
	want := map[string]string{soon: "new ", later: "new remind ", late: "late ", room: "late "}
	if len(got) != len(want) {
		t.Fatalf("due: %v, want %v", got, want)
	}
	for id, k := range want {
		if got[id] != k {
			t.Fatalf("due %s: %q, want %q", id, got[id], k)
		}
	}
	list, err := s.DueAt(ctx, time.Now())
	must(err)
	for _, d := range list {
		ok, err := s.Claim(ctx, d)
		must(err)
		if !ok {
			t.Fatalf("not claimed: %+v", d)
		}
		if ok, _ := s.Claim(ctx, d); ok {
			t.Fatalf("claimed twice: %+v", d)
		}
	}
	if got := due(); len(got) != 0 {
		t.Fatalf("due after all is sent: %v", got)
	}
	// moved: the reminder and the lateness are due again, the first letter is not
	b, err := s.Booking(ctx, late)
	must(err)
	b.Start, b.End = now.Add(-time.Hour), now.Add(-30*time.Minute)
	_, err = s.SaveBooking(ctx, boss, b)
	must(err)
	if got := due(); got[late] != "late " || len(got) != 1 {
		t.Fatalf("due after a move: %v", got)
	}
	// came — no lateness
	must(s.SetStatus(ctx, boss, late, "came"))
	if got := due(); len(got) != 0 {
		t.Fatalf("due after the client came: %v", got)
	}

	// the client: confirms, cancels; a booking gone or cancelled stays
	sb, err := s.Booking(ctx, soon)
	must(err)
	if _, _, err := s.ClientStatus(ctx, sb.ClientToken, "came"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a client marks themselves come: %v", err)
	}
	if b, ok, err := s.ClientStatus(ctx, sb.ClientToken, "confirmed"); err != nil || !ok || b.Status != "confirmed" {
		t.Fatalf("confirmed: %+v %v", b, err)
	}
	if _, ok, err := s.ClientStatus(ctx, sb.ClientToken, "confirmed"); err != nil || ok {
		t.Fatalf("confirmed twice: %v %v", ok, err)
	}
	if b, _, err := s.ClientStatus(ctx, sb.ClientToken, "cancelled"); err != nil || b.Status != "cancelled" {
		t.Fatalf("cancelled: %+v %v", b, err)
	}
	if _, _, err := s.ClientStatus(ctx, sb.ClientToken, "confirmed"); !errors.Is(err, ErrClosed) {
		t.Fatalf("a cancelled booking confirmed: %v", err)
	}
	gone, err := s.Booking(ctx, room)
	must(err)
	if _, _, err := s.ClientStatus(ctx, gone.ClientToken, "cancelled"); !errors.Is(err, ErrClosed) {
		t.Fatalf("a booking gone cancelled: %v", err)
	}
	if _, _, err := s.ClientStatus(ctx, "nope", "cancelled"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown token: %v", err)
	}
	if d, err := s.DueOf(ctx, soon); err != nil || d.Notify != ann || d.Kind != "" {
		t.Fatalf("due of a booking: %+v %v", d, err)
	}
}

// TestConfirm: «Подтверждение записи» — the request to confirm within its hours to an unconfirmed booking with an
// e-mail (not one made inside them, not a confirmed one), the note to the employee within the alert's hours if the
// asked client has not confirmed; each once, again after a move.
func TestConfirm(t *testing.T) {
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
	var boss, ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@go24.test') RETURNING id`).Scan(&ann))
	s := NewStore(pool)
	types, err := s.Types(ctx)
	must(err)
	all := []int{1, 2, 3, 4, 5, 6, 7}
	res := Resource{TypeID: types[0].ID, Name: "Врач", UserID: ann, Slot: 30, Days: all, From: 0, To: 1440, NotifyMail: true}
	for _, bad := range [][2]int{{-1, 0}, {400, 0}, {24, 24}, {0, 3}, {24, -1}} {
		res.ConfirmHours, res.ConfirmAlert = bad[0], bad[1]
		if _, err := s.SaveResource(ctx, boss, res); !errors.Is(err, ErrConfirm) {
			t.Fatalf("confirmation %v: %v", bad, err)
		}
	}
	res.ConfirmHours, res.ConfirmAlert = 24, 3
	doc, err := s.SaveResource(ctx, boss, res)
	must(err)
	if r, err := s.Resource(ctx, doc); err != nil || r.ConfirmHours != 24 || r.ConfirmAlert != 3 {
		t.Fatalf("resource: %+v %v", r, err)
	}

	now := time.Now().Truncate(time.Minute)
	book := func(start time.Time, email string) string {
		t.Helper()
		id, err := s.SaveBooking(ctx, boss, Booking{ResourceID: doc, Start: start, End: start.Add(30 * time.Minute), ClientName: "Пётр", ClientEmail: email})
		must(err)
		must(exec(ctx, pool, `UPDATE bookings SET created_at = now() - interval '3 days', mailed_at = now() WHERE id = $1`, id))
		return id
	}
	ask := book(now.Add(10*time.Hour), "p@go24.test")  // within 24 h: asked
	alert := book(now.Add(2*time.Hour), "p@go24.test") // within 3 h: asked, then the employee is told
	far := book(now.Add(48*time.Hour), "p@go24.test")  // not yet
	sure := book(now.Add(12*time.Hour), "p@go24.test") // confirmed: nothing
	noMail := book(now.Add(5*time.Hour), "")           // no e-mail: not asked, so nobody told
	inside, err := s.SaveBooking(ctx, boss, Booking{ResourceID: doc, Start: now.Add(6 * time.Hour), End: now.Add(6*time.Hour + 30*time.Minute),
		ClientName: "Ира", ClientEmail: "i@go24.test"}) // made inside the hours: its first letter asks
	must(err)
	must(s.SetStatus(ctx, boss, sure, "confirmed"))
	_, _ = far, noMail

	due := func() map[string]string {
		t.Helper()
		list, err := s.DueAt(ctx, time.Now())
		must(err)
		m := map[string]string{}
		for _, d := range list {
			if d.Kind == DueNew {
				continue
			}
			m[d.B.ID] += d.Kind + " "
			if d.Kind == DueUnconfirmed && d.Notify != ann {
				t.Fatalf("told: %+v", d)
			}
		}
		return m
	}
	send := func() {
		t.Helper()
		list, err := s.DueAt(ctx, time.Now())
		must(err)
		for _, d := range list {
			_, err := s.Claim(ctx, d)
			must(err)
		}
	}
	if got := due(); len(got) != 2 || got[ask] != "confirm " || got[alert] != "confirm " || got[inside] != "" {
		t.Fatalf("due: %v", got)
	}
	send()
	if got := due(); len(got) != 1 || got[alert] != "unconfirmed " {
		t.Fatalf("due after the requests: %v", got)
	}
	send()
	if got := due(); len(got) != 0 {
		t.Fatalf("due after all is sent: %v", got)
	}
	// moved within the hours: asked again; confirmed — no note
	b, err := s.Booking(ctx, alert)
	must(err)
	b.Start, b.End = now.Add(150*time.Minute), now.Add(180*time.Minute)
	_, err = s.SaveBooking(ctx, boss, b)
	must(err)
	if got := due(); got[alert] != "confirm " || len(got) != 1 {
		t.Fatalf("due after a move: %v", got)
	}
	send()
	if _, _, err := s.ClientStatus(ctx, b.ClientToken, "confirmed"); err != nil {
		t.Fatal(err)
	}
	if got := due(); len(got) != 0 {
		t.Fatalf("due after the client confirmed: %v", got)
	}
}

func exec(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) error {
	_, err := pool.Exec(ctx, sql, args...)
	return err
}
