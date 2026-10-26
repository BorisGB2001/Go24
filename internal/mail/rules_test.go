package mail

import (
	"context"
	"fmt"
	"net/mail"
	"net/smtp"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestRuleMatch(t *testing.T) {
	f := facts{from: "Ivan@Client.test", name: "Иван Петров", to: "me@go24.test", cc: "boss@go24.test", subject: "Счёт №5 за октябрь",
		body: "Оплатите до пятницы", me: "me@go24.test", attach: true, importance: 1}
	for _, c := range []struct {
		r    Rule
		want bool
	}{
		{Rule{}, true}, // no conditions: every letter
		{Rule{If: Cond{From: "ivan@client.test"}}, true},
		{Rule{If: Cond{From: "петров"}}, true}, // the name too
		{Rule{If: Cond{From: "anna@x, client.test"}}, true},
		{Rule{If: Cond{From: "anna@x"}}, false},
		{Rule{If: Cond{Subject: "счёт", Attach: true, Importance: "high"}}, true},
		{Rule{If: Cond{Subject: "счёт", Importance: "low"}}, false}, // all of them must hold
		{Rule{If: Cond{Body: "пятниц"}}, true},
		{Rule{If: Cond{To: "boss@"}}, true},
		{Rule{If: Cond{ToMe: true}}, true},
		{Rule{If: Cond{CcMe: true}}, false},
		{Rule{If: Cond{OnlyMe: true}}, false}, // boss is in Cc
		{Rule{If: Cond{From: "ivan"}, Except: Cond{Subject: "октябрь"}}, false},
		{Rule{If: Cond{From: "ivan"}, Except: Cond{Subject: "ноябрь", CcMe: true}}, true}, // no exception holds
	} {
		if got := c.r.matches(f); got != c.want {
			t.Errorf("%+v: %v, want %v", c.r, got, c.want)
		}
	}
	only := facts{to: "Me <ME@go24.test>", me: "me@go24.test"}
	if !(Rule{If: Cond{OnlyMe: true, ToMe: true}}).matches(only) {
		t.Error("only me")
	}
	if !(Cond{}).Empty() || (Cond{Attach: true}).Empty() || !(Do{}).Empty() || (Do{Forward: "a@b"}).Empty() {
		t.Error("Empty")
	}
}

func TestOOOText(t *testing.T) {
	o := OOO{Internal: "in", External: "out", Outside: "all"}
	for _, c := range []struct {
		outside       string
		inside, wrote bool
		want          string
		ok            bool
	}{
		{"all", true, false, "in", true},
		{"all", false, false, "out", true},
		{"none", false, true, "", false},
		{"none", true, false, "in", true},
		{"contacts", false, false, "", false},
		{"contacts", false, true, "out", true},
	} {
		o.Outside = c.outside
		if got, ok := o.text(c.inside, c.wrote); got != c.want || ok != c.ok {
			t.Errorf("%+v: %q %v", c, got, ok)
		}
	}
	o.External, o.Outside = "", "all"
	if got, _ := o.text(false, false); got != "in" {
		t.Errorf("no outside text: %q", got)
	}
}

func TestComposeAuto(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	to, _ := addresses("x@y.test")
	msg, _ := compose(envelope{From: mailAddr("Ivan", "me@go24.test"), To: to, Host: "go24.test", Now: now, Auto: "auto-replied",
		ReplyTo: ptrAddr("Ivan", "ivan@client.test"), ResentFrom: ptrAddr("", "me@go24.test")},
		Letter{Subject: "Re", Text: "hi", Files: []Attachment{{Name: "Счёт.eml", ContentType: "message/rfc822", Data: []byte("From: a@b\r\n\r\nx\r\n")}}})
	s := string(msg)
	for _, want := range []string{"Auto-Submitted: auto-replied\r\n", "X-Auto-Response-Suppress: All\r\n", "Reply-To: \"Ivan\" <ivan@client.test>\r\n",
		"Resent-From: <me@go24.test>\r\n", "Resent-To: <x@y.test>\r\n", "Content-Type: message/rfc822", "Content-Transfer-Encoding: 7bit", "From: a@b"} {
		if !strings.Contains(s, want) {
			t.Errorf("no %q in\n%s", want, s)
		}
	}
	if fwdSubject("Fwd: x") != "Fwd: x" || fwdSubject("x") != "FW: x" {
		t.Error("fwdSubject")
	}
}

// TestRulesArrive runs against GreenMail and the test database (as TestMailbox): the letters that come after the
// first fetch get the automatic reply (once per sender, none to a newsletter, the text for outside senders) and go
// through the rules (category, flag, move with «Прекратить обработку», forward, forward as an attachment, redirect,
// delete; an exception keeps a letter off a rule); «Запустить сейчас»; «Очистка» now and standing.
func TestRulesArrive(t *testing.T) {
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
	addr, boss, shop, fwd := fmt.Sprintf("rob%d@go24.test", n), fmt.Sprintf("boss%d@client.test", n), fmt.Sprintf("news%d@shop.test", n),
		fmt.Sprintf("fwd%d@other.test", n)
	var rob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('rob', 'Роб', $1) RETURNING id`, addr).Scan(&rob))
	s := NewStore(pool, "test-secret")
	s.Tr = func(key string, args ...any) string { return fmt.Sprintf(key+"|%v", args) }
	var logged []error
	s.Log = func(what, id string, err error) { logged = append(logged, fmt.Errorf("%s: %w", what, err)) }
	send := func(from, raw string) { must(smtp.SendMail(host+":3025", nil, from, []string{addr}, []byte(raw))) }
	send(boss, "From: "+boss+"\r\nTo: "+addr+"\r\nSubject: Old\r\nDate: Mon, 19 Oct 2026 10:00:00 +0200\r\nMessage-ID: <old@client.test>\r\n\r\nold one\r\n")
	box, err := s.Add(ctx, rob, Settings{Email: addr, IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box)) // the first fetch: no rules, no replies
	in, err := s.folderByRole(ctx, box, "inbox")
	must(err)
	fid, err := s.CreateFolder(ctx, rob, box, "", "Bills")
	must(err)
	cats, err := s.Categories(ctx, rob, [2]string{"red", "Важное"})
	must(err)

	// automatic replies: on, a text for outside senders
	must(s.SaveOOO(ctx, rob, OOO{Mode: OOOOn, Internal: "Я в отпуске", External: "I am away", Outside: "all"}))
	// rules: bills → category, flag, move to Bills, stop; anything from the boss but «urgent» → forward three ways
	_, err = s.SaveRule(ctx, rob, Rule{Name: "Bills", Enabled: true, Stop: true, If: Cond{Subject: "invoice"},
		Do: Do{Category: cats[0].ID, Flag: true, Move: fid}})
	must(err)
	_, err = s.SaveRule(ctx, rob, Rule{Name: "Boss", Enabled: true, If: Cond{From: boss}, Except: Cond{Subject: "urgent"},
		Do: Do{Forward: fwd, ForwardAttach: fwd, Redirect: fwd, Read: true}})
	must(err)
	_, err = s.SaveRule(ctx, rob, Rule{Name: "Shop", Enabled: true, If: Cond{From: "shop.test"}, Do: Do{Delete: true}})
	must(err)
	_, err = s.SaveRule(ctx, rob, Rule{Name: "Off", Enabled: false, If: Cond{}, Do: Do{Pin: true}})
	must(err)
	if _, err := s.SaveRule(ctx, rob, Rule{Name: "Nothing", If: Cond{From: "x"}}); err == nil {
		t.Fatal("a rule without actions")
	}

	send(boss, "From: "+boss+"\r\nTo: "+addr+"\r\nSubject: Invoice 5\r\nDate: Mon, 19 Oct 2026 10:01:00 +0200\r\nMessage-ID: <inv@client.test>\r\n\r\npay\r\n")
	send(boss, "From: "+boss+"\r\nTo: "+addr+"\r\nSubject: Hello\r\nDate: Mon, 19 Oct 2026 10:02:00 +0200\r\nMessage-ID: <hello@client.test>\r\n\r\nhow are you\r\n")
	send(boss, "From: "+boss+"\r\nTo: "+addr+"\r\nSubject: Urgent thing\r\nDate: Mon, 19 Oct 2026 10:03:00 +0200\r\nMessage-ID: <urg@client.test>\r\n\r\nnow\r\n")
	send(shop, "From: "+shop+"\r\nTo: "+addr+"\r\nSubject: Sale\r\nDate: Mon, 19 Oct 2026 10:04:00 +0200\r\nList-Unsubscribe: <mailto:x@shop.test>\r\nMessage-ID: <sale@shop.test>\r\n\r\n-50%\r\n")
	must(s.Sync(ctx, box))
	if len(logged) > 0 {
		t.Fatalf("logged: %v", logged)
	}
	list, err := s.List(ctx, rob, in.ID, "", false)
	must(err)
	subjects := map[string]Message{}
	for _, m := range list {
		subjects[m.Subject] = m
	}
	if _, ok := subjects["Invoice 5"]; ok || len(list) != 3 || !subjects["Hello"].Seen || subjects["Urgent thing"].Seen || subjects["Old"].Seen ||
		subjects["Hello"].Pinned {
		t.Fatalf("inbox = %+v", list)
	}
	bills, err := s.List(ctx, rob, fid, "", false)
	must(err)
	if len(bills) != 1 || bills[0].Subject != "Invoice 5" || !bills[0].Flagged || len(bills[0].Categories) != 1 {
		t.Fatalf("bills = %+v", bills)
	}
	trash, err := s.folderByRole(ctx, box, "trash")
	must(err)
	if gone, _ := s.List(ctx, rob, trash.ID, "", false); len(gone) != 1 || gone[0].Subject != "Sale" {
		t.Fatalf("trash = %+v", gone)
	}

	// the boss got one automatic reply (three letters, once per period); the shop none
	replies := inbox(t, host, boss)
	if len(replies) != 1 || !strings.Contains(replies[0], "Auto-Submitted: auto-replied") || !strings.Contains(replies[0], "I am away") ||
		!strings.Contains(replies[0], "mail.ooo.subject|[") {
		t.Fatalf("replies to the boss = %q", replies)
	}
	if got := inbox(t, host, shop); len(got) != 0 {
		t.Fatalf("a newsletter answered: %q", got)
	}
	got := strings.Join(inbox(t, host, fwd), "\n=====\n")
	if strings.Count(got, "Auto-Submitted: auto-forwarded") != 3 || !strings.Contains(got, "Subject: FW: Hello") ||
		!strings.Contains(got, "message/rfc822") || !strings.Contains(got, "Resent-From:") || !strings.Contains(got, "Reply-To: <"+boss+">") ||
		strings.Contains(got, "Urgent") {
		t.Fatalf("forwarded = %s", got)
	}

	// «Запустить сейчас»: the rule that is off, on «Bills»
	rules, err := s.Rules(ctx, rob)
	must(err)
	if len(rules) != 4 || rules[3].Name != "Off" {
		t.Fatalf("rules = %+v", rules)
	}
	must(s.MoveRule(ctx, rob, rules[3].ID, -1))
	if rules, _ = s.Rules(ctx, rob); rules[2].Name != "Off" {
		t.Fatalf("moved = %+v", rules)
	}
	k, err := s.RunRules(ctx, rob, fid, []string{rules[2].ID})
	must(err)
	if bills, _ = s.List(ctx, rob, fid, "", false); k != 1 || !bills[0].Pinned {
		t.Fatalf("run now: %d %+v", k, bills)
	}
	if _, err := s.RunRules(ctx, rob, fid, nil); err != nil { // those on: nothing more happens to the letter
		t.Fatal(err)
	}

	// «Очистка»: keep the boss's latest letter only, standing (a pinned letter stays)
	must(s.Act(ctx, rob, []string{subjects["Hello"].ID}, "pin", ""))
	k, err = s.SweepNow(ctx, rob, []string{subjects["Hello"].ID}, "latest", "archive", 0)
	must(err)
	if list, _ = s.List(ctx, rob, in.ID, "", false); k != 1 || len(list) != 2 { // the pinned letter stays
		t.Fatalf("sweep: %d %+v", k, list)
	}
	sw, err := s.Sweeps(ctx, rob)
	must(err)
	if len(sw) != 1 || sw[0].Mode != "latest" || sw[0].Sender != boss {
		t.Fatalf("sweeps = %+v", sw)
	}
	must(s.SaveOOO(ctx, rob, OOO{Mode: OOOOff, Outside: "all"}))
	send(boss, "From: "+boss+"\r\nTo: "+addr+"\r\nSubject: Urgent again\r\nDate: Mon, 19 Oct 2026 10:05:00 +0200\r\nMessage-ID: <urg2@client.test>\r\n\r\nnow\r\n")
	must(s.Sync(ctx, box))
	if errs := s.SweepDue(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	list, _ = s.List(ctx, rob, in.ID, "", false)
	var left []string
	for _, m := range list {
		left = append(left, m.Subject)
	}
	if strings.Join(left, ",") != "Hello,Urgent again" { // the pinned one and the latest
		t.Fatalf("after the standing sweep: %v", left)
	}
	if len(inbox(t, host, boss)) != 1 {
		t.Fatal("a reply while off")
	}
	must(s.DropSweep(ctx, rob, sw[0].ID))
}

func mailAddr(name, addr string) mail.Address { return mail.Address{Name: name, Address: addr} }
func ptrAddr(name, addr string) *mail.Address { return &mail.Address{Name: name, Address: addr} }
