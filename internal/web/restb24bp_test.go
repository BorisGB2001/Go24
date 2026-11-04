package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestRestPortalBP: bizproc.* by name — the templates with DOCUMENT_TYPE and PARAMETERS, a process started on a deal by its
// number with parameters (an employee as user_<number>), its tasks listed, completed and delegated,
// the running processes, terminate and kill, a template deleted by an administrator only.
func TestRestPortalBP(t *testing.T) {
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
	user := func(name string, admin bool) (string, int64) {
		var id string
		var n int64
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, admin) VALUES ($1, $1, $2) RETURNING id, num`, name, admin).Scan(&id, &n))
		return id, n
	}
	anna, annaNum := user("Анна", true)
	boris, borisNum := user("Борис", false)
	eve, eveNum := user("Ева", false)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), I18n: bundle,
		CRM: crm.NewStore(pool), Audit: audit.NewStore(pool), BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.BP = bp.NewStore(pool, srv.Tasks, srv.CRM, srv.Company)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hook := func(uid string, n int64) string {
		token, err := srv.Keys.Create(ctx, uid, "n8n", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	annaHook, borisHook, eveHook := hook(anna, annaNum), hook(boris, borisNum), hook(eve, eveNum)
	call := func(h, method, body string, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+h+method+".json", "application/json", strings.NewReader(body))
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
	str := func(v any) string { return portalStr(v) }

	deal := str(call(annaHook, "crm.deal.add", `{"fields": {"TITLE": "Поставка"}}`, 200)["result"])
	tid, err := srv.BP.SaveTemplate(ctx, anna, bp.Template{Name: "Согласование сделки", Entity: "deal", Active: true,
		Fields: []bp.Field{{Key: "sum", Label: "Сумма", Kind: "number", Required: true}, {Key: "who", Label: "Кто", Kind: "user"}},
		Steps:  []bp.Step{{Kind: "review", Text: "Ознакомьтесь", To: boris}, {Kind: "approve", Text: "Утвердите", To: boris, Delegate: "all"}}})
	must(err)
	_, err = srv.BP.SaveTemplate(ctx, anna, bp.Template{Name: "Отпуск", Active: true, Steps: []bp.Step{{Kind: "approve", Text: "Отпуск?", To: anna}}})
	must(err)

	// templates
	list := call(borisHook, "bizproc.workflow.template.list", `{"filter": {"DOCUMENT_TYPE": ["crm", "CCrmDocumentDeal", "DEAL"]}}`, 200)
	rows := list["result"].([]any)
	if len(rows) != 1 || list["total"].(float64) != 1 {
		t.Fatalf("template.list by DOCUMENT_TYPE: %v", list)
	}
	tpl := rows[0].(map[string]any)
	sum, _ := tpl["PARAMETERS"].(map[string]any)["sum"].(map[string]any)
	if tpl["NAME"] != "Согласование сделки" || tpl["MODULE_ID"] != "crm" || tpl["ENTITY"] != "CCrmDocumentDeal" || sum["Type"] != "double" ||
		sum["Required"] != "Y" || tpl["XML_ID"] != tid {
		t.Errorf("template.list: %v", tpl)
	}
	tnum := str(tpl["ID"])
	if got := call(borisHook, "bizproc.workflow.template.list", `{"select": ["NAME"], "order": {"NAME": "ASC"}}`, 200)["result"].([]any); len(got) != 2 ||
		got[0].(map[string]any)["NAME"] != "Отпуск" || len(got[0].(map[string]any)) != 2 {
		t.Errorf("template.list select, order: %v", got)
	}

	// start: a deal by its number, the parameters
	doc := `["crm", "CCrmDocumentDeal", "DEAL_` + deal + `"]`
	if e := call(borisHook, "bizproc.workflow.start", `{"TEMPLATE_ID": `+tnum+`, "DOCUMENT_ID": ["crm", "CCrmDocumentLead", "LEAD_1"]}`, 400); e["error"] != "ERROR_WRONG_DOCUMENT_ID" {
		t.Errorf("start with a lead: %v", e)
	}
	wf := str(call(borisHook, "bizproc.workflow.start", `{"TEMPLATE_ID": `+tnum+`, "DOCUMENT_ID": `+doc+`,
		"PARAMETERS": {"sum": 1500, "who": "user_`+num(eveNum)+`"}}`, 200)["result"])
	x, err := srv.BP.Instance(tasks.WithAdmin(ctx), anna, wf)
	must(err)
	if x.Values["sum"] != "1500" || x.Values["who"] != eve || x.Status != "running" {
		t.Errorf("the process started: %v %s", x.Values, x.Status)
	}

	// tasks: the review, then the approval delegated to Eve and approved
	task := func(h string) map[string]any {
		t.Helper()
		got := call(h, "bizproc.task.list", `{}`, 200)["result"].([]any)
		if len(got) != 1 {
			t.Fatalf("task.list: %v", got)
		}
		return got[0].(map[string]any)
	}
	rv := task(borisHook)
	if rv["ACTIVITY"] != "ReviewActivity" || rv["WORKFLOW_ID"] != wf || rv["DOCUMENT_ID"] != "DEAL_"+deal || rv["USER_ID"] != num(borisNum) ||
		rv["WORKFLOW_TEMPLATE_ID"] != tnum || rv["WORKFLOW_STARTED_BY"] != num(borisNum) || rv["DESCRIPTION"] != "Ознакомьтесь" {
		t.Errorf("task.list: %v", rv)
	}
	if e := call(borisHook, "bizproc.task.complete", `{"TASK_ID": `+str(rv["ID"])+`, "STATUS": 2}`, 400); e["error"] != "ERROR_WRONG_STATUS" {
		t.Errorf("a review rejected: %v", e)
	}
	call(borisHook, "bizproc.task.complete", `{"TASK_ID": `+str(rv["ID"])+`, "STATUS": "ok"}`, 200)
	ap := task(borisHook)
	if ap["ACTIVITY"] != "ApproveActivity" {
		t.Errorf("the next task: %v", ap)
	}
	if got := call(eveHook, "bizproc.task.list", `{"filter": {"USER_ID": `+num(borisNum)+`}}`, 403); got["error"] != "ACCESS_DENIED" {
		t.Errorf("another's tasks: %v", got)
	}
	if got := call(annaHook, "bizproc.task.list", `{"filter": {"USER_ID": `+num(borisNum)+`}}`, 200)["result"].([]any); len(got) != 1 {
		t.Errorf("an administrator, Boris's tasks: %v", got)
	}
	call(borisHook, "bizproc.task.delegate", `{"TASK_IDS": [`+str(ap["ID"])+`], "FROM_USER_ID": `+num(borisNum)+`, "TO_USER_ID": `+num(eveNum)+`}`, 200)
	if got := call(borisHook, "bizproc.task.list", `{}`, 200)["result"].([]any); len(got) != 0 {
		t.Errorf("delegated, still Boris's: %v", got)
	}
	call(eveHook, "bizproc.task.complete", `{"TASK_ID": `+str(task(eveHook)["ID"])+`, "STATUS": 1, "COMMENT": "Да"}`, 200)
	if x, _ = srv.BP.Instance(tasks.WithAdmin(ctx), anna, wf); x.Status != "done" {
		t.Errorf("approved: %s", x.Status)
	}
	// the decided tasks: by USER_STATUS keeps them
	if got := call(borisHook, "bizproc.task.list", `{"filter": {"USER_STATUS": 3}}`, 200)["result"].([]any); len(got) != 1 ||
		got[0].(map[string]any)["ACTIVITY"] != "ReviewActivity" || got[0].(map[string]any)["STATUS"] != "3" {
		t.Errorf("Boris's seen review: %v", got)
	}
	if got := call(eveHook, "bizproc.task.list", `{"filter": {">USER_STATUS": 0}}`, 200)["result"].([]any); len(got) != 1 ||
		got[0].(map[string]any)["USER_STATUS"] != "1" || got[0].(map[string]any)["USER_COMMENT"] != "Да" {
		t.Errorf("Eve's approval: %v", got)
	}

	// the running processes, terminate, kill
	wf2 := str(call(borisHook, "bizproc.workflow.start", `{"TEMPLATE_ID": `+tnum+`, "DOCUMENT_ID": `+doc+`, "PARAMETERS": {"sum": 1}}`, 200)["result"])
	wf3 := str(call(borisHook, "bizproc.workflow.start", `{"TEMPLATE_ID": `+tnum+`, "DOCUMENT_ID": `+doc+`, "PARAMETERS": {"sum": 2}}`, 200)["result"])
	inst := call(annaHook, "bizproc.workflow.instances", `{"select": ["DOCUMENT_ID", "TEMPLATE_ID"]}`, 200)
	if inst["total"].(float64) != 2 || inst["result"].([]any)[0].(map[string]any)["DOCUMENT_ID"] != "DEAL_"+deal {
		t.Errorf("instances: %v", inst)
	}
	if got := call(eveHook, "bizproc.workflow.instances", `{}`, 200)["result"].([]any); len(got) != 0 {
		t.Errorf("Eve's running processes: %v", got)
	}
	call(borisHook, "bizproc.workflow.terminate", `{"ID": "`+wf2+`", "STATUS": "Не нужно"}`, 200)
	call(borisHook, "bizproc.workflow.kill", `{"ID": "`+wf3+`"}`, 403)
	call(annaHook, "bizproc.workflow.kill", `{"ID": "`+wf3+`"}`, 200)
	if got := call(borisHook, "bizproc.workflow.instances", `{}`, 200)["result"].([]any); len(got) != 0 {
		t.Errorf("after terminate and kill: %v", got)
	}

	// a standalone template: no document
	call(annaHook, "bizproc.workflow.start", `{"TEMPLATE_ID": "`+tid+`", "DOCUMENT_ID": ["crm", "CCrmDocumentDeal", "DEAL_999"]}`, 400)

	// delete: an administrator only
	call(borisHook, "bizproc.workflow.template.delete", `{"ID": `+tnum+`}`, 403)
	call(annaHook, "bizproc.workflow.template.delete", `{"ID": `+tnum+`}`, 200)
	if got := call(annaHook, "bizproc.workflow.template.list", `{}`, 200)["result"].([]any); len(got) != 1 {
		t.Errorf("after delete: %v", got)
	}
}
