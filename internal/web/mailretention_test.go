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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailRetentionHTTP: «Назначить политику» of a folder (its menu) and of a letter («…»), the line of the reading
// pane, the REST methods; another's folder and letter are not found.
func TestMailRetentionHTTP(t *testing.T) {
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
	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Mail: mail.NewStore(pool, "x"), I18n: bundle, Keys: keys, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox, letter string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'inbox', 'inbox', true) RETURNING id::text`, box).Scan(&inbox))
	must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, subject, body, sent_at)
		VALUES ($1, $2, 1, 'ann@client.test', 'Счёт', 'Текст', '2026-10-01 10:00+02') RETURNING id::text`, box, inbox).Scan(&letter))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), eve: cookie(eve)}
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

	page := do(anna, "GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	if !strings.Contains(page, `action="/mail/folders/`+inbox+`/policy"`) || !strings.Contains(page, `action="/mail/letters/policy"`) || strings.Contains(page, "mpol-line") {
		t.Error("no «Назначить политику» in the menus, or a policy line without a policy")
	}
	do(eve, "POST", "/mail/folders/"+inbox+"/policy", url.Values{"policy": {"del_1m"}}, 404)
	do(anna, "POST", "/mail/folders/"+inbox+"/policy", url.Values{"policy": {"del_2d"}}, 422)
	do(anna, "POST", "/mail/folders/"+inbox+"/policy", url.Values{"policy": {"del_1m"}}, 200)
	page = do(anna, "GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	for _, want := range []string{"mpol-line", "Удалить через 1 месяц", "(политика папки)", "31.10.2026", `<option value="del_1m" selected>`} {
		if !strings.Contains(page, want) {
			t.Errorf("the letter's page has no %s", want)
		}
	}
	do(eve, "POST", "/mail/letters/policy", url.Values{"letter": {letter}, "policy": {"never"}}, 404)
	if out := do(anna, "POST", "/mail/letters/policy", url.Values{"letter": {letter}, "policy": {"never"}}, 200); !strings.Contains(out, `"letters": 1`) {
		t.Errorf("letter policy = %s", out)
	}
	page = do(anna, "GET", "/mail?folder="+inbox+"&letter="+letter, nil, 200)
	if !strings.Contains(page, "Никогда не удалять") || strings.Contains(page, "(политика папки)") {
		t.Error("the letter's own policy is not shown")
	}

	// REST
	api := func(who, method, path, body string, want int) map[string]any {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"mail:read", "mail:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, b)
		}
		out := map[string]any{}
		_ = json.Unmarshal(b, &out)
		return out
	}
	if out := api(anna, "GET", "/api/v1/mail/letters/"+letter+"/policy", "", 200); out["policy"] != "never" || out["folder"] != "del_1m" || out["expires"] != nil {
		t.Errorf("GET letter policy = %v", out)
	}
	if out := api(anna, "PUT", "/api/v1/mail/letters/"+letter+"/policy", `{"policy":""}`, 200); out["policy"] != "del_1m" || out["expires"] == nil {
		t.Errorf("PUT letter policy = %v", out)
	}
	if out := api(anna, "PUT", "/api/v1/mail/folders/"+inbox+"/policy", `{"policy":"arch_1y"}`, 200); out["policy"] != "arch_1y" {
		t.Errorf("PUT folder policy = %v", out)
	}
	api(eve, "GET", "/api/v1/mail/folders/"+inbox+"/policy", "", 404)
	api(eve, "PUT", "/api/v1/mail/letters/"+letter+"/policy", `{"policy":"never"}`, 404)
}
