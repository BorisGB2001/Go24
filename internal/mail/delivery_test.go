package mail

import (
	"bytes"
	"context"
	"net/mail"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestDeliveryHeaders(t *testing.T) {
	for in, want := range map[string]string{"": "", "a@b.test": "a@b.test", "Отдел продаж <sales@b.test>; info@b.test": "Отдел продаж <sales@b.test>, info@b.test",
		`"Иванов, Пётр" <p@b.test>`: `"Иванов, Пётр" <p@b.test>`} {
		if got, err := repliesTo(in); err != nil || got != want {
			t.Errorf("repliesTo(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"not an address", strings.Repeat("a@b.test,", MaxRepliesTo+1)} {
		if _, err := repliesTo(bad); err == nil {
			t.Errorf("repliesTo(%q) is fine", bad)
		}
	}
	if got := repliesToOf("Anna <ANNA@b.test>", "anna@b.test"); got != "" {
		t.Errorf("Reply-To of the sender itself: %q", got)
	}
	if got := repliesToOf("=?utf-8?q?=D0=9F=D1=80=D0=BE=D0=B4=D0=B0=D0=B6=D0=B8?= <sales@b.test>", "anna@b.test"); got != "Продажи <sales@b.test>" {
		t.Errorf("Reply-To: %q", got)
	}
	exp := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)
	e := envelope{From: mail.Address{Address: "anna@go24.test"}, To: []*mail.Address{{Address: "bob@go24.test"}}, Host: "go24.test", Now: time.Now(),
		ReplyTo: []*mail.Address{{Name: "Продажи", Address: "sales@go24.test"}, {Address: "info@go24.test"}}}
	msg, _ := compose(e, Letter{Subject: "Акция", Text: "до понедельника", ExpiresAt: &exp})
	p := parseFull(bytes.NewReader(msg))
	if p.expires == nil || !p.expires.Equal(exp) || repliesToOf(p.replyTo, "anna@go24.test") != "Продажи <sales@go24.test>, info@go24.test" {
		t.Fatalf("expires %v, reply-to %q in\n%s", p.expires, p.replyTo, msg)
	}
	if p := parseFull(bytes.NewReader([]byte("Expires: Mon, 2 Nov 2026 18:00:00 +0000\r\nSubject: x\r\n\r\ntext"))); p.expires == nil || !p.expires.Equal(exp) {
		t.Fatalf("Expires: %v", p.expires)
	}
	past, future := time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
	if !(Message{ExpiresAt: &past}).Expired() || (Message{ExpiresAt: &future}).Expired() || (Message{}).Expired() {
		t.Fatal("Expired")
	}
}

// TestDelivery runs against GreenMail and the test database (as TestNoForward): Anna's letter directs replies to the
// sales address and expires in an hour — the draft, her sent copy and Bob's copy keep both; one already out of date
// is Expired.
func TestDelivery(t *testing.T) {
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
	const annaAddr, bobAddr = "dlv-anna@go24.test", "dlv-bob@go24.test"
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('dla', 'Анна', $1) RETURNING id`, annaAddr).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('dlb', 'Борис', $1) RETURNING id`, bobAddr).Scan(&bob))
	s := NewStore(pool, "test-secret")
	boxA, err := s.Add(ctx, anna, Settings{Email: annaAddr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	boxB, err := s.Add(ctx, bob, Settings{Email: bobAddr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)

	if _, err := s.SaveDraft(ctx, anna, Draft{MailboxID: boxA, To: bobAddr, Subject: "x", RepliesTo: "not an address"}); err == nil {
		t.Fatal("a draft with a wrong «Направлять ответы на»")
	}
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	id, err := s.SaveDraft(ctx, anna, Draft{MailboxID: boxA, To: bobAddr, Subject: "Акция", HTML: "<p>До вечера</p>",
		RepliesTo: "Продажи <dlv-sales@go24.test>", ExpiresAt: &exp})
	must(err)
	if d, _ := s.Draft(ctx, anna, id); d.RepliesTo != "Продажи <dlv-sales@go24.test>" || d.ExpiresAt == nil || !d.ExpiresAt.Equal(exp) {
		t.Fatalf("the draft: %q %v", d.RepliesTo, d.ExpiresAt)
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
	for _, m := range []Message{letterOf(anna, boxA, "Акция"), func() Message { must(s.Sync(ctx, boxB)); return letterOf(bob, boxB, "Акция") }()} {
		if m.RepliesTo != "Продажи <dlv-sales@go24.test>" || m.ExpiresAt == nil || !m.ExpiresAt.Equal(exp) || m.Expired() {
			t.Fatalf("%s: %q %v", m.FolderRole, m.RepliesTo, m.ExpiresAt)
		}
	}
	list, err := s.ListBy(ctx, bob, ListOpts{Folder: letterOf(bob, boxB, "Акция").FolderID, Conv: true})
	must(err)
	if len(list) != 1 || list[0].RepliesTo == "" || list[0].ExpiresAt == nil {
		t.Fatalf("the list: %+v", list)
	}
	must(pool.QueryRow(ctx, `UPDATE mail_messages SET expires_at = now() - interval '1 minute' WHERE mailbox_id::text = $1 RETURNING id::text`, boxB).Scan(&id))
	if m, _ := s.Message(ctx, bob, id); !m.Expired() {
		t.Fatal("not out of date")
	}
}
