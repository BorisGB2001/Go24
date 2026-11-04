package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

func TestWithLetter(t *testing.T) {
	for _, c := range [][3]string{
		{"/mail?folder=f&letter=a", "b", "/mail?folder=f&letter=b"},
		{"/mail?folder=f&letter=a", "", "/mail?folder=f"},
		{"/mail?folder=f&letter=a", "b,c", "/mail?folder=f"},
		{"/mail?folder=f", "b&x=1", "/mail?folder=f"},
	} {
		if got := withLetter(c[0], c[1]); got != c[2] {
			t.Errorf("withLetter(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
}

// TestMailHandling: «Обработка сообщений» — kept and checked; a letter opened with «через N секунд» or «не отмечать»
// stays unread for the script to mark; «никогда не отправлять» answers a read receipt by itself; the letter to
// open after the open one leaves the folder.
func TestMailHandling(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id`, box).Scan(&inbox))
	letter := func(uid int, subject, receipt string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, subject, body, sent_at, receipt_to)
			VALUES ($1, $2, $3, 'boss@client.test', 'anna@go24.test', $4, 'текст', now(), $5) RETURNING id`, box, inbox, uid, subject, receipt).Scan(&id))
		return id
	}
	a, b := letter(1, "Первое", ""), letter(2, "Второе", "boss@client.test")
	seen := func(id string) (v bool) {
		must(pool.QueryRow(ctx, `SELECT seen FROM mail_messages WHERE id = $1`, id).Scan(&v))
		return
	}

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values, want int) (string, http.Header) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header
	}

	// the defaults, as in Outlook: read when opened, ask about receipts, the next letter after
	page, _ := do("GET", "/mail/settings?tab=handling", nil, 200)
	for _, want := range []string{`action="/mail/settings/handling"`, `value="0" selected`, `value="ask" checked`, `value="next" checked`, `name="nudges" value="1" checked`} {
		if !strings.Contains(page, want) {
			t.Errorf("the settings have no %s", want)
		}
	}
	_, hdr := do("POST", "/mail/settings/handling", url.Values{"read_after": {"7"}, "receipts": {"ask"}, "after_move": {"next"}}, 303)
	if h, _ := srv.Mail.Handling(ctx, anna); h.ReadAfter != 0 || !strings.Contains(hdr.Get("Location"), "msg=") {
		t.Errorf("a bad choice is kept: %+v %s", h, hdr.Get("Location"))
	}
	do("POST", "/mail/settings/handling", url.Values{"read_after": {"5"}, "receipts": {"never"}, "after_move": {"prev"}}, 303)
	if h, _ := srv.Mail.Handling(ctx, anna); h != (mail.Handling{ReadAfter: 5, Receipts: "never", AfterMove: "prev"}) {
		t.Errorf("kept: %+v", h)
	}

	// read after 5 seconds: the pane leaves the letter unread and says so to the script
	pane, _ := do("GET", "/mail/letters/"+a+"?folder="+inbox, nil, 200)
	if seen(a) || !strings.Contains(pane, `data-read-after="5" data-unread="`+a+`"`) {
		t.Errorf("read at once (%v) or not told: %.400s", seen(a), pane)
	}
	page, _ = do("GET", "/mail?folder="+inbox, nil, 200)
	if !strings.Contains(page, `data-after-move="prev"`) {
		t.Error("the page does not tell the script what opens after")
	}

	// «Никогда не отправлять»: the receipt is answered by itself, no question
	pane, _ = do("GET", "/mail/letters/"+b+"?folder="+inbox, nil, 200)
	if strings.Contains(pane, "/receipt") {
		t.Error("asked about the receipt")
	}
	done := false
	for i := 0; i < 50 && !done; i++ {
		must(pool.QueryRow(ctx, `SELECT receipt_done FROM mail_messages WHERE id = $1`, b).Scan(&done))
		time.Sleep(20 * time.Millisecond)
	}
	if !done {
		t.Error("the receipt is not answered")
	}

	// the letter leaves the folder («Другие»): the page back opens the one the script names
	_, hdr = do("POST", "/mail/act", url.Values{"letter": {a}, "do": {"other"}, "back": {"/mail?folder=" + inbox + "&letter=" + a}, "next": {b}}, 303)
	if loc := hdr.Get("Location"); !strings.Contains(loc, "letter="+b) || strings.Contains(loc, a) {
		t.Errorf("after: %s", loc)
	}

	// read when opened
	must(srv.Mail.SaveHandling(ctx, anna, mail.Handling{ReadAfter: 0, Receipts: "ask", AfterMove: "folder"}))
	pool.Exec(ctx, `UPDATE mail_messages SET seen = false`)
	pane, _ = do("GET", "/mail/letters/"+a+"?folder="+inbox, nil, 200)
	if !seen(a) || strings.Contains(pane, "data-read-after") {
		t.Error("not read when opened")
	}

	// «Очистка беседы»: in the same form, Outlook's defaults; the letter's «Очистить беседу» and the folder's
	// «Очистить папку» say there was nothing to clean (no conversations here)
	page, _ = do("GET", "/mail/settings?tab=handling", nil, 200)
	for _, want := range []string{`name="cleanup_target" value="delete" checked`, `name="cleanup_unread" value="1" checked`, `name="cleanup_signed" value="1" checked`} {
		if !strings.Contains(page, want) {
			t.Errorf("the settings have no %s", want)
		}
	}
	do("POST", "/mail/settings/handling", url.Values{"read_after": {"0"}, "receipts": {"ask"}, "after_move": {"next"}, "cleanup_target": {"archive"},
		"cleanup_flagged": {"1"}}, 303)
	if c, _ := srv.Mail.Cleanup(ctx, anna); c != (mail.Cleanup{Target: "archive", KeepFlagged: true}) {
		t.Errorf("clean up kept: %+v", c)
	}
	page, _ = do("GET", "/mail?folder="+inbox+"&letter="+a, nil, 200)
	if !strings.Contains(page, `action="/mail/folders/`+inbox+`/cleanup"`) || !strings.Contains(page, `name="do" value="cleanup"`) {
		t.Error("no «Очистить» on the page")
	}
	_, hdr = do("POST", "/mail/act", url.Values{"letter": {a}, "do": {"cleanup"}, "back": {"/mail?folder=" + inbox + "&letter=" + a}}, 303)
	if loc := hdr.Get("Location"); !strings.Contains(loc, "letter="+a) || !strings.Contains(loc, "msg=") {
		t.Errorf("after «Очистить беседу»: %s", loc)
	}
	_, hdr = do("POST", "/mail/folders/"+inbox+"/cleanup", url.Values{"sub": {"1"}}, 303)
	if loc := hdr.Get("Location"); !strings.Contains(loc, "folder="+inbox) || !strings.Contains(loc, "msg=") {
		t.Errorf("after «Очистить папку»: %s", loc)
	}
	do("POST", "/mail/folders/00000000-0000-0000-0000-000000000000/cleanup", url.Values{}, 404)

	// «Напоминания»: a question of 4 days ago comes up over the inbox (when on), «Закрыть» puts it away
	var ask string
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, message_id, thread)
		VALUES ($1, $2, 9, 'boss@client.test', 'Шеф', 'anna@go24.test', 'Отчёт', 'Когда будет отчёт?', now() - interval '4 days', 'q1', 'q1') RETURNING id`,
		box, inbox).Scan(&ask))
	if page, _ = do("GET", "/mail?folder="+inbox, nil, 200); strings.Contains(page, `data-nudge=`) {
		t.Error("a nudge though turned off")
	}
	do("POST", "/mail/settings/handling", url.Values{"read_after": {"0"}, "receipts": {"ask"}, "after_move": {"next"}, "nudges": {"1"}}, 303)
	page, _ = do("GET", "/mail?folder="+inbox, nil, 200)
	if !strings.Contains(page, `data-nudge="`+ask+`"`) || !strings.Contains(page, `action="/mail/nudges/`+ask+`/off"`) || !strings.Contains(page, "mode=reply") {
		t.Error("no nudge over the inbox")
	}
	if page, _ = do("GET", "/mail?folder="+inbox+"&q=отчёт", nil, 200); strings.Contains(page, `data-nudge=`) {
		t.Error("a nudge over the search")
	}
	_, hdr = do("POST", "/mail/nudges/"+ask+"/off", url.Values{"back": {"/mail?folder=" + inbox}}, 303)
	if page, _ = do("GET", hdr.Get("Location"), nil, 200); strings.Contains(page, `data-nudge=`) {
		t.Error("the nudge stays after «Закрыть»")
	}
}
