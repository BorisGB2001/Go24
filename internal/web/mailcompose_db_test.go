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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/mail"
	"go24/internal/tasks"
)

// TestMailComposeHTTP drives the editor's pages over HTTP against the test database: the editor of a new letter,
// the draft kept and opened again (from «Черновики» too), «Отправить» with «Отменить», «Отправить позже», the
// suggestions, the settings — and nobody else reaches the user's drafts and signatures.
func TestMailComposeHTTP(t *testing.T) {
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
	var anna, eve string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева Смирнова', 'eve@go24.test') RETURNING id`).Scan(&eve); err != nil {
		t.Fatal(err)
	}
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Mail: mail.NewStore(pool, "x"),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	box, err := srv.Mail.Add(ctx, anna, mail.Settings{Email: "anna@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Mail.Add(ctx, eve, mail.Settings{Email: "eve@go24.test", IMAPHost: "mail.invalid", IMAPPort: 993, SMTPHost: "mail.invalid", SMTPPort: 465, Password: "x"}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		if err := srv.Sessions.Set(rec, auth.Session{UserID: id}); err != nil {
			t.Fatal(err)
		}
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
		req, _ := http.NewRequest(method, ts.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "application/json")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	js := func(s string) map[string]any {
		out := map[string]any{}
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			t.Fatalf("not JSON: %s", s)
		}
		return out
	}

	if page := do(anna, "GET", "/mail?new=1&box="+box, nil, 200); !strings.Contains(page, "data-compose") || !strings.Contains(page, "js/compose.js") {
		t.Fatal("no editor on the page")
	}
	draft := url.Values{"box": {box}, "to": {`"Клиент" <c@x.ru>`}, "subject": {"Счёт"}, "html": {"<p>Привет</p><script>x()</script>"}, "importance": {"1"}}
	id := js(do(anna, "POST", "/mail/drafts/save", draft, 200))["id"].(string)
	if ed := do(anna, "GET", "/mail/compose?draft="+id, nil, 200); !strings.Contains(ed, `value="Счёт"`) || strings.Contains(ed, "script>x") ||
		!strings.Contains(ed, `value="1"`) {
		t.Fatalf("the draft opened again: %.2000s", ed)
	}
	f, _ := srv.Mail.Draft(ctx, anna, id)
	drafts, _ := srv.Mail.Folders(ctx, anna, box)
	var df string
	for _, x := range drafts {
		if x.Role == "drafts" {
			df = x.ID
		}
	}
	if page := do(anna, "GET", "/mail?folder="+df+"&letter="+id, nil, 200); !strings.Contains(page, "data-compose") || f.Subject != "Счёт" {
		t.Fatal("a draft from «Черновики» does not open in the editor")
	}
	if l := do(anna, "GET", "/mail/letters/"+id+"?folder="+df, nil, 200); !strings.Contains(l, "data-compose") {
		t.Fatal("a draft in the reading pane is not the editor")
	}
	// nobody else reaches it
	do(eve, "GET", "/mail/compose?draft="+id, nil, 404)
	do(eve, "POST", "/mail/drafts/save", url.Values{"draft": {id}, "box": {box}}, 404)
	do(eve, "POST", "/mail/drafts/"+id+"/cancel", nil, 422)
	do(eve, "POST", "/mail/drafts/"+id+"/delete", nil, 200)
	if _, err := srv.Mail.Draft(ctx, anna, id); err != nil {
		t.Fatalf("someone else deleted the draft: %v", err)
	}
	// «Отправить»: nobody to send to — said; then «Отменить» for 5 seconds
	if e := js(do(anna, "POST", "/mail/drafts/send", url.Values{"draft": {id}, "box": {box}}, 422)); e["error"] == "" {
		t.Fatal("no message")
	}
	draft.Set("draft", id)
	if r := js(do(anna, "POST", "/mail/drafts/send", draft, 200)); r["undo"] != float64(5) || r["id"] != id {
		t.Fatalf("send = %v", r)
	}
	if d, _ := srv.Mail.Draft(ctx, anna, id); d.SendAt == nil || d.Scheduled {
		t.Fatalf("waiting for undo: %+v", d)
	}
	if r := js(do(anna, "POST", "/mail/drafts/"+id+"/cancel", url.Values{}, 200)); !strings.Contains(r["href"].(string), "draft="+id) {
		t.Fatalf("cancel = %v", r)
	}
	if d, _ := srv.Mail.Draft(ctx, anna, id); d.SendAt != nil {
		t.Fatalf("undone: %+v", d)
	}
	draft.Set("send_at", "tomorrow_am")
	if r := js(do(anna, "POST", "/mail/drafts/send", draft, 200)); r["scheduled"] == nil {
		t.Fatalf("send later = %v", r)
	}
	if d, _ := srv.Mail.Draft(ctx, anna, id); d.SendAt == nil || !d.Scheduled {
		t.Fatalf("scheduled: %+v", d)
	}
	draft.Set("send_at", "2001-01-01T10:00")
	do(anna, "POST", "/mail/drafts/send", draft, 422)
	do(anna, "POST", "/mail/drafts/"+id+"/delete", url.Values{}, 200)

	// suggestions: go24's people
	var got []map[string]string
	_ = json.Unmarshal([]byte(do(anna, "GET", "/mail/rcpt?q="+url.QueryEscape("Смирнова"), nil, 200)), &got)
	if len(got) != 1 || got[0]["email"] != "eve@go24.test" || got[0]["kind"] != "employee" {
		t.Fatalf("suggestions = %v", got)
	}
	// the address book: personal contacts are the user's own, each source marked; the CRM — as the user's role lets
	kept := js(do(anna, "POST", "/mail/contacts", url.Values{"email": {"Пётр Сидоров <sid@ip.test>"}, "company": {"ИП Сидоров"}}, 200))
	if kept["name"] != "Пётр Сидоров" || kept["email"] != "sid@ip.test" || kept["id"] == "" {
		t.Fatalf("contact = %v", kept)
	}
	cid, _ := kept["id"].(string)
	do(anna, "POST", "/mail/contacts", url.Values{"email": {"not an address"}}, 422)
	if again := js(do(anna, "POST", "/mail/contacts", url.Values{"email": {"SID@ip.test"}, "name": {"Сидоров П."}}, 200)); again["id"] != cid {
		t.Fatalf("the same address makes a second contact: %v", again)
	}
	if _, err := srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "ООО Ромашка", Email: "info@romashka.test"}); err != nil {
		t.Fatal(err)
	}
	find := func(who, path string) map[string]string {
		var list []map[string]any
		_ = json.Unmarshal([]byte(do(who, "GET", path, nil, 200)), &list)
		kinds := map[string]string{}
		for _, x := range list {
			kinds[x["email"].(string)], _ = x["kind"].(string)
		}
		return kinds
	}
	if k := find(anna, "/mail/rcpt?q="+url.QueryEscape("Сидоров")); k["sid@ip.test"] != "contact" {
		t.Fatalf("suggestions = %v", k)
	}
	if k := find(anna, "/mail/book?src=&q="); k["sid@ip.test"] != "contact" || k["eve@go24.test"] != "employee" || k["info@romashka.test"] != "crm_company" {
		t.Fatalf("book = %v", k)
	}
	if k := find(anna, "/mail/book?src=contact&q="); len(k) != 1 || k["sid@ip.test"] != "contact" {
		t.Fatalf("personal tab = %v", k)
	}
	if k := find(anna, "/mail/book?src=crm&q="+url.QueryEscape("Ромашка")); len(k) != 1 || k["info@romashka.test"] != "crm_company" {
		t.Fatalf("CRM tab = %v", k)
	}
	if k := find(eve, "/mail/book?src=contact&q="); len(k) != 0 {
		t.Fatalf("another's contacts = %v", k)
	}
	do(anna, "GET", "/mail/book?src=nope", nil, 404)
	do(eve, "POST", "/mail/contacts/"+cid+"/save", url.Values{"email": {"x@ip.test"}}, 404)
	do(eve, "POST", "/mail/contacts/"+cid+"/delete", url.Values{}, 404)
	if c := js(do(anna, "POST", "/mail/contacts/"+cid+"/save", url.Values{"name": {"Пётр"}, "email": {"petr@ip.test"}, "phone": {"+7 900"}}, 200)); c["email"] != "petr@ip.test" {
		t.Fatalf("changed = %v", c)
	}
	do(anna, "POST", "/mail/contacts/"+cid+"/delete", url.Values{}, 200)
	if k := find(anna, "/mail/book?src=contact&q="); len(k) != 0 {
		t.Fatalf("deleted = %v", k)
	}
	// settings: signatures and templates are the user's own
	do(anna, "GET", "/mail/settings?tab=signatures&edit=new", nil, 200)
	do(anna, "POST", "/mail/snippets/signature/new", url.Values{"name": {"Основная"}, "html": {"<p>Анна</p>"}}, 200)
	sigs, _ := srv.Mail.Snippets(ctx, anna, "signature")
	if len(sigs) != 1 {
		t.Fatalf("signatures = %+v", sigs)
	}
	do(anna, "POST", "/mail/settings/boxes/"+box, url.Values{"sig_new": {sigs[0].ID}}, 200)
	if ed := do(anna, "GET", "/mail/compose?box="+box, nil, 200); !strings.Contains(ed, `data-signature=&#34;`+sigs[0].ID) && !strings.Contains(ed, "data-signature") {
		t.Fatal("the mailbox's signature is not in a new letter")
	}
	do(eve, "POST", "/mail/snippets/signature/"+sigs[0].ID+"/delete", url.Values{}, 404)
	do(eve, "POST", "/mail/settings/boxes/"+box, url.Values{}, 404)
	do(anna, "POST", "/mail/settings", url.Values{"undo_seconds": {"10"}, "font": {"Arial"}, "font_size": {"12pt"}}, 200)
	if p, _ := srv.Mail.Prefs(ctx, anna); p.UndoSeconds != 10 || p.Font != "Arial" {
		t.Fatalf("settings = %+v", p)
	}
	do(anna, "POST", "/mail/settings", url.Values{"undo_seconds": {"60"}}, 422)
}
