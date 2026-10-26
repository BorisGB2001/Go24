package mail

import (
	"context"
	"errors"
	"mime"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestChoices(t *testing.T) {
	for in, want := range map[string]string{
		" Утвердить ; Отклонить;; утвердить ": "Утвердить;Отклонить",
		"Да;Нет;Возможно":                     "Да;Нет;Возможно",
		"one\r\ntwo;":                         "one  two",
		"":                                    "",
		";;":                                  "",
		"1;2;3;4;5;6;7;8;9;10;11":             "1;2;3;4;5;6;7;8;9;10",
	} {
		if got := Choices(in); got != want {
			t.Errorf("Choices(%q) = %q, want %q", in, got, want)
		}
	}
	opts := []string{"Утвердить", "Отклонить"}
	for _, c := range []struct{ vote, subject, want string }{
		{"утвердить", "что угодно", "Утвердить"},
		{"", "Отклонить: Бюджет", "Отклонить"},
		{"", "отклонить: Бюджет", "Отклонить"},
		{"", "Re: Бюджет", ""},
		{"", "Отклонить", ""},
		{"Может быть", "Утвердить: Бюджет", ""}, // its own choice is not one of them: the subject does not count
	} {
		if got := choiceOf(opts, c.vote, c.subject); got != c.want {
			t.Errorf("choiceOf(%q, %q) = %q, want %q", c.vote, c.subject, got, c.want)
		}
	}
	p := parseFull(strings.NewReader("From: a@x.test\r\nX-Go24-Voting: =?utf-8?q?=D0=94=D0=B0;=D0=9D=D0=B5=D1=82?=\r\nX-Go24-Vote: Да\r\n" +
		"Content-Type: text/plain\r\n\r\nx\r\n"))
	if p.voting != "Да;Нет" || p.vote != "Да" {
		t.Fatalf("parsed voting %q, vote %q", p.voting, p.vote)
	}
	l := Letter{Subject: "S", Text: "x", Voting: "Да;Нет", Vote: "Да"}
	msg, _ := compose(envelope{From: mail.Address{Address: "a@x.test"}}, l)
	if p := parseFull(strings.NewReader(string(msg))); p.voting != "Да;Нет" || p.vote != "Да" {
		t.Fatalf("sent voting %q, vote %q", p.voting, p.vote)
	}
}

// TestVoting runs against GreenMail and the test database (as TestComposeSend): a letter with voting buttons sent
// from a draft, the buttons in the recipient's letter, a vote sent at once, an answer from another mail program
// counted by its subject, «Отслеживание» with the last answer of each person; nobody else votes the user's letters.
func TestVoting(t *testing.T) {
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
	var vera, oleg string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('vera-v', 'Вера', 'vera.v@go24.test') RETURNING id`).Scan(&vera))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('oleg-v', 'Олег', 'oleg.v@go24.test') RETURNING id`).Scan(&oleg))
	s := NewStore(pool, "test-secret")
	s.Tr = func(key string, args ...any) string { return key + ":" + args[0].(string) }
	vbox, err := s.Add(ctx, vera, Settings{Email: "vera.v@go24.test", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	obox, err := s.Add(ctx, oleg, Settings{Email: "oleg.v@go24.test", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)

	id, err := s.SaveDraft(ctx, vera, Draft{MailboxID: vbox, To: "oleg.v@go24.test", CC: "pavel@people.test", Subject: "Бюджет",
		HTML: "<p>Утверждаем?</p>", Voting: " Утвердить; Отклонить;"})
	must(err)
	if d, err := s.Draft(ctx, vera, id); err != nil || d.Voting != "Утвердить;Отклонить" {
		t.Fatalf("draft = %+v, %v", d, err)
	}
	must(s.SendDraft(ctx, vera, id))
	got := inbox(t, host, "pavel@people.test")
	if len(got) != 1 || !strings.Contains(got[0], "X-Go24-Voting: ") || !strings.Contains(got[0], "mail.vote.text") {
		t.Fatalf("pavel got %q", got)
	}

	inboxOf := func(user, box string) map[string]Message {
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
	letter := inboxOf(oleg, obox)["inbox/Бюджет"]
	if strings.Join(letter.Options(), "|") != "Утвердить|Отклонить" {
		t.Fatalf("oleg's letter = %+v", letter)
	}
	if err := s.Vote(ctx, vera, letter.ID, "Утвердить", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else voted: %v", err)
	}
	if err := s.Vote(ctx, oleg, letter.ID, "Может быть", ""); err == nil {
		t.Fatal("voted for no choice")
	}
	must(s.Vote(ctx, oleg, letter.ID, "утвердить", "Согласен"))
	if v, err := s.Votes(ctx, oleg, letter.ID); err != nil || v.Mine != "Утвердить" {
		t.Fatalf("oleg's vote = %+v, %v", v, err)
	}
	if m, _ := s.Message(ctx, oleg, letter.ID); m.Answered != "reply" {
		t.Fatalf("the letter is not answered: %q", m.Answered)
	}

	sent := inboxOf(vera, vbox)["sent/Бюджет"]
	if sent.Voting != "Утвердить;Отклонить" {
		t.Fatalf("vera's sent letter = %+v", sent)
	}
	// pavel answers from another mail program: only the subject says the choice
	must(smtp.SendMail(host+":3025", nil, "pavel@people.test", []string{"vera.v@go24.test"},
		[]byte("From: pavel@people.test\r\nTo: vera.v@go24.test\r\nSubject: "+mime.QEncoding.Encode("utf-8", "Отклонить: Бюджет")+"\r\nIn-Reply-To: <"+sent.MessageID+">\r\n"+
			"Content-Type: text/plain\r\n\r\nДорого\r\n")))
	box := inboxOf(vera, vbox)
	if box["inbox/Утвердить: Бюджет"].Vote != "Утвердить" {
		t.Fatalf("vera's inbox = %+v", box)
	}
	tr, err := s.Votes(ctx, vera, sent.ID)
	must(err)
	if tr.Asked != 2 || len(tr.Answers) != 2 || tr.Count["Утвердить"] != 1 || tr.Count["Отклонить"] != 1 || tr.Mine != "" {
		t.Fatalf("tracking = %+v", tr)
	}
	// oleg changes his mind: his last answer counts
	must(s.Vote(ctx, oleg, letter.ID, "Отклонить", ""))
	inboxOf(vera, vbox)
	tr, err = s.Votes(ctx, vera, sent.ID)
	must(err)
	if len(tr.Answers) != 2 || tr.Count["Отклонить"] != 2 || tr.Count["Утвердить"] != 0 || tr.Answers[0].Choice != "Отклонить" {
		t.Fatalf("tracking after the change = %+v", tr)
	}
	if _, err := s.Votes(ctx, oleg, sent.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else sees the tracking: %v", err)
	}
}
