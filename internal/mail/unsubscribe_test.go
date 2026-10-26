package mail

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
)

func TestUnsubWays(t *testing.T) {
	for _, c := range []struct {
		header, mailto, web string
		oneClick            bool
		way                 string
	}{
		{"<mailto:leave@shop.test?subject=stop>, <https://shop.test/u?id=1>", "leave@shop.test", "https://shop.test/u?id=1", true, UnsubClick},
		{"<https://shop.test/u>, <mailto:leave@shop.test>", "leave@shop.test", "https://shop.test/u", false, UnsubMail},
		{"<https://shop.test/u>", "", "https://shop.test/u", false, UnsubPage},
		{"<mailto:leave@shop.test>", "leave@shop.test", "", true, UnsubMail},
		{"mailto:leave@shop.test, <javascript:alert(1)>, <ftp://x.test/>", "", "", true, ""},
		{"", "", "", false, ""},
	} {
		mailto, web := unsubWays(c.header)
		got := ""
		if mailto != nil {
			got = mailto.Opaque
		}
		if got != c.mailto || web != c.web || wayOf(mailto, web, c.oneClick) != c.way {
			t.Errorf("%q: mailto %q, web %q, way %q", c.header, got, web, wayOf(mailto, web, c.oneClick))
		}
	}
	for addr, ok := range map[string]bool{"93.184.216.34:443": true, "127.0.0.1:80": false, "192.168.1.15:8080": false, "10.0.0.1:443": false,
		"169.254.169.254:80": false, "[::1]:443": false, "0.0.0.0:80": false} {
		if err := publicOnly("tcp", addr, nil); (err == nil) != ok {
			t.Errorf("%s: %v", addr, err)
		}
	}
	p := parseFull(strings.NewReader("From: shop@x.test\r\nList-Unsubscribe: <https://x.test/u>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n" +
		"Content-Type: text/plain\r\n\r\nSale\r\n"))
	if p.unsub != "<https://x.test/u>" || !p.oneClick {
		t.Fatalf("parsed %q, one click %v", p.unsub, p.oneClick)
	}
}

// TestUnsubscribe runs against GreenMail and the test database (as TestComposeSend): «Отказаться от подписки» in one
// click (the newsletter gets the POST), by a letter to the list's address, on the newsletter's page; a letter of no
// newsletter offers none; nobody else leaves the user's newsletters.
func TestUnsubscribe(t *testing.T) {
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
	var mu sync.Mutex
	var posts []string
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		posts = append(posts, r.Method+" "+r.URL.RequestURI()+" "+string(b))
		mu.Unlock()
	}))
	defer site.Close()
	was := unsubClient
	unsubClient = site.Client() // the test's site is on this computer: publicOnly would refuse it
	defer func() { unsubClient = was }()

	var nina, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('nina-u', 'Нина', 'nina.u@go24.test') RETURNING id`).Scan(&nina))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve-u', 'Ева', 'eve.u@go24.test') RETURNING id`).Scan(&eve))
	s := NewStore(pool, "test-secret")
	letter := func(from, subject, header string) {
		must(smtp.SendMail(host+":3025", nil, from, []string{"nina.u@go24.test"},
			[]byte("From: "+from+"\r\nTo: nina.u@go24.test\r\nSubject: "+subject+"\r\n"+header+"Content-Type: text/plain\r\n\r\nSale\r\n")))
	}
	letter("news@click.test", "Click", "List-Unsubscribe: <mailto:leave@click.test>, <"+site.URL+"/u?id=7>\r\nList-Unsubscribe-Post: List-Unsubscribe=One-Click\r\n")
	letter("news@mailto.test", "Mailto", "List-Unsubscribe: <"+site.URL+"/page>, <mailto:leave.u@go24.test?subject=Stop%20it>\r\n")
	letter("news@page.test", "Page", "List-Unsubscribe: <"+site.URL+"/page?id=9>\r\n")
	letter("friend@people.test", "Hello", "")
	box, err := s.Add(ctx, nina, Settings{Email: "nina.u@go24.test", IMAPHost: host, IMAPPort: 3143, SMTPHost: host, SMTPPort: 3025, Password: "pw"})
	must(err)
	must(s.Sync(ctx, box))
	folders, err := s.Folders(ctx, nina, box)
	must(err)
	in, err := s.Messages(ctx, nina, folders[0].ID, "")
	must(err)
	by := map[string]Message{}
	for _, m := range in {
		by[m.Subject] = m
	}
	if len(by) != 4 || !by["Click"].Unsubscribe || !by["Mailto"].Unsubscribe || !by["Page"].Unsubscribe || by["Hello"].Unsubscribe {
		t.Fatalf("inbox = %+v", in)
	}
	for subject, want := range map[string]string{"Click": UnsubClick, "Mailto": UnsubMail, "Page": UnsubPage, "Hello": ""} {
		if way, page, err := s.UnsubscribeWay(ctx, nina, by[subject].ID); err != nil || way != want || (page != "") != (want == UnsubPage) {
			t.Errorf("%s: way %q, page %q, %v", subject, way, page, err)
		}
	}
	if _, err := s.Unsubscribe(ctx, eve, by["Click"].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("someone else unsubscribed: %v", err)
	}
	if _, err := s.Unsubscribe(ctx, nina, by["Hello"].ID); err == nil {
		t.Fatal("unsubscribed from a letter of no newsletter")
	}

	// one click: the newsletter gets the POST, no letter
	open, err := s.Unsubscribe(ctx, nina, by["Click"].ID)
	must(err)
	if open != "" || len(posts) != 1 || posts[0] != "POST /u?id=7 List-Unsubscribe=One-Click" {
		t.Fatalf("one click: open %q, posts %q", open, posts)
	}
	if m, _ := s.Message(ctx, nina, by["Click"].ID); m.Unsubscribed == nil {
		t.Fatal("the letter does not keep the unsubscribing")
	}
	// mailto: a letter from the mailbox, with the subject asked for
	open, err = s.Unsubscribe(ctx, nina, by["Mailto"].ID)
	must(err)
	sent := 0
	for _, l := range inbox(t, host, "leave.u@go24.test") {
		if strings.Contains(l, "Subject: Stop it") && strings.Contains(l, "From: <nina.u@go24.test>") && strings.Contains(l, "Auto-Submitted: auto-generated") {
			sent++
		}
	}
	if open != "" || sent != 1 || len(posts) != 1 {
		t.Fatalf("mailto: open %q, letters %d, posts %q", open, sent, posts)
	}
	// only a page: the user opens it
	open, err = s.Unsubscribe(ctx, nina, by["Page"].ID)
	must(err)
	if open != site.URL+"/page?id=9" || len(posts) != 1 {
		t.Fatalf("page: open %q, posts %q", open, posts)
	}
}
