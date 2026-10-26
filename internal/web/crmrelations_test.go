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

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCRMRelationsHTTP: «Связи» — the administrator binds a smart process to deals in its settings (an employee
// cannot); the element's card offers the deals to bind it to and binds it; the deal's card lists the child and its
// «+ Создать» makes a new element bound to the deal with the deal's client; REST shows parent_ids, sets them, lists
// the children and creates an element with its parent.
func TestCRMRelationsHTTP(t *testing.T) {
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
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@t', true) RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	typ, err := srv.CRM.AddType(ctx, "Договоры", "")
	must(err)
	tp, err := srv.CRM.PipelinesOf(ctx, typ)
	must(err)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	company, err := srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "ООО Ромашка"})
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Большая сделка", PipelineID: pipes[0].ID, Company: crm.Ref{ID: company}})
	must(err)
	elem, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Договор 1", PipelineID: tp[0].ID})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(cookie *http.Cookie, path string, form url.Values) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := client.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Location")
	}
	admin, worker := cookieOf(anna, true), cookieOf(petr, false)

	settings := url.Values{"op": {"update"}, "id": {typ}, "name": {"Договоры"}, "color": {"39A8EF"}, "with_relations": {"1"}, "parents": {"deals"}}
	do(worker, "/crm/types", settings)
	if k, _ := srv.CRM.TypeParents(ctx, typ); len(k) != 0 {
		t.Fatalf("an employee set the relations: %q", k)
	}
	if code, _, _ := do(admin, "/crm/types", settings); code != http.StatusSeeOther {
		t.Fatalf("saving the relations: %d", code)
	}
	if k, _ := srv.CRM.TypeParents(ctx, typ); len(k) != 1 || k[0] != "" {
		t.Fatalf("relations saved: %q", k)
	}
	if code, body, _ := do(admin, "/crm/types", nil); code != http.StatusOK || !strings.Contains(body, `name="parents" value="deals" checked`) {
		t.Fatalf("the settings: %d %s", code, body)
	}

	// the element's card: the deal to choose, binding it
	code, body, _ := do(admin, "/crm/deals/"+elem, nil)
	if code != http.StatusOK || !strings.Contains(body, `action="/crm/deals/`+elem+`/parent"`) || !strings.Contains(body, `<option value="`+deal+`">Большая сделка`) {
		t.Fatalf("the element's card: %d %s", code, body)
	}
	if code, _, _ := do(admin, "/crm/deals/"+elem+"/parent", url.Values{"kind": {"deals"}, "parent": {deal}}); code != http.StatusSeeOther {
		t.Fatalf("binding: %d", code)
	}
	if code, body, _ = do(admin, "/crm/deals/"+elem, nil); !strings.Contains(body, `<option value="`+deal+`" selected>`) || !strings.Contains(body, "привязано к «Большая сделка»") {
		t.Fatalf("the element bound: %d %s", code, body)
	}

	// the deal's card: the child and «+ Создать» bound to it with its client
	if code, body, _ = do(admin, "/crm/deals/"+deal, nil); code != http.StatusOK || !strings.Contains(body, `href="/crm/deals/`+elem+`"`) ||
		!strings.Contains(body, `name="parent" value="`+deal+`"`) || !strings.Contains(body, `name="company" value="`+company+`"`) {
		t.Fatalf("the deal's card: %d %s", code, body)
	}
	code, _, loc := do(admin, "/crm/deals/new", url.Values{"pipeline": {tp[0].ID}, "parent": {deal}, "company": {company}, "title": {"Договор 2"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/crm/deals/") {
		t.Fatalf("«+ Создать»: %d %s", code, loc)
	}
	kids, err := srv.CRM.Children(ctx, anna, deal)
	must(err)
	if len(kids) != 2 || kids[0].Title != "Договор 2" || kids[0].Company.ID != company || kids[0].TypeID != typ {
		t.Fatalf("children: %+v", kids)
	}
	// REST
	raw := func(method, path, body string) (int, []byte) {
		t.Helper()
		tok, err := srv.Keys.Create(ctx, anna, "rel", []string{"crm:read", "crm:write"}, 0)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	var card struct {
		ID        string   `json:"id"`
		ParentIDs []string `json:"parent_ids"`
	}
	code, b := raw("GET", "/api/v1/crm/deals/"+kids[0].ID, "")
	must(json.Unmarshal(b, &card))
	if code != http.StatusOK || len(card.ParentIDs) != 1 || card.ParentIDs[0] != deal {
		t.Fatalf("GET parent_ids: %d %s", code, b)
	}
	if code, b = raw("PUT", "/api/v1/crm/deals/"+kids[0].ID+"/parents", `{"parent_ids": []}`); code != http.StatusOK {
		t.Fatalf("PUT parents: %d %s", code, b)
	}
	if code, b = raw("PUT", "/api/v1/crm/deals/"+kids[0].ID+"/parents", `{"parent_ids": ["`+elem+`"]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("PUT a parent of a kind not bound: %d %s", code, b)
	}
	var list []struct{ ID string }
	code, b = raw("GET", "/api/v1/crm/deals/"+deal+"/children", "")
	must(json.Unmarshal(b, &list))
	if code != http.StatusOK || len(list) != 1 || list[0].ID != elem {
		t.Fatalf("GET children: %d %s", code, b)
	}
	code, b = raw("POST", "/api/v1/crm/deals", `{"title": "Договор 3", "pipeline_id": "`+tp[0].ID+`", "parent_ids": ["`+deal+`"]}`)
	must(json.Unmarshal(b, &card))
	if code != http.StatusCreated || len(card.ParentIDs) != 1 || card.ParentIDs[0] != deal {
		t.Fatalf("POST with parent_ids: %d %s", code, b)
	}
}
