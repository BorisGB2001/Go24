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

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMCardLayoutHTTP: «Настроить карточку» — the standard sections «О сделке» and «Дополнительно»;
// one's own view (sections renamed and added, fields moved, a field hidden — in the form under «Скрытые поля», its
// value kept on saving); the administrator's common view for everybody, «Вернуть общий вид», «Сбросить на
// стандартный»; a non-administrator may not make the common one; a smart process has its own view.
func TestCRMCardLayoutHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	var bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe := pipes[0].ID

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	as := func(c *http.Cookie, path string, form url.Values) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(c)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	rec = httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: bob}))
	bobCookie := rec.Result().Cookies()[0]
	do := func(path string, form url.Values) (int, string, string) { t.Helper(); return as(cookie, path, form) }
	must(srv.Tasks.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Город", Kind: "string"}))
	var city string
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_deal_fields WHERE name = 'Город'`).Scan(&city))
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Альфа", PipelineID: pipe, Responsible: crm.Ref{ID: bob},
		Comment: "Важный клиент"})
	must(err)
	must(srv.Tasks.SetFieldValuesOf(ctx, tasks.CRMFieldSets["deal"], deal, map[string]string{city: "Москва"}))
	card := "/crm/deals/" + deal
	before := func(page, a, b string) bool {
		i, j := strings.Index(page, a), strings.Index(page, b)
		return i >= 0 && j >= 0 && i < j
	}

	// the standard view: «О сделке» with the comment, «Дополнительно» with the city
	_, page, _ := do(card, nil)
	if !before(page, ">О сделке</h3>", "Важный клиент") || !before(page, "Важный клиент", ">Дополнительно</h3>") ||
		!before(page, ">Дополнительно</h3>", "Москва") || !strings.Contains(page, `href="`+card+`?layout=1"`) {
		t.Fatal("standard view")
	}
	_, page, _ = do(card+"?layout=1", nil)
	if !strings.Contains(page, `data-crmlayout`) || !strings.Contains(page, `data-code="f:`+city+`"`) ||
		!strings.Contains(page, "Сделать общим видом для всех") || strings.Contains(page, "Вернуть общий вид") {
		t.Fatal("editor")
	}

	// anna's own: «Клиент» first with the city, the comment hidden
	mine := `{"sections":[{"name":"Клиент","fields":["f:` + city + `","company","contacts"]},{"code":"main","fields":["amount","responsible"]}],"hidden":["comment"]}`
	code, _, loc := do(card+"/layout", url.Values{"layout": {mine}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, card+"?msg=") {
		t.Fatalf("save mine: %d %s", code, loc)
	}
	_, page, _ = do(card, nil)
	if !before(page, ">Клиент</h3>", "Москва") || !before(page, "Москва", ">О сделке</h3>") || strings.Contains(page, "Важный клиент") ||
		strings.Contains(page, ">Дополнительно</h3>") {
		t.Fatal("own view")
	}
	// in the form the hidden comment waits under «Скрытые поля»; saving keeps it
	_, page, _ = do(card+"?edit=1", nil)
	if !before(page, `<legend>Клиент</legend>`, `name="f_`+city+`"`) || !before(page, "Скрытые поля", `name="comment"`) {
		t.Fatal("form by sections")
	}
	_, page, _ = do(card+"?layout=1", nil)
	if !strings.Contains(page, "Вернуть общий вид") || strings.Contains(page, "Сбросить на стандартный") {
		t.Fatal("editor of one's own view")
	}
	// bob has the standard one and may not make the common one
	if _, page, _ = as(bobCookie, card, nil); !strings.Contains(page, ">О сделке</h3>") || !strings.Contains(page, "Важный клиент") {
		t.Fatal("bob took anna's view")
	}
	if code, _, _ := as(bobCookie, card+"/layout", url.Values{"layout": {mine}, "common": {"1"}}); code < 400 {
		t.Fatalf("bob's common view: %d", code)
	}
	if _, page, _ = as(bobCookie, card+"?layout=1", nil); strings.Contains(page, "Сделать общим видом для всех") {
		t.Fatal("bob is offered the common view")
	}
	// bob's own view, then anna's common one: bob keeps his; «Вернуть общий вид» — he sees the common one
	bobs := `{"sections":[{"code":"main","name":"Главное","fields":["responsible"]}],"hidden":[]}`
	as(bobCookie, card+"/layout", url.Values{"layout": {bobs}})
	common := `{"sections":[{"code":"main","fields":["responsible","comment"]},{"name":"Финансы","fields":["amount"]}],"hidden":["f:` + city + `"]}`
	if code, _, _ := do(card+"/layout", url.Values{"layout": {common}, "common": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("save common: %d", code)
	}
	if _, page, _ = as(bobCookie, card, nil); !strings.Contains(page, ">Главное</h3>") {
		t.Fatal("bob lost his own view")
	}
	as(bobCookie, card+"/layout", url.Values{"reset": {"mine"}})
	if _, page, _ = as(bobCookie, card, nil); !strings.Contains(page, ">Финансы</h3>") || strings.Contains(page, "Москва") ||
		!before(page, ">О сделке</h3>", "Важный клиент") {
		t.Fatal("bob back to the common view")
	}
	// anna's own view gave way to the common one she made
	if _, page, _ = do(card, nil); !strings.Contains(page, ">Финансы</h3>") {
		t.Fatal("anna's view after making it common")
	}
	// a smart process has its own view; bad layouts are refused
	typ, err := srv.CRM.AddType(ctx, "Договоры", "")
	must(err)
	tp, err := srv.CRM.PipelinesOf(ctx, typ)
	must(err)
	item, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Договор 1", PipelineID: tp[0].ID, TypeID: typ, Responsible: crm.Ref{ID: anna}})
	must(err)
	if _, page, _ = do("/crm/deals/"+item, nil); !strings.Contains(page, ">Об элементе</h3>") || strings.Contains(page, ">Финансы</h3>") {
		t.Fatal("smart process view")
	}
	for _, bad := range []string{"{", `{"sections":[{"code":"evil","fields":[]}]}`} {
		if code, _, loc := do(card+"/layout", url.Values{"layout": {bad}}); code != http.StatusSeeOther || !strings.HasPrefix(loc, card+"?layout=1&msg=") {
			t.Fatalf("bad layout %s: %d %s", bad, code, loc)
		}
	}
	// «Сбросить на стандартный»
	do(card+"/layout", url.Values{"reset": {"common"}})
	if _, page, _ = as(bobCookie, card, nil); !strings.Contains(page, ">Дополнительно</h3>") || strings.Contains(page, ">Финансы</h3>") {
		t.Fatal("reset to standard")
	}
}
