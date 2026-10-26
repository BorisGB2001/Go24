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

// TestAnnounceBlock runs against GreenMail and the test database like TestMailbox: a blocked domain's new letter
// goes to junk before anything else; a new letter of «Отсортированные» is a notification of the bell, one of
// «Другие» is not (unless the user wants all of them or a rule says «Уведомить»); the counter; blocking the sender of
// a letter takes their letters to junk; a safe address is not blocked by its domain; the source comes from the server.
func TestAnnounceBlock(t *testing.T) {
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
	addr := fmt.Sprintf("ann%d@go24.test", time.Now().UnixNano())
	var ann string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', $1) RETURNING id`, addr).Scan(&ann))
	s := NewStore(pool, "test-secret")
	n := 0
	send := func(from, subject string) {
		n++
		must(smtp.SendMail(host+":3025", nil, from, []string{addr}, []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMessage-ID: <m%d.%d@x.test>\r\n\r\nText %s\r\n",
			from, addr, subject, n, time.Now().UnixNano(), subject))))
	}
	send("boss@client.test", "First")
	id, err := s.Add(ctx, ann, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, id)) // the first fetch announces nothing
	bell := func() []string {
		rows, err := pool.Query(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'mail' ORDER BY id`, ann)
		must(err)
		list, err := pgxCollectStrings(rows)
		must(err)
		return list
	}
	if got := bell(); len(got) != 0 {
		t.Fatalf("first fetch announced %q", got)
	}
	if _, err := s.AddSender(ctx, ann, SenderBlocked, "spam.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSender(ctx, ann, SenderBlocked, addr); err == nil {
		t.Error("blocked oneself")
	}
	if _, err := s.SaveRule(ctx, ann, Rule{Name: "shop", Enabled: true, If: Cond{From: "shop.test"}, Do: Do{Notify: true}}); err != nil {
		t.Fatal(err)
	}
	send("boss@client.test", "Second")
	send("x@mail.spam.test", "Spam")
	send("noreply@other.test", "Digest")
	send("noreply@shop.test", "Sale")
	must(s.Sync(ctx, id))
	got := bell()
	if len(got) != 2 || !strings.Contains(got[0], "Second") || !strings.Contains(got[1], "Sale") {
		t.Fatalf("bell = %q", got)
	}
	junk, err := s.folderByRole(ctx, id, "junk")
	must(err)
	inJunk, err := s.List(ctx, ann, junk.ID, "", false)
	must(err)
	if len(inJunk) != 1 || inJunk[0].Subject != "Spam" {
		t.Fatalf("junk = %+v", inJunk)
	}
	if u, err := s.Unread(ctx, ann); err != nil || u != 2 { // First, Second; Digest and Sale are «Другие»
		t.Fatalf("unread = %d, %v", u, err)
	}

	inbox, err := s.folderByRole(ctx, id, "inbox")
	must(err)
	letters, err := s.List(ctx, ann, inbox.ID, "", false)
	must(err)
	var second, digest string
	for _, l := range letters {
		switch l.Subject {
		case "Second":
			second = l.ID
		case "Digest":
			digest = l.ID
		}
	}
	src, err := s.Source(ctx, ann, second)
	if err != nil || !strings.Contains(string(src), "Subject: Second") || !strings.Contains(string(src), "Text Second") {
		t.Fatalf("source = %q, %v", src, err)
	}
	if _, err := s.Source(ctx, "00000000-0000-0000-0000-000000000000", second); err == nil {
		t.Error("someone else's source")
	}

	// blocking the sender of «Second» takes «First» along; a safe address of a blocked domain stays
	blocked, moved, err := s.BlockSenders(ctx, ann, []string{second}, false)
	if err != nil || len(blocked) != 1 || blocked[0] != "boss@client.test" || moved != 2 {
		t.Fatalf("block = %v %d %v", blocked, moved, err)
	}
	if _, err := s.TrustSenders(ctx, ann, []string{digest}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSender(ctx, ann, SenderBlocked, "@other.test"); err != nil {
		t.Fatal(err)
	}
	if k, _ := s.SenderKind(ctx, ann, "noreply@other.test"); k != SenderSafe {
		t.Errorf("kind = %q", k)
	}
	if k, _ := s.SenderKind(ctx, ann, "else@sub.other.test"); k != SenderBlocked {
		t.Errorf("kind of a subdomain = %q", k)
	}
	left, _ := s.List(ctx, ann, inbox.ID, "", false)
	if len(left) != 2 { // Digest (safe) and Sale
		t.Fatalf("inbox = %+v", left)
	}
	must(s.SaveNotifyPrefs(ctx, ann, NotifyPrefs{Mode: "all", Sound: false}))
	if p, _ := s.NotifyPrefs(ctx, ann); p.Mode != "all" || p.Sound || p.Desktop {
		t.Errorf("prefs = %+v", p)
	}
	if s.SaveNotifyPrefs(ctx, ann, NotifyPrefs{Mode: "loud"}) == nil {
		t.Error("a bad mode")
	}
	list, _ := s.Senders(ctx, ann, SenderBlocked)
	if len(list) != 3 || !list[0].Domain() {
		t.Errorf("blocked = %+v", list)
	}
	if s.DropSender(ctx, ann, "spam.test") == nil { // not as kept
		t.Error("dropped a sender not kept so")
	}
	must(s.DropSender(ctx, ann, "@spam.test")) // as kept
}
