package mail

import (
	"bytes"
	"context"
	"errors"
	"net/mail"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestNoForwardHeader(t *testing.T) {
	for h, want := range map[string]bool{"do-not-forward": true, " Do-Not-Forward ": true, "x, do-not-forward": true, "": false, "none": false} {
		if got := noForwardOf(h); got != want {
			t.Errorf("noForwardOf(%q) = %v", h, got)
		}
	}
	e := envelope{From: mail.Address{Address: "anna@go24.test"}, To: []*mail.Address{{Address: "bob@go24.test"}}, Host: "go24.test", Now: time.Now()}
	msg, _ := compose(e, Letter{Subject: "Секрет", Text: "текст", NoForward: true})
	if !strings.Contains(string(msg), "X-Go24-Restrict: do-not-forward") || !parseFull(bytes.NewReader(msg)).noForward {
		t.Fatalf("no restriction in\n%s", msg)
	}
	if msg, _ = compose(e, Letter{Subject: "Обычное", Text: "текст"}); strings.Contains(string(msg), noForwardHeader) {
		t.Fatal("an ordinary letter is restricted")
	}
	for _, c := range []struct {
		m    Message
		want bool
	}{{Message{NoForward: true, FolderRole: "inbox"}, true}, {Message{NoForward: true}, true},
		{Message{NoForward: true, FolderRole: "sent"}, false}, {Message{NoForward: true, FolderRole: "drafts"}, false}, {Message{FolderRole: "inbox"}, false}} {
		if c.m.Locked() != c.want {
			t.Errorf("Locked(%+v) = %v", c.m, !c.want)
		}
	}
}

// TestNoForward runs against GreenMail and the test database (as TestComposeSend): Anna sends Bob a letter «Не
// пересылать» — her sent copy keeps the mark and is hers to forward; Bob's copy is restricted: no forward, no forward
// as an attachment, no rule's forward (quietly), no quick step's; his answer is restricted too.
func TestNoForward(t *testing.T) {
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
	const annaAddr, bobAddr = "nofwd-anna@go24.test", "nofwd-bob@go24.test"
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('nfa', 'Анна', $1) RETURNING id`, annaAddr).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('nfb', 'Борис', $1) RETURNING id`, bobAddr).Scan(&bob))
	s := NewStore(pool, "test-secret")
	boxA, err := s.Add(ctx, anna, Settings{Email: annaAddr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	boxB, err := s.Add(ctx, bob, Settings{Email: bobAddr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)

	id, err := s.SaveDraft(ctx, anna, Draft{MailboxID: boxA, To: bobAddr, Subject: "Секрет", HTML: "<p>Только тебе</p>", NoForward: true})
	must(err)
	if d, _ := s.Draft(ctx, anna, id); !d.NoForward {
		t.Fatal("the draft lost «Не пересылать»")
	}
	must(s.SendDraft(ctx, anna, id))
	letterOf := func(user, box, subject string) Message {
		t.Helper()
		var id string
		must(pool.QueryRow(ctx, `SELECT id::text FROM mail_messages WHERE mailbox_id::text = $1 AND subject = $2 ORDER BY sent_at DESC LIMIT 1`,
			box, subject).Scan(&id))
		m, err := s.Message(ctx, user, id)
		must(err)
		return m
	}
	if sent := letterOf(anna, boxA, "Секрет"); !sent.NoForward || sent.Locked() {
		t.Fatalf("the sent copy: no_forward %v, locked %v", sent.NoForward, sent.Locked())
	}
	must(s.Sync(ctx, boxB))
	got := letterOf(bob, boxB, "Секрет")
	if !got.NoForward || !got.Locked() {
		t.Fatalf("Bob's copy is not restricted: %+v", got)
	}

	for _, mode := range []string{"fwd", "fwdatt"} {
		if _, err := s.SaveDraft(ctx, bob, Draft{MailboxID: boxB, Mode: mode, ReplyTo: got.ID, To: "x@buyer.test"}); !errors.Is(err, ErrNoForward) {
			t.Fatalf("%s of a restricted letter: %v", mode, err)
		}
	}
	if err := s.sendOn(ctx, bob, got.ID, "forward", "x@buyer.test", ""); !errors.Is(err, ErrNoForward) {
		t.Fatalf("a quick step forwarded it: %v", err)
	}
	if err := s.ruleSend(ctx, bob, got.ID, "redirect", "x@buyer.test"); err != nil {
		t.Fatalf("a rule's forward of it: %v", err)
	}
	if got := inbox(t, host, "x@buyer.test"); len(got) != 0 {
		t.Fatalf("it went out: %q", got)
	}
	reply, err := s.SaveDraft(ctx, bob, Draft{MailboxID: boxB, Mode: "reply", ReplyTo: got.ID, To: annaAddr, Subject: "RE: Секрет"})
	must(err)
	if d, _ := s.Draft(ctx, bob, reply); !d.NoForward {
		t.Fatal("the answer is not «Не пересылать»")
	}
	// Anna forwards her own letter
	fwd, err := s.SaveDraft(ctx, anna, Draft{MailboxID: boxA, Mode: "fwd", ReplyTo: letterOf(anna, boxA, "Секрет").ID, To: "y@buyer.test"})
	must(err)
	must(s.DeleteDraft(ctx, anna, fwd))
}
