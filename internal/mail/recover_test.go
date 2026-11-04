package mail

import (
	"context"
	"fmt"
	"net/smtp"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRecover against the test database and GreenMail: a letter deleted for good in «Удалённые» is kept, found by
// its subject, put back to «Удалённые» on the server; «Удалить навсегда» forgets it; others see none.
func TestRecover(t *testing.T) {
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
	addr := fmt.Sprintf("rec%d@go24.test", time.Now().UnixNano())
	var bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', $1) RETURNING id`, addr).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	for _, subj := range []string{"Invoice 7", "Party"} {
		must(smtp.SendMail(host+":3025", nil, "boss@client.test", []string{addr}, []byte("From: Boss <boss@client.test>\r\nTo: "+addr+
			"\r\nSubject: "+subj+"\r\nDate: Mon, 12 Oct 2026 10:00:00 +0200\r\n\r\nText of "+subj+"\r\n")))
	}
	box, err := s.Add(ctx, bob, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	inbox, err := s.folderByRole(ctx, box, "inbox")
	must(err)
	in := mustLetters(t, s, bob, inbox.ID)
	if len(in) != 2 {
		t.Fatalf("inbox = %d", len(in))
	}
	must(s.Act(ctx, bob, ids(in), "delete", "")) // to «Удалённые»: nothing kept yet
	if list, _ := s.RecoverableOf(ctx, bob, box, ""); len(list) != 0 {
		t.Fatalf("kept on moving to «Удалённые»: %+v", list)
	}
	trash, err := s.folderByRole(ctx, box, "trash")
	must(err)
	tr := mustLetters(t, s, bob, trash.ID)
	if len(tr) != 2 {
		t.Fatalf("trash = %d", len(tr))
	}
	must(s.Act(ctx, bob, ids(tr)[:1], "delete", "")) // for good: kept
	must(s.Empty(ctx, bob, trash.ID))
	list, err := s.RecoverableOf(ctx, bob, box, "")
	must(err)
	if len(list) != 2 || list[0].Role != "trash" || list[0].FromAddr != "boss@client.test" || list[0].Size == 0 {
		t.Fatalf("recoverable = %+v", list)
	}
	if other, _ := s.RecoverableOf(ctx, eve, box, ""); len(other) != 0 {
		t.Fatalf("eve sees bob's: %+v", other)
	}
	found, err := s.RecoverableOf(ctx, bob, box, "invoice")
	must(err)
	if len(found) != 1 || found[0].Subject != "Invoice 7" {
		t.Fatalf("search = %+v", found)
	}
	if n, err := s.Recover(ctx, eve, []string{found[0].ID}); n != 0 || err == nil {
		t.Fatalf("eve recovered bob's: %d %v", n, err)
	}
	n, err := s.Recover(ctx, bob, []string{found[0].ID})
	must(err)
	if n != 1 {
		t.Fatalf("recovered = %d", n)
	}
	tr = mustLetters(t, s, bob, trash.ID)
	if len(tr) != 1 {
		t.Fatalf("trash after restore = %d", len(tr))
	}
	m, err := s.message(ctx, bob, tr[0].id)
	must(err)
	if m.Subject != "Invoice 7" || m.FromAddr != "boss@client.test" {
		t.Fatalf("restored = %+v", m)
	}
	rest, _ := s.RecoverableOf(ctx, bob, box, "")
	if len(rest) != 1 || rest[0].Subject != "Party" {
		t.Fatalf("left = %+v", rest)
	}
	if n, err := s.ForgetRecoverable(ctx, eve, []string{rest[0].ID}); n != 0 || err != nil {
		t.Fatalf("eve forgot bob's: %d %v", n, err)
	}
	if n, err := s.ForgetRecoverable(ctx, bob, []string{rest[0].ID}); n != 1 || err != nil {
		t.Fatalf("forget = %d %v", n, err)
	}
	// those over RecoverDays go
	must(s.Act(ctx, bob, ids(tr), "delete", ""))
	_, err = pool.Exec(ctx, `UPDATE mail_recoverable SET deleted_at = now() - interval '31 days'`)
	must(err)
	if list, _ := s.RecoverableOf(ctx, bob, box, ""); len(list) != 0 {
		t.Fatalf("old shown: %+v", list)
	}
	must(s.ExpireRecoverable(ctx))
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_recoverable`).Scan(&left))
	if left != 0 {
		t.Fatalf("expired kept: %d", left)
	}
}
