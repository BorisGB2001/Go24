package mail

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
)

// inbox fetches the letters of an address on the GreenMail server, whole.
func inbox(t *testing.T, host, who string) []string {
	t.Helper()
	c, err := dialIMAP(host, 3143, false)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Logout()
	if err := c.Login(who, "pw"); err != nil {
		t.Fatal(err)
	}
	box, err := c.Select("INBOX", true)
	if err != nil {
		t.Fatal(err)
	}
	if box.Messages == 0 {
		return nil
	}
	set := new(imap.SeqSet)
	set.AddRange(1, box.Messages)
	section := &imap.BodySectionName{Peek: true}
	ch := make(chan *imap.Message, 16)
	done := make(chan error, 1)
	go func() { done <- c.Fetch(set, []imap.FetchItem{section.FetchItem()}, ch) }()
	var out []string
	for m := range ch {
		b, _ := io.ReadAll(m.GetBody(section))
		out = append(out, string(b))
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCompose runs against GreenMail and the test database (as TestMailbox): drafts kept and changed, their files
// and pictures, an HTML letter with Копия and СК sent from a draft, a forward with the letter's files, «Отправить
// позже» sent by SendDue, «Отменить» in time, a failed send kept with its reason, a read receipt answered,
// signatures, templates, settings, suggested recipients; nobody else reaches any of it.
func TestComposeSend(t *testing.T) {
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna2', 'Анна Петрова', 'anna.p@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve2', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	var mu sync.Mutex
	stored := map[string][]byte{} // the storage: object key → bytes
	n := 0
	s.Keep = func(ctx context.Context, owner, letterID string, f Attachment) error {
		mu.Lock()
		n++
		key := letterID + "/" + string(rune('a'+n)) + f.Name
		stored[key] = f.Data
		mu.Unlock()
		_, err := pool.Exec(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, mail_id, content_type, content_id) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			key, f.Name, len(f.Data), owner, letterID, f.ContentType, f.ContentID)
		return err
	}
	s.Open = func(ctx context.Context, storageID *string, key string) (io.ReadCloser, error) {
		mu.Lock()
		defer mu.Unlock()
		return io.NopCloser(bytes.NewReader(stored[key])), nil
	}
	// a letter with a file and a read receipt asked for waits in the box
	must(smtp.SendMail(host+":3025", nil, "boss@buyer.test", []string{"anna.p@go24.test"},
		[]byte("From: Клиент <boss@buyer.test>\r\nTo: anna.p@go24.test\r\nCc: cc@buyer.test\r\nSubject: Order\r\nMessage-ID: <order-1@buyer.test>\r\n"+
			"Disposition-Notification-To: boss@buyer.test\r\nImportance: high\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n"+
			"--b\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Нужно <b>10</b> ящиков</p><script>x()</script>\r\n"+
			"--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"spec.txt\"\r\n\r\n10 x box\r\n--b--\r\n")))
	box, err := s.Add(ctx, anna, Settings{Email: "anna.p@go24.test", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	folders, err := s.Folders(ctx, anna, box)
	must(err)
	in, _ := s.Messages(ctx, anna, folders[0].ID, "")
	if len(in) != 1 {
		t.Fatalf("inbox = %+v", in)
	}
	order, err := s.Message(ctx, anna, in[0].ID)
	must(err)
	if !strings.Contains(order.HTML, "<b>10</b>") || strings.Contains(order.HTML, "script") || order.Body != "Нужно 10 ящиков" ||
		order.Importance != 1 || order.ReceiptTo != "boss@buyer.test" {
		t.Fatalf("the HTML letter is kept as %+v", order)
	}

	// a draft: kept in «Черновики», changed, with its files
	var nf *i18n.Error
	d := Draft{MailboxID: box, To: `"Клиент" <boss@buyer.test>`, CC: "cc@buyer.test", BCC: "boss2@buyer.test", Subject: "Счёт",
		HTML: `<p>Счёт <b>во вложении</b></p><script>x()</script>`, Importance: 1, Receipt: true, Mode: "reply", ReplyTo: order.ID}
	id, err := s.SaveDraft(ctx, anna, d)
	must(err)
	got, err := s.Draft(ctx, anna, id)
	must(err)
	if got.HTML != `<p>Счёт <b>во вложении</b></p>` || got.BCC != "boss2@buyer.test" || !got.Receipt || got.Importance != 1 || got.Mode != "reply" {
		t.Fatalf("draft = %+v", got)
	}
	drafts, err := s.folderByRole(ctx, box, "drafts")
	must(err)
	if list, _ := s.Messages(ctx, anna, drafts.ID, ""); len(list) != 1 || !list[0].Draft || list[0].Subject != "Счёт" {
		t.Fatalf("drafts folder = %+v", list)
	}
	d.ID, d.Subject = id, "Счёт №1"
	_, err = s.SaveDraft(ctx, anna, d)
	must(err)
	file, err := s.AttachDraft(ctx, anna, id, Attachment{Name: "счёт.txt", ContentType: "text/plain", Data: []byte("100 руб")}, false)
	must(err)
	pic, err := s.AttachDraft(ctx, anna, id, Attachment{Name: "logo.png", ContentType: "image/png", Data: []byte("\x89PNG")}, true)
	must(err)
	if _, err := s.AttachDraft(ctx, anna, id, Attachment{Name: "x.svg", ContentType: "image/svg+xml", Data: []byte("<svg/>")}, true); !errors.As(err, &nf) {
		t.Fatalf("an SVG picture: %v", err)
	}
	extra, err := s.AttachDraft(ctx, anna, id, Attachment{Name: "лишний.txt", ContentType: "text/plain", Data: []byte("x")}, false)
	must(err)
	must(s.DropDraftFile(ctx, anna, id, extra.ID))
	if files, _ := s.Files(ctx, anna, id); len(files) != 1 || files[0].ID != file.ID {
		t.Fatalf("the draft's attachments = %+v (the picture is not one)", files)
	}
	d.HTML = `<p>Счёт <b>во вложении</b></p><img src="/files/` + pic.ID + `?inline=1">`
	_, err = s.SaveDraft(ctx, anna, d)
	must(err)

	// nobody else reaches the draft
	if _, err := s.Draft(ctx, eve, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else read the draft: %v", err)
	}
	if _, err := s.SaveDraft(ctx, eve, Draft{ID: id, MailboxID: box}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else wrote the draft: %v", err)
	}
	for name, err := range map[string]error{
		"delete":   s.DeleteDraft(ctx, eve, id),
		"schedule": s.ScheduleDraft(ctx, eve, id, time.Now(), true),
		"send":     s.SendDraft(ctx, eve, id),
		"file":     s.DropDraftFile(ctx, eve, id, file.ID),
	} {
		if err == nil {
			t.Errorf("someone else could %s the draft", name)
		}
	}
	if _, err := s.AttachDraft(ctx, eve, id, Attachment{Name: "x", Data: []byte("x")}, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else attached to the draft: %v", err)
	}
	if err := s.CancelSend(ctx, eve, id); err == nil {
		t.Fatal("someone else cancelled the draft")
	}

	// sent: an HTML letter with its picture and file; Копия seen, СК not; the draft is gone, «Отправленные» keeps it all
	must(s.SendDraft(ctx, anna, id))
	if _, err := s.Draft(ctx, anna, id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the sent draft stays: %v", err)
	}
	letters := inbox(t, host, "boss@buyer.test")
	if len(letters) != 1 {
		t.Fatalf("boss got %d letters", len(letters))
	}
	p := parseFull(strings.NewReader(letters[0]))
	if !strings.Contains(p.html, "<b>во вложении</b>") || !strings.Contains(p.html, `src="cid:`) || p.importance != 1 || p.receiptTo != "anna.p@go24.test" ||
		len(p.files) != 2 || len(p.refs) == 0 || p.refs[len(p.refs)-1] != "order-1@buyer.test" {
		t.Fatalf("boss got %+v", p)
	}
	if !strings.Contains(letters[0], "Cc: <cc@buyer.test>") || strings.Contains(letters[0], "boss2") || !strings.Contains(letters[0], "Анна Петрова") &&
		!strings.Contains(letters[0], "=?utf-8?") {
		t.Fatalf("the headers:\n%s", letters[0][:min(len(letters[0]), 800)])
	}
	if len(inbox(t, host, "boss2@buyer.test")) != 1 || len(inbox(t, host, "cc@buyer.test")) != 1 {
		t.Fatal("the copies did not arrive")
	}
	sentF, err := s.folderByRole(ctx, box, "sent")
	must(err)
	sent, _ := s.Messages(ctx, anna, sentF.ID, "")
	if len(sent) != 1 {
		t.Fatalf("sent = %+v", sent)
	}
	sm, err := s.Message(ctx, anna, sent[0].ID)
	must(err)
	if sm.BCC != "boss2@buyer.test" || sm.CC != "cc@buyer.test" || !strings.Contains(sm.HTML, "во вложении") || sm.Importance != 1 {
		t.Fatalf("the sent copy = %+v", sm)
	}
	// the answered letter says so («Вы ответили на это письмо …»), here and after a sync with the server's \Answered
	if o, _ := s.Message(ctx, anna, order.ID); o.Answered != "reply" || o.AnsweredAt == nil {
		t.Fatalf("the answered letter = %q %v", o.Answered, o.AnsweredAt)
	}
	must(s.Sync(ctx, box))
	if o, _ := s.Message(ctx, anna, order.ID); o.Answered != "reply" || o.AnsweredAt == nil {
		t.Fatalf("the answered letter after a sync = %q %v", o.Answered, o.AnsweredAt)
	}

	// a forward takes the letter's files along
	fwd, err := s.SaveDraft(ctx, anna, Draft{MailboxID: box, Mode: "fwd", ReplyTo: order.ID, Subject: "Fwd: Order"})
	must(err)
	if files, _ := s.Files(ctx, anna, fwd); len(files) != 1 || files[0].Name != "spec.txt" {
		t.Fatalf("the forward's files = %+v", files)
	}
	must(s.DeleteDraft(ctx, anna, fwd))
	// «Переслать как вложение»: the letter itself, with its file, goes as «Order.eml»
	fa, err := s.SaveDraft(ctx, anna, Draft{MailboxID: box, Mode: "fwdatt", ReplyTo: order.ID, To: "fa@buyer.test", Subject: "Fwd: Order"})
	must(err)
	if files, _ := s.Files(ctx, anna, fa); len(files) != 1 || files[0].Name != "Order.eml" || files[0].ContentType != "message/rfc822" {
		t.Fatalf("the forward as an attachment's files = %+v", files)
	}
	must(s.SendDraft(ctx, anna, fa))
	if got := inbox(t, host, "fa@buyer.test"); len(got) != 1 || !strings.Contains(got[0], "Content-Type: message/rfc822") ||
		!strings.Contains(got[0], "Subject: Order") || !strings.Contains(got[0], "spec.txt") {
		t.Fatalf("fa got %q", got)
	}
	if o, _ := s.Message(ctx, anna, order.ID); o.Answered != "fwd" {
		t.Fatalf("the letter forwarded as an attachment = %q", o.Answered)
	}

	// «Отправить позже»: not before its time, then by itself
	later, err := s.SaveDraft(ctx, anna, Draft{MailboxID: box, To: "later@buyer.test", Subject: "Позже", HTML: "<p>Позже</p>"})
	must(err)
	must(s.ScheduleDraft(ctx, anna, later, time.Now().Add(1500*time.Millisecond), true))
	if errs := s.SendDue(ctx); len(errs) > 0 || len(inbox(t, host, "later@buyer.test")) != 0 {
		t.Fatalf("sent before its time: %v", errs)
	}
	if ld, _ := s.Draft(ctx, anna, later); ld.SendAt == nil || !ld.Scheduled {
		t.Fatalf("the scheduled draft = %+v", ld)
	}
	time.Sleep(1700 * time.Millisecond)
	if errs := s.SendDue(ctx); len(errs) > 0 || len(inbox(t, host, "later@buyer.test")) != 1 {
		t.Fatalf("not sent at its time: %v", errs)
	}
	if err := s.CancelSend(ctx, anna, later); !errors.As(err, &nf) || nf.Key != "err.mail_gone" {
		t.Fatalf("a sent letter called back: %v", err)
	}

	// «Отменить» in time: it stays a draft
	undo, err := s.SaveDraft(ctx, anna, Draft{MailboxID: box, To: "undo@buyer.test", HTML: "<p>Ой</p>"})
	must(err)
	must(s.ScheduleDraft(ctx, anna, undo, time.Now().Add(time.Second), false))
	must(s.CancelSend(ctx, anna, undo))
	time.Sleep(1100 * time.Millisecond)
	if errs := s.SendDue(ctx); len(errs) > 0 || len(inbox(t, host, "undo@buyer.test")) != 0 {
		t.Fatalf("an undone letter went: %v", errs)
	}
	if ud, _ := s.Draft(ctx, anna, undo); ud.SendAt != nil {
		t.Fatalf("the undone draft = %+v", ud)
	}

	// a letter that cannot go stays a draft with the reason
	bad, err := s.SaveDraft(ctx, anna, Draft{MailboxID: box, To: "not an address", HTML: "<p>x</p>"})
	must(err)
	if err := s.SendDraft(ctx, anna, bad); err == nil {
		t.Fatal("a bad address was sent to")
	}
	if bd, _ := s.Draft(ctx, anna, bad); bd.Error != "err.mail_to" || bd.SendAt != nil {
		t.Fatalf("the failed draft = %+v", bd)
	}

	// the read receipt the client asked for: sent once
	rc := Receipt{Subject: "Прочитано: Order", Text: "Прочитано"}
	if err := s.SendReceipt(ctx, eve, order.ID, rc, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else answered the receipt: %v", err)
	}
	must(s.SendReceipt(ctx, anna, order.ID, rc, false))
	must(s.SendReceipt(ctx, anna, order.ID, rc, false))
	mdn := 0
	for _, l := range inbox(t, host, "boss@buyer.test") {
		if strings.Contains(l, "disposition-notification") && strings.Contains(l, "Original-Message-ID: <order-1@buyer.test>") {
			mdn++
		}
	}
	if mdn != 1 {
		t.Fatalf("read receipts sent: %d", mdn)
	}
	if o, _ := s.Message(ctx, anna, order.ID); !o.ReceiptDone {
		t.Fatal("the receipt is asked again")
	}

	// signatures, templates, settings: the user's own
	sig, err := s.SaveSnippet(ctx, anna, "signature", "", "Основная", "<p>Анна</p><script>x()</script>")
	must(err)
	if _, err := s.SaveSnippet(ctx, anna, "signature", "", "Основная", "x"); !errors.As(err, &nf) {
		t.Fatalf("the same name twice: %v", err)
	}
	must(s.SetSignatures(ctx, anna, box, Signatures{New: sig, Reply: sig}))
	if sg, _ := s.MailboxSignatures(ctx, anna, box); sg.New != sig || sg.Reply != sig {
		t.Fatalf("signatures = %+v", sg)
	}
	if list, _ := s.Snippets(ctx, anna, "signature"); len(list) != 1 || list[0].HTML != "<p>Анна</p>" {
		t.Fatalf("signatures = %+v", list)
	}
	evesig, err := s.SaveSnippet(ctx, eve, "signature", "", "Чужая", "x")
	must(err)
	if err := s.SetSignatures(ctx, anna, box, Signatures{New: evesig}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's signature set: %v", err)
	}
	if err := s.SetSignatures(ctx, eve, box, Signatures{New: evesig}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's mailbox changed: %v", err)
	}
	if _, err := s.SaveSnippet(ctx, eve, "signature", sig, "Взлом", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else changed the signature: %v", err)
	}
	if err := s.DeleteSnippet(ctx, eve, "signature", sig); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else deleted the signature: %v", err)
	}
	tpl, err := s.SaveSnippet(ctx, anna, "template", "", "Спасибо", "<p>Спасибо!</p>")
	must(err)
	if list, _ := s.Snippets(ctx, eve, "template"); len(list) != 0 {
		t.Fatalf("someone else sees the templates: %+v", list)
	}
	must(s.DeleteSnippet(ctx, anna, "template", tpl))
	must(s.DeleteSnippet(ctx, anna, "signature", sig))
	if sg, _ := s.MailboxSignatures(ctx, anna, box); sg.New != "" {
		t.Fatalf("a deleted signature stays the mailbox's: %+v", sg)
	}
	if p, _ := s.Prefs(ctx, anna); p.UndoSeconds != 5 {
		t.Fatalf("default settings = %+v", p)
	}
	if err := s.SavePrefs(ctx, anna, Prefs{UndoSeconds: 11}); err == nil {
		t.Fatal("11 seconds of undo")
	}
	must(s.SavePrefs(ctx, anna, Prefs{UndoSeconds: 0, Font: "Arial", FontSize: "12pt"}))
	if p, _ := s.Prefs(ctx, anna); p.UndoSeconds != 0 || p.Font != "Arial" {
		t.Fatalf("settings = %+v", p)
	}

	// suggestions: whom the user wrote with (the own box left out), go24's people
	if list, _ := s.Correspondents(ctx, anna, "buyer", 10); len(list) < 3 || list[0].Email != "boss@buyer.test" {
		t.Fatalf("correspondents = %+v", list)
	}
	if list, _ := s.Correspondents(ctx, eve, "buyer", 10); len(list) != 0 {
		t.Fatalf("someone else's correspondents = %+v", list)
	}
	if list, _ := s.Employees(ctx, "Петров", 10); len(list) != 1 || list[0].Email != "anna.p@go24.test" {
		t.Fatalf("employees = %+v", list)
	}
	must(s.Remove(ctx, anna, box))
}
