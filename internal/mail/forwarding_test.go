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

// TestForwarding runs against GreenMail and the test database (as TestRulesArrive): «Пересылка» sends each new
// inbox letter on as it is, keeping a copy or putting the letter into «Удалённые» past the rules; a letter already
// forwarded automatically is not sent on again; the addresses are checked; off — nothing is forwarded.
func TestForwarding(t *testing.T) {
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
	addr, boss, fwd := fmt.Sprintf("fwr%d@go24.test", n), fmt.Sprintf("boss%d@client.test", n), fmt.Sprintf("away%d@other.test", n)
	var rob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('rob', 'Роб', $1) RETURNING id`, addr).Scan(&rob))
	s := NewStore(pool, "test-secret")
	var logged []error
	s.Log = func(what, id string, err error) { logged = append(logged, fmt.Errorf("%s: %w", what, err)) }
	send := func(subject, extra string) {
		must(smtp.SendMail(host+":3025", nil, boss, []string{addr}, []byte("From: "+boss+"\r\nTo: "+addr+"\r\nSubject: "+subject+
			"\r\n"+extra+"Message-ID: <"+strings.ReplaceAll(subject, " ", "")+fmt.Sprint(n)+"@client.test>\r\n\r\ntext\r\n")))
	}
	send("Old", "") // the first fetch takes what is there: nothing is forwarded
	box, err := s.Add(ctx, rob, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	in, err := s.folderByRole(ctx, box, "inbox")
	must(err)
	_, err = s.SaveRule(ctx, rob, Rule{Name: "Pin", Enabled: true, Do: Do{Pin: true}})
	must(err)

	if b, err := s.MailboxForward(ctx, rob, box); err != nil || b.On || !b.Keep {
		t.Fatalf("the default = %+v %v", b, err)
	}
	for _, bad := range []BoxForward{{On: true}, {On: true, To: "not an address"}, {On: true, To: strings.ToUpper(addr)},
		{On: true, To: strings.Repeat("a@b.test,", MaxForward+1)}} {
		if err := s.SetMailboxForward(ctx, rob, box, bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	if err := s.SetMailboxForward(ctx, "00000000-0000-0000-0000-000000000000", box, BoxForward{To: fwd}); err == nil {
		t.Fatal("another user's mailbox")
	}

	// a copy kept: the letter is sent on as it is and stays, the rules take it; one forwarded already is not sent on
	must(s.SetMailboxForward(ctx, rob, box, BoxForward{On: true, To: fwd, Keep: true}))
	send("Kept", "")
	send("Looped", "Auto-Submitted: auto-forwarded\r\n")
	must(s.Sync(ctx, box))
	got := inbox(t, host, fwd)
	if len(got) != 1 || !strings.Contains(got[0], "Subject: Kept") || !strings.Contains(got[0], "Resent-From: <"+addr+">") ||
		!strings.Contains(got[0], "Reply-To: <"+boss+">") || !strings.Contains(got[0], "Auto-Submitted: auto-forwarded") {
		t.Fatalf("forwarded = %q", got)
	}
	list, err := s.List(ctx, rob, in.ID, "", false)
	must(err)
	if len(list) != 3 || !list[0].Pinned || !list[1].Pinned || list[2].Pinned {
		t.Fatalf("inbox = %+v", list)
	}

	// no copy: the letter goes to «Удалённые», the rules do not see it
	must(s.SetMailboxForward(ctx, rob, box, BoxForward{On: true, To: fwd}))
	send("Gone", "")
	must(s.Sync(ctx, box))
	if got = inbox(t, host, fwd); len(got) != 2 || !strings.Contains(got[1], "Subject: Gone") {
		t.Fatalf("forwarded = %q", got)
	}
	trash, err := s.folderByRole(ctx, box, "trash") // made on the server by the first deletion
	must(err)
	if gone, _ := s.List(ctx, rob, trash.ID, "", false); len(gone) != 1 || gone[0].Subject != "Gone" || gone[0].Pinned {
		t.Fatalf("trash = %+v", gone)
	}
	if list, _ = s.List(ctx, rob, in.ID, "", false); len(list) != 3 {
		t.Fatalf("inbox = %+v", list)
	}

	// off: the addresses are kept, nothing is forwarded
	must(s.SetMailboxForward(ctx, rob, box, BoxForward{To: fwd, Keep: true}))
	send("Stays", "")
	must(s.Sync(ctx, box))
	if got = inbox(t, host, fwd); len(got) != 2 {
		t.Fatalf("forwarded while off = %q", got)
	}
	if b, _ := s.MailboxForward(ctx, rob, box); b.On || b.To != fwd {
		t.Fatalf("off = %+v", b)
	}
	if len(logged) > 0 {
		t.Fatalf("logged: %v", logged)
	}
}
