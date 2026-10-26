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

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

func TestMailListOf(t *testing.T) {
	get := func(target string, cookies ...*http.Cookie) (mailList, *httptest.ResponseRecorder) {
		r := httptest.NewRequest("GET", target, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		u, _ := url.Parse(target)
		return mailListOf(w, r, u.Query().Get("folder"), "inbox", true, true), w
	}
	l, _ := get("/mail?folder=f1")
	if !l.Focus || l.View != mail.ViewFocused || l.Sort != "date" || l.Turn || l.URL() != "/mail?folder=f1" {
		t.Fatalf("inbox = %+v %s", l, l.URL())
	}
	l, _ = get("/mail?folder=f1&view=other&filter=unread&filter=bad")
	if l.View != mail.ViewOther || l.Filter != "unread" || l.URL() != "/mail?filter=unread&folder=f1&view=other" ||
		l.With("view", "") != "/mail?filter=unread&folder=f1" || l.With("filter", "flagged") != "/mail?folder=f1&view=other&filter=flagged" {
		t.Fatalf("other = %+v %s", l, l.URL())
	}
	// a search looks in both tabs; through all folders when asked
	l, _ = get("/mail?folder=f1&view=other&q=счёт&scope=all")
	if l.Focus || l.View != "" || !l.All || !l.Everywhere || l.URL() != "/mail?folder=f1&q=%D1%81%D1%87%D1%91%D1%82&scope=all" || l.URL("q") != "/mail?folder=f1" {
		t.Fatalf("search = %+v %s", l, l.URL())
	}
	l, _ = get("/mail?folder=f1&adv=1&s_from=boss&s_attach=1&s_words=счёт&q=ignored")
	if l.Q != "from:boss has:attachment счёт" {
		t.Fatalf("advanced = %q", l.Q)
	}
	// the order is kept in the cookie
	l, w := get("/mail?folder=f1&sort=from&turn=1")
	if l.Sort != "from" || !l.Turn || !strings.Contains(w.Header().Get("Set-Cookie"), "mail_sort=from.1") {
		t.Fatalf("sort = %+v %s", l, w.Header().Get("Set-Cookie"))
	}
	if l, _ = get("/mail?folder=f1", &http.Cookie{Name: "mail_sort", Value: "size"}); l.Sort != "size" || l.Turn {
		t.Fatalf("cookie sort = %+v", l)
	}
	if l, _ = get("/mail?folder=f1", &http.Cookie{Name: "mail_sort", Value: "evil.1"}); l.Sort != "date" {
		t.Fatalf("bad sort = %+v", l)
	}
	if l, _ = get("/mail?folder=search:abc&q=x"); !l.Everywhere || l.Saved != "abc" || l.Focus || l.URL() != "/mail?folder=search%3Aabc" {
		t.Fatalf("search folder = %+v %s", l, l.URL())
	}
}

// TestMailFocusSearchHTTP drives the list over HTTP against the test database: the tabs «Отсортированные / Другие»
// with the hint, «Переместить» and «Всегда перемещать», the filter, the order, the search with its marks, the
// suggestions, a search saved as a search folder, the settings — and nobody else gets any of it.
func TestMailFocusSearchHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	must(err)
	var inbox string
	must(pool.QueryRow(ctx, `INSERT INTO mail_folders (mailbox_id, name, role, local) VALUES ($1, 'INBOX', 'inbox', true) RETURNING id::text`, box).Scan(&inbox))
	letter := func(uid int, from, name, subject string, other bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO mail_messages (mailbox_id, folder_id, uid, from_addr, from_name, to_addr, subject, body, sent_at, other)
			VALUES ($1, $2, $3, $4, $5, 'anna@go24.test', $6, 'текст письма', $7, $8) RETURNING id::text`, box, inbox, uid, from, name, subject,
			time.Now().Add(-time.Duration(uid)*time.Hour), other).Scan(&id))
		return id
	}
	letter(1, "ivan@client.test", "Иван Петров", "Заказ коробок", false)
	sale := letter(2, "news@shop.test", "Магазин", "Скидки недели", true)

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

	p := page("/mail?folder=" + inbox)
	if !strings.Contains(p, "Заказ коробок") || strings.Contains(p, "Скидки недели") || !strings.Contains(p, `class="mtab on"`) ||
		!strings.Contains(p, "mother-hint") || !strings.Contains(p, "Магазин") || !strings.Contains(p, `value="other"`) {
		t.Fatalf("focused: %.3000s", p)
	}
	if p = page("/mail?folder=" + inbox + "&view=other"); strings.Contains(p, "Заказ коробок") || !strings.Contains(p, "Скидки недели") ||
		!strings.Contains(p, `value="focus:always"`) || strings.Contains(p, "mother-hint") {
		t.Fatal("other")
	}
	// «Всегда перемещать в Отсортированные» of the shop, from the toolbar
	_, res := do(anna, "POST", "/mail/act", url.Values{"letter": {sale}, "do": {"focus:always"}, "back": {"/mail?folder=" + inbox + "&view=other&letter=" + sale}}, 303)
	if loc := res.Header.Get("Location"); strings.Contains(loc, "letter=") {
		t.Fatalf("back to %s", loc)
	}
	if p = page("/mail?folder=" + inbox); !strings.Contains(p, "Скидки недели") || strings.Contains(p, "mother-hint") {
		t.Fatal("the rule did not move the letter")
	}
	if p = page("/mail/settings?tab=focused"); !strings.Contains(p, "news@shop.test") || !strings.Contains(p, `name="focused" value="1" checked`) {
		t.Fatalf("settings: %.3000s", p)
	}
	do(anna, "POST", "/mail/settings/focused/rule", url.Values{"sender": {"news@shop.test"}, "do": {"delete"}}, 303)
	do(anna, "POST", "/mail/settings/focused", url.Values{}, 303)
	if p = page("/mail?folder=" + inbox); strings.Contains(p, "mtab") {
		t.Fatal("the tabs are off but shown")
	}
	do(anna, "POST", "/mail/settings/focused", url.Values{"focused": {"1"}}, 303)

	// filter, order, search
	if p = page("/mail?folder=" + inbox + "&filter=flagged"); strings.Contains(p, "Заказ коробок") || !strings.Contains(p, `class="empty"`) {
		t.Fatal("filter")
	}
	_, res = do(anna, "GET", "/mail?folder="+inbox+"&sort=subject", nil, 200)
	if !strings.Contains(res.Header.Get("Set-Cookie"), "mail_sort=subject") {
		t.Fatal("the order is not kept")
	}
	p = page("/mail?folder=" + inbox + "&q=" + url.QueryEscape("from:ivan коробок") + "&scope=all")
	if !strings.Contains(p, "Заказ коробок") || strings.Contains(p, "Скидки недели") || !strings.Contains(p, "data-hl=") || !strings.Contains(p, "msearch-res") ||
		!strings.Contains(p, `<option value="all" selected>`) || !strings.Contains(p, `scope=all&letter=`) {
		t.Fatal("search")
	}
	sug, _ := do(anna, "GET", "/mail/suggest?q=from:iv", nil, 200)
	var got struct {
		Recent []string            `json:"recent"`
		People []map[string]string `json:"people"`
	}
	must(json.Unmarshal([]byte(sug), &got))
	if len(got.Recent) != 1 || len(got.People) != 1 || got.People[0]["email"] != "ivan@client.test" { // the word typed, less its operator
		t.Fatalf("suggest = %s", sug)
	}
	if sug, _ = do(anna, "GET", "/mail/suggest?q=zzz", nil, 200); strings.Contains(sug, "коробок") || strings.Contains(sug, "ivan") {
		t.Fatalf("suggest zzz = %s", sug)
	}
	if sug, _ = do(anna, "GET", "/mail/suggest?q=", nil, 200); !strings.Contains(sug, "from:ivan коробок") {
		t.Fatalf("recent = %s", sug)
	}
	if sug, _ = do(eve, "GET", "/mail/suggest?q=", nil, 200); strings.Contains(sug, "коробок") {
		t.Fatal("eve sees anna's searches")
	}

	// a saved search is a search folder
	_, res = do(anna, "POST", "/mail/searches", url.Values{"name": {"Иван"}, "q": {"from:ivan"}, "back": {"/mail"}}, 303)
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "/mail?folder=search%3A") {
		t.Fatalf("saved to %s", loc)
	}
	folder, _ := url.Parse(loc)
	sid := strings.TrimPrefix(folder.Query().Get("folder"), mail.SearchFolder)
	if p = page(loc); !strings.Contains(p, "Заказ коробок") || !strings.Contains(p, "msearches") || !strings.Contains(p, "<code>from:ivan</code>") {
		t.Fatalf("search folder: %.3000s", p)
	}
	do(eve, "GET", loc, nil, 404)
	do(eve, "POST", "/mail/searches/"+sid+"/delete", url.Values{}, 404)
	do(anna, "POST", "/mail/searches/"+sid+"/save", url.Values{"name": {"Иван П."}, "q": {"from:ivan is:unread"}}, 303)
	if p = page("/mail"); !strings.Contains(p, "Иван П.") {
		t.Fatal("renamed search folder")
	}
	do(anna, "POST", "/mail/searches/"+sid+"/delete", url.Values{}, 303)
	if p = page("/mail"); strings.Contains(p, "msearches") {
		t.Fatal("the search folder stays")
	}
	do(anna, "POST", "/mail/searches/recent/clear", url.Values{}, 200)
	if sug, _ = do(anna, "GET", "/mail/suggest?q=", nil, 200); strings.Contains(sug, "коробок") {
		t.Fatal("recent searches stay")
	}
}
