package mail

import (
	"context"
	"errors"
	"fmt"
	"net/smtp"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestStorage runs against GreenMail and the test database like TestActions: «Хранилище» counts the folders on the
// server; «Очистить» older than 6 months moves the old letters to «Удалённые» but the pinned one, then «Очистить»
// «Удалённые» deletes them for good; «Черновики» are not emptied; nobody else sees or empties anything.
func TestParseQuota(t *testing.T) {
	used, limit, ok := parseQuota([]interface{}{"MESSAGE", "5", "1000", "storage", "512", "10240"})
	if !ok || used != 512*1024 || limit != 10240*1024 {
		t.Fatalf("quota = %d %d %v", used, limit, ok)
	}
	if _, _, ok := parseQuota([]interface{}{"MESSAGE", "5", "1000"}); ok {
		t.Fatal("a quota of messages taken for storage")
	}
	if st := (Storage{Used: 3 << 20, Limit: 2 << 20}); st.Percent() != 100 {
		t.Fatalf("percent = %d", st.Percent())
	}
}

func TestStorage(t *testing.T) {
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
	addr := fmt.Sprintf("bob%d@go24.test", time.Now().UnixNano())
	var bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', $1) RETURNING id`, addr).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	old := time.Now().AddDate(-1, 0, 0).Format(time.RFC1123Z)
	for i, date := range []string{old, old, time.Now().Format(time.RFC1123Z)} {
		raw := fmt.Sprintf("From: boss@client.test\r\nTo: %s\r\nSubject: Letter %d\r\nMessage-ID: <s%d@client.test>\r\nDate: %s\r\n\r\n%s\r\n",
			addr, i, i, date, "Some text of the letter")
		must(smtp.SendMail(host+":3025", nil, "boss@client.test", []string{addr}, []byte(raw)))
	}
	id, err := s.Add(ctx, bob, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, id))
	inbox, err := s.folderByRole(ctx, id, "inbox")
	must(err)

	st, err := s.Storage(ctx, bob, id)
	must(err)
	if len(st.Folders) == 0 || st.Folders[0].ID != inbox.ID || st.Folders[0].Letters != 3 || st.Folders[0].Size == 0 || st.Total < st.Folders[0].Size ||
		st.Used == 0 || (st.Limit > 0 && st.Percent() > 100) {
		t.Fatalf("storage = %+v", st)
	}
	if _, err := s.Storage(ctx, eve, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else counted the mailbox: %v", err)
	}

	letters, err := s.List(ctx, bob, inbox.ID, "", false)
	must(err)
	if len(letters) != 3 {
		t.Fatalf("letters = %+v", letters)
	}
	var pinned string
	for _, l := range letters {
		if l.Subject == "Letter 0" {
			pinned = l.ID
		}
	}
	must(s.Act(ctx, bob, []string{pinned}, "pin", ""))
	if _, err := s.Clean(ctx, eve, inbox.ID, 6); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else emptied the folder: %v", err)
	}
	if _, err := s.Clean(ctx, bob, inbox.ID, 5); err == nil {
		t.Fatal("cleaned older than 5 months")
	}
	n, err := s.Clean(ctx, bob, inbox.ID, 6)
	must(err)
	if n != 1 {
		t.Fatalf("cleaned %d letters", n)
	}
	left, _ := s.List(ctx, bob, inbox.ID, "", false)
	if len(left) != 2 || (left[0].Subject != "Letter 2" && left[1].Subject != "Letter 2") {
		t.Fatalf("left = %+v", left)
	}
	trash, err := s.folderByRole(ctx, id, "trash")
	must(err)
	tr, _ := s.List(ctx, bob, trash.ID, "", false)
	if len(tr) != 1 || tr[0].Subject != "Letter 1" {
		t.Fatalf("trash = %+v", tr)
	}
	st, err = s.Storage(ctx, bob, id)
	must(err)
	sizes := map[string]FolderSize{}
	for _, f := range st.Folders {
		sizes[f.ID] = f
	}
	if sizes[inbox.ID].Letters != 2 || sizes[trash.ID].Letters != 1 {
		t.Fatalf("storage after cleaning = %+v", st)
	}

	// «Удалённые» all: for good
	n, err = s.Clean(ctx, bob, trash.ID, 0)
	must(err)
	if tr, _ = s.List(ctx, bob, trash.ID, "", false); n != 1 || len(tr) != 0 {
		t.Fatalf("trash cleaned %d, still %+v", n, tr)
	}
	if st, _ = s.Storage(ctx, bob, id); func() int {
		for _, f := range st.Folders {
			if f.ID == trash.ID {
				return f.Letters
			}
		}
		return -1
	}() != 0 {
		t.Fatalf("trash on the server = %+v", st)
	}
	var drafts string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, selectable, local) VALUES ($1, 'Черновики', 'drafts', true, true)
		RETURNING id`, id).Scan(&drafts))
	if _, err := s.Clean(ctx, bob, drafts, 0); err == nil {
		t.Fatal("drafts cleaned")
	}
	// all of the inbox: the pinned letter stays
	n, err = s.Clean(ctx, bob, inbox.ID, 0)
	must(err)
	if left, _ = s.List(ctx, bob, inbox.ID, "", false); n != 1 || len(left) != 1 || left[0].ID != pinned {
		t.Fatalf("cleaned %d, left %+v", n, left)
	}
}
