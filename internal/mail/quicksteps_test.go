package mail

import (
	"context"
	"net/smtp"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestQuickSteps runs against GreenMail and the test database (as TestMailbox): «Игнорировать беседу» sends the
// conversation and its new answers to «Удалённые» and stopping brings them back; «Готово» and «Ответить и удалить»
// are made once; a quick step marks, categorizes and files the letters, and its steps are checked.
func TestQuickSteps(t *testing.T) {
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
	const me = "quick@go24.test"
	send := func(subject, headers string) {
		t.Helper()
		must(smtp.SendMail(host+":3025", nil, "bob@else.test", []string{me},
			[]byte("From: bob@else.test\r\nTo: "+me+"\r\nSubject: "+subject+"\r\n"+headers+"\r\nText\r\n")))
	}
	var anna, other string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna6', 'Анна', $1) RETURNING id`, me).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan6', 'Иван', 'ivan6@go24.test') RETURNING id`).Scan(&other))
	s := NewStore(pool, "test-secret")
	box, err := s.Add(ctx, anna, Settings{Email: me, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	where := func(subject string) (id, role string) { // the letter and its folder's role
		t.Helper()
		must(pool.QueryRow(ctx, `SELECT x.id::text, f.role FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id
			WHERE x.subject = $1 ORDER BY x.sent_at DESC LIMIT 1`, subject).Scan(&id, &role))
		return id, role
	}

	// «Игнорировать беседу»
	send("Topic", "Message-ID: <root@else.test>\r\n")
	send("Re: Topic", "Message-ID: <a1@else.test>\r\nIn-Reply-To: <root@else.test>\r\nReferences: <root@else.test>\r\n")
	send("Other", "Message-ID: <other@else.test>\r\n")
	must(s.Sync(ctx, box))
	answer, _ := where("Re: Topic")
	if on, err := s.Ignored(ctx, anna, answer); err != nil || on {
		t.Fatalf("ignored before: %v, %v", on, err)
	}
	must(s.Act(ctx, anna, []string{answer}, "ignore", ""))
	for _, subj := range []string{"Topic", "Re: Topic"} {
		if _, role := where(subj); role != "trash" {
			t.Fatalf("%q is in %q, not in the trash", subj, role)
		}
	}
	if _, role := where("Other"); role != "inbox" {
		t.Fatalf("another conversation went to %q", role)
	}
	send("Re: Re: Topic", "Message-ID: <a2@else.test>\r\nIn-Reply-To: <a1@else.test>\r\nReferences: <root@else.test> <a1@else.test>\r\n")
	must(s.Sync(ctx, box))
	late, role := where("Re: Re: Topic")
	if role != "trash" {
		t.Fatalf("a new answer of an ignored conversation is in %q", role)
	}
	if on, err := s.Ignored(ctx, anna, late); err != nil || !on {
		t.Fatalf("ignored: %v, %v", on, err)
	}
	if on, _ := s.Ignored(ctx, other, late); on {
		t.Fatal("someone else sees the letter ignored")
	}
	must(s.Act(ctx, anna, []string{late}, "unignore", ""))
	for _, subj := range []string{"Topic", "Re: Topic", "Re: Re: Topic"} {
		if _, role := where(subj); role != "inbox" {
			t.Fatalf("%q is in %q after stopping to ignore", subj, role)
		}
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM mail_ignored`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d conversations ignored still", n)
	}

	// Outlook's quick steps, made once
	defaults := []QuickStep{{Name: "Готово", Steps: []Step{{Do: "read"}, {Do: "done"}, {Do: "archive"}}},
		{Name: "Ответить и удалить", Steps: []Step{{Do: "reply"}, {Do: "delete"}}}}
	list, err := s.QuickSteps(ctx, anna, defaults...)
	must(err)
	if len(list) != 2 || list[0].Name != "Готово" || len(list[0].Steps) != 3 {
		t.Fatalf("defaults = %+v", list)
	}
	if st, ok := list[1].Reply(); !ok || st.Do != "reply" || !list[1].Files() {
		t.Fatalf("«Ответить и удалить»: %+v %v", st, ok)
	}
	must(s.DeleteQuickStep(ctx, anna, list[1].ID))
	if list, _ = s.QuickSteps(ctx, anna, defaults...); len(list) != 1 {
		t.Fatalf("a deleted default came back: %+v", list)
	}
	if err := s.DeleteQuickStep(ctx, other, list[0].ID); err != ErrNotFound {
		t.Fatalf("someone else's quick step deleted: %v", err)
	}

	// «Готово»: read, complete, archived
	other1, _ := where("Other")
	must(s.DoQuickStep(ctx, anna, []string{other1}, list[0]))
	var seen, done bool
	must(pool.QueryRow(ctx, `SELECT x.seen, x.flag_done, f.role FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id
		WHERE x.subject = 'Other'`).Scan(&seen, &done, &role))
	if !seen || !done || role != "archive" {
		t.Fatalf("«Готово»: seen %v, done %v, in %q", seen, done, role)
	}

	// a quick step of one's own: checked, then a category and a folder
	cat, err := s.SaveCategory(ctx, anna, "", "Клиенты", "green")
	must(err)
	folders, err := s.Folders(ctx, anna, box)
	must(err)
	var inbox, trash string
	for _, f := range folders {
		switch f.Role {
		case "inbox":
			inbox = f.ID
		case "trash":
			trash = f.ID
		}
	}
	for _, bad := range []QuickStep{
		{Name: "", Steps: []Step{{Do: "read"}}},
		{Name: "Empty"},
		{Name: "Two", Steps: []Step{{Do: "archive"}, {Do: "delete"}}},
		{Name: "Replies", Steps: []Step{{Do: "reply"}, {Do: "replyall"}}},
		{Name: "Folder", Steps: []Step{{Do: "move", Arg: "00000000-0000-0000-0000-000000000000"}}},
		{Name: "Forward", Steps: []Step{{Do: "forward", Arg: "not an address"}}},
		{Name: "What", Steps: []Step{{Do: "explode"}}},
	} {
		if _, err := s.SaveQuickStep(ctx, anna, bad); err == nil {
			t.Fatalf("%+v saved", bad)
		}
	}
	if _, err := s.SaveQuickStep(ctx, other, QuickStep{Name: "Cat", Steps: []Step{{Do: "category", Arg: cat}}}); err == nil {
		t.Fatal("someone else's category taken")
	}
	id, err := s.SaveQuickStep(ctx, anna, QuickStep{Name: "Клиентам", Steps: []Step{{Do: "move", Arg: trash}, {Do: "category", Arg: cat}, {Do: "pin"}}})
	must(err)
	q, err := s.QuickStepOf(ctx, anna, id)
	must(err)
	if q.Pos != 1 || len(q.Steps) != 3 {
		t.Fatalf("saved = %+v", q)
	}
	must(s.MoveQuickStep(ctx, anna, id, -1))
	if list, _ = s.QuickSteps(ctx, anna); list[0].ID != id {
		t.Fatalf("moved up: %+v", list)
	}
	topic, _ := where("Topic")
	must(s.DoQuickStep(ctx, anna, []string{topic}, q))
	var pinned bool
	var cats []string
	var folder string
	must(pool.QueryRow(ctx, `SELECT pinned, categories::text[], folder_id::text FROM mail_messages WHERE subject = 'Topic'`).Scan(&pinned, &cats, &folder))
	if !pinned || len(cats) != 1 || cats[0] != cat || folder != trash || folder == inbox {
		t.Fatalf("the quick step: pinned %v, categories %v, folder %s (trash %s)", pinned, cats, folder, trash)
	}
}
