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
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestRestPortalBPActivities: bizproc.activity.add/update/list/delete by an application's administrator; a step of the
// action posts the deal's process to the handler (event_token, document_id, properties, auth); the
// process waits for bizproc.event.send and puts RETURN_VALUES into its fields; bizproc.activity.log writes its history.
func TestRestPortalBPActivities(t *testing.T) {
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
	var boss, anna string
	var bossNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}')
		RETURNING id, num`).Scan(&boss, &bossNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))

	var (
		mu       sync.Mutex
		received []url.Values
	)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bp" {
			return
		}
		_ = r.ParseForm()
		mu.Lock()
		received = append(received, r.PostForm)
		mu.Unlock()
	}))
	defer app.Close()

	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle,
		CRM: crm.NewStore(pool), Company: company.NewStore(pool), AdminGroups: []string{"go24-admins"}, BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.BP = bp.NewStore(pool, srv.Tasks, srv.CRM, srv.Company)
	srv.BP.Apps, srv.BP.AppCall = srv.Apps, srv.BPAppCall
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(method, tok, body string, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+"/rest/"+method+"?auth="+tok, "application/json", strings.NewReader(body))
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}

	id, err := srv.Apps.Save(ctx, boss, apps.App{Name: "Проверка контрагентов", HandlerURL: app.URL + "/page", Scopes: []string{"bp:read", "bp:write"}})
	must(err)
	a, err := srv.Apps.Get(ctx, id)
	must(err)
	must(srv.Apps.Install(ctx, a, boss, srv.BaseURL, "ru"))
	tokens := func(user string) string {
		tk, err := srv.Apps.Issue(ctx, a.ID, user)
		must(err)
		return tk.Access
	}
	bossTok, annaTok := tokens(boss), tokens(anna)

	add := `{"CODE": "check", "HANDLER": "` + app.URL + `/bp", "USE_SUBSCRIPTION": "Y", "NAME": {"ru": "Проверить", "en": "Check"},
		"PROPERTIES": {"inn": {"Name": {"ru": "ИНН"}, "Type": "string", "Required": "Y"}},
		"RETURN_PROPERTIES": {"score": {"Name": "Балл", "Type": "int"}, "status": {"Name": "Статус", "Type": "select", "Options": {"ok": "Действует", "closed": "Ликвидирован"}}}}`
	if e := call("bizproc.activity.add", annaTok, add, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("add by an employee: %v", e)
	}
	if call("bizproc.activity.add", bossTok, add, 200)["result"] != true {
		t.Fatal("add is not true")
	}
	if e := call("bizproc.activity.add", bossTok, add, 400); e["error"] != "ERROR_ACTIVITY_ALREADY_INSTALLED" {
		t.Errorf("added twice: %v", e)
	}
	if e := call("bizproc.activity.add", bossTok, `{"CODE": "x", "HANDLER": "https://elsewhere.example/bp", "NAME": "X"}`, 400); e["error"] != "ERROR_ACTIVITY_VALIDATION_FAILURE" {
		t.Errorf("a handler elsewhere: %v", e)
	}
	if l := call("bizproc.activity.list", bossTok, `{}`, 200)["result"].([]any); len(l) != 1 || l[0] != "check" {
		t.Errorf("list: %v", l)
	}
	if call("bizproc.activity.update", bossTok, `{"CODE": "check", "FIELDS": {"NAME": "Проверить ИНН"}}`, 200)["result"] != true {
		t.Fatal("update is not true")
	}
	if e := call("bizproc.activity.update", bossTok, `{"CODE": "nope", "FIELDS": {"NAME": "X"}}`, 400); e["error"] != "ERROR_ACTIVITY_NOT_FOUND" {
		t.Errorf("update of none: %v", e)
	}
	list, err := srv.Apps.Activities(ctx)
	must(err)
	if len(list) != 1 || list[0].Name != "Проверить ИНН" || len(list[0].Props) != 1 || list[0].Props[0].Name != "ИНН" || !list[0].Props[0].Required ||
		len(list[0].Returns) != 2 || list[0].Returns[1].Key != "status" || len(list[0].Returns[1].Options) != 2 {
		t.Fatalf("activities: %+v", list)
	}

	// a deal's process: the handler gets the step, the answer goes on with it
	tpl, err := srv.BP.SaveTemplate(ctx, boss, bp.Template{Name: "Проверка", Entity: "deal", Active: true, Steps: []bp.Step{
		{Kind: "app", App: list[0].ID, Timeout: 30, Sets: []bp.Assign{{Field: "inn", Value: "7707083893"}}}}})
	must(err)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	d, err := srv.CRM.SaveDeal(ctx, boss, crm.Deal{Title: "Альфа", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boss}})
	must(err)
	var dealNum string
	must(pool.QueryRow(ctx, `SELECT num::text FROM crm_deals WHERE id::text = $1`, d).Scan(&dealNum))
	proc, err := srv.BP.Start(ctx, boss, tpl, d, nil)
	must(err)
	mu.Lock()
	got := received
	mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("posted: %+v", got)
	}
	f := got[0]
	token := f.Get("event_token")
	if f.Get("workflow_id") != proc || f.Get("code") != "check" || !strings.HasPrefix(token, proc+"|A1|") || f.Get("properties[inn]") != "7707083893" ||
		f.Get("document_id[0]") != "crm" || f.Get("document_id[1]") != "CCrmDocumentDeal" || f.Get("document_id[2]") != "DEAL_"+dealNum ||
		f.Get("document_type[2]") != "DEAL" || f.Get("use_subscription") != "Y" || f.Get("timeout_duration") != "1800" ||
		f.Get("auth[access_token]") == "" || f.Get("auth[user_id]") != strconv.FormatInt(bossNum, 10) || f.Get("auth[client_endpoint]") != "https://go24.example/rest/" {
		t.Fatalf("the form: %v", f)
	}
	if call("bizproc.activity.log", bossTok, `{"EVENT_TOKEN": "`+token+`", "LOG_MESSAGE": "Запрос ушёл"}`, 200)["result"] != true {
		t.Fatal("log is not true")
	}
	if call("bizproc.event.send", annaTok, `{"EVENT_TOKEN": "`+token+`", "RETURN_VALUES": {"score": 42, "status": "ok"}, "LOG_MESSAGE": "Действует"}`, 200)["result"] != true {
		t.Fatal("event.send is not true")
	}
	x, err := srv.BP.Instance(tasks.WithAdmin(ctx), boss, proc)
	must(err)
	if x.Status != "done" || x.Values["app_check_score"] != "42" || x.Values["app_check_status"] != "ok" {
		t.Fatalf("the process: %+v", x)
	}
	if e := call("bizproc.event.send", bossTok, `{"EVENT_TOKEN": "`+token+`"}`, 400); e["error"] != "ERROR_WRONG_EVENT_TOKEN" {
		t.Errorf("answered twice: %v", e)
	}

	if call("bizproc.activity.delete", bossTok, `{"CODE": "check"}`, 200)["result"] != true {
		t.Fatal("delete is not true")
	}
	if e := call("bizproc.activity.delete", bossTok, `{"CODE": "check"}`, 400); e["error"] != "ERROR_ACTIVITY_NOT_FOUND" {
		t.Errorf("deleted twice: %v", e)
	}
}
