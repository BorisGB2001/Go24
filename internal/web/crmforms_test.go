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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/openlines"
	"go24/internal/sites"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMFormsHTTP: an administrator makes a «CRM-форма» (the fields in the editor's order, the custom field of the
// lead, the consent); someone else may not; the client sees it at /cf/{token} and sends it: a robot's fast sending
// makes nothing, a refusal shows the form again with what was typed, a good one makes the lead and shows the text —
// or goes to the form's address (inside a site — the page on top does); the results link the lead; a switched off
// form is gone; REST lists the forms and the results.
func TestCRMFormsHTTP(t *testing.T) {
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
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool), BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	must(srv.Tasks.AddFieldTo(ctx, tasks.CRMFieldSets["lead"], tasks.Field{Name: "Город", Kind: "string"}))
	var city string
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_lead_fields WHERE name = 'Город'`).Scan(&city))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	admin, other := cookieOf(auth.Session{UserID: anna, Admin: true}), cookieOf(auth.Session{UserID: bob})
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(c *http.Cookie, path string, form url.Values) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if c != nil {
			req.AddCookie(c)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", hs.URL)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}

	// the editor: phone before name, the city with its own label, the comment not ticked
	edit := url.Values{"name": {"Заявка"}, "active": {"1"}, "scheme": {"lead"}, "consent": {"Согласен на обработку данных"},
		"success": {"Ждите звонка"}, "key": {"phone", "name", "f:" + city, "comment"}, "on_phone": {"1"}, "on_name": {"1"},
		"req_name": {"1"}, "on_f:" + city: {"1"}, "label_f:" + city: {"Ваш город"}}
	if code, _, _ := do(other, "/crm/forms/new", edit); code < 400 {
		t.Fatalf("not an administrator makes a form: %d", code)
	}
	code, _, loc := do(admin, "/crm/forms/new", edit)
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/crm/forms/") {
		t.Fatalf("create: %d %s", code, loc)
	}
	id := strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/crm/forms/")
	f, err := srv.CRM.Form(ctx, id)
	must(err)
	if len(f.Fields) != 3 || f.Fields[0].Key != "phone" || !f.Fields[1].Required || f.Fields[2].Label != "Ваш город" {
		t.Fatalf("fields = %+v", f.Fields)
	}
	_, page, _ := do(admin, "/crm/forms/"+id, nil)
	if !strings.Contains(page, `data-form="https://go24.test/cf/`+f.Token+`"`) || !strings.Contains(page, `value="Ваш город"`) {
		t.Fatal("the editor: no embedding code or label")
	}
	if _, page, _ = do(other, "/crm/forms", nil); !strings.Contains(page, ">Заявка</b>") || strings.Contains(page, `href="/crm/forms/`+id+`/results"`) {
		t.Fatal("the list for someone else")
	}

	// the client
	pub := "/cf/" + f.Token
	_, page, _ = do(nil, pub, nil)
	if p, n := strings.Index(page, `name="phone"`), strings.Index(page, `name="name"`); p < 0 || n < p || !strings.Contains(page, "Ваш город") ||
		!strings.Contains(page, `name="consent"`) {
		t.Fatal("the public form")
	}
	leads := func() int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_leads`).Scan(&n))
		return n
	}
	ago := strconv.FormatInt(time.Now().Add(-10*time.Second).Unix(), 10)
	if _, page, _ = do(nil, pub, url.Values{"t": {strconv.FormatInt(time.Now().Unix(), 10)}, "name": {"Робот"}, "consent": {"1"}}); !strings.Contains(page, "Ждите звонка") || leads() != 0 {
		t.Fatal("a robot's fast sending")
	}
	code, page, _ = do(nil, pub, url.Values{"t": {ago}, "name": {"Иван"}, "phone": {"+79001234567"}})
	if code != http.StatusUnprocessableEntity || !strings.Contains(page, `value="Иван"`) || !strings.Contains(page, `value="`+ago+`"`) || leads() != 0 {
		t.Fatalf("no consent: %d", code)
	}
	code, page, _ = do(nil, pub, url.Values{"t": {ago}, "name": {"Иван"}, "phone": {"+79001234567"}, "f:" + city: {"Тверь"}, "consent": {"1"},
		"page": {"https://site.ru/contacts"}})
	if code != http.StatusOK || !strings.Contains(page, "Ждите звонка") {
		t.Fatalf("send: %d", code)
	}
	var leadID, title, comment string
	must(pool.QueryRow(ctx, `SELECT id::text, title, comment FROM crm_leads`).Scan(&leadID, &title, &comment))
	if !strings.Contains(title, "Заявка") || !strings.Contains(title, "Иван") || !strings.Contains(comment, "https://site.ru/contacts") {
		t.Fatalf("lead %q %q", title, comment)
	}
	_, page, _ = do(admin, "/crm/forms/"+id+"/results", nil)
	if !strings.Contains(page, `href="/crm/leads/`+leadID+`"`) || !strings.Contains(page, "Тверь") {
		t.Fatal("results")
	}
	if code, _, _ := do(other, "/crm/forms/"+id+"/results", nil); code < 400 {
		t.Fatal("someone else sees the results")
	}

	// the address after sending: at once, or from inside a site — by the page on top
	edit.Set("redirect", "https://site.ru/thanks")
	edit.Del("consent")
	if code, _, _ := do(admin, "/crm/forms/"+id, edit); code != http.StatusSeeOther {
		t.Fatalf("save: %d", code)
	}
	if code, _, loc := do(nil, pub, url.Values{"t": {ago}, "name": {"Пётр"}}); code != http.StatusSeeOther || loc != "https://site.ru/thanks" {
		t.Fatalf("redirect: %d %s", code, loc)
	}
	if _, page, _ := do(nil, pub, url.Values{"t": {ago}, "name": {"Олег"}, "embed": {"1"}}); !strings.Contains(page, "top.location.href") {
		t.Fatal("redirect inside a site")
	}

	// sent in an open line's dialogue (GO24DEV-703): the operator's «Форма», the card in the widget, the form inside it
	// with the dialogue's key — the lead is the dialogue's, no redirect inside the chat, the result links the dialogue
	crmFormHits = &sites.Hits{Max: 5, Per: 10 * time.Minute}
	srv.OpenLines = openlines.NewStore(pool, srv.CRM)
	srv.OpenLines.Tr = func(key string, args ...any) string { return key }
	wid, err := srv.OpenLines.SaveWidget(ctx, anna, openlines.Widget{Name: "Линия", Active: true})
	must(err)
	sid, vtok, err := srv.OpenLines.Visit(ctx, wid, "", "")
	must(err)
	must(srv.OpenLines.VisitorSay(ctx, wid, vtok, "Здравствуйте"))
	if _, page, _ := do(admin, "/openlines?s="+sid, nil); !strings.Contains(page, `name="op" value="form"`) || !strings.Contains(page, `<option value="`+id+`">Заявка</option>`) {
		t.Fatal("no «Форма» in the dialogue")
	}
	if code, _, loc := do(admin, "/openlines/s/"+sid, url.Values{"op": {"form"}, "form": {id}}); code != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Fatalf("send the form: %d %s", code, loc)
	}
	req, _ := http.NewRequest("GET", hs.URL+"/w/"+wid+"/api/messages", nil)
	req.Header.Set("X-Go24-Visitor", vtok)
	res, err := client.Do(req)
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var got struct {
		Messages []struct {
			Body string
			Form *struct {
				ID, URL string
				Done    bool
			}
		}
	}
	must(json.Unmarshal(raw, &got))
	last := got.Messages[len(got.Messages)-1]
	if last.Form == nil || last.Form.ID != id || last.Body != "ol.form_body" || !strings.Contains(last.Form.URL, "/cf/"+f.Token+"?embed=1&ol=") {
		t.Fatalf("the widget's form card: %s", raw)
	}
	inChat, _ := url.Parse(last.Form.URL)
	if _, page, _ := do(nil, inChat.RequestURI(), nil); !strings.Contains(page, `name="ol" value="`+inChat.Query().Get("ol")+`"`) {
		t.Fatal("the form in the chat has no dialogue's key")
	}
	before := leads()
	_, page, _ = do(nil, pub, url.Values{"t": {ago}, "name": {"Ольга"}, "embed": {"1"}, "ol": {inChat.Query().Get("ol")}})
	if strings.Contains(page, "top.location.href") || !strings.Contains(page, "cf-done") {
		t.Fatal("the form filled in the chat")
	}
	var bound string
	must(pool.QueryRow(ctx, `SELECT coalesce(lead_id::text, '') FROM ol_sessions WHERE id = $1`, sid).Scan(&bound))
	if leads() != before+1 || bound == "" {
		t.Fatalf("the filling's lead is not the dialogue's: %d → %d, %q", before, leads(), bound)
	}
	if _, page, _ := do(admin, "/crm/forms/"+id+"/results", nil); !strings.Contains(page, `href="/openlines?s=`+sid+`"`) {
		t.Fatal("the result has no dialogue")
	}
	if _, page, _ := do(admin, "/openlines?s="+sid, nil); !strings.Contains(page, "ol.form_filled") && !strings.Contains(page, "ol-formdone") {
		t.Fatal("the operator does not see the form filled")
	}
	wtok, err := srv.Keys.Create(ctx, anna, "ol", []string{"openlines:read", "openlines:write"}, 0)
	must(err)
	req, _ = http.NewRequest("POST", hs.URL+"/api/v1/openlines/dialogs/"+sid+"/form", strings.NewReader(`{"form_id": "`+id+`"}`))
	req.Header.Set("Authorization", "Bearer "+wtok)
	res, err = client.Do(req)
	must(err)
	res.Body.Close()
	req, _ = http.NewRequest("GET", hs.URL+"/api/v1/openlines/dialogs/"+sid+"/messages", nil)
	req.Header.Set("Authorization", "Bearer "+wtok)
	res2, err := client.Do(req)
	must(err)
	raw, _ = io.ReadAll(res2.Body)
	res2.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(raw), `"kind": "form"`) || !strings.Contains(string(raw), `"done_at"`) {
		t.Fatalf("REST form: %d %s", res.StatusCode, raw)
	}

	// REST
	tok, err := srv.Keys.Create(ctx, anna, "crm", []string{"crm:read"}, 0)
	must(err)
	api := func(path string) (int, string) {
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if code, out := api("/api/v1/crm/forms"); code != http.StatusOK || !strings.Contains(out, "https://go24.test/cf/"+f.Token) {
		t.Fatalf("REST forms: %d %s", code, out)
	}
	if code, out := api("/api/v1/crm/forms/" + id + "/results"); code != http.StatusOK || !strings.Contains(out, leadID) {
		t.Fatalf("REST results: %d %s", code, out)
	}

	// switched off; deleted
	edit.Del("active")
	do(admin, "/crm/forms/"+id, edit)
	if code, _, _ := do(nil, pub, nil); code != http.StatusNotFound {
		t.Fatalf("a switched off form: %d", code)
	}
	if code, _, loc := do(admin, "/crm/forms/"+id, url.Values{"op": {"delete"}}); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/crm/forms?") {
		t.Fatalf("delete: %d %s", code, loc)
	}
	if leads() != 4 {
		t.Fatal("the leads went with the form")
	}
}
