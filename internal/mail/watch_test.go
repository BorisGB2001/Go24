package mail

import (
	"context"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestWatch runs against GreenMail and the test database (as TestMailbox): with the inbox watched (IDLE) a letter
// that comes is in go24 and announced without a sync, one read on the phone is read in go24 and the open pages hear
// of it; the watch ends with its context.
func TestWatch(t *testing.T) {
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
	send := func(subject string) {
		t.Helper()
		must(smtp.SendMail(host+":3025", nil, "boss@buyer.test", []string{"watch@go24.test"},
			[]byte("From: boss@buyer.test\r\nTo: watch@go24.test\r\nSubject: "+subject+"\r\n\r\nText\r\n")))
	}
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna4', 'Анна', 'watch@go24.test') RETURNING id`).Scan(&anna))
	s := NewStore(pool, "test-secret")
	send("First")
	box, err := s.Add(ctx, anna, Settings{Email: "watch@go24.test", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))

	listen, err := pool.Acquire(ctx)
	must(err)
	defer listen.Release()
	_, err = listen.Exec(ctx, `LISTEN go24_notify`)
	must(err)
	woken := func(what string) string { // the next wake of anna's pages
		t.Helper()
		wctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		for {
			n, err := listen.Conn().WaitForNotification(wctx)
			if err != nil {
				t.Fatalf("%s: the pages were not woken: %v", what, err)
			}
			if strings.HasPrefix(n.Payload, "m"+anna+":") {
				return strings.TrimPrefix(n.Payload, "m"+anna+":")
			}
		}
	}
	unread := func() int {
		t.Helper()
		n, err := s.Unread(ctx, anna)
		must(err)
		return n
	}

	watchRecheck = 5 * time.Second // GreenMail does not tell an idling session of flags set in another
	defer func() { watchRecheck = time.Minute }()
	wctx, stop := context.WithCancel(ctx)
	ended := make(chan struct{})
	go func() { s.Watch(wctx); close(ended) }()
	time.Sleep(3 * time.Second) // the watch is idling
	send("Second")
	if p := woken("a new letter"); !strings.Contains(p, "Second") {
		t.Fatalf("the news = %s", p)
	}
	if n := unread(); n != 2 {
		t.Fatalf("unread = %d, want 2", n)
	}

	// read on the phone
	c, err := dialIMAP(host, 3143, false)
	must(err)
	defer c.Logout()
	must(c.Login("watch@go24.test", "pw"))
	_, err = c.Select("INBOX", false)
	must(err)
	all := new(imap.SeqSet)
	all.AddRange(1, 0)
	must(c.Store(all, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.SeenFlag}, nil))
	if p := woken("read on the phone"); p != "{}" {
		t.Fatalf("the news = %s", p)
	}
	if n := unread(); n != 0 {
		t.Fatalf("unread = %d after reading on the phone", n)
	}

	stop()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the watch did not end")
	}
}
