package mail

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestImport runs against GreenMail and the test database (as TestMailbox): files .eml and .msg put into a folder
// are its letters, read, with their own dates; a file that is no letter is skipped; the import is no new mail
// (nothing new in «Отсортированные»: no notification); go24's own folder takes no import.
func TestImport(t *testing.T) {
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
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna-imp', 'Анна', 'import@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr-imp', 'Пётр', 'petr-imp@go24.test') RETURNING id`).Scan(&petr))
	s := NewStore(pool, "test-secret")
	box, err := s.Add(ctx, anna, Settings{Email: "import@go24.test", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	inbox, err := s.folderByRole(ctx, box, "inbox")
	must(err)
	archive, err := s.CreateFolder(ctx, anna, box, "", "Архив 2025")
	must(err)

	eml := "From: Клиент <client@buyer.test>\nTo: import@go24.test\nSubject: Old order\nDate: Mon, 06 Jan 2025 10:00:00 +0000\nMessage-ID: <old-1@buyer.test>\n\nЗаказ\n"
	msg := writeCFB(mapi{str: map[uint16]string{prSubject: "Из Outlook", prSenderName: "Борис", prSenderSMTP: "boris@buyer.test", prBody: "Текст"},
		fixed: map[uint32]uint64{prSubmitTime<<16 | 0x0040: toFileTime(time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC))}}.node("Root Entry", 32))
	res, err := s.Import(ctx, anna, archive, []ImportFile{{"order.eml", []byte(eml)}, {"outlook.msg", msg}, {"notes.txt", []byte("buy milk")}})
	must(err)
	if res.Done != 2 || len(res.Skipped) != 1 || res.Skipped[0] != "notes.txt" {
		t.Fatalf("import = %+v", res)
	}
	list, err := s.Messages(ctx, anna, archive, "")
	must(err)
	got := map[string]Message{}
	for _, m := range list {
		got[m.Subject] = m
	}
	if o, m := got["Old order"], got["Из Outlook"]; len(list) != 2 || !o.Seen || !m.Seen || o.FromAddr != "client@buyer.test" || m.FromAddr != "boris@buyer.test" ||
		!o.SentAt.Equal(time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)) || !m.SentAt.Equal(time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("letters = %+v", list)
	}

	// into the inbox: a letter of it, not new mail
	res, err = s.Import(ctx, anna, inbox.ID, []ImportFile{{"again.eml", []byte(eml)}})
	must(err)
	var unseen int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_messages WHERE folder_id = $1 AND NOT seen`, inbox.ID).Scan(&unseen))
	if res.Done != 1 || unseen != 0 {
		t.Fatalf("inbox import = %+v, unseen %d", res, unseen)
	}

	// another's folder, go24's own folder, too many files
	if _, err := s.Import(ctx, petr, archive, []ImportFile{{"order.eml", []byte(eml)}}); err == nil {
		t.Fatal("imported into another's folder")
	}
	var local string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'Local', '', true) RETURNING id::text`, box).Scan(&local))
	if _, err := s.Import(ctx, anna, local, []ImportFile{{"order.eml", []byte(eml)}}); err == nil {
		t.Fatal("imported into go24's own folder")
	}
	if _, err := s.Import(ctx, anna, archive, make([]ImportFile, MaxImportFiles+1)); err == nil {
		t.Fatal("too many files taken")
	}
}
