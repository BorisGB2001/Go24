package mail

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestSendAs runs against GreenMail and the test database (as TestForwarding): «Адреса отправителя» — the sender
// name and the aliases are checked and kept, a letter and a draft go from the alias chosen with its name (the SMTP
// envelope stays the mailbox's), an address the mailbox does not send from is refused.
func TestSendAs(t *testing.T) {
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
	addr, sales, to := fmt.Sprintf("sa%d@go24.test", n), fmt.Sprintf("sales%d@go24.test", n), fmt.Sprintf("client%d@client.test", n)
	var rob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('rob', 'Роб', $1) RETURNING id`, addr).Scan(&rob))
	s := NewStore(pool, "test-secret")
	box, err := s.Add(ctx, rob, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)

	if b, err := s.MailboxSenders(ctx, rob, box); err != nil || b != (BoxSenders{}) {
		t.Fatalf("the default = %+v %v", b, err)
	}
	for _, bad := range []BoxSenders{{SendAs: "not an address"}, {SendAs: strings.ToUpper(addr)}, {SendAs: sales + ", " + sales},
		{SendAs: strings.Repeat("a@b.test,", MaxSendAs+1)}} {
		if err := s.SetMailboxSenders(ctx, rob, box, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := s.SetMailboxSenders(ctx, "00000000-0000-0000-0000-000000000000", box, BoxSenders{SendAs: sales}); err == nil {
		t.Fatal("another user's mailbox")
	}
	must(s.SetMailboxSenders(ctx, rob, box, BoxSenders{Name: " Роб Петров ", SendAs: "Отдел продаж <" + sales + ">; info" + sales,
		Default: strings.ToUpper(sales)}))
	if b, _ := s.MailboxSenders(ctx, rob, box); b.Name != "Роб Петров" || b.SendAs != "Отдел продаж <"+sales+">, info"+sales || b.Default != sales {
		t.Fatalf("kept = %+v", b)
	}
	froms, err := s.FromAddresses(ctx, rob)
	must(err)
	if len(froms) != 3 || froms[0] != (FromAddress{box, addr, "Роб Петров", false}) || froms[1] != (FromAddress{box, sales, "Отдел продаж", true}) ||
		froms[2] != (FromAddress{box, "info" + sales, "Роб Петров", false}) {
		t.Fatalf("from addresses = %+v", froms)
	}

	// a letter from the alias: its name in From, the copy in «Отправленные» is from it too
	must(s.SendLetter(ctx, rob, box, Letter{From: sales, To: to, Subject: "Alias", Text: "text"}))
	got := inbox(t, host, to)
	if len(got) != 1 || !strings.Contains(got[0], "From: =?utf-8?") || !strings.Contains(got[0], "<"+sales+">") {
		t.Fatalf("sent = %q", got)
	}
	var from string
	must(pool.QueryRow(ctx, `SELECT x.from_addr FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE f.role = 'sent' AND x.subject = 'Alias'`).Scan(&from))
	if from != sales {
		t.Fatalf("the sent copy is from %q", from)
	}
	if err := s.SendLetter(ctx, rob, box, Letter{From: "other@go24.test", To: to, Subject: "No", Text: "text"}); err == nil {
		t.Fatal("sent from an address not the mailbox's")
	}

	// a draft keeps the address chosen and goes from it; the mailbox's own by default
	id, err := s.SaveDraft(ctx, rob, Draft{MailboxID: box, From: "INFO" + strings.ToUpper(sales), To: to, Subject: "Draft", HTML: "<p>x</p>"})
	must(err)
	if d, err := s.Draft(ctx, rob, id); err != nil || d.From != "info"+sales {
		t.Fatalf("draft = %+v %v", d, err)
	}
	must(s.SendDraft(ctx, rob, id))
	if got = inbox(t, host, to); len(got) != 2 || !strings.Contains(got[1], "<info"+sales+">") {
		t.Fatalf("sent = %q", got)
	}
	if _, err := s.SaveDraft(ctx, rob, Draft{MailboxID: box, From: "other@go24.test", To: to}); err == nil {
		t.Fatal("a draft from an address not the mailbox's")
	}
	id, err = s.SaveDraft(ctx, rob, Draft{MailboxID: box, To: to, Subject: "Own", HTML: "<p>x</p>"})
	must(err)
	if d, _ := s.Draft(ctx, rob, id); d.From != addr {
		t.Fatalf("own draft from %q", d.From)
	}

	// the alias taken away: new letters are from the mailbox's own again
	must(s.SetMailboxSenders(ctx, rob, box, BoxSenders{Default: sales}))
	if b, _ := s.MailboxSenders(ctx, rob, box); b.Default != "" || b.SendAs != "" {
		t.Fatalf("after = %+v", b)
	}
}
