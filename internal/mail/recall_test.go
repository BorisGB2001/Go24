package mail

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

// TestRecall runs against GreenMail and the test database (as TestVoting): a letter recalled from its people — the
// unread copy deleted on the server and its owner told, the read one left, an outside address not reached; the
// results kept; nobody but the sender recalls it. «Отправить повторно» sends it again as a new letter.
func TestRecall(t *testing.T) {
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
	var vera, oleg, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('vera-r', 'Вера', 'vera.r@go24.test') RETURNING id`).Scan(&vera))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('oleg-r', 'Олег', 'oleg.r@go24.test') RETURNING id`).Scan(&oleg))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna-r', 'Анна', 'anna.r@go24.test') RETURNING id`).Scan(&anna))
	s := NewStore(pool, "test-secret")
	s.Tr = func(key string, args ...any) string { return key }
	add := func(user, email string) string {
		id, err := s.Add(ctx, user, Settings{Email: email, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
		must(err)
		return id
	}
	vbox, obox, abox := add(vera, "vera.r@go24.test"), add(oleg, "oleg.r@go24.test"), add(anna, "anna.r@go24.test")
	must(s.Send(ctx, vera, vbox, "oleg.r@go24.test, anna.r@go24.test, pavel.r@people.test", "Отчёт", "Черновой вариант"))

	letters := func(user, box string) map[string]Message {
		t.Helper()
		must(s.Sync(ctx, box))
		folders, err := s.Folders(ctx, user, box)
		must(err)
		out := map[string]Message{}
		for _, f := range folders {
			list, err := s.Messages(ctx, user, f.ID, "")
			must(err)
			for _, m := range list {
				out[f.Role+"/"+m.Subject] = m
			}
		}
		return out
	}
	if _, ok := letters(oleg, obox)["inbox/Отчёт"]; !ok {
		t.Fatal("oleg got no letter")
	}
	read := letters(anna, abox)["inbox/Отчёт"]
	must(s.Act(ctx, anna, []string{read.ID}, "read", ""))
	sent := letters(vera, vbox)["sent/Отчёт"]
	if sent.ID == "" {
		t.Fatal("no sent letter")
	}

	if _, err := s.Recall(ctx, oleg, sent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else recalled it: %v", err)
	}
	if _, err := s.Recall(ctx, oleg, letters(oleg, obox)["inbox/Отчёт"].ID); err == nil {
		t.Fatal("a letter come in was recalled")
	}
	res, err := s.Recall(ctx, vera, sent.ID)
	must(err)
	got := map[string]string{}
	for _, r := range res {
		got[r.Addr] = r.Status
	}
	if len(got) != 3 || got["oleg.r@go24.test"] != RecallDone || got["anna.r@go24.test"] != RecallRead || got["pavel.r@people.test"] != RecallExternal {
		t.Fatalf("recall = %+v", res)
	}
	if _, ok := letters(oleg, obox)["inbox/Отчёт"]; ok {
		t.Fatal("oleg's copy is still there")
	}
	if _, ok := letters(anna, abox)["inbox/Отчёт"]; !ok {
		t.Fatal("anna's read copy is gone")
	}
	var told int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'mail_recall' AND actor_id = $2 AND body = 'Отчёт'`, oleg, vera).Scan(&told))
	if told != 1 {
		t.Fatalf("oleg told %d times", told)
	}
	// recalled again: oleg stays recalled
	res, err = s.Recall(ctx, vera, sent.ID)
	must(err)
	for _, r := range res {
		if r.Addr == "oleg.r@go24.test" && r.Status != RecallDone {
			t.Fatalf("again: %+v", res)
		}
	}
	if list, err := s.Recalls(ctx, oleg, sent.MessageID); err != nil || len(list) != 0 {
		t.Fatalf("oleg sees vera's recall: %+v, %v", list, err)
	}

	// «Отправить повторно»: a new letter, not an answer; the sent one is not marked answered
	id, err := s.SaveDraft(ctx, vera, Draft{MailboxID: vbox, To: sent.To, Subject: sent.Subject, HTML: "<p>Окончательный вариант</p>", ReplyTo: sent.ID, Mode: "resend"})
	must(err)
	must(s.SendDraft(ctx, vera, id))
	again := letters(oleg, obox)["inbox/Отчёт"]
	if again.ID == "" || again.Thread != again.MessageID {
		t.Fatalf("resent letter = %+v", again)
	}
	if m, _ := s.Message(ctx, vera, sent.ID); m.Answered != "" {
		t.Fatalf("the sent letter is marked %q", m.Answered)
	}
}
