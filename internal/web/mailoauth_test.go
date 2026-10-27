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
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestMailOAuthHTTP: «Подключить ящик» offers the services go24 has an application at; a button sends the user to
// the service with a signed state; the way back takes no state of another user or a forged one; a mailbox connected
// by signing in shows how and «Войти снова». (Signing in itself: mail.TestOAuthMailbox.)
func TestMailOAuthHTTP(t *testing.T) {
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		BaseURL: "https://go24.test"}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookies := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		cookies[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookies[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(raw)
	}

	// no application: no buttons, and a forged request is told so
	if _, _, page := do(anna, "GET", "/mail", nil); strings.Contains(page, `action="/mail/oauth"`) {
		t.Fatal("buttons without an application")
	}
	if code, to, _ := do(anna, "POST", "/mail/oauth", url.Values{"service": {"google"}}); code != http.StatusSeeOther || !strings.Contains(to, "msg=") {
		t.Fatalf("no application: %d %s", code, to)
	}

	srv.Mail.OAuth = map[string]mail.OAuthApp{"google": {ClientID: "gid", Secret: "gs"}, "yandex": {ClientID: "yid", Secret: "ys"}}
	_, _, page := do(anna, "GET", "/mail", nil)
	for _, want := range []string{`value="google"`, `value="yandex"`, "Gmail (Google)", "Яндекс Почта"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the connect form has no %s", want)
		}
	}
	if strings.Contains(page, `value="microsoft"`) {
		t.Fatal("Microsoft offered without an application")
	}
	code, to, _ := do(anna, "POST", "/mail/oauth", url.Values{"service": {"google"}})
	u, err := url.Parse(to)
	must(err)
	if code != http.StatusSeeOther || u.Host != "accounts.google.com" || u.Query().Get("client_id") != "gid" ||
		u.Query().Get("redirect_uri") != "https://go24.test/mail/oauth" || u.Query().Get("access_type") != "offline" ||
		!strings.Contains(u.Query().Get("scope"), "https://mail.google.com/") {
		t.Fatalf("to Google: %d %s", code, to)
	}
	state := u.Query().Get("state")

	// the way back: not someone else's state, not a forged one, not a refusal at the service
	for _, c := range []struct{ who, q string }{
		{eve, "state=" + url.QueryEscape(state) + "&code=c"},
		{anna, "state=" + url.QueryEscape(state+"x") + "&code=c"},
		{anna, "state=" + url.QueryEscape(state) + "&error=access_denied"},
	} {
		code, to, _ := do(c.who, "GET", "/mail/oauth?"+c.q, nil)
		if code != http.StatusSeeOther || !strings.HasPrefix(to, "/mail?msg=") {
			t.Fatalf("%s: %d %s", c.q, code, to)
		}
		if msg, _ := url.QueryUnescape(strings.TrimPrefix(to, "/mail?msg=")); !strings.Contains(msg, "Вход не выполнен") {
			t.Fatalf("%s: %s", c.q, msg)
		}
	}

	// a mailbox connected by signing in: how, and «Войти снова» when its access is gone
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@gmail.com", Password: "x"})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE mailboxes SET oauth = 'google', error = 'err.mail_oauth_again' WHERE id = $1`, box)
	must(err)
	_, _, page = do(anna, "GET", "/mail?box="+box, nil)
	for _, want := range []string{"вход Google", "Войти снова", "Доступ к ящику отозван или истёк"} {
		if !strings.Contains(page, want) {
			t.Fatalf("the mailbox shows no %s", want)
		}
	}
}
