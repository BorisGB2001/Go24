package mail

import (
	"context"
	"fmt"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestInviteByMail against a mail server (GreenMail) and the test database: the organizer's go24 meeting goes as an
// invitation (REQUEST) to someone's mailbox, where it is read and accepted with words; the answer (REPLY) comes back
// to the organizer's mailbox and marks the person accepted; an answer from a shared mailbox goes as the mailbox.
func TestInviteByMail(t *testing.T) {
	url, host := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_MAIL_HOST")
	if url == "" || host == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_MAIL_HOST not set")
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
	n := time.Now().UnixNano()
	orgAddr, annaAddr := fmt.Sprintf("org%d@go24.test", n), fmt.Sprintf("anna%d@go24.test", n)
	var org, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('org', 'Организатор', $1) RETURNING id`, orgAddr).Scan(&org))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', $1) RETURNING id`, annaAddr).Scan(&anna))
	s := NewStore(pool, "test")
	s.Tr = func(key string, args ...any) string { return key + fmt.Sprint(args...) }
	orgBox, err := s.Add(ctx, org, Settings{Email: orgAddr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	annaBox, err := s.Add(ctx, anna, Settings{Email: annaAddr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	// GreenMail makes a mailbox on its first letter
	for _, a := range []string{orgAddr, annaAddr} {
		must(smtp.SendMail(host+":3025", nil, "hello@go24.test", []string{a}, []byte("From: hello@go24.test\r\nTo: "+a+"\r\nSubject: hi\r\n\r\nhi\r\n")))
	}
	must(s.Sync(ctx, orgBox))
	must(s.Sync(ctx, annaBox))

	// the organizer's meeting with Anna from outside
	start := time.Now().Add(24 * time.Hour).Truncate(time.Minute)
	var ev string
	must(pool.QueryRow(ctx, `INSERT INTO events (owner_id, title, starts_at, ends_at, ical_uid, mailbox_id) VALUES ($1, 'Встреча', $2, $3, 'meet@go24', $4) RETURNING id`,
		org, start, start.Add(time.Hour), orgBox).Scan(&ev))
	_, err = pool.Exec(ctx, `INSERT INTO event_externals (event_id, email) VALUES ($1, $2)`, ev, annaAddr)
	must(err)
	in := Invite{UID: "meet@go24", Summary: "Встреча", Location: "Офис", Start: start, End: start.Add(time.Hour),
		Organizer: Person{Email: orgAddr, Name: "Организатор"}, Attendees: []Person{{Email: annaAddr, RSVP: true}}}
	must(s.SendCalendar(ctx, org, orgBox, []string{annaAddr}, "Встреча", "Приходите", in, "REQUEST", nil))
	time.Sleep(time.Second)
	must(s.Sync(ctx, annaBox))
	inbox, err := s.folderByRole(ctx, annaBox, "inbox")
	must(err)
	list, err := s.List(ctx, anna, inbox.ID, "", false)
	must(err)
	var letter string
	for _, m := range list {
		if m.Subject == "Встреча" {
			letter = m.ID
		}
	}
	if letter == "" {
		t.Fatalf("no invitation came: %+v", list)
	}
	got, ok, err := s.Invite(ctx, anna, letter)
	must(err)
	if !ok || got.Method != "REQUEST" || got.UID != "meet@go24" || got.Location != "Офис" || !got.Start.Equal(start) {
		t.Fatalf("invitation = %+v %v", got, ok)
	}
	// accepted with words: in Anna's calendar, the answer goes to the organizer
	ev2, err := s.Answer(ctx, anna, letter, "accepted", "send", "Буду вовремя")
	must(err)
	if ev2 == "" {
		t.Fatal("not in the calendar")
	}
	time.Sleep(time.Second)
	must(s.Sync(ctx, orgBox))
	var status string
	must(pool.QueryRow(ctx, `SELECT status FROM event_externals WHERE event_id = $1`, ev).Scan(&status))
	if status != "accepted" {
		t.Fatalf("the organizer's meeting: %s", status)
	}
	oin, _ := s.folderByRole(ctx, orgBox, "inbox")
	olist, _ := s.List(ctx, org, oin.ID, "", false)
	var answer string
	for _, m := range olist {
		if strings.HasPrefix(m.Subject, "mail.inv.subject.accepted") {
			answer = m.ID
		}
	}
	if answer == "" {
		t.Fatalf("no answer came: %+v", olist)
	}
	r, ok, err := s.Invite(ctx, org, answer)
	must(err)
	if !ok || r.Method != "REPLY" || r.Comment != "Буду вовремя" || len(r.Attendees) != 1 || r.Attendees[0].Status != "accepted" {
		t.Fatalf("answer = %+v", r)
	}
	if m, _ := s.message(ctx, org, answer); m.Body != "Буду вовремя" {
		t.Fatalf("answer letter = %+v", m)
	}

	// the organizer's mailbox shared: Anna (send) answers the letter as «Отдел»
	must(s.ShareBox(ctx, org, orgBox, "Отдел", "", "none"))
	must(s.SetSharePerson(ctx, org, orgBox, anna, "send"))
	must(s.SendShared(ctx, anna, orgBox, answer, Letter{To: annaAddr, Subject: "Re: ответ", Text: "Спасибо"}))
	time.Sleep(time.Second)
	must(s.Sync(ctx, annaBox))
	list, _ = s.List(ctx, anna, inbox.ID, "", false)
	var shared *Message
	for i := range list {
		if list[i].Subject == "Re: ответ" {
			shared = &list[i]
		}
	}
	if shared == nil || shared.FromName != "Отдел" || !strings.EqualFold(shared.FromAddr, orgAddr) {
		t.Fatalf("sent as the mailbox: %+v", shared)
	}
	tk, err := s.LetterTicket(ctx, anna, orgBox, answer)
	must(err)
	if tk.Status != "progress" || tk.AssigneeID != anna {
		t.Fatalf("answered request = %+v", tk)
	}
}
