package mail

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestSenderAuth(t *testing.T) {
	for _, c := range []struct{ results, from, verdict, via string }{
		{"mx.google.com; dkim=pass header.i=@client.test header.s=s1 header.b=abc; spf=pass (google.com: domain of a@client.test designates 1.2.3.4) smtp.mailfrom=a@client.test; dmarc=pass (p=NONE sp=NONE dis=NONE) header.from=client.test",
			"a@client.test", AuthPass, ""},
		{"mx.google.com; spf=softfail (domain of transitioning x@bank.test) smtp.mailfrom=x@bank.test; dmarc=fail (p=REJECT) header.from=bank.test", "x@bank.test", AuthFail, ""},
		{"mx.yandex.ru; spf=pass (mx.yandex.ru: domain of mail.sender.test designates 5.6.7.8) smtp.mailfrom=bounce@mail.sender.test; dkim=pass header.i=@sender.test", "news@sender.test", AuthPass, ""}, // relaxed alignment
		{"mx.test; spf=pass smtp.mailfrom=bounce@esp.test; dkim=pass header.d=esp.test", "shop@brand.test", "", "esp.test"},                                                                               // «через» the mailing service
		{"mx.test; spf=fail smtp.mailfrom=boss@company.test; dkim=none", "boss@company.test", AuthFail, ""},
		{"mx.test; spf=none smtp.mailfrom=a@b.test; dkim=none", "a@b.test", "", ""},
		{"spf.protection.outlook.com; spf=pass smtp.mailfrom=x.test; dkim=none; dmarc=none action=none header.from=y.test;compauth=fail reason=001", "a@y.test", AuthFail, ""},
		{"mx.test (comment; with semicolon); dkim=pass (2048-bit key; unprotected) header.d=a.test", "u@a.test", AuthPass, ""},
		{"", "a@b.test", "", ""},
	} {
		if v, via := senderAuth(c.results, c.from); v != c.verdict || via != c.via {
			t.Errorf("senderAuth(%q, %s) = %q, %q; want %q, %q", c.results, c.from, v, via, c.verdict, c.via)
		}
	}
	// the topmost Authentication-Results is the receiving server's; a forged one below it does not count
	p := parseFull(strings.NewReader("Authentication-Results: mx.test; dmarc=fail header.from=bank.test\r\nAuthentication-Results: evil; dmarc=pass\r\n" +
		"From: x@bank.test\r\nSubject: s\r\n\r\ntext"))
	if v, _ := senderAuth(p.authResults, "x@bank.test"); v != AuthFail {
		t.Errorf("topmost: %q from %q", v, p.authResults)
	}
}

func TestLookalike(t *testing.T) {
	fam := []string{"go24.test", "client.ru", "sberbank.ru", "ya.ru"}
	for d, want := range map[string]string{
		"go24.test":          "",            // the same
		"mail.client.ru":     "",            // a subdomain of a familiar one
		"c1ient.ru":          "client.ru",   // 1 for l
		"sberbank.ru.test":   "",            // another registered domain, not one letter off
		"sberbamk.ru":        "sberbank.ru", // one letter changed
		"sbrebank.ru":        "sberbank.ru", // two swapped
		"xn--sberbnk-6fg.ru": "sberbank.ru", // Cyrillic «а» in punycode
		"yb.ru":              "",            // short ones are not compared letter by letter
		"other.test":         "",
	} {
		if got := lookalike(d, fam); got != want {
			t.Errorf("lookalike(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestCheckSenderReport(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
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
	var bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@go24.test') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	box, err := s.Add(ctx, bob, Settings{Email: "bob@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	folders := map[string]string{}
	for _, role := range []string{"inbox", "sent", "junk", "trash"} {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $2, true) RETURNING id::text`, box, role).Scan(&id))
		folders[role] = id
	}
	uid := int64(0)
	letter := func(folder, from, to, auth string) string {
		t.Helper()
		uid++
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, subject, body, sent_at, message_id, sender_auth)
			VALUES ($1, $2, $3, $4, $5, 'Тема', 'Текст', now(), $6, $7) RETURNING id::text`, box, folders[folder], uid, from, to, "m"+from+to, auth).Scan(&id))
		return id
	}
	check := func(id string) SenderCheck {
		t.Helper()
		m, err := s.message(ctx, bob, id)
		must(err)
		c, err := s.CheckSender(ctx, bob, m)
		must(err)
		return c
	}
	letter("sent", "bob@go24.test", `"Клиент" <ann@client.test>`, "")
	known := letter("inbox", "ann@client.test", "bob@go24.test", AuthPass) // the user wrote to Анна
	if c := check(known); c.Any() || c.Auth != AuthPass {
		t.Errorf("a known sender: %+v", c)
	}
	fake := letter("inbox", "ann@c1ient.test", "bob@go24.test", AuthFail)
	if c := check(fake); !c.First || c.Like != "client.test" || c.Auth != AuthFail {
		t.Errorf("a lookalike: %+v", c)
	}
	if c := check(letter("inbox", "eve@go24.test", "bob@go24.test", "")); c.Any() { // a colleague
		t.Errorf("a colleague: %+v", c)
	}
	newOne := letter("inbox", "new@other.test", "bob@go24.test", "")
	if c := check(newOne); !c.First || c.Like != "" {
		t.Errorf("a first letter: %+v", c)
	}
	letter("inbox", "new@other.test", "bob@go24.test", "")
	if c := check(newOne); c.First { // the second letter from the address
		t.Errorf("the second letter: %+v", c)
	}
	if c := check(letter("sent", "bob@go24.test", "x@y.test", "")); c.Any() {
		t.Errorf("an own letter: %+v", c)
	}
	// «Сообщить о фишинге»: recorded, to «Удалённые»; no reporting address — no copy to send
	note := func(m Message) ReportNote { return ReportNote{Subject: "Phishing: " + m.Subject} }
	if _, _, err := s.ReportLetters(ctx, eve, []string{fake}, "phishing", note); err != ErrNotFound {
		t.Fatalf("another's letter reported: %v", err)
	}
	n, send, err := s.ReportLetters(ctx, bob, []string{fake}, "phishing", note)
	must(err)
	must(send(ctx))
	var role string
	must(pool.QueryRow(ctx, `SELECT f.role FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.id = $1`, fake).Scan(&role))
	if n != 1 || role != "trash" {
		t.Errorf("reported phishing: %d, in %s", n, role)
	}
	// «Сообщить о нежелательной почте» with the reporting address set: to «Нежелательная почта», recorded with it
	if err := s.SetReportTo(ctx, bob, "not an address"); err == nil {
		t.Error("a bad reporting address is kept")
	}
	must(s.SetReportTo(ctx, bob, "Безопасность <security@go24.test>"))
	if to, _ := s.ReportTo(ctx); to != "security@go24.test" {
		t.Errorf("ReportTo = %q", to)
	}
	_, _, err = s.ReportLetters(ctx, bob, []string{newOne}, "junk", note)
	must(err)
	must(pool.QueryRow(ctx, `SELECT f.role FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.id = $1`, newOne).Scan(&role))
	rs, err := s.Reports(ctx, 10)
	must(err)
	if role != "junk" || len(rs) != 2 || rs[0].Kind != "junk" || rs[0].SentTo != "security@go24.test" || rs[0].User != "Борис" || rs[1].Kind != "phishing" || rs[1].FromAddr != "ann@c1ient.test" {
		t.Errorf("reports: %s, %+v", role, rs)
	}
	if _, _, err := s.ReportLetters(ctx, bob, []string{known}, "spam", note); err == nil {
		t.Error("an unknown kind")
	}
}
