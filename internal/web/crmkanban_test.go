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
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMKanbanQuickHTTP: the kanban of deals and leads — each column shows its sum and every card its
// part of it (the page recounts them on a move); «+» of an open column makes a card right in that stage, with the
// client found by name or a new contact, asking for the required fields; a lead is not made «Качественный» this way.
func TestCRMKanbanQuickHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe, second, won := pipes[0].ID, pipes[0].Stages[1].ID, ""
	for _, st := range pipes[0].Stages {
		if st.Semantic == "success" {
			won = st.ID
		}
	}

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Admin: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(path string, form url.Values, header ...string) (int, string, string) {
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
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	asJSON := []string{"Accept", "application/json"}
	company, err := srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "Ромашка"})
	must(err)
	_, err = srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Старая", PipelineID: pipe, Stage: crm.Stage{ID: second}, Amount: 500})
	must(err)

	// the kanban: each column's sum, the cards' parts of it, «+» in the open stages only, the clients' hints
	_, page, _ := do("/crm/deals", nil)
	for _, want := range []string{`class="colsum" data-sum="500"`, `data-sum="500"`, `data-quick`, `list="crmclients"`, `<option value="Ромашка">`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the kanban misses %s", want)
		}
	}
	if strings.Contains(page, `data-quick><input type="hidden" name="stage" value="`+won+`"`) || !strings.Contains(page, `data-quick><input type="hidden" name="stage" value="`+second+`"`) {
		t.Fatal("«+» in a final stage")
	}

	// «+»: the card goes to its column with the company found by name
	code, body, _ := do("/crm/deals/new", url.Values{"title": {"Быстрая"}, "pipeline": {pipe}, "stage": {second}, "amount": {"1 500"},
		"client": {"ромашка"}}, asJSON...)
	var res struct{ ID, Card, Error string }
	must(json.Unmarshal([]byte(body), &res))
	if code != http.StatusCreated || !strings.Contains(res.Card, "Быстрая") || !strings.Contains(res.Card, `data-sum="1500"`) {
		t.Fatalf("quick deal: %d %s", code, body)
	}
	d, err := srv.CRM.Deal(ctx, res.ID)
	must(err)
	if d.Stage.ID != second || d.Company.ID != company {
		t.Fatalf("quick deal: stage %s company %s", d.Stage.ID, d.Company.ID)
	}
	// a client not known: a new contact
	_, body, _ = do("/crm/deals/new", url.Values{"title": {"С контактом"}, "pipeline": {pipe}, "stage": {second}, "client": {"Пётр Новиков"}}, asJSON...)
	res = struct{ ID, Card, Error string }{}
	must(json.Unmarshal([]byte(body), &res))
	var contact string
	must(pool.QueryRow(ctx, `SELECT x.name FROM crm_deal_contacts dc JOIN crm_contacts x ON x.id = dc.contact_id WHERE dc.deal_id = $1`, res.ID).Scan(&contact))
	if contact != "Пётр Новиков" {
		t.Fatalf("quick deal's contact: %q", contact)
	}

	// a field required always: the answer names it, sent with it the card is made
	if code, _, _ := do("/settings/crm-deal-fields", url.Values{"name": {"Регион"}, "kind": {"string"}, "required": {"1"}, "required_at": {"all"}}); code != http.StatusSeeOther {
		t.Fatalf("add field: %d", code)
	}
	var region string
	must(pool.QueryRow(ctx, `SELECT id FROM crm_deal_fields WHERE name = 'Регион'`).Scan(&region))
	quick := url.Values{"title": {"С полем"}, "pipeline": {pipe}, "stage": {second}}
	if code, body, _ := do("/crm/deals/new", quick, asJSON...); code != http.StatusUnprocessableEntity || !strings.Contains(body, region) {
		t.Fatalf("required field: %d %s", code, body)
	}
	quick.Set("f_"+region, "Север")
	if code, body, _ := do("/crm/deals/new", quick, asJSON...); code != http.StatusCreated {
		t.Fatalf("with the required field: %d %s", code, body)
	}

	// leads: into a column of the kanban, but «Качественный» only by conversion
	stages, err := srv.CRM.LeadStages(ctx)
	must(err)
	var success string
	for _, st := range stages {
		if st.Semantic == "success" {
			success = st.ID
		}
	}
	code, body, _ = do("/crm/leads/new", url.Values{"title": {"Лид"}, "stage": {stages[1].ID}, "name": {"Иван"}, "phone": {"+7 900"}}, asJSON...)
	res = struct{ ID, Card, Error string }{}
	must(json.Unmarshal([]byte(body), &res))
	l, err := srv.CRM.Lead(ctx, res.ID)
	must(err)
	if code != http.StatusCreated || l.Stage.ID != stages[1].ID || l.Name != "Иван" || l.Phone != "+7 900" {
		t.Fatalf("quick lead: %d %s", code, body)
	}
	if code, _, _ := do("/crm/leads/new", url.Values{"title": {"Лид"}, "stage": {success}}, asJSON...); code < 400 {
		t.Fatalf("quick lead in «Качественный»: %d", code)
	}
	// the usual form still opens the new card
	if code, _, loc := do("/crm/leads/new", url.Values{"title": {"Обычный"}}); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/crm/leads/") {
		t.Fatalf("form: %d %s", code, loc)
	}
}
