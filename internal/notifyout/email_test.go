package notifyout

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/notify"
)

func TestParseSMTP(t *testing.T) {
	for raw, want := range map[string]smtpServer{
		"smtps://notify@company.ru@smtp.company.ru":        {host: "smtp.company.ru", port: "465", login: "notify@company.ru", from: "notify@company.ru", implicit: true},
		"smtp://robot@mail.lan:2525?from=go24@company.ru":  {host: "mail.lan", port: "2525", login: "robot", from: "go24@company.ru"},
		"smtp://mail.lan?from=go24@company.ru":             {host: "mail.lan", port: "587", from: "go24@company.ru"},
		"smtps://notify%40company.ru@smtp.company.ru:465/": {host: "smtp.company.ru", port: "465", login: "notify@company.ru", from: "notify@company.ru", implicit: true},
	} {
		if got, err := parseSMTP(raw); err != nil || got != want {
			t.Errorf("%s: %+v %v", raw, got, err)
		}
	}
	for _, raw := range []string{"https://smtp.company.ru", "smtp://robot@mail.lan", "smtps://", "smtp://a@b?from=x y@z"} {
		if _, err := parseSMTP(raw); err == nil {
			t.Errorf("%s: taken", raw)
		}
	}
}

// fakeSMTP is a mail server on the machine itself: login «robot», password «good»; gone@… is refused.
type fakeSMTP struct {
	ln   net.Listener
	mu   sync.Mutex
	sent []string // to: raw letter
}

func newFakeSMTP(t *testing.T) *fakeSMTP {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSMTP{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r, w := bufio.NewReader(c), bufio.NewWriter(c)
	say := func(s string) { _, _ = w.WriteString(s + "\r\n"); _ = w.Flush() }
	say("220 fake ESMTP")
	authed := false
	var to string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.TrimSpace(line)
		switch up := strings.ToUpper(cmd); {
		case strings.HasPrefix(up, "EHLO"):
			say("250-fake\r\n250 AUTH PLAIN")
		case strings.HasPrefix(up, "AUTH PLAIN"):
			raw, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(cmd[len("AUTH PLAIN"):]))
			if authed = string(raw) == "\x00robot\x00good"; authed {
				say("235 ok")
			} else {
				say("535 bad credentials")
			}
		case strings.HasPrefix(up, "MAIL FROM:"):
			if !authed {
				say("530 auth first")
			} else {
				say("250 ok")
			}
		case strings.HasPrefix(up, "RCPT TO:"):
			if to = strings.Trim(cmd[len("RCPT TO:"):], "<> "); strings.HasPrefix(to, "gone@") {
				say("550 no such mailbox")
			} else {
				say("250 ok")
			}
		case up == "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.sent = append(f.sent, to+": "+b.String())
			f.mu.Unlock()
			say("250 queued")
		case up == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func (f *fakeSMTP) took() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sent
	f.sent = nil
	return out
}

// TestEmail: the mail server is set by its smtp:// address and the password (a web address or a wrong password
// refused); «Подключить» links the work address at once (none — refused); a notification is mailed only after it has
// stayed unread for the delay — subject and text with the link; an address the server refuses drops the link.
func TestEmail(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@company.ru') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ('ivan', 'Иван') RETURNING id`).Scan(&ivan))
	note := func(kind string, age time.Duration) int64 {
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body, created_at) VALUES ($1, $2, '', now() - $3::interval)
			RETURNING id`, anna, kind, age.String()).Scan(&id))
		return id
	}
	fs := newFakeSMTP(t)
	s := New(pool, "go24 secret")
	s.Channels = []Channel{&Email{Timeout: 5 * time.Second}}
	s.Notify, s.AppURL, s.Log = notify.NewStore(pool), "https://go24.test", slog.New(slog.NewTextHandler(io.Discard, nil))
	s.Text = func(it notify.Item) string { return "Борис поставил задачу [" + it.Kind + "]" }
	s.Tr = func(key string, _ ...any) string { return key }

	server := "smtp://robot@" + fs.ln.Addr().String() + "?from=go24@company.ru"
	if err := s.SaveBot(ctx, "email", true, "https://"+fs.ln.Addr().String(), "good", false, anna); err == nil {
		t.Fatal("a web address was kept")
	}
	if err := s.SaveBot(ctx, "email", true, server, "bad", false, anna); err == nil {
		t.Fatal("a wrong password was kept")
	}
	must(s.SaveBot(ctx, "email", true, server, "good", false, anna))
	if b, err := s.Bot(ctx, "email"); err != nil || !b.Ready() || b.Name != "go24@company.ru" {
		t.Fatalf("the server: %+v %v", b, err)
	}
	if _, err := s.StartLink(ctx, ivan, "email"); err == nil {
		t.Fatal("linked without an address")
	}
	note("comment", time.Hour) // before the link: not mailed
	l, err := s.StartLink(ctx, anna, "email")
	must(err)
	if !l.Linked || l.Account != "anna@company.ru" || l.Code != "" {
		t.Fatalf("the link: %+v", l)
	}
	fresh := note("comment", 0)
	read := note("status", 0)
	must(s.Notify.MarkRead(ctx, anna, read))
	s.Pass(ctx)
	if got := fs.took(); len(got) != 0 {
		t.Fatalf("mailed before the delay: %q", got)
	}
	must2 := func(_ any, err error) { must(err) }
	must2(pool.Exec(ctx, `UPDATE notifications SET created_at = created_at - interval '11 minutes' WHERE id IN ($1, $2)`, fresh, read))
	s.Pass(ctx)
	got := fs.took()
	if len(got) != 1 || !strings.HasPrefix(got[0], "anna@company.ru: ") {
		t.Fatalf("mailed: %q", got)
	}
	m, err := mail.ReadMessage(strings.NewReader(strings.TrimPrefix(got[0], "anna@company.ru: ")))
	must(err)
	subject, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	body, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	if subject != "Борис поставил задачу [comment]" || m.Header.Get("From") != "go24 <go24@company.ru>" ||
		string(body) != "Борис поставил задачу [comment]\r\n\r\nhttps://go24.test/notifications/"+strconv.FormatInt(fresh, 10) {
		t.Fatalf("the letter: %q %q %q", subject, m.Header.Get("From"), body)
	}
	s.Pass(ctx)
	if got := fs.took(); len(got) != 0 {
		t.Fatalf("mailed again: %q", got)
	}

	must2(pool.Exec(ctx, `UPDATE users SET email = 'gone@company.ru' WHERE id = $1`, anna))
	_, err = s.StartLink(ctx, anna, "email")
	must(err)
	note("comment", time.Hour)
	s.Pass(ctx)
	if links, _ := s.Links(ctx, anna); len(links) != 1 || links[0].Linked {
		t.Fatalf("an address the server refuses kept the link: %+v", links)
	}
}
