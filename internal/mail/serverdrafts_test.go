package mail

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// serverDrafts: the subjects of the letters in the server's «Drafts» of a GreenMail address, and whether all of them
// are \Draft.
func serverDrafts(t *testing.T, host, who string) ([]string, bool) {
	t.Helper()
	c, err := dialIMAP(host, 3143, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if err := c.Login(who, "pw"); err != nil {
		t.Fatal(err)
	}
	box, err := c.Select("Drafts", true)
	if err != nil {
		t.Fatal(err)
	}
	if box.Messages == 0 {
		return nil, true
	}
	set := new(imap.SeqSet)
	set.AddRange(1, box.Messages)
	ch := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(set, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags}, ch) }()
	var out []string
	draft := true
	for m := range ch {
		out = append(out, m.Envelope.Subject)
		ok := false
		for _, f := range m.Flags {
			ok = ok || f == imap.DraftFlag
		}
		draft = draft && ok
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return out, draft
}

// TestServerDrafts runs against GreenMail and the test database (as TestMailbox): a go24 draft is put into the
// server's «Drafts» at the sync, changed — the copy replaced, sent or deleted — removed; it is not fetched back as
// a letter of its own, while a draft written in another program is; a mailbox with drafts on the server goes.
func TestServerDrafts(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna3', 'Анна', 'drafts@go24.test') RETURNING id`).Scan(&anna))
	s := NewStore(pool, "test-secret")
	c, err := dialIMAP(host, 3143, false)
	must(err)
	must(c.Login("drafts@go24.test", "pw"))
	must(c.Create("Drafts"))
	box, err := s.Add(ctx, anna, Settings{Email: "drafts@go24.test", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	f, err := s.folderByRole(ctx, box, "drafts")
	must(err)
	if f.Local || f.Name != "Drafts" {
		t.Fatalf("drafts folder = %+v", f)
	}
	local := func() int {
		t.Helper()
		list, err := s.Messages(ctx, anna, f.ID, "")
		must(err)
		return len(list)
	}
	check := func(want ...string) {
		t.Helper()
		got, draft := serverDrafts(t, host, "drafts@go24.test")
		if strings.Join(got, "|") != strings.Join(want, "|") || !draft {
			t.Fatalf("server drafts = %q (all \\Draft: %v), want %q", got, draft, want)
		}
	}

	id, err := s.SaveDraft(ctx, anna, Draft{MailboxID: box, To: "boss@buyer.test", BCC: "x@buyer.test", Subject: "Счёт 1", HTML: "<p>Счёт</p>"})
	must(err)
	must(s.Sync(ctx, box))
	check("Счёт 1")
	if n := local(); n != 1 {
		t.Fatalf("go24 drafts = %d: the server copy came back", n)
	}
	must(s.Sync(ctx, box)) // nothing changed: nothing put again
	check("Счёт 1")
	_, err = s.SaveDraft(ctx, anna, Draft{ID: id, MailboxID: box, To: "boss@buyer.test, not yet", Subject: "Счёт 2", HTML: "<p>Счёт</p>"})
	must(err)
	must(s.Sync(ctx, box))
	check("Счёт 2")

	// a draft from the phone is a letter of «Черновики»
	must(c.Append("Drafts", []string{imap.DraftFlag}, time.Now(), bytes.NewReader([]byte(
		"From: drafts@go24.test\r\nTo: a@buyer.test\r\nSubject: From phone\r\nMessage-ID: <phone-1@go24.test>\r\n\r\nПривет\r\n"))))
	must(s.Sync(ctx, box))
	if n := local(); n != 2 {
		t.Fatalf("go24 drafts = %d, want the phone's too", n)
	}
	// opened, it is go24's draft; its server copy is replaced by go24's, once
	var phone string
	must(pool.QueryRow(ctx, `SELECT id::text FROM mail_messages WHERE message_id = 'phone-1@go24.test'`).Scan(&phone))
	if ok, err := s.AdoptDraft(ctx, anna, phone); !ok || err != nil {
		t.Fatalf("the phone's draft not taken: %v %v", ok, err)
	}
	if ok, err := s.AdoptDraft(ctx, anna, phone); ok || err != nil {
		t.Fatalf("taken twice: %v %v", ok, err)
	}
	if pd, err := s.Draft(ctx, anna, phone); err != nil || pd.To != "a@buyer.test" || !strings.Contains(pd.HTML, "Привет") {
		t.Fatalf("the phone's draft = %+v %v", pd, err)
	}
	must(s.Sync(ctx, box))
	check("Счёт 2", "From phone")
	if n := local(); n != 2 {
		t.Fatalf("go24 drafts = %d after the phone's was taken", n)
	}

	// sent: its copy goes; deleted: its copy goes
	_, err = s.SaveDraft(ctx, anna, Draft{ID: id, MailboxID: box, To: "boss@buyer.test", Subject: "Счёт 2", HTML: "<p>Счёт</p>"})
	must(err)
	must(s.SendDraft(ctx, anna, id))
	gone, err := s.SaveDraft(ctx, anna, Draft{MailboxID: box, Subject: "Ненужный"})
	must(err)
	must(s.Sync(ctx, box))
	check("From phone", "Ненужный")
	must(s.DeleteDraft(ctx, anna, gone))
	must(s.Sync(ctx, box))
	check("From phone")
	if n := local(); n != 1 {
		t.Fatalf("go24 drafts = %d after send and delete", n)
	}
	var drops int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_draft_drops`).Scan(&drops))
	if drops != 0 {
		t.Fatalf("copies left to remove: %d", drops)
	}

	// the mailbox goes with a draft on the server
	_, err = s.SaveDraft(ctx, anna, Draft{MailboxID: box, Subject: "Последний"})
	must(err)
	must(s.Sync(ctx, box))
	must(s.Remove(ctx, anna, box))
	_ = c.Logout()
}
