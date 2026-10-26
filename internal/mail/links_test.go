package mail

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestInvitesAndShares against the test database: an invitation answered, then updated and cancelled by the
// organizer; an answer to the user's own meeting; the levels of a shared mailbox (a department with its
// sub-departments, people, guests) and its requests (assigned, done, opened again by a new letter).
func TestInvitesAndShares(t *testing.T) {
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
	user := func(sub, name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $2, $1 || '@go24.test', $3) RETURNING id`, sub, name, guest).Scan(&id))
		return id
	}
	anna, eve, petr, gina, gone := user("anna", "Анна", false), user("eve", "Ева", false), user("petr", "Пётр", false), user("gina", "Гостья", true), user("gone", "Ушедший", false)
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	must2(pool.Exec(ctx, `UPDATE users SET active = false WHERE id = $1`, gone))
	s := NewStore(pool, "test")
	s.Tr = func(key string, args ...any) string { return key + fmt.Sprint(args...) }
	box, err := s.Add(ctx, anna, Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	folder := func(name, role string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $3, true) RETURNING id::text`, box, name, role).Scan(&id))
		return id
	}
	inbox, sent := folder("INBOX", "inbox"), folder("Sent", "sent")
	uid := 0
	letter := func(f, from, subject, thread, ics string, at time.Time) string {
		uid++
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, subject, body, sent_at, thread, ics)
			VALUES ($1, $2, $3, $4, 'anna@go24.test', $5, 'текст', $6, $7, $8) RETURNING id::text`, box, f, uid, from, subject, at, thread, ics).Scan(&id))
		return id
	}
	start := time.Now().Add(48 * time.Hour).Truncate(time.Hour)
	req := Invite{UID: "meet-1", Summary: "Планёрка", Location: "Зал", Start: start, End: start.Add(time.Hour),
		Organizer: Person{Email: "ivan@client.test", Name: "Иван"}, Attendees: []Person{{Email: "anna@go24.test", RSVP: true}}}
	invLetter := letter(inbox, "ivan@client.test", "Планёрка", "meet", BuildICS(req, "REQUEST", time.Now()), time.Now())

	// accepted: the meeting is in the calendar; nobody is told (reply none: no mail server here)
	ev, err := s.Answer(ctx, anna, invLetter, "accepted", "none", "")
	must(err)
	var title, desc string
	var at time.Time
	must(pool.QueryRow(ctx, `SELECT title, description, starts_at FROM events WHERE id = $1 AND owner_id = $2`, ev, anna).Scan(&title, &desc, &at))
	if title != "Планёрка" || !at.Equal(start) || !strings.Contains(desc, "ivan@client.test") {
		t.Fatalf("event = %q %q %v", title, desc, at)
	}
	if _, err := s.Answer(ctx, eve, invLetter, "accepted", "none", ""); !errors.Is(err, ErrNotFound) { // not her letter
		t.Fatalf("someone else's invitation: %v", err)
	}
	// tentative keeps the same meeting
	ev2, err := s.Answer(ctx, anna, invLetter, "tentative", "none", "")
	must(err)
	if st, _ := s.InviteAnswer(ctx, anna, "meet-1"); ev2 != ev || st.Status != "tentative" || st.EventID != ev {
		t.Fatalf("tentative = %s %+v", ev2, st)
	}
	// the organizer moves it: it moves in the calendar, the answer is asked again (an older version answered)
	a := account{Mailbox: Mailbox{ID: box, UserID: anna, Email: "anna@go24.test"}}
	moved := req
	moved.Sequence, moved.Start, moved.End = 1, start.Add(2*time.Hour), start.Add(3*time.Hour)
	s.invitesArrived(ctx, a, BuildICS(moved, "REQUEST", time.Now()))
	must(pool.QueryRow(ctx, `SELECT starts_at FROM events WHERE id = $1`, ev).Scan(&at))
	if st, _ := s.InviteAnswer(ctx, anna, "meet-1"); !at.Equal(moved.Start) || st.Sequence != 0 {
		t.Fatalf("update = %v %+v", at, st)
	}
	// cancelled: marked in the calendar; removing it deletes the meeting
	s.invitesArrived(ctx, a, BuildICS(moved, "CANCEL", time.Now()))
	s.invitesArrived(ctx, a, BuildICS(moved, "CANCEL", time.Now())) // twice is once
	must(pool.QueryRow(ctx, `SELECT title FROM events WHERE id = $1`, ev).Scan(&title))
	if title != "mail.inv.cancelled_prefixПланёрка" {
		t.Fatalf("cancelled = %q", title)
	}
	cancelLetter := letter(inbox, "ivan@client.test", "Отменено: Планёрка", "meet", BuildICS(moved, "CANCEL", time.Now()), time.Now())
	if _, err := s.Answer(ctx, anna, cancelLetter, "accepted", "none", ""); err != nil {
		t.Fatal(err)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM events WHERE id = $1`, ev).Scan(&n))
	if st, _ := s.InviteAnswer(ctx, anna, "meet-1"); n != 0 || st.Status != "cancelled" {
		t.Fatalf("removed = %d %+v", n, st)
	}

	// an answer to the user's own meeting sent by mail: the person from outside's status
	var own string
	must(pool.QueryRow(ctx, `INSERT INTO events (owner_id, title, starts_at, ends_at, ical_uid) VALUES ($1, 'Обзор', now(), now(), 'own@go24') RETURNING id`, anna).Scan(&own))
	must2(pool.Exec(ctx, `INSERT INTO event_externals (event_id, email) VALUES ($1, 'ivan@client.test')`, own))
	reply := Reply(Invite{UID: "own@go24", Start: time.Now(), End: time.Now(), Organizer: Person{Email: "anna@go24.test"}}, Person{Email: "IVAN@client.test"}, "declined", "")
	s.invitesArrived(ctx, account{Mailbox: Mailbox{ID: box, UserID: eve}}, BuildICS(reply, "REPLY", time.Now())) // not her meeting
	var status string
	must(pool.QueryRow(ctx, `SELECT status FROM event_externals WHERE event_id = $1`, own).Scan(&status))
	if status != "invited" {
		t.Fatalf("someone else's answer taken: %s", status)
	}
	s.invitesArrived(ctx, a, BuildICS(reply, "REPLY", time.Now()))
	must(pool.QueryRow(ctx, `SELECT status FROM event_externals WHERE event_id = $1`, own).Scan(&status))
	if status != "declined" {
		t.Fatalf("answer = %s", status)
	}

	// shared mailboxes: a department with a sub-department
	var root, sales, team string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Продажи', $1) RETURNING id`, root).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Опт', $1) RETURNING id`, sales).Scan(&team))
	for _, u := range []string{eve, gina, gone} {
		must2(pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, u, team))
	}
	if l, _ := s.SharedLevel(ctx, eve, box); l != "" {
		t.Fatalf("not shared yet: %s", l)
	}
	if err := s.ShareBox(ctx, eve, box, "Продажи", sales, "read"); err == nil { // not hers
		t.Fatal("eve shared anna's mailbox")
	}
	must(s.ShareBox(ctx, anna, box, "Отдел продаж", sales, "read"))
	for who, want := range map[string]string{anna: "manage", eve: "read", petr: "", gina: "", gone: ""} {
		if l, err := s.SharedLevel(ctx, who, box); err != nil || l != want {
			t.Fatalf("level of %s = %q, want %q (%v)", who, l, want, err)
		}
	}
	if err := s.SetSharePerson(ctx, eve, box, petr, "read"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a reader gives access: %v", err)
	}
	if err := s.SetSharePerson(ctx, anna, box, gina, "read"); err == nil {
		t.Fatal("a guest got access")
	}
	must(s.SetSharePerson(ctx, anna, box, petr, "send"))
	if l, _ := s.SharedLevel(ctx, petr, box); l != "send" {
		t.Fatalf("petr = %s", l)
	}
	shares, err := s.Shares(ctx, eve)
	must(err)
	if len(shares) != 1 || shares[0].Title() != "Отдел продаж" || shares[0].Level != "read" || len(shares[0].People) != 1 {
		t.Fatalf("shares = %+v", shares)
	}
	if list, _ := s.Shares(ctx, gina); len(list) != 0 {
		t.Fatalf("a guest sees %d shared mailboxes", len(list))
	}

	// requests: a conversation from outside with the answer, a letter of its own
	q1 := letter(inbox, "client@x.test", "Вопрос", "q@x.test", "", time.Now().Add(-3*time.Hour))
	letter(sent, "anna@go24.test", "Re: Вопрос", "q@x.test", "", time.Now().Add(-2*time.Hour))
	letter(sent, "anna@go24.test", "Своё", "own@go24", "", time.Now().Add(-time.Hour))
	list, err := s.Tickets(ctx, eve, box, TicketFilter{})
	must(err)
	var found *Ticket
	for i := range list {
		if list[i].Thread == "q@x.test" {
			found = &list[i]
		}
		if list[i].Subject == "Своё" {
			t.Fatal("the mailbox's own letter is a request")
		}
	}
	if found == nil || found.Count != 2 || found.Status != "new" || found.FromAddr != "client@x.test" || found.Subject != "Re: Вопрос" {
		t.Fatalf("tickets = %+v", list)
	}
	if _, err := s.Tickets(ctx, gina, box, TicketFilter{}); err == nil {
		t.Fatal("a guest reads the requests")
	}
	if err := s.Assign(ctx, eve, box, q1, eve); !errors.Is(err, ErrForbidden) { // reading only
		t.Fatalf("a reader assigns: %v", err)
	}
	if err := s.Assign(ctx, petr, box, q1, gina); err == nil {
		t.Fatal("a guest assigned")
	}
	must(s.Assign(ctx, petr, box, q1, eve))
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'mail_assign' AND url LIKE '/mail/shared/%'`, eve).Scan(&n))
	if n != 1 {
		t.Fatalf("notifications = %d", n)
	}
	mine, _ := s.Tickets(ctx, eve, box, TicketFilter{Mine: true})
	if len(mine) != 1 || mine[0].Status != "progress" || mine[0].AssigneeName != "Ева" {
		t.Fatalf("mine = %+v", mine)
	}
	must(s.SetTicketStatus(ctx, petr, box, q1, "done"))
	if open, _ := s.Tickets(ctx, eve, box, TicketFilter{}); len(open) != len(list)-1 {
		t.Fatalf("done is not open: %d of %d", len(open), len(list))
	}
	time.Sleep(10 * time.Millisecond)
	letter(inbox, "client@x.test", "Re: Вопрос", "q@x.test", "", time.Now().Add(time.Minute)) // the client writes again
	tk, err := s.LetterTicket(ctx, eve, box, q1)
	must(err)
	if tk.Status != "progress" || !tk.Reopened || tk.Count != 3 {
		t.Fatalf("reopened = %+v", tk)
	}
	if !s.SharedCan(ctx, eve, q1) || s.SharedCan(ctx, gina, q1) || s.SharedCan(ctx, gone, q1) {
		t.Fatal("SharedCan")
	}
	must(s.UnshareBox(ctx, anna, box))
	if l, _ := s.SharedLevel(ctx, eve, box); l != "" || s.SharedCan(ctx, eve, q1) {
		t.Fatal("unshared mailbox still open")
	}
}
