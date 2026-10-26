package mail

import (
	"context"
	"errors"
	"fmt"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestThreadOf(t *testing.T) {
	for _, c := range []struct {
		own  string
		refs []string
		want string
	}{
		{"<A@x>", nil, "a@x"},
		{"<b@x>", []string{"<Root@x>", "<a@x>"}, "root@x"},
		{"<b@x>", []string{"", "<a@x>"}, "a@x"},
		{"", nil, ""},
	} {
		if got := threadOf(c.own, c.refs); got != c.want {
			t.Errorf("threadOf(%q, %q) = %q, want %q", c.own, c.refs, got, c.want)
		}
	}
	p := parseFull(strings.NewReader("Message-ID: <c@x>\r\nReferences: <root@x> <a@x>\r\nIn-Reply-To: <b@x>\r\nContent-Type: text/plain\r\n\r\nhi\r\n"))
	if p.body != "hi" || threadOf("<c@x>", p.refs) != "root@x" || len(p.refs) != 3 || p.refs[2] != "b@x" {
		t.Fatalf("parsed = %+v", p)
	}
}

// TestActions runs against GreenMail and the test database like TestMailbox: a conversation (a letter, its answer
// from elsewhere and go24's answer) is one row; the flag and read go to the server and come back; pin, categories
// and snooze stay with a letter moved by archive; archive, junk and delete make their folders; delete in «Удалённые»
// deletes for good; nobody else can do anything with the letters.
func TestActions(t *testing.T) {
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
	addr := fmt.Sprintf("bob%d@go24.test", time.Now().UnixNano()) // GreenMail keeps its boxes between runs
	var bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', $1) RETURNING id`, addr).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	send := func(raw string) {
		must(smtp.SendMail(host+":3025", nil, "boss@client.test", []string{addr}, []byte(raw)))
	}
	send("From: boss@client.test\r\nTo: " + addr + "\r\nSubject: Order\r\nMessage-ID: <root@client.test>\r\nDate: Mon, 12 Oct 2026 10:00:00 +0200\r\n\r\nTen boxes\r\n")
	send("From: boss@client.test\r\nTo: " + addr + "\r\nSubject: Re: Order\r\nMessage-ID: <two@client.test>\r\nIn-Reply-To: <root@client.test>\r\n" +
		"References: <root@client.test>\r\nDate: Mon, 12 Oct 2026 11:00:00 +0200\r\n\r\nTwenty, please\r\n")
	send("From: news@shop.test\r\nTo: " + addr + "\r\nSubject: Sale\r\nMessage-ID: <sale@shop.test>\r\nDate: Mon, 12 Oct 2026 12:00:00 +0200\r\n\r\n-50%\r\n")
	id, err := s.Add(ctx, bob, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, id))
	inbox, err := s.folderByRole(ctx, id, "inbox")
	must(err)

	conv, err := s.List(ctx, bob, inbox.ID, "", true)
	must(err)
	if len(conv) != 2 || conv[0].Subject != "Sale" || conv[1].Subject != "Re: Order" || conv[1].Count != 2 || conv[1].Unread != 2 ||
		len(conv[1].IDs) != 2 || conv[1].Thread != "root@client.test" {
		t.Fatalf("conversations = %+v", conv)
	}
	flat, _ := s.List(ctx, bob, inbox.ID, "", false)
	if len(flat) != 3 {
		t.Fatalf("letters = %d", len(flat))
	}
	sale := conv[0].ID
	order := conv[1].IDs
	if !conv[0].Other || conv[1].Other { // a shop's news@ goes under «Другие», a client's letters stay focused
		t.Fatalf("other = %v %v", conv[0].Other, conv[1].Other)
	}

	// go24's answer joins the conversation; opening it reads all its letters
	must(s.SendReply(ctx, bob, id, conv[1].ID, "boss@client.test", "Re: Order", "Done"))
	thread, err := s.Conversation(ctx, bob, conv[1].ID)
	must(err)
	if len(thread) != 3 || thread[0].Subject != "Order" || thread[2].Body != "Done" || thread[2].Thread != "root@client.test" {
		t.Fatalf("conversation = %+v", thread)
	}
	if conv, _ = s.List(ctx, bob, inbox.ID, "", true); conv[1].Unread != 0 || !conv[1].Seen {
		t.Fatalf("the opened conversation is unread: %+v", conv[1])
	}

	// the flag goes to the server; taken off there, it goes here; completed stays
	must(s.Act(ctx, bob, []string{sale}, "flag", "2026-10-20"))
	if m, _ := s.message(ctx, bob, sale); !m.Flagged || m.FlagDue == nil || m.FlagDue.Format("2006-01-02") != "2026-10-20" {
		t.Fatalf("flagged = %+v", m)
	}
	if f, _ := s.List(ctx, bob, FlaggedFolder, "", false); len(f) != 1 || f[0].ID != sale {
		t.Fatalf("«С флажком» = %+v", f)
	}
	imapc, err := dialIMAP(host, 3143, false)
	must(err)
	defer imapc.Logout()
	must(imapc.Login(addr, "pw"))
	flags := func(folder string) map[string][]string { // subject → flags on the server
		_, err := imapc.Select(folder, true)
		must(err)
		set := new(imap.SeqSet)
		set.AddRange(1, 0)
		ch := make(chan *imap.Message, 10)
		done := make(chan error, 1)
		go func() { done <- imapc.Fetch(set, []imap.FetchItem{imap.FetchEnvelope, imap.FetchFlags}, ch) }()
		out := map[string][]string{}
		for m := range ch {
			out[m.Envelope.Subject] = m.Flags
		}
		must(<-done)
		return out
	}
	has := func(fl []string, f string) bool {
		for _, x := range fl {
			if x == f {
				return true
			}
		}
		return false
	}
	if fl := flags("INBOX"); !has(fl["Sale"], imap.FlaggedFlag) || !has(fl["Order"], imap.SeenFlag) {
		t.Fatalf("server flags = %v", fl)
	}
	must(s.Act(ctx, bob, []string{sale}, "unread", ""))
	if fl := flags("INBOX"); has(fl["Sale"], imap.SeenFlag) {
		t.Fatalf("unread here, read on the server: %v", fl)
	}
	must(s.Act(ctx, bob, []string{sale}, "done", ""))
	must(s.Sync(ctx, id))
	if m, _ := s.message(ctx, bob, sale); m.Flagged || !m.FlagDone || m.FlagDue == nil {
		t.Fatalf("completed = %+v", m)
	}

	// categories: given to all, taken from all; a stranger's category is not one
	cats, err := s.Categories(ctx, bob, [2]string{"red", "Красная"}, [2]string{"blue", "Синяя"})
	must(err)
	if len(cats) != 2 {
		t.Fatalf("default categories = %+v", cats)
	}
	red := cats[0].ID
	must(s.Act(ctx, bob, order, "category", red))
	if c, _ := s.List(ctx, bob, CategoryFolder+red, "", false); len(c) != 2 {
		t.Fatalf("category list = %+v", c)
	}
	must(s.Act(ctx, bob, append(order, sale), "category", red)) // not all have it: all get it
	if c, _ := s.List(ctx, bob, CategoryFolder+red, "", false); len(c) != 3 {
		t.Fatalf("category given to all = %d", len(c))
	}
	must(s.Act(ctx, bob, []string{sale}, "category", red))
	if c, _ := s.List(ctx, bob, CategoryFolder+red, "", false); len(c) != 2 {
		t.Fatalf("category taken off = %d", len(c))
	}
	evecat, err := s.SaveCategory(ctx, eve, "", "Ева", "green")
	must(err)
	if err := s.Act(ctx, bob, []string{sale}, "category", evecat); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's category given: %v", err)
	}
	if _, err := s.List(ctx, bob, CategoryFolder+evecat, "", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else's category listed: %v", err)
	}

	// pin on top; snooze hides till then and brings it back unread on top
	must(s.Act(ctx, bob, order[:1], "pin", ""))
	if l, _ := s.List(ctx, bob, inbox.ID, "", false); l[0].ID != order[0] || !l[0].Pinned {
		t.Fatalf("pinned first: %+v", l[0])
	}
	if err := s.Act(ctx, bob, []string{sale}, "snooze", time.Now().Add(-time.Hour).Format(time.RFC3339)); err == nil {
		t.Fatal("snoozed into the past")
	}
	must(s.Act(ctx, bob, []string{sale}, "read", ""))
	must(s.Act(ctx, bob, []string{sale}, "snooze", time.Now().Add(time.Hour).Format(time.RFC3339)))
	if l, _ := s.List(ctx, bob, inbox.ID, "", false); len(l) != 2 {
		t.Fatalf("snoozed still listed: %d", len(l))
	}
	if l, _ := s.List(ctx, bob, SnoozedFolder, "", false); len(l) != 1 || !l[0].Snoozed() {
		t.Fatalf("«Отложенные» = %+v", l)
	}
	_, err = pool.Exec(ctx, `UPDATE mail_messages SET snooze_until = now() - interval '1 second' WHERE id::text = $1`, sale)
	must(err)
	must(s.WakeDue(ctx))
	if l, _ := s.List(ctx, bob, inbox.ID, "", false); len(l) != 3 || l[1].ID != sale || l[1].Seen {
		t.Fatalf("back from snooze = %+v", l)
	}
	if fl := flags("INBOX"); has(fl["Sale"], imap.SeenFlag) {
		t.Fatalf("woke unread here, read on the server: %v", fl)
	}

	// the flag's reminder (GO24DEV-292): only to come; it flags the letter
	if err := s.Act(ctx, bob, order[:1], "remind", time.Now().Add(-time.Minute).Format(time.RFC3339)); err == nil {
		t.Fatal("a reminder in the past")
	}
	must(s.Act(ctx, bob, order[:1], "remind", time.Now().Add(time.Hour).Format(time.RFC3339)))
	if l, _ := s.Message(ctx, bob, order[0]); !l.Flagged || l.FlagRemind == nil {
		t.Fatalf("reminded = %v %v", l.Flagged, l.FlagRemind)
	}
	must(s.RemindDue(ctx)) // not yet
	reminded := func() (n int) {
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'mail_remind' AND url LIKE '/mail?folder=%&letter=%'`, bob).Scan(&n))
		return
	}
	if n := reminded(); n != 0 {
		t.Fatalf("told before the time: %d", n)
	}

	// archive: the server has no «Архив» — go24 makes it; the pin, the category and the reminder go along
	must(s.Act(ctx, bob, order, "archive", ""))
	arch, err := s.folderByRole(ctx, id, "archive")
	must(err)
	a, _ := s.List(ctx, bob, arch.ID, "", false)
	if arch.Name != "Archive" || len(a) != 2 || !a[0].Pinned || !a[0].HasCategory(red) || !a[1].HasCategory(red) {
		t.Fatalf("archive %+v = %+v", arch, a)
	}
	if a[0].FlagRemind == nil || !a[0].Flagged {
		t.Fatalf("the reminder lost in the archive: %+v", a[0])
	}
	_, err = pool.Exec(ctx, `UPDATE mail_messages SET flag_remind = now() - interval '1 second' WHERE id::text = $1`, a[0].ID)
	must(err)
	must(s.RemindDue(ctx))
	must(s.RemindDue(ctx)) // once
	if l, _ := s.Message(ctx, bob, a[0].ID); reminded() != 1 || l.FlagRemind != nil || !l.Flagged {
		t.Fatalf("reminded %d times, then %v %v", reminded(), l.FlagRemind, l.Flagged)
	}
	// taken off with the flag; or by itself
	must(s.Act(ctx, bob, []string{a[0].ID}, "remind", "2099-01-01T08:00:00Z"))
	must(s.Act(ctx, bob, []string{a[0].ID}, "unflag", ""))
	if l, _ := s.Message(ctx, bob, a[0].ID); l.FlagRemind != nil {
		t.Fatal("the reminder outlived the flag")
	}
	must(s.Act(ctx, bob, []string{a[0].ID}, "remind", "2099-01-01T08:00:00Z"))
	must(s.Act(ctx, bob, []string{a[0].ID}, "unremind", ""))
	if l, _ := s.Message(ctx, bob, a[0].ID); l.FlagRemind != nil || !l.Flagged {
		t.Fatalf("unremind = %v %v", l.FlagRemind, l.Flagged)
	}
	// moved back to the inbox and to the archive again with the toolbar's «Переместить»
	must(s.Act(ctx, bob, []string{a[0].ID, a[1].ID}, "move", inbox.ID))
	back, _ := s.List(ctx, bob, inbox.ID, "", false)
	if len(back) != 3 {
		t.Fatalf("moved back = %d", len(back))
	}
	var again []string
	for _, m := range back {
		if m.Thread == "root@client.test" {
			again = append(again, m.ID)
		}
	}
	must(s.Act(ctx, bob, again, "move", arch.ID))
	if a, _ = s.List(ctx, bob, arch.ID, "", false); len(a) != 2 || !a[0].Pinned {
		t.Fatalf("archive again = %+v", a)
	}
	// junk and back
	must(s.Act(ctx, bob, []string{sale}, "junk", ""))
	junk, _ := s.folderByRole(ctx, id, "junk")
	j, _ := s.List(ctx, bob, junk.ID, "", false)
	if len(j) != 1 {
		t.Fatalf("junk = %+v", j)
	}
	must(s.Act(ctx, bob, []string{j[0].ID}, "notjunk", ""))
	in, _ := s.List(ctx, bob, inbox.ID, "", false)
	if len(in) != 1 || in[0].Subject != "Sale" {
		t.Fatalf("not junk = %+v", in)
	}
	// delete: into «Удалённые», then for good
	must(s.Act(ctx, bob, []string{in[0].ID}, "delete", ""))
	trash, _ := s.folderByRole(ctx, id, "trash")
	tr, _ := s.List(ctx, bob, trash.ID, "", false)
	if len(tr) != 1 || len(flags("Trash")) != 1 {
		t.Fatalf("trash = %+v", tr)
	}
	// a stranger can do nothing with them; nor read all or empty the folders
	if err := s.ReadAll(ctx, eve, inbox.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else read the folder: %v", err)
	}
	if err := s.Empty(ctx, eve, trash.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else emptied the folder: %v", err)
	}
	if err := s.Empty(ctx, bob, inbox.ID); err == nil {
		t.Fatal("the inbox emptied")
	}
	for _, act := range Actions {
		if err := s.Act(ctx, eve, []string{tr[0].ID, a[0].ID}, act, ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("someone else did %s: %v", act, err)
		}
	}
	if _, err := s.Conversation(ctx, eve, a[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else read the conversation: %v", err)
	}
	if err := s.SendReply(ctx, eve, id, a[0].ID, "x@y.test", "x", "y"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else answered from the mailbox: %v", err)
	}
	if err := s.DeleteCategory(ctx, eve, red); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else deleted the category: %v", err)
	}
	must(s.Act(ctx, bob, []string{tr[0].ID}, "delete", ""))
	if tr, _ = s.List(ctx, bob, trash.ID, "", false); len(tr) != 0 || len(flags("Trash")) != 0 {
		t.Fatalf("deleted for good, still %+v", tr)
	}
	// «Отметить все как прочитанные» here and on the server; «Очистить папку» deletes all for good
	must(s.Act(ctx, bob, ids(mustLetters(t, s, bob, arch.ID)), "unread", ""))
	must(s.ReadAll(ctx, bob, arch.ID))
	if f, _ := s.Folder(ctx, bob, arch.ID); f.Unseen != 0 {
		t.Fatalf("still unread after «read all»: %d", f.Unseen)
	}
	for subj, fl := range flags("Archive") {
		if !has(fl, imap.SeenFlag) {
			t.Fatalf("%s unread on the server", subj)
		}
	}
	must(s.Act(ctx, bob, ids(mustLetters(t, s, bob, arch.ID))[:1], "delete", ""))
	must(s.Empty(ctx, bob, trash.ID))
	if tr, _ = s.List(ctx, bob, trash.ID, "", false); len(tr) != 0 || len(flags("Trash")) != 0 {
		t.Fatalf("emptied, still %+v", tr)
	}
	// a deleted category leaves the letters
	must(s.DeleteCategory(ctx, bob, red))
	if a, _ = s.List(ctx, bob, arch.ID, "", false); len(a) != 1 || a[0].HasCategory(red) {
		t.Fatalf("deleted category still on %+v", a)
	}
}

func mustLetters(t *testing.T, s *Store, userID, folderID string) []letter {
	t.Helper()
	list, err := s.lettersWhere(context.Background(), `f.id::text = $1 AND m.user_id::text = $2`, folderID, userID)
	if err != nil || len(list) == 0 {
		t.Fatalf("letters of %s: %v %d", folderID, err, len(list))
	}
	return list
}
