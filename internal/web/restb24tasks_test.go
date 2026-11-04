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
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

func TestCamelPortal(t *testing.T) {
	for in, want := range map[string]string{"ID": "id", "RESPONSIBLE_ID": "responsibleId", "START_DATE_PLAN": "startDatePlan"} {
		if got := camelPortal(in); got != want {
			t.Errorf("camelPortal(%s) = %s, want %s", in, got, want)
		}
	}
	for in, want := range map[string]string{"responsibleId": "RESPONSIBLE_ID", "TITLE": "TITLE", "title": "TITLE", "groupId": "GROUP_ID"} {
		if got := upperPortal(in); got != want {
			t.Errorf("upperPortal(%s) = %s, want %s", in, got, want)
		}
	}
}

// TestRestPortalTasks: tasks.task.* by numbers with the fields, the actions, task.commentitem.*,
// task.checklistitem.*, department.get; the rights of go24 hold — an employee does not see a task not theirs.
func TestRestPortalTasks(t *testing.T) {
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
	var anna, ivan, olga string
	var annaNum, ivanNum, olgaNum int64
	for _, u := range []struct {
		sub, name string
		id        *string
		num       *int64
	}{{"anna", "Анна Петрова", &anna, &annaNum}, {"ivan", "Иван Сидоров", &ivan, &ivanNum}, {"olga", "Ольга Белова", &olga, &olgaNum}} {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id, num`,
			u.sub, u.name).Scan(u.id, u.num))
	}
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), I18n: bundle,
		Audit: audit.NewStore(pool), BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	_, err = srv.Tasks.CreateProject(ctx, anna, "GO", "Разработка", "")
	must(err)
	pr, err := srv.Tasks.ProjectByKey(ctx, anna, "GO")
	must(err)
	must(srv.Tasks.SetMember(ctx, pr, ivan, "member"))
	_, err = srv.Tasks.CreateProject(ctx, olga, "OLGA", "Ольгин", "")
	must(err)
	var goNum int64
	must(pool.QueryRow(ctx, `SELECT num FROM projects WHERE key = 'GO'`).Scan(&goNum))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(path, ctype, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", ctype)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", path, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hookOf := func(id string, n int64) string {
		token, err := srv.Keys.Create(ctx, id, "n8n", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	hook, ivanHook, olgaHook := hookOf(anna, annaNum), hookOf(ivan, ivanNum), hookOf(olga, olgaNum)
	form := func(hook, method string, v url.Values) any {
		t.Helper()
		return call(hook+method, "application/x-www-form-urlencoded", v.Encode(), 200)["result"]
	}
	js := func(hook, method, body string) any {
		t.Helper()
		return call(hook+method+".json", "application/json", body, 200)["result"]
	}
	str := func(v any) string { return portalStr(v) }
	task := func(v any) map[string]any { t.Helper(); return v.(map[string]any)["task"].(map[string]any) }

	// getFields
	f := form(hook, "tasks.task.getFields", nil).(map[string]any)["fields"].(map[string]any)
	if pri := f["PRIORITY"].(map[string]any); pri["type"] != "enum" || len(pri["values"].(map[string]any)) != 3 || f["ID"].(map[string]any)["primary"] != true {
		t.Errorf("tasks.task.getFields: %v", f)
	}

	// add: numbers in, numbers out, the answer in camelCase
	tk := task(form(hook, "tasks.task.add", url.Values{"fields[TITLE]": {"Выгрузка в 1С"}, "fields[DESCRIPTION]": {"Каждую ночь"},
		"fields[RESPONSIBLE_ID]": {num(ivanNum)}, "fields[GROUP_ID]": {num(goNum)}, "fields[PRIORITY]": {"2"},
		"fields[DEADLINE]": {"2026-11-30T18:00:00+01:00"}, "fields[AUDITORS][]": {num(annaNum)}, "fields[TAGS][]": {"1С", "ночь"},
		"fields[TIME_ESTIMATE]": {"7200"}, "fields[START_DATE_PLAN]": {"2026-11-02"}, "fields[UF_X]": {"skipped"}}))
	id := str(tk["id"])
	for k, want := range map[string]string{"title": "Выгрузка в 1С", "responsibleId": num(ivanNum), "createdBy": num(annaNum),
		"groupId": num(goNum), "priority": "2", "status": "2", "timeEstimate": "7200", "taskControl": "N", "commentsCount": "0"} {
		if str(tk[k]) != want {
			t.Errorf("tasks.task.add %s = %v, want %s", k, tk[k], want)
		}
	}
	if dl, _ := time.Parse(time.RFC3339, str(tk["deadline"])); !dl.Equal(time.Date(2026, 11, 30, 17, 0, 0, 0, time.UTC)) || !strings.HasPrefix(str(tk["startDatePlan"]), "2026-11-02T00:00:00") {
		t.Errorf("tasks.task.add dates: %v %v", tk["deadline"], tk["startDatePlan"])
	}
	if a := tk["auditors"].([]any); len(a) != 1 || a[0] != num(annaNum) || len(tk["tags"].([]any)) != 2 {
		t.Errorf("tasks.task.add auditors %v tags %v", tk["auditors"], tk["tags"])
	}
	if tk["responsible"].(map[string]any)["name"] != "Иван Сидоров" || tk["group"].(map[string]any)["name"] != "Разработка" {
		t.Errorf("tasks.task.add people: %v %v", tk["responsible"], tk["group"])
	}
	// a subtask, given no group: the project of the caller's last task
	sub := task(js(hook, "tasks.task.add", `{"fields": {"TITLE": "Проверить выгрузку", "PARENT_ID": `+id+`}}`))
	if str(sub["parentId"]) != id || str(sub["groupId"]) != num(goNum) || str(sub["responsibleId"]) != num(annaNum) {
		t.Errorf("a subtask: %v", sub)
	}

	// get: select in capitals or camelCase
	g := task(form(hook, "tasks.task.get", url.Values{"taskId": {id}, "select[]": {"TITLE", "responsibleId"}}))
	if len(g) != 4 || g["title"] != "Выгрузка в 1С" || g["responsible"] == nil { // id, title, responsibleId, responsible
		t.Errorf("tasks.task.get select: %v", g)
	}

	// list: filters
	for q, want := range map[string]int{"filter[RESPONSIBLE_ID]=" + num(ivanNum): 1, "filter[%25TITLE]=выгруз": 2, "filter[PARENT_ID]=" + id: 1,
		"filter[TAG]=1с": 1, "filter[AUDITOR]=" + num(annaNum): 1, "filter[MEMBER]=" + num(ivanNum): 1, "filter[>PRIORITY]=1": 1,
		"filter[STATUS]=2": 2, "filter[!ID]=" + id: 1, "filter[GROUP_ID]=" + num(goNum): 2, "filter[<DEADLINE]=2026-12-01": 1,
		"filter[responsibleId]=" + num(annaNum): 1} {
		got := call(hook+"tasks.task.list", "application/x-www-form-urlencoded", q, 200)
		if n := len(got["result"].(map[string]any)["tasks"].([]any)); n != want || int(got["total"].(float64)) != want {
			t.Errorf("tasks.task.list %s: %d, want %d", q, n, want)
		}
	}
	first := call(hook+"tasks.task.list", "application/x-www-form-urlencoded", "order[ID]=desc", 200)["result"].(map[string]any)["tasks"].([]any)
	if str(first[0].(map[string]any)["id"]) != str(sub["id"]) {
		t.Errorf("tasks.task.list order[ID]=desc: %v", first)
	}
	// rights: Olga sees no task of the project, Ivan sees his own
	if got := call(olgaHook+"tasks.task.list", "application/x-www-form-urlencoded", "", 200); got["total"].(float64) != 0 {
		t.Errorf("Olga sees tasks: %v", got)
	}
	if e := call(olgaHook+"tasks.task.get", "application/x-www-form-urlencoded", "taskId="+id, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("Olga gets a task: %v", e)
	}
	if e := call(hook+"tasks.task.get", "application/x-www-form-urlencoded", "taskId=99999", 400); e["error"] != "ERROR_NOT_FOUND" {
		t.Errorf("no such task: %v", e)
	}

	// update: only the fields given
	up := task(js(hook, "tasks.task.update", `{"taskId": `+id+`, "fields": {"TITLE": "Выгрузка в 1С (ночь)", "ACCOMPLICES": [`+num(annaNum)+`],
		"TASK_CONTROL": "Y", "TAGS": ["1С"]}}`))
	if up["title"] != "Выгрузка в 1С (ночь)" || up["taskControl"] != "Y" || str(up["responsibleId"]) != num(ivanNum) ||
		len(up["accomplices"].([]any)) != 1 || len(up["auditors"].([]any)) != 1 || len(up["tags"].([]any)) != 1 {
		t.Errorf("tasks.task.update: %v", up)
	}

	// actions: Ivan starts and completes, the task goes to Anna's control, she approves
	if st := task(form(ivanHook, "tasks.task.start", url.Values{"taskId": {id}}))["status"]; st != "3" {
		t.Errorf("start: status %v", st)
	}
	if st := task(form(ivanHook, "tasks.task.complete", url.Values{"taskId": {id}}))["status"]; st != "4" {
		t.Errorf("complete under control: status %v", st)
	}
	if done := task(form(hook, "tasks.task.approve", url.Values{"taskId": {id}})); done["status"] != "5" || str(done["closedBy"]) != num(annaNum) {
		t.Errorf("approve: %v", done)
	}
	if e := call(olgaHook+"tasks.task.renew", "application/x-www-form-urlencoded", "taskId="+id, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("Olga renews: %v", e)
	}
	if d := task(form(hook, "tasks.task.delegate", url.Values{"taskId": {str(sub["id"])}, "userId": {num(ivanNum)}})); str(d["responsibleId"]) != num(ivanNum) {
		t.Errorf("delegate: %v", d)
	}
	if r := form(hook, "tasks.task.favorite.add", url.Values{"taskId": {id}}).(map[string]any); r["task"] != true {
		t.Errorf("favorite.add: %v", r)
	}

	// comments: by place, as the old methods take them
	cm := str(js(hook, "task.commentitem.add", `[`+id+`, {"POST_MESSAGE": "Проверил выгрузку"}]`))
	cm2 := str(form(ivanHook, "task.commentitem.add", url.Values{"TASKID": {id}, "FIELDS[POST_MESSAGE]": {"Спасибо"}}))
	list := form(hook, "task.commentitem.getlist", url.Values{"TASKID": {id}, "ORDER[ID]": {"desc"}}).([]any)
	if len(list) != 2 || list[0].(map[string]any)["ID"] != cm2 || list[1].(map[string]any)["AUTHOR_ID"] != num(annaNum) {
		t.Errorf("task.commentitem.getlist: %v", list)
	}
	if l := form(hook, "task.commentitem.getlist", url.Values{"TASKID": {id}, "FILTER[AUTHOR_ID]": {num(ivanNum)}}).([]any); len(l) != 1 {
		t.Errorf("task.commentitem.getlist FILTER: %v", l)
	}
	if form(hook, "task.commentitem.update", url.Values{"TASKID": {id}, "ITEMID": {cm}, "FIELDS[POST_MESSAGE]": {"Проверил"}}) != true {
		t.Error("task.commentitem.update")
	}
	if c := form(hook, "task.commentitem.get", url.Values{"TASKID": {id}, "ITEMID": {cm}}).(map[string]any); c["POST_MESSAGE"] != "Проверил" {
		t.Errorf("task.commentitem.get: %v", c)
	}
	if e := call(hook+"task.commentitem.update", "application/x-www-form-urlencoded", "TASKID="+id+"&ITEMID="+cm2+"&FIELDS[POST_MESSAGE]=x", 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("another's comment changed: %v", e)
	}
	if form(hook, "task.commentitem.delete", url.Values{"TASKID": {id}, "ITEMID": {cm}}) != true {
		t.Error("task.commentitem.delete")
	}

	// checklist
	sid := str(sub["id"])
	it := str(form(ivanHook, "task.checklistitem.add", url.Values{"TASKID": {sid}, "FIELDS[TITLE]": {"Сверить остатки"}}))
	it2 := str(js(ivanHook, "task.checklistitem.add", `[`+sid+`, {"TITLE": "Отчёт", "IS_COMPLETE": "Y"}]`))
	if form(ivanHook, "task.checklistitem.complete", url.Values{"TASKID": {sid}, "ITEMID": {it}}) != true ||
		form(ivanHook, "task.checklistitem.complete", url.Values{"TASKID": {sid}, "ITEMID": {it}}) != true {
		t.Error("task.checklistitem.complete")
	}
	items := form(ivanHook, "task.checklistitem.getlist", url.Values{"TASKID": {sid}}).([]any)
	if len(items) != 2 || items[0].(map[string]any)["IS_COMPLETE"] != "Y" || items[1].(map[string]any)["ID"] != it2 ||
		items[1].(map[string]any)["IS_COMPLETE"] != "Y" || items[0].(map[string]any)["TOGGLED_BY"] != num(ivanNum) {
		t.Errorf("task.checklistitem.getlist: %v", items)
	}
	form(ivanHook, "task.checklistitem.renew", url.Values{"TASKID": {sid}, "ITEMID": {it}})
	form(ivanHook, "task.checklistitem.update", url.Values{"TASKID": {sid}, "ITEMID": {it2}, "FIELDS[TITLE]": {"Отчёт за месяц"}})
	if i := form(ivanHook, "task.checklistitem.get", url.Values{"TASKID": {sid}, "ITEMID": {it}}).(map[string]any); i["IS_COMPLETE"] != "N" {
		t.Errorf("renew: %v", i)
	}
	if i := form(ivanHook, "task.checklistitem.get", url.Values{"TASKID": {sid}, "ITEMID": {it2}}).(map[string]any); i["TITLE"] != "Отчёт за месяц" {
		t.Errorf("update: %v", i)
	}
	form(ivanHook, "task.checklistitem.delete", url.Values{"TASKID": {sid}, "ITEMID": {it}})
	if l := form(ivanHook, "task.checklistitem.getlist", url.Values{"TASKID": {sid}}).([]any); len(l) != 1 {
		t.Errorf("after delete: %v", l)
	}

	// delete: to the trash; the number is gone from the lists
	if r := form(hook, "tasks.task.delete", url.Values{"taskId": {sid}}).(map[string]any); r["task"] != true {
		t.Errorf("tasks.task.delete: %v", r)
	}
	call(hook+"tasks.task.get", "application/x-www-form-urlencoded", "taskId="+sid, 400)

	// batch: a task and its comment in one call
	b := form(hook, "batch", url.Values{"cmd[t]": {"tasks.task.add?fields[TITLE]=Из пакета&fields[GROUP_ID]=" + num(goNum)},
		"cmd[c]": {"task.commentitem.add?TASKID=$result[t][task][id]&FIELDS[POST_MESSAGE]=ok"}}).(map[string]any)
	if r := b["result"].(map[string]any); r["c"] == nil || len(b["result_error"].([]any)) != 0 {
		t.Errorf("batch: %v", b)
	}

	// department.get
	var dep, sub2 int64
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Компания', $1) RETURNING num`, anna).Scan(&dep))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, sort) VALUES ('Склад', (SELECT id FROM departments WHERE num = $1), 100)
		RETURNING num`, dep).Scan(&sub2))
	deps := form(hook, "department.get", nil).([]any)
	if len(deps) != 2 || deps[0].(map[string]any)["UF_HEAD"] != num(annaNum) || deps[1].(map[string]any)["PARENT"] != num(dep) {
		t.Errorf("department.get: %v", deps)
	}
	if d := form(hook, "department.get", url.Values{"PARENT": {num(dep)}}).([]any); len(d) != 1 || d[0].(map[string]any)["NAME"] != "Склад" {
		t.Errorf("department.get PARENT: %v", d)
	}
	ms := form(hook, "methods", url.Values{"scope": {"task"}}).([]any)
	if !slices.Contains(ms, any("tasks.task.list")) {
		t.Errorf("methods scope=task: %v", ms)
	}
}
