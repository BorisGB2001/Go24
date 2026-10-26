package mail

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/pst"
)

func TestMboxrd(t *testing.T) {
	letter := "From: a@x.ru\r\nSubject: s\r\n\r\nFrom here\r\n>From there\r\n>>From  far\r\nFromage\r\n no From\r\nend"
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	m := &imap.Message{InternalDate: time.Date(2026, 10, 5, 9, 3, 7, 0, time.UTC),
		Envelope: &imap.Envelope{From: []*imap.Address{{MailboxName: "a", HostName: "x.ru"}}}}
	if err := writeMboxrd(w, m, strings.NewReader(letter)); err != nil {
		t.Fatal(err)
	}
	if err := writeMboxrd(w, &imap.Message{InternalDate: m.InternalDate}, strings.NewReader("Subject: 2\n\nx\n")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	want := "From a@x.ru Mon Oct  5 09:03:07 2026\nFrom: a@x.ru\nSubject: s\n\n>From here\n>>From there\n>>>From  far\nFromage\n no From\nend\n\n" +
		"From MAILER-DAEMON Mon Oct  5 09:03:07 2026\nSubject: 2\n\nx\n\n"
	if b.String() != want {
		t.Fatalf("mbox:\n%q\nwant\n%q", b.String(), want)
	}
}

func TestMboxrdLongLine(t *testing.T) {
	long := strings.Repeat("a", 70<<10) + "From x" // the «From » is inside the line, not at its start
	var b bytes.Buffer
	w := bufio.NewWriter(&b)
	if err := mboxrdBody(w, strings.NewReader(long+"\r\nFrom y")); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if want := long + "\n>From y\n\n"; b.String() != want {
		t.Fatalf("long line: %d bytes, tail %q", b.Len(), b.String()[b.Len()-20:])
	}
}

func TestExportPath(t *testing.T) {
	for _, c := range []struct {
		f    Folder
		want string
	}{
		{Folder{Name: "INBOX"}, "INBOX"},
		{Folder{Name: "INBOX/Счета/2026", Delim: "/"}, "INBOX/Счета/2026"},
		{Folder{Name: "Работа.Клиенты: А/Б", Delim: "."}, "Работа/Клиенты_ А_Б"},
		{Folder{Name: "..", Delim: "/"}, "_"},
	} {
		if got := exportPath(c.f); got != c.want {
			t.Errorf("exportPath(%q) = %q, want %q", c.f.Name, got, c.want)
		}
	}
}

func TestExport(t *testing.T) {
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
	var ira, oleg string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ira-x', 'Ира', 'ira.x@go24.test') RETURNING id`).Scan(&ira))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('oleg-x', 'Олег', 'oleg.x@go24.test') RETURNING id`).Scan(&oleg))
	s := NewStore(pool, "test-secret")
	addr := fmt.Sprintf("ira.%d@go24.test", time.Now().UnixNano())
	box, err := s.Add(ctx, ira, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	inbox, err := s.folderByRole(ctx, box, "inbox")
	must(err)
	sub, err := s.CreateFolder(ctx, ira, box, inbox.ID, "Счета")
	must(err)
	letter := func(subject, date, body string) ImportFile {
		return ImportFile{Name: subject + ".eml", Data: []byte("From: Клиент <c@x.ru>\r\nTo: " + addr + "\r\nSubject: " + subject +
			"\r\nDate: " + date + "\r\nMessage-ID: <" + subject + "@x.ru>\r\n\r\n" + body + "\r\n")}
	}
	_, err = s.Import(ctx, ira, inbox.ID, []ImportFile{letter("old", "Mon, 05 Jan 2026 10:00:00 +0000", "давно"),
		letter("new", "Mon, 05 Oct 2026 10:00:00 +0000", "From the start\r\nтекст")})
	must(err)
	_, err = s.Import(ctx, ira, sub, []ImportFile{letter("bill", "Tue, 06 Oct 2026 10:00:00 +0000", "счёт")})
	must(err)

	if _, err := s.AddExport(ctx, oleg, NewExport{MailboxID: box, Format: "mbox"}); err == nil {
		t.Fatal("someone else's mailbox exported")
	}
	if _, err := s.AddExport(ctx, ira, NewExport{MailboxID: box, Format: "zip"}); err == nil {
		t.Fatal("unknown format accepted")
	}
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id, err := s.AddExport(ctx, ira, NewExport{MailboxID: box, Folders: []string{inbox.ID}, Subfolders: true, Since: &since, Format: "mbox", Lang: "ru-RU"})
	must(err)
	e, ok, err := s.ClaimExport(ctx)
	must(err)
	if !ok || e.ID != id || e.Status != "running" {
		t.Fatalf("claimed %+v %v", e, ok)
	}
	if _, ok, _ := s.ClaimExport(ctx); ok {
		t.Fatal("the running export is claimed twice")
	}
	if err := s.DeleteExport(ctx, ira, id); err == nil {
		t.Fatal("the running export is deleted")
	}
	files := func(e Export) map[string]string {
		t.Helper()
		var b bytes.Buffer
		n, err := s.WriteExport(ctx, e, &b, nil)
		must(err)
		z, err := zip.NewReader(bytes.NewReader(b.Bytes()), int64(b.Len()))
		must(err)
		out := map[string]string{"": fmt.Sprint(n)}
		for _, f := range z.File {
			r, err := f.Open()
			must(err)
			data, _ := io.ReadAll(r)
			out[f.Name] = string(data)
		}
		return out
	}
	got := files(e)
	if got[""] != "2" || len(got) != 3 {
		t.Fatalf("mbox export: %v", got)
	}
	in, bills := got["INBOX.mbox"], got["INBOX/Счета.mbox"]
	if !strings.HasPrefix(in, "From c@x.ru ") || !strings.Contains(in, "Subject: new\n") || !strings.Contains(in, "\n>From the start\n") ||
		strings.Contains(in, "давно") || strings.Contains(in, "\r") || !strings.Contains(bills, "Subject: bill") {
		t.Fatalf("mbox files: %q / %q", in, bills)
	}
	must(s.FinishExport(ctx, id, 2, 100, "", "/disk", ""))
	if e, err := s.GetExport(ctx, ira, id); err != nil || e.Status != "done" || e.Letters != 2 || e.URL != "/disk" || e.FinishedAt == nil {
		t.Fatalf("finished: %+v %v", e, err)
	}

	until := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	got = files(Export{UserID: ira, MailboxID: box, Format: "eml", Until: &until}) // the whole mailbox up to the 5th of October
	if got[""] != "2" || got["INBOX/00001 old.eml"] == "" || got["INBOX/00002 new.eml"] == "" || !strings.Contains(got["INBOX/00002 new.eml"], "\r\nFrom the start") {
		t.Fatalf("eml export: %v", keys(got))
	}
	if _, ok := got["INBOX/Счета/"]; !ok { // its letter is later: the folder stays, empty
		t.Fatalf("eml export without the empty folder: %v", keys(got))
	}
	// Outlook's file: the folders' tree under «Top of Personal Folders», the inbox named as go24 names it
	out := tempPST(t)
	tr := func(key string, _ ...any) string { return map[string]string{"mail.folder.inbox": "Входящие"}[key] }
	n, err := s.WriteExport(ctx, Export{UserID: ira, MailboxID: box, Email: addr, Format: "pst", Tr: tr}, out, nil)
	must(err)
	tree, f := readPST(t, out.Name())
	pin, pbill := tree["Top of Personal Folders/Входящие"], tree["Top of Personal Folders/Входящие/Счета"]
	if n != 3 || len(pin) != 2 || pin[0] != "old" || pin[1] != "new" || len(pbill) != 1 || pbill[0] != "bill" {
		t.Fatalf("pst export: %d letters, %v", n, tree)
	}
	folders, _ := f.Folders()
	l, err := f.Letter(folders["Top of Personal Folders/Входящие"][1])
	must(err)
	if body := l.Props.String(pst.TagBody); !strings.Contains(body, "From the start") || l.Props.String(pst.TagSenderEmail) != "c@x.ru" ||
		len(l.Recipients) != 1 || l.Recipients[0].String(pst.TagEmailAddress) != addr {
		t.Fatalf("pst letter: %q %+v", body, l.Recipients)
	}
	if _, err := s.WriteExport(ctx, Export{UserID: ira, MailboxID: box, Format: "pst"}, &bytes.Buffer{}, nil); err == nil {
		t.Fatal("a .pst written into a buffer")
	}

	must(s.DeleteExport(ctx, ira, id))
	if list, err := s.Exports(ctx, ira); err != nil || len(list) != 0 {
		t.Fatalf("exports after delete: %v %v", list, err)
	}
}

func keys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
