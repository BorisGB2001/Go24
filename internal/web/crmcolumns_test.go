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

// TestCRMColumnsHTTP: «Настроить колонки» of the CRM lists — the default columns until one picks; the
// chosen ones (built-in and custom fields) in their order, the others hidden in the gear; per person and list (a smart
// process has its own); unknown columns refused; «Сбросить» brings back the default ones.
func TestCRMColumnsHTTP(t *testing.T) {
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
	must(srv.Tasks.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Город", Kind: "string"}))
	var city string
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_deal_fields WHERE name = 'Город'`).Scan(&city))
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Альфа", PipelineID: pipe, Responsible: crm.Ref{ID: anna}})
	must(err)
	must(srv.Tasks.SetFieldValuesOf(ctx, tasks.CRMFieldSets["deal"], deal, map[string]string{city: "Москва"}))
	must(srv.CRM.SetObservers(ctx, anna, "deal", deal, []string{anna}))
	shown := func(page, col string) bool {
		t.Helper()
		i := strings.Index(page, `<th data-col="`+col+`"`)
		if i < 0 {
			t.Fatalf("no column %s", col)
		}
		return !strings.HasPrefix(page[i+len(`<th data-col="`+col+`"`):], " hidden")
	}

	// the default ones; the field and «Дата завершения» only offered
	_, page, _ := do("/crm/deals?view=list", nil)
	if !shown(page, "stage") || !shown(page, "responsible") || shown(page, "close_date") || shown(page, "f:"+city) ||
		!strings.Contains(page, `name="scope" value="deal"`) || !strings.Contains(page, "> Город</label>") {
		t.Fatal("default columns")
	}
	// picked: the field, the amount, the observers — in this order; the stage hidden; the field's value in the list
	code, _, _ := do("/crm/columns", url.Values{"scope": {"deal"}, "cols": {"f:" + city, "amount", "observers"}}, "Accept", "application/json")
	if code != http.StatusNoContent {
		t.Fatalf("save: %d", code)
	}
	_, page, _ = do("/crm/deals?view=list", nil)
	if shown(page, "stage") || !shown(page, "f:"+city) || !shown(page, "observers") || !strings.Contains(page, ">Москва</td>") ||
		!strings.Contains(page, ">Анна</td>") {
		t.Fatal("picked columns")
	}
	if a, b := strings.Index(page, `<th data-col="f:`+city), strings.Index(page, `<th data-col="amount"`); a > b {
		t.Fatal("the order of the columns")
	}
	if s := strings.Index(page, `<th data-col="stage"`); s < strings.Index(page, `<th data-col="observers"`) {
		t.Fatal("a hidden column before the shown ones")
	}
	// the leads and the smart processes keep their own; bob has the default ones
	_, err = srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка", Responsible: crm.Ref{ID: anna}})
	must(err)
	_, page, _ = do("/crm/leads?view=list", nil)
	if !shown(page, "stage") || !shown(page, "contact_data") || shown(page, "phone") {
		t.Fatal("leads took the deals' columns")
	}
	typ, err := srv.CRM.AddType(ctx, "Договоры", "")
	must(err)
	tp, err := srv.CRM.PipelinesOf(ctx, typ)
	must(err)
	_, err = srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Договор 1", PipelineID: tp[0].ID, TypeID: typ, Responsible: crm.Ref{ID: anna}})
	must(err)
	_, page, _ = do("/crm/deals?view=list&type="+typ, nil)
	if !shown(page, "stage") || !strings.Contains(page, `name="scope" value="type:`+typ+`"`) || strings.Contains(page, `data-col="deal_type"`) {
		t.Fatal("smart process columns")
	}
	cols, err := srv.CRM.Columns(ctx, bob, "deal", []string{city})
	must(err)
	if strings.Join(cols, ",") != strings.Join(crm.DefaultListColumns["deal"], ",") {
		t.Fatalf("bob's columns %v", cols)
	}
	// unknown columns and lists are refused
	if code, _, _ := do("/crm/columns", url.Values{"scope": {"deal"}, "cols": {"password"}}, "Accept", "application/json"); code < 400 {
		t.Fatalf("unknown column: %d", code)
	}
	if code, _, _ := do("/crm/columns", url.Values{"scope": {"task"}, "cols": {"stage"}}); code != http.StatusNotFound {
		t.Fatalf("unknown list: %d", code)
	}
	// contacts: «Должность» as its own column instead of under the name; companies — the default ones
	_, err = srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Иван", Position: "Директор", Responsible: crm.Ref{ID: anna}})
	must(err)
	_, page, _ = do("/crm/contacts", nil)
	if !strings.Contains(page, `<div class="small muted">Директор</div>`) || shown(page, "position") {
		t.Fatal("contacts: default")
	}
	do("/crm/columns", url.Values{"scope": {"contact"}, "cols": {"position", "phone"}}, "Accept", "application/json")
	_, page, _ = do("/crm/contacts", nil)
	if strings.Contains(page, `<div class="small muted">Директор</div>`) || !shown(page, "position") || shown(page, "email") {
		t.Fatal("contacts: position column")
	}
	_, err = srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "Ромашка", Responsible: crm.Ref{ID: anna}})
	must(err)
	if _, page, _ = do("/crm/companies", nil); !strings.Contains(page, `name="scope" value="company"`) {
		t.Fatal("companies: no gear")
	}
	// «Сбросить»
	code, _, loc := do("/crm/columns", url.Values{"scope": {"deal"}, "reset": {"1"}, "cols": {"amount"}, "back": {"/crm/deals?view=list"}})
	if code != http.StatusSeeOther || loc != "/crm/deals?view=list" {
		t.Fatalf("reset: %d %s", code, loc)
	}
	if _, page, _ = do("/crm/deals?view=list", nil); !shown(page, "stage") || shown(page, "f:"+city) {
		t.Fatal("reset: not the default columns")
	}
}
