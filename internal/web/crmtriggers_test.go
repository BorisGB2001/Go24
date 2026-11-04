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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCRMTriggersHTTP: the administrator sets «Изменение полей» with its fields, a condition and «back», and
// «Webhook» (an employee cannot); the settings show the fields and the webhook's code; the card's form changing
// the field moves the deal, REST with the code moves it on, a wrong code is refused; the client following a tracked
// link of «Переход по ссылке» moves it, one of the portal does not.
func TestCRMTriggersHTTP(t *testing.T) {
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
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	st := pipes[0].Stages
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Amount: 100, Responsible: crm.Ref{ID: anna}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(cookie *http.Cookie, path string, form url.Values) (int, string) {
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
		return resp.StatusCode, string(b)
	}
	stageOf := func() string {
		t.Helper()
		d, err := srv.CRM.Deal(ctx, deal)
		must(err)
		return d.Stage.ID
	}
	admin, worker := cookieOf(anna, true), cookieOf(petr, false)

	field := url.Values{"stage": {st[2].ID}, "kind": {"field"}, "fields": {"comment", "amount"}, "if_field": {"amount"}, "if_op": {"gt"},
		"if_value": {"50"}, "back_ok": {"1"}, "back": {"/crm/robots"}}
	if code, _ := do(worker, "/crm/triggers", field); code != http.StatusForbidden {
		t.Fatalf("an employee set a trigger: %d", code)
	}
	if code, body := do(admin, "/crm/triggers", field); code != http.StatusSeeOther {
		t.Fatalf("«Изменение полей»: %d %s", code, body)
	}
	if code, _ := do(admin, "/crm/triggers", url.Values{"stage": {st[3].ID}, "kind": {"webhook"}, "back": {"/crm/robots"}}); code != http.StatusSeeOther {
		t.Fatalf("«Webhook»: %d", code)
	}
	if code, _ := do(admin, "/crm/triggers", url.Values{"stage": {st[4].ID}, "kind": {"task"}, "task_status": {"completed"}, "back": {"/crm/robots"}}); code != http.StatusSeeOther {
		t.Fatalf("«Изменение статуса задачи»: %d", code)
	}
	if trs, _ := srv.CRM.Triggers(ctx, []string{st[4].ID}); len(trs) != 1 || trs[0].Params.Status != "completed" {
		t.Fatalf("the task trigger: %+v", trs)
	}
	if code, _ := do(admin, "/crm/triggers", url.Values{"stage": {st[4].ID}, "kind": {"link"}, "link_url": {"https://shop.test/offer"}, "back": {"/crm/robots"}}); code != http.StatusSeeOther {
		t.Fatalf("«Переход по ссылке»: %d", code)
	}
	trs, err := srv.CRM.Triggers(ctx, []string{st[2].ID, st[3].ID})
	must(err)
	var code string
	for _, tr := range trs {
		switch tr.Kind {
		case "field":
			if p := tr.Params; len(p.Fields) != 2 || !p.Back || p.If == nil || p.If.Field != "amount" || p.If.Value != "50" {
				t.Fatalf("the fields trigger: %+v", p)
			}
		case "webhook":
			code = tr.Params.Code
		}
	}
	if code == "" {
		t.Fatalf("triggers: %+v", trs)
	}
	if c, body := do(admin, "/crm/robots", nil); c != http.StatusOK || !strings.Contains(body, code) || !strings.Contains(body, `name="back_ok"`) ||
		!strings.Contains(body, `name="task_status"`) || !strings.Contains(body, `name="link_url"`) || !strings.Contains(body, "https://shop.test/offer") {
		t.Fatalf("the settings: %d", c)
	}

	// the card's form: the title alone keeps it, the comment moves it
	form := url.Values{"title": {"Поставка"}, "pipeline": {pipes[0].ID}, "stage": {st[0].ID}, "amount": {"100"}, "responsible": {anna}}
	if c, body := do(admin, "/crm/deals/"+deal, form); c != http.StatusSeeOther || stageOf() != st[0].ID {
		t.Fatalf("saved without a change: %d %s, at %s", c, body, stageOf())
	}
	form.Set("comment", "Клиент просит скидку")
	if c, _ := do(admin, "/crm/deals/"+deal, form); c != http.StatusSeeOther || stageOf() != st[2].ID {
		t.Fatalf("the comment changed, the deal at %s", stageOf())
	}

	// REST: «Webhook» by its code
	raw := func(body string) int {
		t.Helper()
		tok, err := srv.Keys.Create(ctx, anna, "hook", []string{"crm:read", "crm:write"}, 0)
		must(err)
		req, _ := http.NewRequest("POST", hs.URL+"/api/v1/crm/deals/"+deal+"/trigger", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		must(err)
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := raw(`{"code": "wrong"}`); c < 400 || stageOf() != st[2].ID {
		t.Fatalf("a wrong code: %d, at %s", c, stageOf())
	}
	if c := raw(`{"code": "` + code + `"}`); c != http.StatusOK || stageOf() != st[3].ID {
		t.Fatalf("the webhook: %d, at %s", c, stageOf())
	}

	// «Переход по ссылке»: one of the portal opening the link goes on and moves nothing, the client moves the deal
	_, err = pool.Exec(ctx, `INSERT INTO crm_trigger_links (token, entity, entity_id, url, author_id) VALUES ('tok1', 'deal', $1, 'https://shop.test/offer', $2)`, deal, anna)
	must(err)
	if c, _ := do(admin, "/cl/tok1", nil); c != http.StatusFound || stageOf() != st[3].ID {
		t.Fatalf("an employee followed the link: %d, at %s", c, stageOf())
	}
	follow := func(token string) (int, string) {
		t.Helper()
		resp, err := client.Get(hs.URL + "/cl/" + token)
		must(err)
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Location")
	}
	if c, _ := follow("nope"); c != http.StatusNotFound {
		t.Fatalf("an unknown link: %d", c)
	}
	if c, to := follow("tok1"); c != http.StatusFound || to != "https://shop.test/offer" || stageOf() != st[4].ID {
		t.Fatalf("the client followed the link: %d → %s, at %s", c, to, stageOf())
	}
}
