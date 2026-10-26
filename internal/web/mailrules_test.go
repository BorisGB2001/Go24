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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailRulesHTTP drives the rules, «Очистка» and the automatic replies over HTTP against the test database (go24's
// own folders, no mail server): «Создать правило» from a letter, the rule's form, order, on/off, «Запустить сейчас»;
// «Очистка» from the toolbar with its dialog, a standing one in the settings; the automatic replies with their banner,
// turned on by an absence and off from the banner; the REST API; nobody else gets any of it.
func TestMailRulesHTTP(t *testing.T) {
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
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"), Keys: keys,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	folder := func(name, role string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, $2, $3, true) RETURNING id::text`, box, name, role).Scan(&id))
		return id
	}
	inbox, bills, archive := folder("INBOX", "inbox"), folder("Bills", ""), folder("Archive", "archive")
	folder("Trash", "trash")
	letter := func(uid int, from, subject string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at)
			VALUES ($1, $2, $3, $4, 'Иван', 'anna@go24.test', $5, 'текст', $6) RETURNING id::text`, box, inbox, uid, from, subject,
			time.Now().Add(-time.Duration(uid)*24*time.Hour)).Scan(&id))
		return id
	}
	invoice := letter(1, "ivan@client.test", "Счёт за октябрь")
	letter(2, "ivan@client.test", "Привет")
	news := letter(3, "news@shop.test", "Скидки")
	letter(20, "news@shop.test", "Старые скидки")

	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), eve: cookie(eve)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) (string, *http.Response) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, ts.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.500s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res
	}
	page := func(path string) string {
		t.Helper()
		p, _ := do(anna, "GET", path, nil, 200)
		return p
	}
	cats, err := srv.Mail.Categories(ctx, anna, [2]string{"red", "Бухгалтерия"})
	must(err)

	// «Создать правило» from the letter's toolbar: the form is filled with its sender, its subject offered
	p := page("/mail?folder=" + inbox)
	if !strings.Contains(p, `value="rule"`) || !strings.Contains(p, `value="sweep"`) {
		t.Fatal("no «Создать правило» / «Очистка» on the toolbar")
	}
	_, res := do(anna, "POST", "/mail/act", url.Values{"letter": {invoice}, "do": {"rule"}, "back": {"/mail"}}, 303)
	loc := res.Header.Get("Location")
	if p = page(loc); !strings.Contains(p, `name="if_from" value="ivan@client.test"`) || !strings.Contains(p, `name="if_subject_letter" value="Счёт за октябрь"`) {
		t.Fatalf("rule from a letter (%s): %.3000s", loc, p)
	}
	do(eve, "GET", loc, nil, 404) // not her letter
	do(anna, "POST", "/mail/rules", url.Values{"name": {"Счета"}, "enabled": {"1"}, "if_from": {"ivan@client.test"}, "if_subject_letter": {"Счёт"},
		"do_category": {cats[0].ID}, "do_move": {bills}, "do_read": {"1"}, "stop": {"1"}}, 303)
	do(anna, "POST", "/mail/rules", url.Values{"name": {"Магазин"}, "enabled": {"1"}, "if_from": {"shop.test"}, "ex_subject": {"старые"},
		"do_flag": {"1"}}, 303)
	if _, res = do(anna, "POST", "/mail/rules", url.Values{"name": {"Пусто"}}, 303); !strings.Contains(res.Header.Get("Location"), "msg=") {
		t.Fatal("a rule without actions saved")
	}
	rules, err := srv.Mail.Rules(ctx, anna)
	must(err)
	if len(rules) != 2 || rules[0].If.Subject != "Счёт" || rules[0].Do.Move != bills || !rules[0].Stop || rules[1].Except.Subject != "старые" {
		t.Fatalf("rules = %+v", rules)
	}
	if p = page("/mail/settings?tab=rules"); !strings.Contains(p, "Счета") || !strings.Contains(p, "Бухгалтерия") || !strings.Contains(p, "/mail/rules/run") {
		t.Fatalf("rules tab: %.3000s", p)
	}
	if p = page("/mail/settings?tab=rules&edit=" + rules[1].ID); !strings.Contains(p, `name="ex_subject" value="старые"`) {
		t.Fatal("edit form")
	}
	do(eve, "GET", "/mail/settings?tab=rules&edit="+rules[1].ID, nil, 404)
	do(eve, "POST", "/mail/rules/"+rules[1].ID+"/do", url.Values{"do": {"delete"}}, 404)
	do(eve, "POST", "/mail/rules/"+rules[1].ID+"/save", url.Values{"name": {"x"}, "do_flag": {"1"}}, 404)
	do(anna, "POST", "/mail/rules/"+rules[1].ID+"/do", url.Values{"do": {"up"}}, 303)
	do(anna, "POST", "/mail/rules/"+rules[1].ID+"/do", url.Values{"do": {"off"}}, 303)
	if rules, _ = srv.Mail.Rules(ctx, anna); rules[0].Name != "Магазин" || rules[0].Enabled {
		t.Fatalf("order, off: %+v", rules)
	}

	// «Запустить сейчас»: the rules that are on; then the one that is off, ticked
	do(eve, "POST", "/mail/rules/run", url.Values{"folder": {inbox}}, 404)
	_, res = do(anna, "POST", "/mail/rules/run", url.Values{"folder": {inbox}}, 303)
	if !strings.Contains(res.Header.Get("Location"), "msg=") {
		t.Fatal("no result")
	}
	moved, err := srv.Mail.List(ctx, anna, bills, "", false)
	must(err)
	if len(moved) != 1 || moved[0].Subject != "Счёт за октябрь" || !moved[0].Seen || len(moved[0].Categories) != 1 {
		t.Fatalf("bills = %+v", moved)
	}
	do(anna, "POST", "/mail/rules/run", url.Values{"folder": {inbox}, "rule": {rules[0].ID}}, 303)
	left, _ := srv.Mail.List(ctx, anna, inbox, "", false)
	flagged := map[string]bool{}
	for _, m := range left {
		flagged[m.Subject] = m.Flagged
	}
	if len(left) != 3 || !flagged["Скидки"] || flagged["Старые скидки"] || flagged["Привет"] { // the exception kept the old one off
		t.Fatalf("after the shop rule: %+v", flagged)
	}

	// «Очистка» from the toolbar: the dialog, then the standing «older than 10 days» to the archive
	_, res = do(anna, "POST", "/mail/act", url.Values{"letter": {news}, "do": {"sweep"}, "back": {"/mail?folder=" + inbox}}, 303)
	loc = res.Header.Get("Location")
	if p = page(loc); !strings.Contains(p, "news@shop.test") || !strings.Contains(p, `value="older"`) || !strings.Contains(p, `value="archive"`) {
		t.Fatalf("sweep dialog: %.3000s", p)
	}
	do(eve, "GET", loc, nil, 404)
	do(eve, "POST", "/mail/sweep", url.Values{"letter": {news}, "mode": {"all"}, "target": {"delete"}}, 404)
	_, res = do(anna, "POST", "/mail/sweep", url.Values{"letter": {news}, "mode": {"older"}, "days": {"10"}, "target": {"archive"},
		"back": {"/mail?folder=" + inbox + "&letter=" + news}}, 303)
	if loc = res.Header.Get("Location"); strings.Contains(loc, "letter=") || !strings.Contains(loc, "msg=") {
		t.Fatalf("back to %s", loc)
	}
	if arch, _ := srv.Mail.List(ctx, anna, archive, "", false); len(arch) != 1 || arch[0].Subject != "Старые скидки" {
		t.Fatalf("archive = %+v", arch)
	}
	if p = page("/mail/settings?tab=sweep"); !strings.Contains(p, "news@shop.test") || !strings.Contains(p, "10") {
		t.Fatalf("sweep tab: %.3000s", p)
	}
	sw, err := srv.Mail.Sweeps(ctx, anna)
	must(err)
	do(eve, "POST", "/mail/sweeps/"+sw[0].ID+"/delete", url.Values{}, 404)
	letter(30, "news@shop.test", "Совсем старые")
	if errs := srv.Mail.SweepDue(ctx); len(errs) > 0 {
		t.Fatal(errs)
	}
	if arch, _ := srv.Mail.List(ctx, anna, archive, "", false); len(arch) != 2 {
		t.Fatalf("the standing sweep: %+v", arch)
	}
	do(anna, "POST", "/mail/sweeps/"+sw[0].ID+"/delete", url.Values{}, 303)

	// automatic replies: a period, the banner; an absence turns them on, the banner turns that one off
	if p = page("/mail?folder=" + inbox); strings.Contains(p, "mooo") {
		t.Fatal("a banner while off")
	}
	from, to := time.Now().Add(-time.Hour).Format("2006-01-02T15:04"), time.Now().Add(48*time.Hour).Format("2006-01-02T15:04")
	do(anna, "POST", "/mail/settings/ooo", url.Values{"mode": {"period"}, "from": {to}, "to": {from}, "outside": {"all"}}, 303) // the end before the start
	if o, _ := srv.Mail.OOO(ctx, anna); o.Mode != mail.OOOOff {
		t.Fatal("a bad period saved")
	}
	do(anna, "POST", "/mail/settings/ooo", url.Values{"mode": {"period"}, "from": {from}, "to": {to}, "outside": {"contacts"},
		"internal": {"Я в отпуске"}, "absence": {"1"}}, 303)
	if p = page("/mail?folder=" + inbox); !strings.Contains(p, "mooo") || !strings.Contains(p, "/mail/ooo/off") {
		t.Fatalf("no banner: %.2000s", p)
	}
	if p = page("/mail/settings?tab=ooo"); !strings.Contains(p, "Я в отпуске") || !strings.Contains(p, `value="contacts" checked`) {
		t.Fatalf("ooo tab: %.3000s", p)
	}
	if p, _ = do(eve, "GET", "/mail/settings?tab=ooo", nil, 200); strings.Contains(p, "Я в отпуске") {
		t.Fatal("eve sees anna's reply")
	}
	do(anna, "POST", "/mail/ooo/off", url.Values{"back": {"/mail"}}, 303)
	if p = page("/mail?folder=" + inbox); strings.Contains(p, "mooo") {
		t.Fatal("the banner stays")
	}
	today := time.Now().Format("2006-01-02")
	_, err = pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by) VALUES ($1, 'vacation', $2::date, $2::date + 3, $1)`, anna, today)
	must(err)
	if p = page("/mail?folder=" + inbox); !strings.Contains(p, "mooo") || !strings.Contains(p, "по отсутствию: Отпуск") {
		t.Fatalf("no banner by the absence: %.2000s", p)
	}
	do(anna, "POST", "/mail/ooo/off", url.Values{"back": {"/mail"}}, 303)
	if _, st, _ := srv.Mail.OOONow(ctx, anna, time.Now()); st.On {
		t.Fatal("still on after «Выключить» in an absence")
	}

	// the REST API
	tok, err := keys.Create(ctx, anna, "k", []string{"mail:read", "mail:write"}, time.Hour)
	must(err)
	api := func(method, path, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, raw)
		}
		return string(raw)
	}
	var got []mail.Rule
	must(json.Unmarshal([]byte(api("GET", "/api/v1/mail/rules", "", 200)), &got))
	if len(got) != 2 || got[0].Name != "Магазин" || got[1].Do.Move != bills {
		t.Fatalf("api rules = %+v", got)
	}
	api("POST", "/api/v1/mail/rules", `{"name": "API", "enabled": true, "if": {"attach": true}, "do": {"pin": true}}`, 200)
	api("POST", "/api/v1/mail/rules", `{"name": "API"}`, 422)
	api("POST", "/api/v1/mail/rules/"+got[0].ID, `{"move": 1}`, 200)
	api("DELETE", "/api/v1/mail/rules/"+got[0].ID, "", 200)
	if out := api("POST", "/api/v1/mail/ooo", `{"mode": "on", "internal": "нет меня", "outside": "none"}`, 200); !strings.Contains(out, `"on": true`) {
		t.Fatalf("api ooo = %s", out)
	}
	if out := api("GET", "/api/v1/mail/sweeps", "", 200); strings.TrimSpace(out) != "[]" {
		t.Fatalf("api sweeps = %s", out)
	}

	// blocked and safe senders, how new letters are announced
	api("POST", "/api/v1/mail/senders", `{"sender": "Spam@Junk.test"}`, 200)
	api("POST", "/api/v1/mail/senders", `{"sender": "@friends.test", "kind": "safe"}`, 200)
	api("POST", "/api/v1/mail/senders", `{"sender": "nobody"}`, 422)
	if out := api("GET", "/api/v1/mail/senders", "", 200); !strings.Contains(out, `"spam@junk.test"`) || strings.Contains(out, "friends") {
		t.Fatalf("api blocked = %s", out)
	}
	if out := api("GET", "/api/v1/mail/senders?kind=safe", "", 200); !strings.Contains(out, `"domain": true`) {
		t.Fatalf("api safe = %s", out)
	}
	api("DELETE", "/api/v1/mail/senders/spam@junk.test", "", 200)
	api("DELETE", "/api/v1/mail/senders/spam@junk.test", "", 404)
	if out := api("POST", "/api/v1/mail/notify", `{"mode": "all", "sound": false}`, 200); !strings.Contains(out, `"mode": "all"`) ||
		!strings.Contains(out, `"sound": false`) || !strings.Contains(out, `"desktop": true`) || !strings.Contains(out, `"unread"`) {
		t.Fatalf("api notify = %s", out)
	}
	api("POST", "/api/v1/mail/notify", `{"mode": "loud"}`, 422)
}
