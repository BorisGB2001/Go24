package mail

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/pst"
)

func TestImportFormat(t *testing.T) {
	for _, c := range []struct {
		name, head, want string
	}{
		{"backup.pst", "!BDN....", "pst"},
		{"почта.zip", "PK\x03\x04", "zip"},
		{"Inbox", "From - Mon", "mbox"},
		{"a.eml", "Subject: x", "eml"},
		{"Inbox.mbox", "", "mbox"},
		{"a.docx", "PK\x03\x04", "zip"},
		{"a.txt", "hello", ""},
	} {
		if got := ImportFormat(c.name, []byte(c.head)); got != c.want {
			t.Errorf("ImportFormat(%q) = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestImportRole(t *testing.T) {
	for name, want := range map[string]string{"Входящие": "inbox", "INBOX": "inbox", "Sent Items": "sent", "Отправленные": "sent",
		"Deleted Items": "trash", "Удаленные": "trash", "Junk E-mail": "junk", "Черновики": "drafts", "Исходящие": "", "Проекты": ""} {
		if got := importRole(name); got != want {
			t.Errorf("importRole(%q) = %q, want %q", name, got, want)
		}
	}
}

// collect reads the letters of a file as the import does.
func collect(t *testing.T, format string, f *os.File) []fileLetter {
	t.Helper()
	var out []fileLetter
	if err := fileLetters(format, f, "go24.test", func(l fileLetter) error { out = append(out, l); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func tempFile(t *testing.T, name string, data []byte) *os.File {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestMboxLetters(t *testing.T) {
	mbox := "From - Mon Oct  5 09:03:07 2026\nX-Mozilla-Status: 0005\nFrom: a@x.ru\nTo: b@x.ru\nSubject: one\nMessage-ID: <1@x.ru>\n\n>From here\n>>From there\n\n" +
		"From b@x.ru Mon Oct  5 10:00:00 2026\nStatus: O\nFrom: b@x.ru\nSubject: two\n\ntext\n\n" +
		"From c@x.ru Mon Oct  5 11:00:00 2026\nnot a letter at all\n"
	got := collect(t, "mbox", tempFile(t, "Inbox", []byte(mbox)))
	if len(got) != 3 || got[2].msg != nil {
		t.Fatalf("mbox: %d letters", len(got))
	}
	one, two := string(got[0].msg), string(got[1].msg)
	if !strings.Contains(one, "\r\nFrom here\r\n>From there\r\n") || letterID(got[0].msg) != "1@x.ru" {
		t.Errorf("mbox letter: %q", one)
	}
	if fmt.Sprint(got[0].flags) != fmt.Sprint([]string{imap.SeenFlag, imap.FlaggedFlag}) || len(got[1].flags) != 0 || !strings.Contains(two, "Subject: two") {
		t.Errorf("mbox flags: %v %v", got[0].flags, got[1].flags)
	}
}

func TestZipLetters(t *testing.T) {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	add := func(name, data string) {
		w, _ := z.Create(name)
		w.Write([]byte(data))
	}
	add("INBOX/Счета/00001 bill.eml", "From: c@x.ru\r\nSubject: bill\r\n\r\nсчёт\r\n")
	add("INBOX.mbox", "From c@x.ru Mon Oct  5 10:00:00 2026\nFrom: c@x.ru\nSubject: hi\n\nx\n")
	add("readme.txt", "no mail")
	add("__MACOSX/INBOX.mbox", "From x")
	z.Close()
	got := collect(t, "zip", tempFile(t, "mail.zip", b.Bytes()))
	if len(got) != 2 || strings.Join(got[0].path, "/") != "INBOX" || strings.Join(got[1].path, "/") != "INBOX/Счета" ||
		!bytes.Contains(got[1].msg, []byte("Subject: bill")) {
		t.Fatalf("zip: %+v", got)
	}
}

func TestPSTLetters(t *testing.T) {
	out := tempPST(t)
	w, err := pst.Create(out, "ira@go24.test")
	if err != nil {
		t.Fatal(err)
	}
	inbox, _ := w.AddFolder(w.Top(), "Входящие", pst.Inbox)
	bills, _ := w.AddFolder(inbox, "Счета", pst.Normal)
	contacts, _ := w.AddFolder(w.Top(), "Контакты", pst.Normal)
	html := `{\rtf1\ansi\ansicpg1251\fromhtml1 {\*\htmltag19 <html>}{\*\htmltag50 <body>}{\*\htmltag64 <p>}\'f1\'f7\'b8\'f2{\*\htmltag72 </p>}{\*\htmltag58 </body>}{\*\htmltag27 </html>}}`
	rtf := append([]byte{0, 0, 0, 0, 0, 0, 0, 0, 'M', 'E', 'L', 'A', 0, 0, 0, 0}, html...)
	rtf[4] = byte(len(html))
	when := time.Date(2026, 10, 5, 9, 0, 0, 0, time.UTC)
	letters := []struct {
		f pst.Folder
		m *pst.Message
	}{
		{inbox, &pst.Message{Props: []pst.Prop{{Tag: pst.TagSubject, Value: "Привет"}, {Tag: pst.TagBody, Value: "текст"},
			{Tag: pst.TagSenderName, Value: "Клиент"}, {Tag: pst.TagSenderEmail, Value: "c@x.ru"}, {Tag: pst.TagMessageDeliveryTime, Value: when},
			{Tag: pst.TagInternetMessageID, Value: "<p1@x.ru>"}, {Tag: pst.TagMessageFlags, Value: 1}, {Tag: pst.TagFlagStatus, Value: 2},
			{Tag: pst.TagLastVerbExecuted, Value: 102}},
			Recipients:  []pst.Recipient{{Type: 1, Name: "Ира", Email: "ira@go24.test"}, {Type: 2, Name: "Олег", Email: "oleg@go24.test"}},
			Attachments: []pst.Attachment{{Filename: "счёт.pdf", MimeType: "application/pdf", Data: []byte("%PDF-1.4")}}}},
		{bills, &pst.Message{Props: []pst.Prop{{Tag: pst.TagSubject, Value: "RTF"}, {Tag: pst.TagSenderEmail, Value: "c@x.ru"},
			{Tag: 0x10090102, Value: rtf}}}},
		{contacts, &pst.Message{Props: []pst.Prop{{Tag: pst.TagMessageClass, Value: "IPM.Contact"}, {Tag: pst.TagSubject, Value: "Олег"}}}},
	}
	for _, l := range letters {
		if err := w.AddMessage(l.f, l.m); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	got := collect(t, "pst", out)
	if len(got) != 3 {
		t.Fatalf("pst: %d letters", len(got))
	}
	first, rtfl, contact := got[0], got[1], got[2]
	msg := string(first.msg)
	if strings.Join(first.path, "/") != "Входящие" || !first.date.Equal(when) || letterID(first.msg) != "p1@x.ru" ||
		!strings.Contains(msg, "c@x.ru") || !strings.Contains(msg, "oleg@go24.test") || !strings.Contains(msg, "application/pdf") {
		t.Errorf("pst letter %v %v: %s", first.path, first.date, msg)
	}
	if fmt.Sprint(first.flags) != fmt.Sprint([]string{imap.SeenFlag, imap.FlaggedFlag, imap.AnsweredFlag}) {
		t.Errorf("pst flags: %v", first.flags)
	}
	if strings.Join(rtfl.path, "/") != "Входящие/Счета" || !strings.Contains(string(rtfl.msg), "text/html") || len(rtfl.flags) != 0 {
		t.Errorf("RTF letter %v: %s", rtfl.path, rtfl.msg)
	}
	if contact.msg != nil || strings.Join(contact.path, "/") != "Контакты" {
		t.Errorf("a contact taken for a letter: %s", contact.msg)
	}
	if _, err := pstFileLetters(t, tempFile(t, "old.pst", append([]byte("!BDN\x00\x00\x00\x00SM\x0e\x00"), make([]byte, 600)...))); err == nil {
		t.Error("an ANSI .pst read")
	}
}

func pstFileLetters(t *testing.T, f *os.File) (int, error) {
	n := 0
	err := fileLetters("pst", f, "go24.test", func(fileLetter) error { n++; return nil })
	return n, err
}

func TestFileImport(t *testing.T) {
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
	mailbox := func(name string) (string, string) {
		addr := fmt.Sprintf("%s.%d@go24.test", name, time.Now().UnixNano())
		box, err := s.Add(ctx, ira, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
		must(err)
		must(s.Sync(ctx, box))
		return box, addr
	}
	from, fromAddr := mailbox("old")
	to, _ := mailbox("new")
	inbox, err := s.folderByRole(ctx, from, "inbox")
	must(err)
	sub, err := s.CreateFolder(ctx, ira, from, inbox.ID, "Счета")
	must(err)
	projects, err := s.CreateFolder(ctx, ira, from, "", "Проекты")
	must(err)
	letter := func(subject string) ImportFile {
		return ImportFile{Name: subject + ".eml", Data: []byte("From: Клиент <c@x.ru>\r\nTo: " + fromAddr + "\r\nSubject: " + subject +
			"\r\nDate: Mon, 05 Oct 2026 10:00:00 +0000\r\nMessage-ID: <" + subject + "@x.ru>\r\n\r\nтекст\r\n")}
	}
	_, err = s.Import(ctx, ira, inbox.ID, []ImportFile{letter("one"), letter("two")})
	must(err)
	_, err = s.Import(ctx, ira, sub, []ImportFile{letter("bill")})
	must(err)
	_, err = s.Import(ctx, ira, projects, []ImportFile{letter("plan")})
	must(err)

	// the old mailbox as Outlook's file, as «Экспорт» makes it
	out := tempPST(t)
	tr := func(key string, _ ...any) string { return map[string]string{"mail.folder.inbox": "Входящие"}[key] }
	_, err = s.WriteExport(ctx, Export{UserID: ira, MailboxID: from, Email: fromAddr, Format: "pst", Tr: tr}, out, nil)
	must(err)

	if _, err := s.AddImport(ctx, oleg, NewImport{MailboxID: to, Format: "pst"}); err == nil {
		t.Fatal("an import into someone else's mailbox")
	}
	if _, err := s.AddImport(ctx, ira, NewImport{MailboxID: to, Format: "docx"}); err == nil {
		t.Fatal("unknown format accepted")
	}
	run := func(n NewImport, f *os.File) ImportCount {
		t.Helper()
		n.MailboxID = to
		id, err := s.AddImport(ctx, ira, n)
		must(err)
		e, ok, err := s.ClaimImport(ctx)
		must(err)
		if !ok || e.ID != id || e.Status != "running" {
			t.Fatalf("claimed %+v %v", e, ok)
		}
		e.Tr = tr
		c, err := s.RunImport(ctx, e, f, func(ImportCount) {})
		must(err)
		must(s.FinishImport(ctx, id, c, ""))
		return c
	}
	totals := func() map[string]int {
		folders, err := s.Folders(ctx, ira, to)
		must(err)
		out := map[string]int{}
		for _, f := range folders {
			out[f.Name] = f.Total
		}
		return out
	}
	c := run(NewImport{Format: "pst", Name: "old.pst"}, out)
	got := totals()
	if c.Letters != 4 || c.Same != 0 || c.Folders != 2 || got["INBOX"] != 2 || got["INBOX.Счета"]+got["INBOX/Счета"] != 1 || got["Проекты"] != 1 {
		t.Fatalf("into the same folders: %+v, %v", c, got)
	}
	// once more: all of them are there already
	if c := run(NewImport{Format: "pst", Name: "old.pst"}, out); c.Letters != 0 || c.Same != 4 {
		t.Fatalf("again: %+v", c)
	}
	// into a folder, duplicates allowed: the tree goes under it, the inbox by its name
	archive, err := s.CreateFolder(ctx, ira, to, "", "Старая почта")
	must(err)
	c = run(NewImport{Format: "pst", Name: "old.pst", FolderID: archive, Dupes: true}, out)
	got = totals()
	delim := "/"
	if _, ok := got["INBOX.Счета"]; ok {
		delim = "."
	}
	if c.Letters != 4 || got["Старая почта"+delim+"Входящие"] != 2 || got["Старая почта"+delim+"Входящие"+delim+"Счета"] != 1 ||
		got["Старая почта"+delim+"Проекты"] != 1 {
		t.Fatalf("into a folder: %+v, %v", c, got)
	}
	// a restarted import goes on from its position
	id, err := s.AddImport(ctx, ira, NewImport{MailboxID: to, Format: "pst", Name: "old.pst", FolderID: archive, Dupes: true})
	must(err)
	e, _, err := s.ClaimImport(ctx)
	must(err)
	e.Position = 3
	c, err = s.RunImport(ctx, e, out, func(ImportCount) {})
	must(err)
	if c.Letters != 1 || c.Position != 4 {
		t.Fatalf("restarted: %+v", c)
	}
	must(s.FinishImport(ctx, id, c, ""))
	list, err := s.Imports(ctx, ira)
	if err != nil || len(list) != 4 || list[0].Status != "done" || list[0].Letters != 1 {
		t.Fatalf("imports: %+v %v", list, err)
	}
	must(s.DeleteImport(ctx, ira, id))
	if _, err := s.GetImport(ctx, ira, id); err != ErrNotFound {
		t.Fatalf("deleted import: %v", err)
	}
}
