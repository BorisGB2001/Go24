package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"go24/internal/bi"
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/feed"
	"go24/internal/matrixsync"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestAPI drives REST API v1 over HTTP against a throwaway database (GO24_TEST_DATABASE_URL).
func TestAPI(t *testing.T) {
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
	var uid string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('api', 'API User', 'api@test') RETURNING id`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	keys := apikeys.NewStore(pool)
	full, err := keys.Create(ctx, uid, "full", apikeys.AllScopes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	readOnly, err := keys.Create(ctx, uid, "ro", []string{"tasks:read", "projects:read"}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	srv := &Server{
		DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, CRM: crm.NewStore(pool), Company: company.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), // the file rows only: uploads need the S3 storage
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	srv.BP = bp.NewStore(pool, srv.Tasks, srv.CRM, srv.Company)
	srv.BP.Tr = func(key string, _ ...any) string { return map[string]string{"bp.bool.Y": "Да", "bp.bool.N": "Нет"}[key] }
	srv.Feed = feed.NewStore(pool, srv.Tasks)
	srv.BP.Feed = srv.Feed
	srv.BI = bi.NewStore(pool, srv.CRM)
	ts := httptest.NewServer(srv.Routes())
	defer ts.Close()

	call := func(method, path, token string, body any, wantStatus int) map[string]any {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rd)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != wantStatus {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.StatusCode, wantStatus, raw)
		}
		out := map[string]any{}
		_ = json.Unmarshal(raw, &out)
		return out
	}

	if e := call("GET", "/api/v1/me", "", nil, http.StatusUnauthorized); e["code"] != "err.api_no_auth" || e["error"] == "" {
		t.Fatalf("error body = %v", e)
	}
	call("GET", "/api/v1/me", "go24_bad", nil, http.StatusUnauthorized)
	if me := call("GET", "/api/v1/me", full, nil, http.StatusOK); me["email"] != "api@test" {
		t.Fatalf("me = %v", me)
	}

	call("POST", "/api/v1/projects", readOnly, map[string]any{"key": "RO", "name": "x"}, http.StatusForbidden)
	call("POST", "/api/v1/projects", full, map[string]any{"key": "1", "name": "x"}, http.StatusUnprocessableEntity)
	call("POST", "/api/v1/projects", full, map[string]any{"key": "API", "name": "Через API"}, http.StatusCreated)
	call("POST", "/api/v1/projects", full, map[string]any{"key": "API", "name": "x", "bogus": 1}, http.StatusUnprocessableEntity)

	task := call("POST", "/api/v1/projects/API/tasks", full, map[string]any{
		"title": "Из API", "requires_control": true, "deadline": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
	}, http.StatusCreated)
	if task["key"] != "API-1" || task["status"] != "pending" {
		t.Fatalf("created task = %v", task)
	}

	task = call("POST", "/api/v1/tasks/API-1/actions/start", full, nil, http.StatusOK)
	if task["status"] != "in_progress" {
		t.Fatalf("after start = %v", task["status"])
	}
	call("POST", "/api/v1/tasks/API-1/actions/approve", full, nil, http.StatusUnprocessableEntity)

	ver := int(task["version"].(float64))
	call("PATCH", "/api/v1/tasks/API-1", full, map[string]any{"title": "Старая версия", "version": ver - 1}, http.StatusConflict)
	task = call("PATCH", "/api/v1/tasks/API-1", full, map[string]any{"title": "Переименована", "priority": 2, "version": ver}, http.StatusOK)
	if task["title"] != "Переименована" || task["priority"].(float64) != 2 || task["requires_control"] != true {
		t.Fatalf("after patch = %v", task)
	}

	ver = int(task["version"].(float64))
	task = call("PATCH", "/api/v1/tasks/API-1", full, map[string]any{"start_plan": "2026-11-02", "end_plan": "2026-11-06", "version": ver}, http.StatusOK)
	if task["start_plan"] != "2026-11-02" || task["end_plan"] != "2026-11-06" || task["title"] != "Переименована" {
		t.Fatalf("plan dates via API: %v", task)
	}
	// the task parameters; a PATCH without them (or without the deadline rules) keeps them
	if _, err := pool.Exec(ctx, `UPDATE tasks SET allow_change_deadline = true WHERE number = 1`); err != nil {
		t.Fatal(err)
	}
	task = call("GET", "/api/v1/tasks/API-1", full, nil, http.StatusOK)
	task = call("PATCH", "/api/v1/tasks/API-1", full, map[string]any{"result_required": true, "version": int(task["version"].(float64))}, http.StatusOK)
	task = call("PATCH", "/api/v1/tasks/API-1", full, map[string]any{"title": "Переименована", "priority": 1, "version": int(task["version"].(float64))}, http.StatusOK)
	var allow bool
	if err := pool.QueryRow(ctx, `SELECT allow_change_deadline FROM tasks WHERE number = 1`).Scan(&allow); err != nil {
		t.Fatal(err)
	}
	if task["result_required"] != true || task["dates_from_subtasks"] != false || !allow {
		t.Fatalf("parameters after a patch: %v, allow_change_deadline %v", task, allow)
	}
	if e := call("POST", "/api/v1/tasks/API-1/actions/complete", full, nil, http.StatusUnprocessableEntity); e["code"] != "err.result_required" {
		t.Fatalf("completed without a result: %v", e)
	}
	task = call("PATCH", "/api/v1/tasks/API-1", full, map[string]any{"result_required": false, "version": int(task["version"].(float64))}, http.StatusOK)
	task = call("POST", "/api/v1/tasks/API-1/time", full, map[string]any{"duration": "1:30", "comment": "API"}, http.StatusOK)
	if task["time_spent_seconds"].(float64) != 5400 {
		t.Fatalf("time via API: %v", task["time_spent_seconds"])
	}

	task = call("POST", "/api/v1/tasks/API-1/checklist", full, map[string]any{"title": "Шаг 1"}, http.StatusOK)
	item := task["checklist"].([]any)[0].(map[string]any)
	task = call("POST", "/api/v1/tasks/API-1/checklist/"+item["id"].(string)+"/toggle", full, nil, http.StatusOK)
	if !task["checklist"].([]any)[0].(map[string]any)["done"].(bool) {
		t.Fatal("checklist item not toggled")
	}
	// tags, «Связанные задачи», files of the card
	task = call("POST", "/api/v1/tasks/API-1/tags", full, map[string]any{"tag": "#почта"}, http.StatusOK)
	if tags := task["tags"].([]any); len(tags) != 1 || tags[0] != "почта" {
		t.Fatalf("tags via API: %v", task["tags"])
	}
	if task = call("POST", "/api/v1/tasks/API-1/tags", full, map[string]any{"tag": "почта", "remove": true}, http.StatusOK); len(task["tags"].([]any)) != 0 {
		t.Fatalf("tag not removed: %v", task["tags"])
	}
	call("POST", "/api/v1/tasks/API-1/tags", readOnly, map[string]any{"tag": "x"}, http.StatusForbidden)
	call("POST", "/api/v1/tasks/API-1/files", full, nil, http.StatusUnprocessableEntity) // no file
	task = call("POST", "/api/v1/tasks/API-1/comments", full, map[string]any{"body": "Комментарий из API"}, http.StatusOK)
	if len(task["comments"].([]any)) != 1 {
		t.Fatal("comment not added")
	}
	// an answer quotes the message, the author changes and deletes it (GO24DEV-771)
	first := task["comments"].([]any)[0].(map[string]any)["id"].(string)
	task = call("POST", "/api/v1/tasks/API-1/comments", full, map[string]any{"body": "Ответ", "reply_to": first}, http.StatusOK)
	reply := task["comments"].([]any)[1].(map[string]any)
	if reply["reply_to"] != first || reply["edited_at"] != nil {
		t.Fatalf("reply via API: %v", reply)
	}
	task = call("PATCH", "/api/v1/tasks/API-1/comments/"+reply["id"].(string), full, map[string]any{"body": "Ответ исправлен"}, http.StatusOK)
	if c := task["comments"].([]any)[1].(map[string]any); c["body"] != "Ответ исправлен" || c["edited_at"] == nil {
		t.Fatalf("edited via API: %v", c)
	}
	call("PATCH", "/api/v1/tasks/API-1/comments/"+reply["id"].(string), readOnly, map[string]any{"body": "x"}, http.StatusForbidden)
	task = call("DELETE", "/api/v1/tasks/API-1/comments/"+reply["id"].(string), full, nil, http.StatusOK)
	if len(task["comments"].([]any)) != 1 {
		t.Fatalf("deleted via API: %v", task["comments"])
	}
	call("DELETE", "/api/v1/tasks/API-1/comments/"+reply["id"].(string), full, nil, http.StatusNotFound)

	call("POST", "/api/v1/tasks/API-1/actions/start", readOnly, nil, http.StatusForbidden)
	call("GET", "/api/v1/tasks/API-1", readOnly, nil, http.StatusOK)
	call("GET", "/api/v1/tasks/NOPE-1", readOnly, nil, http.StatusNotFound)

	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/tasks?role=responsible", nil)
	req.Header.Set("Authorization", "Bearer "+readOnly)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	_ = json.NewDecoder(res.Body).Decode(&list)
	res.Body.Close()
	if len(list) != 1 || list[0]["key"] != "API-1" {
		t.Fatalf("my tasks = %v", list)
	}

	// Drag & drop on the kanban: the page script posts with Accept: application/json and updates in place.
	rec := httptest.NewRecorder()
	if err := srv.Sessions.Set(rec, auth.Session{UserID: uid, Name: "API User"}); err != nil {
		t.Fatal(err)
	}
	cookie := rec.Result().Cookies()[0]
	post := func(path string, form url.Values) (int, map[string]any) {
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.AddCookie(cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	// API-1 is in progress with result review: "complete" by the creator closes it directly
	code, out := post("/t/API-1/action", url.Values{"action": {"complete"}})
	if code != http.StatusOK || out["status"] != "completed" || out["counters"] == nil {
		t.Fatalf("json action: %d %v", code, out)
	}
	if moves := out["moves"].(map[string]any); moves["pending"] != "renew" {
		t.Fatalf("moves after complete = %v", moves)
	}
	code, out = post("/t/API-1/action", url.Values{"action": {"complete"}}) // not allowed any more
	if code != http.StatusUnprocessableEntity || out["error"] == "" || out["code"] != "err.action_not_allowed" {
		t.Fatalf("json action error: %d %v", code, out)
	}
	code, _ = post("/plan/move", url.Values{"task": {"API-1"}, "stage": {""}})
	if code != http.StatusNoContent {
		t.Fatalf("plan move: %d", code)
	}
	code, out = post("/plan/move", url.Values{"task": {"NOPE-1"}, "stage": {""}})
	if code != http.StatusNotFound || out["error"] == "" {
		t.Fatalf("plan move of a missing task: %d %v", code, out)
	}

	// Card forms: custom fields (an administrator defines them) and reminders.
	rec = httptest.NewRecorder()
	if err := srv.Sessions.Set(rec, auth.Session{UserID: uid, Name: "API User", Admin: true}); err != nil {
		t.Fatal(err)
	}
	admin := rec.Result().Cookies()[0]
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	form := func(c *http.Cookie, path string, v url.Values) string {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+path, strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(c)
		res, err := noFollow.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusForbidden {
			return "forbidden"
		}
		return res.Header.Get("Location")
	}
	if got := form(cookie, "/settings/task-fields", url.Values{"name": {"Бюджет"}, "kind": {"number"}}); got != "forbidden" {
		t.Fatalf("a non-administrator defined a field: %s", got)
	}
	if got := form(admin, "/settings/task-fields", url.Values{"name": {"Бюджет"}, "kind": {"number"}}); !strings.HasPrefix(got, "/settings/task-fields?msg=") {
		t.Fatalf("add field: %s", got)
	}
	if got := form(admin, "/settings/user-fields", url.Values{"name": {"Хобби"}, "kind": {"string"}}); !strings.HasPrefix(got, "/settings/user-fields?msg=") {
		t.Fatalf("add a profile field: %s", got)
	}
	if got := form(admin, "/settings/nonsense", url.Values{"name": {"x"}, "kind": {"string"}}); got != "forbidden" {
		t.Fatalf("an unknown field set: %s", got)
	}
	fields, err := srv.Tasks.Fields(ctx)
	if err != nil || len(fields) != 1 {
		t.Fatalf("fields = %v %v", fields, err)
	}
	if got := form(cookie, "/t/API-1/fields", url.Values{"f_" + fields[0].ID: {"2 000"}}); got != "/t/API-1" {
		t.Fatalf("fill fields: %s", got)
	}
	at := time.Now().Add(time.Hour).Format("2006-01-02T15:04")
	if got := form(cookie, "/t/API-1/reminders", url.Values{"when": {"at"}, "at": {at}, "recipient": {"self"}}); got != "/t/API-1" {
		t.Fatalf("add reminder: %s", got)
	}
	if got := form(cookie, "/t/API-1/reminders", url.Values{"when": {"deadline"}, "before": {"7"}, "recipient": {"self"}}); got == "/t/API-1" {
		t.Fatal("an offset the card does not offer was accepted")
	}
	// «Сроки»: dropping into a column moves the deadline; «overdue» is refused
	code, out = post("/deadlines/move", url.Values{"task": {"API-1"}, "period": {"next_week"}})
	if code != http.StatusOK || out["deadline"] == "" {
		t.Fatalf("deadline move: %d %v", code, out)
	}
	if code, _ = post("/deadlines/move", url.Values{"task": {"API-1"}, "period": {"overdue"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("move to overdue: %d", code)
	}
	// the list filter: a preset saves only the filter's parameters
	if got := form(cookie, "/tasks/presets", url.Values{"name": {"Срочные"}, "query": {"prio=high&q=API&junk=1&status="}}); got != "/tasks?prio=high&q=API" {
		t.Fatalf("save preset: %s", got)
	}
	if ps, _ := srv.Tasks.Presets(ctx, uid); len(ps) != 1 || ps[0].Query != "prio=high&q=API" {
		t.Fatalf("presets = %v", ps)
	}
	// group actions: two tasks get a tag, an unknown one is counted as not changed
	plan2 := call("POST", "/api/v1/projects/API/tasks", full, map[string]any{"title": "Вторая", "start_plan": "2026-11-09", "end_plan": "2026-11-13"}, http.StatusCreated)
	if plan2["start_plan"] != "2026-11-09" || plan2["end_plan"] != "2026-11-13" {
		t.Fatalf("plan dates of a new task: %v .. %v", plan2["start_plan"], plan2["end_plan"])
	}
	got := form(cookie, "/tasks/bulk", url.Values{"op": {"tag"}, "tag": {"пакет"}, "task": {"API-1", "API-2", "NOPE-9"}, "back": {"/my?role=creator"}})
	if !strings.HasPrefix(got, "/my?role=creator&msg=") {
		t.Fatalf("bulk: %s", got)
	}
	if list, _ := srv.Tasks.ListTasks(ctx, tasks.Filter{Viewer: uid, Tag: "пакет"}); len(list) != 2 {
		t.Fatalf("tagged = %d", len(list))
	}
	// «Связанные задачи» via API: API-1 follows API-2
	task = call("POST", "/api/v1/tasks/API-1/links", full, map[string]any{"pred": "API-2"}, http.StatusOK)
	links := task["links"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["key"] != "API-2" || links[0].(map[string]any)["kind"] != "pred" {
		t.Fatalf("links via API: %v", links)
	}
	if other := call("GET", "/api/v1/tasks/API-2", full, nil, http.StatusOK); other["links"].([]any)[0].(map[string]any)["kind"] != "succ" {
		t.Fatalf("the other side of the link: %v", other["links"])
	}
	task = call("DELETE", "/api/v1/tasks/API-1/links/"+links[0].(map[string]any)["id"].(string), full, nil, http.StatusOK)
	if len(task["links"].([]any)) != 0 || task["files"] == nil {
		t.Fatalf("link not removed / no files: %v %v", task["links"], task["files"])
	}
	if got := form(cookie, "/tasks/bulk", url.Values{"op": {"tag"}, "tag": {"x"}, "back": {"//evil.example/"}}); !strings.HasPrefix(got, "/tasks?msg=") {
		t.Fatalf("bulk with a foreign back and no tasks: %s", got)
	}
	// CRM through its forms: a lead, its conversion, the deal on the kanban
	got = form(cookie, "/crm/leads/new", url.Values{"title": {"Заявка"}, "name": {"Иван"}, "company_title": {"ООО"}, "amount": {"1 000,5"}})
	lead := strings.TrimPrefix(strings.SplitN(got, "?", 2)[0], "/crm/leads/")
	if !strings.HasPrefix(got, "/crm/leads/") || lead == "new" {
		t.Fatalf("new lead: %s", got)
	}
	got = form(cookie, "/crm/leads/"+lead+"/convert", nil)
	deal := strings.TrimPrefix(strings.SplitN(got, "?", 2)[0], "/crm/deals/")
	d, err := srv.CRM.Deal(ctx, deal)
	if err != nil || d.Amount != 1000.5 || d.Contact.Name != "Иван" {
		t.Fatalf("converted deal %s: %+v %v", got, d, err)
	}
	// the dialog «Конвертация»: a contact only — the existing one; nothing at all is refused
	lead2, err := srv.CRM.SaveLead(ctx, uid, crm.Lead{Title: "Вторая заявка", Name: "Иван", Responsible: crm.Ref{ID: uid}})
	if err != nil {
		t.Fatal(err)
	}
	if got = form(cookie, "/crm/leads/"+lead2+"/convert", url.Values{"dialog": {"1"}}); !strings.HasPrefix(got, "/crm/leads/"+lead2) {
		t.Fatalf("converting into nothing: %s", got)
	}
	if got = form(cookie, "/crm/leads/"+lead2+"/convert", url.Values{"dialog": {"1"}, "contact": {"1"}, "contact_id": {d.Contact.ID}}); !strings.HasPrefix(got, "/crm/contacts/"+d.Contact.ID) {
		t.Fatalf("converting into a contact: %s", got)
	}
	if l2, _ := srv.CRM.Lead(ctx, lead2); l2.ContactID != d.Contact.ID || l2.DealID != "" {
		t.Fatalf("lead converted into a contact: %+v", l2)
	}
	// «Контакты» of the deal's form: two, the second first; an empty choice is skipped
	petr, err := srv.CRM.SaveContact(ctx, uid, crm.Contact{Name: "Пётр"})
	if err != nil {
		t.Fatal(err)
	}
	if got = form(cookie, "/crm/deals/"+deal, url.Values{"title": {d.Title}, "contact": {petr, "", d.Contact.ID}, "responsible": {uid}}); !strings.HasPrefix(got, "/crm/deals/"+deal) {
		t.Fatalf("deal's form: %s", got)
	}
	if d, err = srv.CRM.Deal(ctx, deal); err != nil || d.Contact.Name != "Пётр" || len(d.Contacts) != 2 || d.Contacts[1].Name != "Иван" {
		t.Fatalf("deal's contacts: %+v %v", d.Contacts, err)
	}
	for _, q := range []string{"", "?edit=1"} {
		req, _ := http.NewRequest("GET", ts.URL+"/crm/deals/"+deal+q, nil)
		req.AddCookie(cookie)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		res.Body.Close()
		ok := strings.Contains(string(body), "/crm/contacts/"+petr) // the card shows both contacts
		if q != "" {
			ok = strings.Count(string(body), `name="contact"`) == 3 // the form: both and an empty one
		}
		if res.StatusCode != 200 || !ok {
			t.Fatalf("deal's card%s: %d", q, res.StatusCode)
		}
	}
	pipes, _ := srv.CRM.Pipelines(ctx)
	if code, out := post("/crm/deals/"+deal+"/stage", url.Values{"stage": {pipes[0].Stages[3].ID}}); code != http.StatusNoContent {
		t.Fatalf("kanban move: %d %v", code, out)
	}
	if code, _ := post("/crm/deals/"+deal+"/stage", url.Values{"stage": {"nope"}}); code != http.StatusUnprocessableEntity {
		t.Fatalf("a move to no stage: %d", code)
	}
	// a task made from the deal's card is linked to it
	got = form(cookie, "/tasks/new", url.Values{"project": {"API"}, "title": {"Позвонить клиенту"}, "crm": {"/crm/deals/" + deal}})
	if linked, _ := srv.Tasks.ListTasks(ctx, tasks.Filter{Viewer: uid, CRMLink: "/crm/deals/" + deal}); len(linked) != 1 {
		t.Fatalf("task from the deal (%s): %d linked", got, len(linked))
	}
	// a business process through the constructor's forms: a template, a step, a start, a decision
	got = form(admin, "/bp/templates/new", url.Values{"op": {"meta"}, "name": {"Счёт"}, "entity": {"none"}, "active": {"on"}, "f_key": {"sum"}, "f_label": {"Сумма"}, "f_kind": {"number"}, "f_req": {"1"}})
	tpl := strings.TrimPrefix(got, "/bp/templates/")
	if !strings.HasPrefix(got, "/bp/templates/") || strings.Contains(tpl, "?") {
		t.Fatalf("new template: %s", got)
	}
	// «Параметры» as rows: a bool with its default, a user field with an employee by id, an empty row left out
	if got := form(admin, "/bp/templates/"+tpl, url.Values{"op": {"meta"}, "name": {"Счёт"}, "entity": {"none"}, "active": {"on"},
		"f_key": {"sum", "urgent", "payer", ""}, "f_label": {"Сумма", "Срочно", "Плательщик", ""}, "f_kind": {"number", "bool", "user", "string"},
		"f_req": {"1", "", "", ""}, "f_opts": {"", "", "", ""}, "f_def": {"", "Y", uid, ""}}); got != "/bp/templates/"+tpl {
		t.Fatalf("the fields: %s", got)
	}
	if tp, _ := srv.BP.Template(ctx, tpl); len(tp.Fields) != 3 || tp.Fields[1].Default != "Y" || tp.Fields[2].Default != uid || !tp.Fields[0].Required {
		t.Fatalf("the fields saved: %+v", tp.Fields)
	}
	if got := form(admin, "/bp/templates/"+tpl, url.Values{"op": {"add"}, "kind": {"approve"}, "to": {uid}, "text": {"Оплатить {sum}?"}}); got != "/bp/templates/"+tpl {
		t.Fatalf("add a step: %s", got)
	}
	if got := form(cookie, "/bp/templates/"+tpl, url.Values{"op": {"add"}, "kind": {"notify"}, "text": {"x"}}); got != "forbidden" {
		t.Fatalf("a non-administrator changed a template: %s", got)
	}
	got = form(cookie, "/bp/start/"+tpl, url.Values{"f_sum": {"500"}})
	if !strings.HasPrefix(got, "/bp/i/") {
		t.Fatalf("start: %s", got)
	}
	mine, _ := srv.BP.MyApprovals(ctx, uid)
	if len(mine) != 1 || mine[0].Text != "Оплатить 500?" {
		t.Fatalf("approvals = %+v", mine)
	}
	// «Делегировать»: only to a subordinate here, the approval stays
	var colleague string
	if err := pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('col', 'Коллега', 'col@test') RETURNING id`).Scan(&colleague); err != nil {
		t.Fatal(err)
	}
	if got := form(cookie, "/bp/approvals/"+mine[0].ID+"/delegate", url.Values{"to": {colleague}}); !strings.Contains(got, "msg=") {
		t.Fatalf("delegated to a non-subordinate: %s", got)
	}
	if got := form(cookie, "/bp/approvals/"+mine[0].ID, url.Values{"decision": {"approve"}}); got != "/bp?tab=approvals" {
		t.Fatalf("decide: %s", got)
	}
	// «Остановить» by who started it, then the list of all and «Удалить» by an administrator
	running := strings.TrimPrefix(form(cookie, "/bp/start/"+tpl, url.Values{"f_sum": {"700"}}), "/bp/i/")
	if got := form(cookie, "/bp/i/"+running+"/stop", url.Values{"reason": {"Передумал"}}); got != "/bp/i/"+running {
		t.Fatalf("stop: %s", got)
	}
	if got := form(cookie, "/bp/i/"+running+"/stop", nil); !strings.Contains(got, "msg=") {
		t.Fatalf("stopped twice: %s", got)
	}
	if mine, _ := srv.BP.MyApprovals(ctx, uid); len(mine) != 0 {
		t.Fatalf("a stopped process still asks: %+v", mine)
	}
	page := func(c *http.Cookie, path string) string {
		req, _ := http.NewRequest("GET", ts.URL+path, nil)
		req.AddCookie(c)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return string(body)
	}
	if body := page(admin, "/bp?tab=all&status=stopped"); !strings.Contains(body, "/bp/i/"+running) || strings.Contains(body, "/bp/i/"+mine[0].InstanceID) {
		t.Fatal("all the processes, the stopped ones: wrong list")
	}
	form(admin, "/bp/templates/"+tpl, url.Values{"op": {"add"}, "kind": {"review"}, "text": {"Счёт"}, "to": {"field:payer"}})
	if body := page(admin, "/bp/templates/"+tpl); !strings.Contains(body, "data-bpfields") || !strings.Contains(body, `data-extra="field:payer"`) ||
		!strings.Contains(body, "из поля «Плательщик»") || !strings.Contains(body, `name="f_scope"`) {
		t.Fatal("the template's page: no fields' table, no user field to choose for a step or its label in the steps")
	}
	if pt, _ := srv.BP.Template(ctx, tpl); len(pt.Steps) > 0 {
		form(admin, "/bp/templates/"+tpl, url.Values{"op": {"delete"}, "n": {strconv.Itoa(len(pt.Steps) - 1)}})
	}
	if body := page(cookie, "/bp/start/"+tpl); !strings.Contains(body, `name="f_urgent" value="Y" checked`) || !strings.Contains(body, `data-name="f_payer"`) {
		t.Fatal("the start form: no checked box or no employee picker")
	}
	if body := page(admin, "/bp/i/"+running); !strings.Contains(body, "Передумал") {
		t.Fatal("the reason is not in the history")
	}
	if body := page(cookie, "/bp?tab=all"); strings.Contains(body, "/bp/i/"+running+"/delete") {
		t.Fatal("a non-administrator sees all the processes")
	}
	if got := form(cookie, "/bp/i/"+running+"/delete", nil); !strings.Contains(got, "msg=") {
		t.Fatalf("a non-administrator deleted a process: %s", got)
	}
	if got := form(admin, "/bp/i/"+running+"/delete", nil); got != "/bp?tab=all" {
		t.Fatalf("delete: %s", got)
	}
	if _, err := srv.BP.Instance(ctx, uid, running); !errors.Is(err, bp.ErrNotFound) {
		t.Fatalf("deleted process: %v", err)
	}
	// «Стандартные шаблоны» with the accountant, once; «Внести в график отсутствий» needs the form's dates
	if body := page(admin, "/bp/templates"); !strings.Contains(body, `action="/bp/standard"`) {
		t.Fatal("no «Стандартные шаблоны»")
	}
	if got := form(cookie, "/bp/standard", url.Values{"accountant": {uid}}); got != "forbidden" {
		t.Fatalf("a non-administrator installed the templates: %s", got)
	}
	if got := form(admin, "/bp/standard", url.Values{"accountant": {uid}}); !strings.HasPrefix(got, "/bp/templates?msg=") {
		t.Fatalf("install: %s", got)
	}
	if body := page(admin, "/bp/templates"); strings.Contains(body, `action="/bp/standard"`) || !strings.Contains(body, "Отпуск") {
		t.Fatal("the standard templates are not installed")
	}
	var vacation string
	all, _ := srv.BP.Templates(ctx, "none", true)
	for _, x := range all {
		if x.Name == "Отпуск" {
			vacation = x.ID
		}
	}
	if got := form(admin, "/bp/templates/"+tpl, url.Values{"op": {"add"}, "kind": {"absence"}, "absence": {"dayoff"}, "start": {"sum"}, "end": {"sum"}}); !strings.Contains(got, "msg=") {
		t.Fatalf("an absence without dates: %s", got)
	}
	if got := form(admin, "/bp/templates/"+vacation, url.Values{"op": {"add"}, "kind": {"absence"}, "absence": {"dayoff"}, "start": {"from"}, "end": {"to"}}); got != "/bp/templates/"+vacation {
		t.Fatalf("add an absence: %s", got)
	}
	if vt, _ := srv.BP.Template(ctx, vacation); len(vt.Steps) != 5 || vt.Steps[4].Absence != "dayoff" || vt.Steps[4].Start != "from" {
		t.Fatalf("the absence step: %+v", vt.Steps)
	}
	if body := page(admin, "/bp/templates/"+vacation); !strings.Contains(body, "<code>from</code> — <code>to</code>") {
		t.Fatal("the absence step is not in the constructor")
	}
	// «Цикл» back to the first step and «Запуск бизнес-процесса» of the template with its sum
	if got := form(admin, "/bp/templates/"+vacation, url.Values{"op": {"add"}, "kind": {"loop"}, "field": {"from"}, "cmp": {"empty"}, "goto": {"1"}}); got != "/bp/templates/"+vacation {
		t.Fatalf("add a loop: %s", got)
	}
	if got := form(admin, "/bp/templates/"+vacation, url.Values{"op": {"add"}, "kind": {"start"}, "template": {tpl}, "sets": {"sum = 5"}}); got != "/bp/templates/"+vacation {
		t.Fatalf("add a start: %s", got)
	}
	if vt, _ := srv.BP.Template(ctx, vacation); len(vt.Steps) != 7 || vt.Steps[5].Kind != "loop" || vt.Steps[6].Template != tpl || vt.Steps[6].Text == "" {
		t.Fatalf("the loop and the start: %+v", vt.Steps[5:])
	}
	// the feed: «Бизнес-процессы» of the composer; a process started is a post with its state and the decision in it
	if body := page(cookie, "/feed"); !strings.Contains(body, `href="/bp/start/`+vacation+`"`) {
		t.Fatal("no processes in the composer")
	}
	inFeed := strings.TrimPrefix(form(cookie, "/bp/start/"+tpl, url.Values{"f_sum": {"900"}}), "/bp/i/")
	posts, err := srv.Feed.List(ctx, uid, feed.Filter{Kind: "bp"})
	if err != nil || len(posts) == 0 || posts[0].Post.Process != inFeed || posts[0].Post.Body != "Сумма: 900\nСрочно: Нет" {
		t.Fatalf("the process's post: %+v %v", posts, err)
	}
	postID := posts[0].Post.ID
	body := page(cookie, "/feed?kind=bp")
	if !strings.Contains(body, `href="/bp/i/`+inFeed+`"`) || !strings.Contains(body, "Оплатить 900?") || !strings.Contains(body, `value="/feed?post=`+postID+`"`) {
		t.Fatal("the process is not in its post")
	}
	mine, _ = srv.BP.MyApprovals(ctx, uid)
	if got := form(cookie, "/bp/approvals/"+mine[0].ID, url.Values{"decision": {"approve"}, "back": {"/feed?post=" + postID}}); got != "/feed?post="+postID {
		t.Fatalf("decided in the feed: %s", got)
	}
	if body := page(cookie, "/feed?post="+postID); strings.Contains(body, "Оплатить 900?") || !strings.Contains(body, "bp-done") {
		t.Fatal("the post does not show the process done")
	}
	// «Изменить карточку», «Создать карточку», «Письмо»: the constructor keeps each step's own inputs
	got = form(admin, "/bp/templates/new", url.Values{"op": {"meta"}, "name": {"Сделка"}, "entity": {"deal"}, "active": {"on"}})
	dealTpl := strings.TrimPrefix(got, "/bp/templates/")
	for _, v := range []url.Values{
		{"kind": {"update"}, "to": {uid}, "sets": {"amount = 100\ncomment = {starter}\n"}},
		{"kind": {"create"}, "card_entity": {"contact"}, "text": {"Иван"}, "to": {"responsible"}, "sets": {"phone = +7"}},
		{"kind": {"mail"}, "from": {"responsible"}, "mail_to": {"client"}, "email": {"a@b.c"}, "subject": {"Счёт"}, "text": {"Текст"}},
	} {
		v.Set("op", "add")
		if got := form(admin, "/bp/templates/"+dealTpl, v); got != "/bp/templates/"+dealTpl {
			t.Fatalf("add %s: %s", v.Get("kind"), got)
		}
	}
	if got := form(admin, "/bp/templates/"+dealTpl, url.Values{"op": {"add"}, "kind": {"update"}, "sets": {"нет поля = 1"}}); !strings.Contains(got, "msg=") {
		t.Fatalf("an unknown field: %s", got)
	}
	dt, err := srv.BP.Template(ctx, dealTpl)
	if err != nil || len(dt.Steps) != 4 || dt.Steps[1].To != "" || len(dt.Steps[1].Sets) != 2 || dt.Steps[2].Entity != "contact" ||
		dt.Steps[3].To != "client" || dt.Steps[3].Subject != "Счёт" || dt.Steps[3].From != "responsible" {
		t.Fatalf("the steps: %+v %v", dt.Steps, err)
	}
	if body := page(admin, "/bp/templates/"+dealTpl); !strings.Contains(body, "<code>amount</code> = «100»") || !strings.Contains(body, `data-for="update create"`) {
		t.Fatal("the steps are not in the constructor")
	}
	// «Параллельное выполнение» inserted before the branches' steps: the branches run at once, the process waits on both
	got = form(admin, "/bp/templates/new", url.Values{"op": {"meta"}, "name": {"Параллельно"}, "entity": {"none"}, "active": {"on"}})
	parTpl := strings.TrimPrefix(got, "/bp/templates/")
	for _, v := range []url.Values{
		{"kind": {"approve"}, "to": {uid}, "text": {"Ветка 1"}},
		{"kind": {"review"}, "to": {uid}, "text": {"Ветка 2"}},
		{"kind": {"parallel"}, "branches": {"4"}, "goto": {"0"}, "at": {"2"}},
	} {
		v.Set("op", "add")
		if got := form(admin, "/bp/templates/"+parTpl, v); got != "/bp/templates/"+parTpl {
			t.Fatalf("add %s: %s", v.Get("kind"), got)
		}
	}
	if pt, _ := srv.BP.Template(ctx, parTpl); len(pt.Steps) != 4 || pt.Steps[1].Kind != "parallel" || pt.Steps[1].Branches[0] != 4 {
		t.Fatalf("the parallel steps: %+v", pt.Steps)
	}
	if got := form(admin, "/bp/templates/"+parTpl, url.Values{"op": {"delete"}, "n": {"3"}}); !strings.Contains(got, "msg=") {
		t.Fatalf("a branch left empty: %s", got)
	}
	par := strings.TrimPrefix(form(cookie, "/bp/start/"+parTpl, nil), "/bp/i/")
	if mine, _ := srv.BP.MyApprovals(ctx, uid); len(mine) != 2 {
		t.Fatalf("both branches ask: %+v", mine)
	}
	if body := page(cookie, "/bp/i/"+par); strings.Count(body, `<li class="now">`) != 3 || !strings.Contains(body, `name="decision"`) {
		t.Fatal("the branches are not shown running")
	}
	for _, a := range func() []bp.Approval { m, _ := srv.BP.MyApprovals(ctx, uid); return m }() {
		if got := form(cookie, "/bp/approvals/"+a.ID, url.Values{"decision": {"approve"}, "back": {"/bp/i/" + par}}); got != "/bp/i/"+par {
			t.Fatalf("decide in a branch: %s", got)
		}
	}
	if x, _ := srv.BP.Instance(ctx, uid, par); x.Status != "done" {
		t.Fatalf("the branches did not join: %s", x.Status)
	}
	// «Запрос дополнительной информации»: a step with its fields, the form in «Мои задания», the answer into the process
	got = form(admin, "/bp/templates/new", url.Values{"op": {"meta"}, "name": {"Уточнение"}, "entity": {"none"}, "active": {"on"}})
	reqTpl := strings.TrimPrefix(got, "/bp/templates/")
	if got := form(admin, "/bp/templates/"+reqTpl, url.Values{"op": {"add"}, "kind": {"request"}, "to": {uid}, "text": {"Бюджет?"},
		"rf_key": {"budget"}, "rf_label": {"Бюджет"}, "rf_kind": {"number"}, "rf_req": {"1"}, "reject": {"on"}, "goto": {"0"}}); got != "/bp/templates/"+reqTpl {
		t.Fatalf("add a request: %s", got)
	}
	if body := page(admin, "/bp/templates/"+reqTpl); !strings.Contains(body, "<code>budget</code>") {
		t.Fatal("the request's fields are not in the constructor")
	}
	asked := strings.TrimPrefix(form(cookie, "/bp/start/"+reqTpl, nil), "/bp/i/")
	if body := page(cookie, "/bp?tab=approvals"); !strings.Contains(body, `name="f_budget"`) || !strings.Contains(body, `value="answer"`) {
		t.Fatal("no request's form in «Мои задания»")
	}
	mine, _ = srv.BP.MyApprovals(ctx, uid)
	if got := form(cookie, "/bp/approvals/"+mine[0].ID, url.Values{"decision": {"answer"}, "f_budget": {"abc"}, "back": {"/bp/i/" + asked}}); !strings.Contains(got, "msg=") {
		t.Fatalf("a bad answer: %s", got)
	}
	if got := form(cookie, "/bp/approvals/"+mine[0].ID, url.Values{"decision": {"answer"}, "f_budget": {"1200"}, "back": {"/bp/i/" + asked}}); got != "/bp/i/"+asked {
		t.Fatalf("answer: %s", got)
	}
	if x, _ := srv.BP.Instance(ctx, uid, asked); x.Status != "done" || x.Values["budget"] != "1200" {
		t.Fatalf("after the answer: %s %v", x.Status, x.Values)
	}
	if body := page(cookie, "/bp/i/"+asked); !strings.Contains(body, "1200") {
		t.Fatal("the answer is not on the process's page")
	}
	// the API for n8n: a lead, its stage, a list, a process
	apiLead := call("POST", "/api/v1/crm/leads", full, map[string]any{"title": "Из n8n", "name": "Пётр", "phone": "+7 1", "source": "n8n"}, http.StatusCreated)
	if apiLead["kind"] != "lead" || apiLead["stage_code"] != "lead_new" || apiLead["source"] != "n8n" {
		t.Fatalf("api lead = %v", lead)
	}
	call("POST", "/api/v1/crm/leads", readOnly, map[string]any{"title": "x"}, http.StatusForbidden)
	stages := call("GET", "/api/v1/crm/pipelines", full, nil, http.StatusOK)
	second := stages["lead_stages"].([]any)[1].(map[string]any)["id"].(string)
	moved := call("POST", "/api/v1/crm/leads/"+apiLead["id"].(string)+"/stage", full, map[string]any{"stage_id": second}, http.StatusOK)
	if moved["stage_code"] != "lead_in_process" {
		t.Fatalf("moved = %v", moved)
	}
	call("GET", "/api/v1/crm/leads/00000000-0000-0000-0000-000000000000", full, nil, http.StatusNotFound)
	call("POST", "/api/v1/crm/leads/"+apiLead["id"].(string)+"/comments", full, map[string]any{"body": "звонок из n8n"}, http.StatusNoContent)
	conv := call("POST", "/api/v1/crm/leads/"+apiLead["id"].(string)+"/convert", full, map[string]any{"deal": false, "contact": true}, http.StatusOK)
	if conv["contact_id"] == nil || conv["deal_id"] != nil || conv["stage_code"] != "lead_converted" {
		t.Fatalf("api convert = %v", conv)
	}
	call("POST", "/api/v1/crm/leads/"+apiLead["id"].(string)+"/convert", readOnly, nil, http.StatusForbidden)
	apiDeal := call("POST", "/api/v1/crm/deals", full, map[string]any{"title": "Сделка из n8n", "amount": 1000}, http.StatusCreated)
	if apiDeal["stage_code"] != "new" || apiDeal["amount"].(float64) != 1000 {
		t.Fatalf("api deal = %v", apiDeal)
	}
	// PATCH changes only the fields given; DELETE puts a card into the recycle bin (GO24DEV-759)
	tmp := call("POST", "/api/v1/crm/deals", full, map[string]any{"title": "Черновик", "amount": 500, "comment": "к правке"}, http.StatusCreated)
	tmpURL := "/api/v1/crm/deals/" + tmp["id"].(string)
	if upd := call("PATCH", tmpURL, full, map[string]any{"title": "Правка из n8n"}, http.StatusOK); upd["title"] != "Правка из n8n" ||
		upd["amount"].(float64) != 500 || upd["comment"] != "к правке" {
		t.Fatalf("patched deal = %v", upd)
	}
	call("PATCH", tmpURL, readOnly, map[string]any{"title": "x"}, http.StatusForbidden)
	call("PATCH", tmpURL, full, map[string]any{"no_such_field": 1}, http.StatusUnprocessableEntity)
	pc := call("POST", "/api/v1/crm/contacts", full, map[string]any{"name": "Ольга", "phone": "+7 900", "email": "o@x.ru"}, http.StatusCreated)
	if upd := call("PATCH", "/api/v1/crm/contacts/"+pc["id"].(string), full, map[string]any{"email": "olga@x.ru"}, http.StatusOK); upd["email"] != "olga@x.ru" ||
		upd["phone"] != "+7 900" || upd["name"] != "Ольга" {
		t.Fatalf("patched contact = %v", upd)
	}
	call("DELETE", tmpURL, readOnly, nil, http.StatusForbidden)
	call("DELETE", tmpURL, full, nil, http.StatusNoContent)
	call("GET", tmpURL, full, nil, http.StatusNotFound)
	// BI: the deals by stage (the amounts add up), the columns named; without the scope — forbidden; an unknown field — the error's key
	bq := call("POST", "/api/v1/bi/query", full, map[string]any{"dataset": "crm_deal", "dims": []string{"stage"},
		"measures": []map[string]string{{"agg": "sum", "field": "amount"}}}, http.StatusOK)
	total := 0.0
	for _, r := range bq["rows"].([]any) {
		total += r.([]any)[1].(float64)
	}
	if total != 1000 || bq["columns"].([]any)[1].(map[string]any)["name"] != "Сумма" {
		t.Fatalf("bi query = %v", bq)
	}
	call("GET", "/api/v1/bi/datasets", full, nil, http.StatusOK)
	call("GET", "/api/v1/bi/datasets", readOnly, nil, http.StatusForbidden)
	if e := call("POST", "/api/v1/bi/query", full, map[string]any{"dataset": "task", "dims": []string{"x"}}, http.StatusUnprocessableEntity); e["code"] != "bi.err.field" {
		t.Fatalf("bi error = %v", e)
	}
	// «Складской учёт» of a deal: its row reserved on the warehouse, «Реализация» through the API and from the card
	wh, err := srv.CRM.SaveWarehouse(ctx, crm.Warehouse{Name: "Основной", Responsible: crm.Ref{ID: uid}})
	if err != nil {
		t.Fatal(err)
	}
	chair, err := srv.CRM.SaveProduct(ctx, uid, crm.Product{Name: "Стул", Price: 1200})
	if err != nil {
		t.Fatal(err)
	}
	dealURL := "/api/v1/crm/deals/" + apiDeal["id"].(string)
	call("POST", dealURL+"/shipment", full, nil, http.StatusUnprocessableEntity) // no products
	call("PUT", dealURL+"/products", full, []map[string]any{{"product_id": chair, "quantity": 2, "warehouse_id": wh, "reserve_until": "2099-01-01"}}, http.StatusOK)
	if rows, _ := srv.CRM.ProductRows(ctx, "deal", apiDeal["id"].(string)); len(rows) != 1 || rows[0].Reserve != 2 || rows[0].ReserveUntil == nil {
		t.Fatalf("reserved rows %+v", rows)
	}
	ship := call("POST", dealURL+"/shipment", full, nil, http.StatusCreated)
	if ship["kind"] != "shipment" || ship["deal_id"] != apiDeal["id"] || ship["amount"].(float64) != 2400 {
		t.Fatalf("api shipment %v", ship)
	}
	call("POST", dealURL+"/shipment", readOnly, nil, http.StatusForbidden)
	if got := form(cookie, "/crm/deals/"+deal+"/products", url.Values{"product": {chair}, "name": {"Стул"}, "price": {"1000"}, "quantity": {"3"},
		"warehouse": {wh}, "reserve": {""}, "reserve_until": {""}}); !strings.HasPrefix(got, "/crm/deals/"+deal) {
		t.Fatalf("deal's rows: %s", got)
	}
	if got := form(cookie, "/crm/deals/"+deal+"/shipment", nil); !strings.HasPrefix(got, "/store/docs/") {
		t.Fatalf("shipment from the card: %s", got)
	}
	if res, _ := srv.CRM.Reserves(ctx, []string{chair}, ""); res[chair][wh] != 5 {
		t.Fatalf("reserves %v", res)
	}
	call("POST", "/api/v1/bp/"+tpl+"/start", full, map[string]any{"values": map[string]string{"sum": "70"}}, http.StatusCreated)
	id1, _ := srv.Tasks.TaskIDByKey(ctx, "API-1")
	vals, _ := srv.Tasks.FieldValues(ctx, id1)
	rems, _ := srv.Tasks.Reminders(ctx, uid, id1)
	if len(vals) != 1 || vals[0].Value != "2000" || len(rems) != 1 {
		t.Fatalf("after the forms: fields %v, reminders %v", vals, rems)
	}
}

func TestAPIMessageReactions(t *testing.T) {
	m := matrixsync.Message{ID: "$1", Body: "hi", Reactions: []tasks.Reaction{{Key: "👍", Count: 2, Mine: true, Names: "bob, eve"}}}
	b, _ := json.Marshal(toAPIMessage(m))
	if !strings.Contains(string(b), `"reactions":[{"key":"👍","count":2,"names":"bob, eve","mine":true}]`) {
		t.Fatalf("reactions in the message: %s", b)
	}
	if b, _ := json.Marshal(toAPIMessage(matrixsync.Message{ID: "$2"})); strings.Contains(string(b), "reactions") {
		t.Fatalf("no reactions: %s", b)
	}
}
