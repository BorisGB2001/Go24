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
)

// TestMailSafetyHTTP: the sender's warnings in the reading pane (not checked — «?», a lookalike domain, a first
// letter, «через»), «Сообщить о фишинге» and «о нежелательной почте», the reporting address of the administrators.
func TestMailSafetyHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Mail: mail.NewStore(pool, "x"), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	folders := map[string]string{}
	for _, role := range []string{"inbox", "sent", "junk", "trash"} {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $2, true) RETURNING id::text`, box, role).Scan(&id))
		folders[role] = id
	}
	uid := int64(5000000000)
	letter := func(folder, from, name, to, auth, via string) string {
		t.Helper()
		uid++
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, sender_auth, sender_via)
			VALUES ($1, $2, $3, $4, $5, $6, 'Счёт', 'Оплатите', now(), $7, $8) RETURNING id::text`, box, folders[folder], uid, from, name, to, auth, via).Scan(&id))
		return id
	}
	letter("sent", "anna@go24.test", "", "Банк <info@bank.test>", "", "")
	letter("inbox", "info@bank.test", "Банк", "anna@go24.test", mail.AuthPass, "")
	fake := letter("inbox", "info@bamk.test", "Банк", "anna@go24.test", mail.AuthFail, "")
	news := letter("inbox", "shop@brand.test", "Магазин", "anna@go24.test", "", "esp.test")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(s auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, s))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(auth.Session{UserID: anna, Name: "Анна"}), eve: cookie(auth.Session{UserID: eve}),
		"admin": cookie(auth.Session{UserID: eve, Admin: true, AdminMode: true})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "application/json")
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
		return string(raw)
	}

	page := do(anna, "GET", "/mail?folder="+folders["inbox"]+"&letter="+fake, nil, 200)
	for _, want := range []string{`class="row mc-banner msa-bad"`, "info@bamk.test", "bank.test", `class="msa-ava"`, `action="/mail/report"`,
		`name="kind" value="phishing"`, `name="kind" value="junk"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the lookalike's page has no %s", want)
		}
	}
	page = do(anna, "GET", "/mail?folder="+folders["inbox"]+"&letter="+news, nil, 200)
	if !strings.Contains(page, "msa-first") || !strings.Contains(page, "msa-via") || !strings.Contains(page, "esp.test") || strings.Contains(page, "msa-bad") {
		t.Errorf("a first letter «через» a service: %.3000s", page[strings.Index(page, "mailletter"):])
	}

	// «Сообщить о фишинге»: to «Удалённые», recorded; another's letter — not found
	do(eve, "POST", "/mail/report", url.Values{"letter": {fake}, "kind": {"phishing"}}, 404)
	do(anna, "POST", "/mail/report", url.Values{"letter": {fake}, "kind": {"bad"}}, 422)
	if out := do(anna, "POST", "/mail/report", url.Values{"letter": {fake}, "kind": {"phishing"}}, 200); !strings.Contains(out, `"letters": 1`) {
		t.Errorf("report = %s", out)
	}
	var role string
	must(pool.QueryRow(ctx, `SELECT f.role FROM mail_messages x JOIN mail_folders f ON f.id = x.folder_id WHERE x.id = $1`, fake).Scan(&role))
	if role != "trash" {
		t.Errorf("phishing in %s", role)
	}

	// the reporting address: administrators only, the reports listed for them
	if page := do(anna, "GET", "/mail/settings?tab=junk", nil, 200); strings.Contains(page, `action="/mail/settings/report"`) {
		t.Error("the reporting address shown to an employee")
	}
	do(anna, "POST", "/mail/settings/report", url.Values{"to": {"security@go24.test"}}, 403)
	do("admin", "POST", "/mail/settings/report", url.Values{"to": {"security@go24.test"}}, 200)
	if to, _ := srv.Mail.ReportTo(ctx); to != "security@go24.test" {
		t.Errorf("ReportTo = %q", to)
	}
	do(anna, "POST", "/mail/report", url.Values{"letter": {news}, "kind": {"junk"}}, 200)
	page = do("admin", "GET", "/mail/settings?tab=junk", nil, 200)
	for _, want := range []string{`value="security@go24.test"`, "info@bamk.test", "shop@brand.test", "Анна"} {
		if !strings.Contains(page, want) {
			t.Errorf("the reports have no %s", want)
		}
	}
}
