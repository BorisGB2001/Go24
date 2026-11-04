package mail

import (
	"context"
	"fmt"
	"net/smtp"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestRedundant(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 10, 28, 10, m, 0, 0, time.UTC) }
	l := func(id, thread string, m int, text string, keep bool) cleanLetter {
		return cleanLetter{id: id, thread: thread, sent: at(m), text: quoteText(text), keep: keep}
	}
	list := []cleanLetter{
		l("a", "t1", 0, "Привет! Пришли отчёт за октябрь.", false),
		l("b", "t1", 1, "Вот отчёт.\n\n> Привет! Пришли\n> отчёт за октябрь.", false),
		l("c", "t1", 2, "Спасибо!\n> Вот отчёт.\n>> Привет! Пришли отчёт за октябрь.", false),
		l("d", "t1", 3, "Не могу сейчас.\n> Привет! Пришли отчёт.", false), // changed the quote: a branch of its own
		l("e", "t2", 0, "Встреча в пятницу в 15:00", true),                  // kept (unread, a flag…)
		l("f", "t2", 1, "Приду.\n> Встреча в пятницу в 15:00", false),
		l("g", "t3", 0, "Ок", false), // too short to be taken for quoted
		l("h", "t3", 1, "Ок, тогда до завтра. > Ок", false),
		l("i", "t4", 0, "Тот же самый текст письма", false), // a copy at the same time is no later letter
		l("j", "t4", 0, "Тот же самый текст письма", false),
		l("k", "t5", 0, "Письмо другой беседы целиком", false),
		l("m", "t6", 1, "Ответ.\n> Письмо другой беседы целиком", false),
	}
	got := redundant(list)
	sort.Strings(got)
	if fmt.Sprint(got) != "[a b]" {
		t.Fatalf("redundant = %v", got)
	}
	if q := quoteText("> Hello,\r\n>  World 2026!"); q != "helloworld2026" {
		t.Fatalf("quoteText = %q", q)
	}
}

func TestCleanUp(t *testing.T) {
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
	n := time.Now().UnixNano()
	addr, ann := fmt.Sprintf("kim%d@go24.test", n), fmt.Sprintf("ann%d@client.test", n)
	var kim string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('kim', 'Ким', $1) RETURNING id`, addr).Scan(&kim))
	s := NewStore(pool, "test-secret")
	box, err := s.Add(ctx, kim, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	send := func(id, refs, subject, minute, body string) {
		t.Helper()
		h := "From: " + ann + "\r\nTo: " + addr + "\r\nSubject: " + subject + "\r\nDate: Wed, 28 Oct 2026 10:" + minute + ":00 +0100\r\nMessage-ID: <" + id + "@client.test>\r\n"
		if refs != "" { // the whole chain, the parent last
			parent := refs[len(refs)-1:]
			h += "In-Reply-To: <" + parent + "@client.test>\r\nReferences:"
			for _, r := range refs {
				h += " <" + string(r) + "@client.test>"
			}
			h += "\r\n"
		}
		must(smtp.SendMail(host+":3025", nil, ann, []string{addr}, []byte(h+"Content-Type: text/plain; charset=utf-8\r\n\r\n"+body+"\r\n")))
	}
	send("a", "", "Report", "00", "Пришли, пожалуйста, отчёт за октябрь.")
	send("b", "a", "Re: Report", "01", "Вот отчёт.\r\n\r\n> Пришли, пожалуйста, отчёт за октябрь.")
	send("c", "ab", "Re: Report", "02", "Спасибо!\r\n> Вот отчёт.\r\n>> Пришли, пожалуйста, отчёт за октябрь.")
	send("d", "a", "Re: Report", "03", "Не сейчас.\r\n> Пришли отчёт.")
	send("e", "", "Meeting", "04", "Встреча в пятницу в 15:00.")
	send("f", "e", "Re: Meeting", "05", "Приду.\r\n> Встреча в пятницу в 15:00.")
	must(s.Sync(ctx, box))
	in, err := s.folderByRole(ctx, box, "inbox")
	must(err)
	subjects := func(folder string) string { // a moved letter is another one (its id, as its UID, is new)
		t.Helper()
		list, err := s.List(ctx, kim, folder, "", false)
		must(err)
		var out []string
		for _, m := range list {
			out = append(out, m.Subject)
		}
		sort.Strings(out)
		return fmt.Sprint(out)
	}
	byBody := map[string]string{}
	rows, err := pool.Query(ctx, `SELECT left(body, 5), id::text FROM mail_messages`)
	must(err)
	for rows.Next() {
		var b, id string
		must(rows.Scan(&b, &id))
		byBody[b] = id
	}
	rows.Close()
	a, b, c, e := byBody["Пришл"], byBody["Вот о"], byBody["Спаси"], byBody["Встре"]
	if len(byBody) != 6 || a == "" || b == "" || c == "" || e == "" {
		t.Fatalf("letters = %v", byBody)
	}

	// all unread: «Не переносить непрочитанные» keeps them
	if k, err := s.CleanUpConversations(ctx, kim, []string{c}); err != nil || k != 0 {
		t.Fatalf("unread cleaned: %d %v", k, err)
	}
	must(s.Act(ctx, kim, []string{a, b, c, e}, "read", ""))
	must(s.Act(ctx, kim, []string{e}, "flag", ""))
	k, err := s.CleanUpConversations(ctx, kim, []string{c})
	must(err)
	trash, err := s.folderByRole(ctx, box, "trash")
	must(err)
	if got := subjects(trash.ID); k != 2 || got != "[Re: Report Report]" {
		t.Fatalf("cleaned %d, trash = %v", k, got)
	}
	if got := subjects(in.ID); got != "[Meeting Re: Meeting Re: Report Re: Report]" {
		t.Fatalf("inbox = %q", got)
	}
	if k, _ := s.CleanUpFolder(ctx, kim, in.ID, true); k != 0 { // the flagged one stays
		t.Fatalf("flagged cleaned: %d", k)
	}

	// «Очистить папку» with flags not kept, to «Архив»
	c0, err := s.Cleanup(ctx, kim)
	must(err)
	if c0 != (Cleanup{Target: "delete", KeepUnread: true, KeepCategorized: true, KeepFlagged: true, KeepSigned: true}) {
		t.Fatalf("defaults = %+v", c0)
	}
	if err := s.SaveCleanup(ctx, kim, Cleanup{Target: "nowhere"}); err == nil {
		t.Fatal("a bad target saved")
	}
	must(s.SaveCleanup(ctx, kim, Cleanup{Target: "archive", KeepUnread: true}))
	k, err = s.CleanUpFolder(ctx, kim, in.ID, false)
	must(err)
	arch, err := s.folderByRole(ctx, box, "archive")
	must(err)
	if got := subjects(arch.ID); k != 1 || got != "[Meeting]" {
		t.Fatalf("cleaned %d, archive = %v", k, got)
	}
	if k, _ := s.CleanUpFolder(ctx, kim, arch.ID, false); k != 0 { // «Архив» is not archived again
		t.Fatalf("archive cleaned: %d", k)
	}
	if _, err := s.CleanUpFolder(ctx, "00000000-0000-0000-0000-000000000000", in.ID, false); err == nil {
		t.Fatal("another user's folder cleaned")
	}
}
