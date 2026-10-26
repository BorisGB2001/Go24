package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailSendAsHTTP: «Адреса отправителя» shows a form per mailbox and keeps what is saved (a bad address is told,
// not kept); the editor offers the aliases in «От» with the default one chosen, a draft keeps the address chosen,
// an answer to a letter that came to an alias goes from it.
func TestMailSendAsHTTP(t *testing.T) {
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

	page, _ := do("GET", "/mail/settings?tab=senders", nil, 200)
	if !strings.Contains(page, `action="/mail/settings/boxes/`+box+`/senders"`) {
		t.Fatal("the settings have no form of the mailbox")
	}
	_, hdr := do("POST", "/mail/settings/boxes/"+box+"/senders", url.Values{"send_as": {"not an address"}}, 303)
	if f, _ := srv.Mail.MailboxSenders(ctx, anna, box); f.SendAs != "" || !strings.Contains(hdr.Get("Location"), "msg=") {
		t.Errorf("a bad address is kept: %+v %s", f, hdr.Get("Location"))
	}
	do("POST", "/mail/settings/boxes/"+box+"/senders", url.Values{"sender_name": {"Анна П."}, "send_as": {"Продажи <sales@go24.test>"}}, 303)
	page, _ = do("GET", "/mail/settings?tab=senders", nil, 200)
	if !strings.Contains(page, `value="Анна П."`) || !strings.Contains(page, `<option value="sales@go24.test">`) {
		t.Error("the saved addresses are not shown")
	}
	do("POST", "/mail/settings/boxes/"+box+"/senders", url.Values{"sender_name": {"Анна П."}, "send_as": {"Продажи <sales@go24.test>"},
		"send_default": {"sales@go24.test"}}, 303)

	ed, _ := do("GET", "/mail/compose", nil, 200)
	if !strings.Contains(ed, `name="from" value="sales@go24.test"`) || !strings.Contains(ed, `data-from="sales@go24.test" selected>Продажи &lt;sales@go24.test&gt;`) ||
		!strings.Contains(ed, `data-from="anna@go24.test">Анна П. &lt;anna@go24.test&gt;`) {
		t.Errorf("«От» of a new letter: %s", ed)
	}
	raw, _ := do("POST", "/mail/drafts/save", url.Values{"box": {box}, "from": {"anna@go24.test"}, "to": {"c@client.test"}, "html": {"<p>x</p>"}}, 200)
	var saved struct{ ID string }
	must(json.Unmarshal([]byte(raw), &saved))
	id := saved.ID
	if ed, _ = do("GET", "/mail/compose?draft="+id, nil, 200); !strings.Contains(ed, `name="from" value="anna@go24.test"`) {
		t.Error("the draft lost its address")
	}
	do("POST", "/mail/drafts/save", url.Values{"box": {box}, "from": {"other@go24.test"}, "to": {"c@client.test"}}, 422)

	var in, letter string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role) VALUES ($1, 'INBOX', 'inbox') RETURNING id::text`, box).Scan(&in))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, to_addr, subject, body, sent_at)
		VALUES ($1, $2, 1, 'c@client.test', 'Sales@go24.test', 'Заказ', 'x', now()) RETURNING id::text`, box, in).Scan(&letter))
	if ed, _ = do("GET", "/mail/compose?letter="+letter+"&mode=all", nil, 200); !strings.Contains(ed, `name="from" value="sales@go24.test"`) ||
		!strings.Contains(ed, `value="c@client.test"`) {
		t.Errorf("an answer: %s", ed)
	}
}
