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

// TestCRMRequiredHTTP: «Обязательные поля» of deals — a field required always must be filled to create a deal (the
// form of a new deal asks for it), one required at a stage stops the kanban, the stage bar, the card's form and
// the group action there; the kanban gets the fields to ask for and moves the deal with them; the settings change it.
func TestCRMRequiredHTTP(t *testing.T) {
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
	pipe, first, won := pipes[0].ID, pipes[0].Stages[0].ID, pipes[0].Stages[2].ID

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
	fieldID := func(name string) string {
		t.Helper()
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM crm_deal_fields WHERE name = $1`, name).Scan(&id))
		return id
	}
	stageOf := func(deal string) string {
		t.Helper()
		var st string
		must(pool.QueryRow(ctx, `SELECT stage_id FROM crm_deals WHERE id = $1`, deal).Scan(&st))
		return st
	}

	// the settings: «Регион» always, «Договор» only at the third stage, a yes/no never
	for _, f := range []url.Values{
		{"name": {"Регион"}, "kind": {"string"}, "required": {"1"}, "required_at": {"all"}},
		{"name": {"Договор"}, "kind": {"string"}, "required": {"1"}, "required_at": {"stages"}, "stages": {won}},
		{"name": {"Срочно"}, "kind": {"bool"}, "required": {"1"}},
	} {
		if code, _, _ := do("/settings/crm-deal-fields", f); code != http.StatusSeeOther {
			t.Fatalf("add field %v: %d", f, code)
		}
	}
	region, contract := fieldID("Регион"), fieldID("Договор")
	if _, page, _ := do("/settings/crm-deal-fields", nil); !strings.Contains(page, `name="required_at"`) || !strings.Contains(page, `value="`+won+`" checked`) {
		t.Fatal("the settings miss the required stages")
	}
	if _, page, _ := do("/settings/task-fields", nil); strings.Contains(page, `name="required"`) {
		t.Fatal("task fields offer «required»")
	}

	// a new deal: the form asks for «Регион», saving without it is refused
	if _, page, _ := do("/crm/deals", nil); !strings.Contains(page, `name="f_`+region+`" required`) || strings.Contains(page, `name="f_`+contract+`"`) {
		t.Fatal("the new deal's form: «Регион» only")
	}
	newDeal := url.Values{"title": {"Поставка"}, "pipeline": {pipe}, "responsible": {anna}}
	if code, _, loc := do("/crm/deals/new", newDeal); code != http.StatusSeeOther || !strings.Contains(loc, url.QueryEscape("Регион")) {
		t.Fatalf("a deal without «Регион»: %d %s", code, loc)
	}
	code, body, _ := do("/crm/deals/new", newDeal, "X-Go24-Form", "1")
	var refused struct {
		Error  string
		Fields []struct{ ID, Name string }
	}
	must(json.Unmarshal([]byte(body), &refused))
	if code != http.StatusUnprocessableEntity || len(refused.Fields) != 1 || refused.Fields[0].ID != region {
		t.Fatalf("the background form: %d %s", code, body)
	}
	newDeal.Set("f_"+region, "Москва")
	code, _, loc := do("/crm/deals/new", newDeal)
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/crm/deals/") {
		t.Fatalf("a deal with «Регион»: %d %s", code, loc)
	}
	deal := strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/crm/deals/")
	vals, err := srv.Tasks.FieldValuesOf(ctx, tasks.CRMFieldSets["deal"], deal)
	must(err)
	if vals[0].Name != "Договор" || vals[1].Value != "Москва" { // ordered by sort, name
		t.Fatalf("values: %+v", vals)
	}

	// the kanban to the third stage: refused with «Договор» to ask for, then moved with it
	card := "/crm/deals/" + deal
	code, body, _ = do(card+"/stage", url.Values{"stage": {won}}, "Accept", "application/json")
	must(json.Unmarshal([]byte(body), &refused))
	if code != http.StatusUnprocessableEntity || len(refused.Fields) != 1 || refused.Fields[0].Name != "Договор" || !strings.Contains(refused.Error, "Договор") {
		t.Fatalf("the kanban: %d %s", code, body)
	}
	if stageOf(deal) != first {
		t.Fatal("the deal moved without «Договор»")
	}
	// the stage bar without scripts: back to the card with the message
	if code, _, loc := do(card+"/stage", url.Values{"stage": {won}}); code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("the stage bar: %d %s", code, loc)
	}
	if code, _, _ := do(card+"/stage", url.Values{"stage": {won}, "f_" + contract: {"№ 7"}}, "Accept", "application/json"); code != http.StatusNoContent {
		t.Fatalf("the kanban with «Договор»: %d", code)
	}
	if stageOf(deal) != won {
		t.Fatal("the deal did not move")
	}
	if vals, _ = srv.Tasks.FieldValuesOf(ctx, tasks.CRMFieldSets["deal"], deal); vals[0].Value != "№ 7" || vals[1].Value != "Москва" {
		t.Fatalf("values after the move: %+v", vals)
	}
	// the card's form at that stage: «Договор» is required there, clearing it is refused
	if _, page, _ := do(card+"?edit=1", nil); !strings.Contains(page, `name="f_`+contract+`" value="№ 7" maxlength="2000" required`) {
		t.Fatal("the card's form does not require «Договор»")
	}
	edit := url.Values{"title": {"Поставка"}, "responsible": {anna}, "with_fields": {"1"}, "f_" + region: {"Москва"}, "f_" + contract: {""}}
	if code, _, loc := do(card, edit); code != http.StatusSeeOther || !strings.Contains(loc, "edit=1") {
		t.Fatalf("clearing «Договор»: %d %s", code, loc)
	}
	if vals, _ = srv.Tasks.FieldValuesOf(ctx, tasks.CRMFieldSets["deal"], deal); vals[0].Value != "№ 7" {
		t.Fatal("«Договор» cleared")
	}

	// the group action skips a deal lacking «Договор»
	other, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Ещё", PipelineID: pipe}) // the store does not check
	must(err)
	must(srv.Tasks.SetFieldValuesOf(ctx, tasks.CRMFieldSets["deal"], other, map[string]string{region: "Тверь"}))
	if code, _, loc := do("/crm/bulk/deals", url.Values{"op": {"stage"}, "value": {won}, "id": {other}}); code != http.StatusSeeOther || stageOf(other) != first {
		t.Fatalf("the group action: %d %s", code, loc)
	}

	// «Договор» no longer required: the deal moves
	if code, _, _ := do("/settings/crm-deal-fields/"+contract+"/required", url.Values{}); code != http.StatusSeeOther {
		t.Fatal("unset required")
	}
	if code, _, _ := do("/crm/deals/"+other+"/stage", url.Values{"stage": {won}}, "Accept", "application/json"); code != http.StatusNoContent || stageOf(other) != won {
		t.Fatalf("after the settings: %d", code)
	}
}
