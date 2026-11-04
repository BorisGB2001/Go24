package calendar

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestGuestOptions: the options of a meeting for its people, as «Ещё» — the invited invite others when
// the meeting lets them (not those who declined, not a stranger), the organizer hears of it; the organizer is not
// told of the answers; the invited do not see one another.
func TestGuestOptions(t *testing.T) {
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
	user := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, guest) VALUES ($1, $1, $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	boss, anna, bob, eve, guest := user("boss", false), user("anna", false), user("bob", false), user("eve", false), user("guest", true)
	s := NewStore(pool)
	day := time.Date(2026, 10, 15, 10, 0, 0, 0, time.Local)
	count := func(u, kind string) (n int) {
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2`, u, kind).Scan(&n))
		return
	}

	// a meeting without the options: the invited do not invite
	plain, err := s.Save(ctx, boss, Event{Title: "Обычная", Start: day, End: day.Add(time.Hour)}, []string{anna})
	must(err)
	if _, err := s.Invite(ctx, anna, plain, []string{bob}); err != ErrForbidden {
		t.Fatalf("invited into a meeting that does not let: %v", err)
	}
	must(s.Respond(ctx, anna, plain, "accepted"))
	if count(boss, "event_reply") != 1 {
		t.Fatal("the organizer did not hear of the answer")
	}

	id, err := s.Save(ctx, boss, Event{Title: "Планёрка", Start: day, End: day.Add(time.Hour), AllowInvite: true, QuietAnswers: true, HideGuests: true},
		[]string{anna, bob})
	must(err)
	e, err := s.Get(ctx, boss, id)
	must(err)
	if !e.AllowInvite || !e.QuietAnswers || !e.HideGuests || len(e.Attendees) != 2 || e.Others != 0 {
		t.Fatalf("the organizer's meeting %+v", e)
	}
	// «Скрыть список участников»: anna sees herself and how many more
	e, err = s.Get(ctx, anna, id)
	must(err)
	if len(e.Attendees) != 1 || e.Attendees[0].ID != anna || e.Others != 1 {
		t.Fatalf("anna sees %+v, others %d", e.Attendees, e.Others)
	}
	// «Уведомлять об ответах» off: the organizer is not told
	must(s.Respond(ctx, anna, id, "accepted"))
	if count(boss, "event_reply") != 1 {
		t.Fatal("the organizer was told of a quiet answer")
	}

	// «Участники могут приглашать других»
	if _, err := s.Invite(ctx, eve, id, []string{eve}); err != ErrForbidden {
		t.Fatalf("a stranger invited: %v", err)
	}
	if _, err := s.Invite(ctx, anna, id, []string{guest}); err == nil {
		t.Fatal("a guest from outside invited into a meeting outside collabs")
	}
	added, err := s.Invite(ctx, anna, id, []string{eve, bob, boss, eve})
	must(err)
	if len(added) != 1 || added[0] != eve {
		t.Fatalf("added %v", added)
	}
	if count(eve, "event") != 1 || count(boss, "event_invited") != 1 {
		t.Fatal("the invitation's notifications")
	}
	var body string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'event_invited'`, boss).Scan(&body))
	if body != "eve" {
		t.Fatalf("the organizer is told %q", body)
	}
	if e, _ := s.Get(ctx, eve, id); e.MyStatus != "invited" || e.Others != 2 {
		t.Fatalf("eve's meeting %+v", e)
	}
	// the one who declined invites no more; the organizer does, without telling themself
	must(s.Respond(ctx, bob, id, "declined"))
	if _, err := s.Invite(ctx, bob, id, []string{user("zed", false)}); err != ErrForbidden {
		t.Fatalf("the declined invited: %v", err)
	}
	must(pool.QueryRow(ctx, `SELECT id::text FROM users WHERE name = 'zed'`).Scan(&body))
	if added, err := s.Invite(ctx, boss, plain, []string{body}); err != nil || len(added) != 1 || count(boss, "event_invited") != 1 {
		t.Fatalf("the organizer invited %v %v", added, err)
	}

	// the options are kept by a save that gives them, turned off by one that does not set them
	e, err = s.Get(ctx, boss, id)
	must(err)
	e.HideGuests = false
	var people []string
	for _, a := range e.Attendees {
		people = append(people, a.ID)
	}
	_, err = s.Save(ctx, boss, e, people)
	must(err)
	if e, _ := s.Get(ctx, anna, id); len(e.Attendees) != 3 || e.Others != 0 || !e.AllowInvite {
		t.Fatalf("after the list is shown again %+v", e)
	}
}
