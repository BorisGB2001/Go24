package mail

import (
	"context"
	"errors"
	"net/smtp"
	"os"
	"strings"
	"testing"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

func TestSeal(t *testing.T) {
	s := NewStore(nil, "secret")
	sealed, err := s.seal("пароль")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), "пароль") {
		t.Fatal("the password is kept in the open")
	}
	if got, err := s.open(sealed); err != nil || got != "пароль" {
		t.Fatalf("open = %q %v", got, err)
	}
	if _, err := NewStore(nil, "other").open(sealed); err == nil {
		t.Fatal("another key opened the password")
	}
}

func TestAttachments(t *testing.T) {
	letter := "Content-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nСчёт во вложении\r\n" +
		"--b\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"invoice.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0x\r\n--b--\r\n"
	body, files := parse(strings.NewReader(letter))
	if body != "Счёт во вложении" || len(files) != 1 || files[0].Name != "invoice.pdf" || string(files[0].Data) != "%PDF-1" || files[0].ContentType != "application/pdf" {
		t.Fatalf("parsed %q %+v", body, files)
	}
}

func TestPreset(t *testing.T) {
	st := Settings{Email: "boris@Gmail.com", Password: "abcd efgh ijkl mnop"}
	if err := st.check(); err != nil {
		t.Fatal(err)
	}
	if st.IMAPHost != "imap.gmail.com" || st.IMAPPort != 993 || st.SMTPHost != "smtp.gmail.com" || st.SMTPPort != 465 || st.Login != st.Email || st.Password != "abcdefghijklmnop" {
		t.Fatalf("gmail preset = %+v", st)
	}
	own := Settings{Email: "a@corp.ru", IMAPHost: "mx.corp.ru", IMAPPort: 143, SMTPHost: "mx.corp.ru", SMTPPort: 587, Password: "x"}
	if own.check(); own.IMAPHost != "mx.corp.ru" {
		t.Fatal("a given server is kept")
	}
	if err := (&Settings{Email: "a@corp.ru", Password: "x"}).check(); err == nil {
		t.Fatal("an unknown domain needs its servers")
	}
}

func TestText(t *testing.T) {
	letter := "Content-Type: multipart/alternative; boundary=b\r\n\r\n--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Привет,<br>мир</p><script>x()</script>\r\n--b--\r\n"
	if got := text(strings.NewReader(letter)); got != "Привет,\nмир" {
		t.Fatalf("html letter = %q", got)
	}
}

// TestMailbox runs against a GreenMail test server (GO24_TEST_MAIL_HOST: IMAP 3143, SMTP 3025, no TLS) and the
// test database: a letter sent to the box comes into go24, a letter sent from go24 reaches the server and «Отправленные»;
// the folders: roles by name, own folders made, renamed and deleted on the server, letters moved, changes brought back.
func TestMailbox(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	s := NewStore(pool, "test-secret")
	var kept []string
	s.Keep = func(ctx context.Context, owner, letterID string, f Attachment) error {
		kept = append(kept, f.Name)
		_, err := pool.Exec(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, mail_id, content_type) VALUES ($1, $2, $3, $4, $5, $6)`,
			"k/"+f.Name, f.Name, len(f.Data), owner, letterID, f.ContentType)
		return err
	}
	st := Settings{Email: "anna@go24.test", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"}
	var ve *i18n.Error
	if _, err := s.Add(ctx, anna, Settings{Email: "nope", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "x"}); !errors.As(err, &ve) {
		t.Fatalf("a bad e-mail: %v", err)
	}
	// a letter waiting in the box before go24 connects it
	must(smtp.SendMail(host+":3025", nil, "boss@client.test", []string{"anna@go24.test"},
		[]byte("From: Клиент <boss@client.test>\r\nTo: anna@go24.test\r\nSubject: =?utf-8?B?0JfQsNC60LDQtw==?=\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n"+
			"--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nНужно 10 ящиков\r\n--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"spec.txt\"\r\n\r\n10 x box\r\n--b--\r\n")))
	must(Probe(st))
	id, err := s.Add(ctx, anna, st)
	must(err)
	must(s.Sync(ctx, id))
	folders, err := s.Folders(ctx, anna, id)
	must(err)
	if len(folders) != 1 || folders[0].Role != "inbox" || folders[0].Unseen != 1 {
		t.Fatalf("folders = %+v", folders)
	}
	inbox := folders[0].ID
	list, err := s.Messages(ctx, anna, inbox, "")
	must(err)
	if len(list) != 1 || list[0].Subject != "Заказ" || list[0].FromName != "Клиент" || list[0].Body != "Нужно 10 ящиков" || list[0].Seen {
		t.Fatalf("inbox = %+v", list)
	}
	must(s.Sync(ctx, id)) // nothing new: not twice
	if list, _ = s.Messages(ctx, anna, inbox, ""); len(list) != 1 {
		t.Fatalf("synced twice: %d letters", len(list))
	}
	if list[0].Attached != 1 {
		t.Fatalf("the letter's files in the list: %d", list[0].Attached)
	}
	if conv, _ := s.List(ctx, anna, inbox, "", true); len(conv) != 1 || conv[0].Attached != 1 {
		t.Fatalf("the conversation's files in the list: %+v", conv)
	}
	if all, _ := s.Messages(ctx, anna, AllInboxes, ""); len(all) != 1 || all[0].MailboxEmail != "anna@go24.test" {
		t.Fatalf("all inboxes = %+v", all)
	}
	m, err := s.Message(ctx, anna, list[0].ID)
	must(err)
	if files, _ := s.Files(ctx, anna, m.ID); len(kept) != 1 || len(files) != 1 || files[0].Name != "spec.txt" || files[0].ContentType != "text/plain" || !s.Owns(ctx, anna, m.ID) {
		t.Fatalf("attachments: kept %v, files %+v", kept, files)
	}
	if mb, _ := s.Mailbox(ctx, anna, id); mb.Unseen != 0 || m.Subject != "Заказ" {
		t.Fatalf("after reading: %+v", mb)
	}
	// an answer from go24 lands in the box of the client (GreenMail keeps every address) and, as the server has no
	// «Отправленные», in go24's own
	must(s.Send(ctx, anna, id, "Клиент <boss@client.test>", "Re: Заказ", "Готово", Attachment{Name: "счёт.txt", ContentType: "text/plain", Data: []byte("100 руб")}))
	localSent, err := s.folderByRole(ctx, id, "sent")
	must(err)
	sent, _ := s.Messages(ctx, anna, localSent.ID, "")
	if !localSent.Local || len(sent) != 1 || sent[0].To != "boss@client.test" || !sent[0].Outgoing() {
		t.Fatalf("sent = %+v in %+v", sent, localSent)
	}
	if files, _ := s.Files(ctx, anna, sent[0].ID); len(files) != 1 || files[0].Name != "счёт.txt" {
		t.Fatalf("sent copy files = %+v", files)
	}

	// folders made elsewhere come with their roles by name; go24's «Sent» becomes the server's, its letter stays
	imapc, err := dialIMAP(host, 3143, false)
	must(err)
	defer imapc.Logout()
	must(imapc.Login("anna@go24.test", "pw"))
	must(imapc.Create("Archive"))
	must(imapc.Create("Sent"))
	must(s.Sync(ctx, id))
	roles := func() map[string]string {
		folders, err := s.Folders(ctx, anna, id)
		must(err)
		out := map[string]string{}
		for _, f := range folders {
			out[f.Name] = f.Role
		}
		return out
	}
	if r := roles(); r["Archive"] != "archive" || r["Sent"] != "sent" || r["INBOX"] != "inbox" || len(r) != 3 {
		t.Fatalf("roles = %v", r)
	}
	if sent, _ = s.Messages(ctx, anna, localSent.ID, ""); len(sent) != 1 {
		t.Fatalf("go24's sent letter lost: %+v", sent)
	}
	// now sent letters go to the server's «Sent» and come back from it
	must(s.Send(ctx, anna, id, "boss@client.test", "Ещё", "Второе"))
	sent, _ = s.Messages(ctx, anna, localSent.ID, "Второе")
	if all, _ := s.Messages(ctx, anna, localSent.ID, ""); len(all) != 2 || len(sent) != 1 || sent[0].Subject != "Ещё" {
		t.Fatalf("sent via the server = %+v", sent)
	}

	// own folders: made, nested, renamed with what is inside, deleted; role folders stay as they are
	proj, err := s.CreateFolder(ctx, anna, id, "", "Проекты")
	must(err)
	sub, err := s.CreateFolder(ctx, anna, id, proj, "2026")
	must(err)
	if _, err := s.CreateFolder(ctx, anna, id, "", "проекты"); !errors.As(err, &ve) {
		t.Fatalf("the same name twice: %v", err)
	}
	if _, err := s.CreateFolder(ctx, anna, id, "", ""); !errors.As(err, &ve) {
		t.Fatalf("no name: %v", err)
	}
	must(s.RenameFolder(ctx, anna, proj, "Клиенты"))
	f, err := s.Folder(ctx, anna, sub)
	must(err)
	if f.Title() != "2026" || f.Depth() != 1 || !strings.HasPrefix(f.Name, "Клиенты"+f.Delim) {
		t.Fatalf("the sub-folder after renaming: %+v", f)
	}
	must(s.Sync(ctx, id)) // the server has them under the new names
	if r := roles(); len(r) != 5 || r["Клиенты"+f.Delim+"2026"] != "" {
		t.Fatalf("after renaming, folders = %v", r)
	}
	if err := s.DeleteFolder(ctx, anna, proj); !errors.As(err, &ve) {
		t.Fatalf("a folder with sub-folders deleted: %v", err)
	}
	if err := s.RenameFolder(ctx, anna, inbox, "x"); !errors.As(err, &ve) {
		t.Fatalf("the inbox renamed: %v", err)
	}
	must(s.DeleteFolder(ctx, anna, sub))
	must(s.DeleteFolder(ctx, anna, proj))
	if r := roles(); len(r) != 3 {
		t.Fatalf("after deleting, folders = %v", r)
	}

	// moving a letter: on the server, it comes into the archive with its files and leaves the inbox
	archive, err := s.folderByRole(ctx, id, "archive")
	must(err)
	must(s.Move(ctx, anna, []string{list[0].ID}, archive.ID))
	if in, _ := s.Messages(ctx, anna, inbox, ""); len(in) != 0 {
		t.Fatalf("the moved letter is still in the inbox: %+v", in)
	}
	arch, _ := s.Messages(ctx, anna, archive.ID, "")
	if len(arch) != 1 || arch[0].Subject != "Заказ" || !arch[0].Seen {
		t.Fatalf("archive = %+v", arch)
	}
	if files, _ := s.Files(ctx, anna, arch[0].ID); len(files) != 1 {
		t.Fatalf("the moved letter's files: %+v", files)
	}
	// what changes on the server comes into go24: unread again, then deleted
	_, err = imapc.Select("Archive", false)
	must(err)
	all := new(imap.SeqSet)
	all.AddRange(1, 0)
	must(imapc.Store(all, imap.FormatFlagsOp(imap.RemoveFlags, true), []interface{}{imap.SeenFlag}, nil))
	must(s.Sync(ctx, id))
	if arch, _ = s.Messages(ctx, anna, archive.ID, ""); len(arch) != 1 || arch[0].Seen {
		t.Fatalf("unread on the server, here %+v", arch)
	}
	must(s.Act(ctx, anna, []string{arch[0].ID}, "read", "")) // reading in go24 reads it on the server
	_, err = pool.Exec(ctx, `UPDATE mail_messages SET seen = false WHERE id = $1`, arch[0].ID)
	must(err)
	must(s.Sync(ctx, id))
	if arch, _ = s.Messages(ctx, anna, archive.ID, ""); len(arch) != 1 || !arch[0].Seen {
		t.Fatalf("read in go24, the server says %+v", arch)
	}
	must(imapc.Store(all, imap.FormatFlagsOp(imap.AddFlags, true), []interface{}{imap.DeletedFlag}, nil))
	must(imapc.Expunge(nil))
	must(s.Sync(ctx, id))
	if arch, _ = s.Messages(ctx, anna, archive.ID, ""); len(arch) != 0 {
		t.Fatalf("deleted on the server, here %+v", arch)
	}

	if err := s.Send(ctx, anna, id, "boss@client.test", "x", "y", make([]Attachment, MaxSendFiles+1)...); !errors.As(err, &ve) {
		t.Fatalf("too many files: %v", err)
	}
	if err := s.Send(ctx, anna, id, "not an address", "x", "y"); !errors.As(err, &ve) {
		t.Fatalf("a bad recipient: %v", err)
	}
	var stranger string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&stranger))
	if s.Owns(ctx, stranger, sent[0].ID) {
		t.Fatal("someone else owns the letter's files")
	}
	if _, err := s.Message(ctx, stranger, sent[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else read the letter: %v", err)
	}
	if _, err := s.CreateFolder(ctx, stranger, id, "", "Чужая"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else made a folder: %v", err)
	}
	if err := s.Move(ctx, stranger, []string{sent[0].ID}, inbox); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else moved the letter: %v", err)
	}
	must(s.Remove(ctx, anna, id))
}
