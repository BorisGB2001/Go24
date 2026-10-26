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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailForwardSettings: «Пересылка» shows a form per mailbox and keeps what is saved; a bad address is told,
// not kept.
func TestMailForwardSettings(t *testing.T) {
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

	// the default, as in Outlook: off, a copy kept
	page, _ := do("GET", "/mail/settings?tab=forwarding", nil, 200)
	for _, want := range []string{`action="/mail/settings/boxes/` + box + `/forward"`, `name="forward_keep" value="1" checked`} {
		if !strings.Contains(page, want) {
			t.Errorf("the settings have no %s", want)
		}
	}
	if strings.Contains(page, `name="forward_on" value="1" checked`) {
		t.Error("on by default")
	}
	_, hdr := do("POST", "/mail/settings/boxes/"+box+"/forward", url.Values{"forward_on": {"1"}, "forward_to": {"not an address"}}, 303)
	if f, _ := srv.Mail.MailboxForward(ctx, anna, box); f.On || !strings.Contains(hdr.Get("Location"), "msg=") {
		t.Errorf("a bad address is kept: %+v %s", f, hdr.Get("Location"))
	}
	do("POST", "/mail/settings/boxes/"+box+"/forward", url.Values{"forward_on": {"1"}, "forward_to": {"away@other.test"}}, 303)
	if f, _ := srv.Mail.MailboxForward(ctx, anna, box); f != (mail.BoxForward{On: true, To: "away@other.test"}) {
		t.Errorf("kept: %+v", f)
	}
	page, _ = do("GET", "/mail/settings?tab=forwarding", nil, 200)
	if !strings.Contains(page, `value="away@other.test"`) || strings.Contains(page, `name="forward_keep" value="1" checked`) {
		t.Error("the saved forwarding is not shown")
	}
}
