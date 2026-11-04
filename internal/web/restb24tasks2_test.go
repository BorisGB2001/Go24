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
	"testing"

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

// TestRestPortalTasksMore: task.elapseditem.*, task.stages.* of a group and of «Мой план», task.dependence.*,
// tasks.task.result.*, history.list, counters.get, task.planner.getlist, startwatch/stopwatch; the rights of go24 hold.
func TestRestPortalTasksMore(t *testing.T) {
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

	newTask := func(hook, title, resp string) string {
		t.Helper()
		return str(task(form(hook, "tasks.task.add", url.Values{"fields[TITLE]": {title}, "fields[GROUP_ID]": {num(goNum)},
			"fields[RESPONSIBLE_ID]": {resp}, "fields[DEADLINE]": {"2020-01-10T18:00:00"}}))["id"])
	}
	id := newTask(hook, "Выгрузка в 1С", num(ivanNum))
	id2 := newTask(hook, "Проверка выгрузки", num(ivanNum))
	errOf := func(hook, method, q string, status int) string {
		t.Helper()
		return str(call(hook+method, "application/x-www-form-urlencoded", q, status)["error"])
	}

	// task.elapseditem: Ivan logs time, the list, get, update, delete
	e1 := str(form(ivanHook, "task.elapseditem.add", url.Values{"TASKID": {id}, "ARFIELDS[SECONDS]": {"3600"},
		"ARFIELDS[COMMENT_TEXT]": {"Настроил обмен"}, "ARFIELDS[CREATED_DATE]": {"2026-10-20"}}))
	e2 := str(js(ivanHook, "task.elapseditem.add", `[`+id+`, {"MINUTES": 30}]`))
	str(form(ivanHook, "task.elapseditem.add", url.Values{"TASKID": {id2}, "ARFIELDS[SECONDS]": {"600"}}))
	items := form(hook, "task.elapseditem.getlist", url.Values{"TASKID": {id}}).([]any)
	if len(items) != 2 {
		t.Fatalf("task.elapseditem.getlist: %v", items)
	}
	if i := items[0].(map[string]any); i["ID"] != e1 || i["SECONDS"] != "3600" || i["MINUTES"] != "60" || i["USER_ID"] != num(ivanNum) ||
		i["SOURCE"] != "2" || i["COMMENT_TEXT"] != "Настроил обмен" || !strings.HasPrefix(str(i["CREATED_DATE"]), "2026-10-20T") {
		t.Errorf("an entry: %v", i)
	}
	if g := form(hook, "task.elapseditem.get", url.Values{"TASKID": {id}, "ITEMID": {e2}}).(map[string]any); g["SECONDS"] != "1800" {
		t.Errorf("task.elapseditem.get: %v", g)
	}
	all := call(ivanHook+"task.elapseditem.getlist", "application/x-www-form-urlencoded", "ORDER[SECONDS]=desc&FILTER[%3E%3DSECONDS]=1000", 200)
	if l := all["result"].([]any); len(l) != 2 || l[0].(map[string]any)["ID"] != e1 || all["total"].(float64) != 2 {
		t.Errorf("getlist without a task: %v", all)
	}
	if l := form(olgaHook, "task.elapseditem.getlist", nil).([]any); len(l) != 0 {
		t.Errorf("Olga sees entries: %v", l)
	}
	if form(ivanHook, "task.elapseditem.update", url.Values{"TASKID": {id}, "ITEMID": {e2}, "ARFIELDS[SECONDS]": {"2700"}}) != true {
		t.Error("task.elapseditem.update")
	}
	if g := form(hook, "task.elapseditem.get", url.Values{"TASKID": {id}, "ITEMID": {e2}}).(map[string]any); g["SECONDS"] != "2700" || g["COMMENT_TEXT"] != "" {
		t.Errorf("after update: %v", g)
	}
	if e := errOf(ivanHook, "task.elapseditem.add", "TASKID="+id+"&ARFIELDS[SECONDS]=60&ARFIELDS[USER_ID]="+num(annaNum), 403); e != "ACCESS_DENIED" {
		t.Errorf("time logged for another: %v", e)
	}
	form(ivanHook, "task.elapseditem.delete", url.Values{"TASKID": {id}, "ITEMID": {e2}})
	if g := task(form(hook, "tasks.task.get", url.Values{"taskId": {id}})); str(g["timeSpentInLogs"]) != "3600" {
		t.Errorf("time spent after delete: %v", g["timeSpentInLogs"])
	}

	// task.stages of the group: the three of go24, a new one before «Сделаны», moving a task completes it
	stages := form(hook, "task.stages.get", url.Values{"entityId": {num(goNum)}}).(map[string]any)
	if len(stages) != 3 {
		t.Fatalf("task.stages.get: %v", stages)
	}
	var finish string
	for k, v := range stages {
		if st := v.(map[string]any); st["SYSTEM_TYPE"] == "FINISH" {
			finish = k
			if st["TITLE"] != "Сделаны" || st["ENTITY_TYPE"] != "G" || st["ENTITY_ID"] != num(goNum) || st["SORT"] != "300" {
				t.Errorf("the finish stage: %v", st)
			}
		}
	}
	review := str(form(hook, "task.stages.add", url.Values{"fields[TITLE]": {"Ревью"}, "fields[COLOR]": {"FFAB00"}, "fields[ENTITY_ID]": {num(goNum)}}))
	stages = form(hook, "task.stages.get", url.Values{"entityId": {num(goNum)}}).(map[string]any)
	if st := stages[review].(map[string]any); st["TITLE"] != "Ревью" || st["COLOR"] != "FFAB00" || st["SORT"] != "300" {
		t.Errorf("a new stage: %v", stages)
	}
	if e := errOf(ivanHook, "task.stages.add", "fields[TITLE]=x&fields[ENTITY_ID]="+num(goNum), 403); e != "ACCESS_DENIED" {
		t.Errorf("a member adds a stage: %v", e)
	}
	form(hook, "task.stages.update", url.Values{"id": {review}, "fields[TITLE]": {"Код-ревью"}})
	if form(ivanHook, "task.stages.canmovetask", url.Values{"entityId": {num(goNum)}, "entityType": {"G"}}) != true ||
		form(olgaHook, "task.stages.canmovetask", url.Values{"entityId": {num(goNum)}, "entityType": {"G"}}) != false {
		t.Error("task.stages.canmovetask")
	}
	form(ivanHook, "task.stages.movetask", url.Values{"id": {id2}, "stageId": {review}})
	form(ivanHook, "task.stages.movetask", url.Values{"id": {id2}, "stageId": {finish}})
	if g := task(form(hook, "tasks.task.get", url.Values{"taskId": {id2}})); g["status"] != "5" {
		t.Errorf("moved to «Сделаны»: status %v", g["status"])
	}
	if e := errOf(hook, "task.stages.delete", "id="+finish, 400); e != "ERROR_CORE" {
		t.Errorf("the finish stage deleted: %v", e)
	}
	form(hook, "task.stages.delete", url.Values{"id": {review}})

	// «Мой план»: entityId 0, the stages are Ivan's alone
	plan := form(ivanHook, "task.stages.get", url.Values{"entityId": {"0"}}).(map[string]any)
	if len(plan) != 3 {
		t.Fatalf("my plan: %v", plan)
	}
	mine := str(form(ivanHook, "task.stages.add", url.Values{"fields[TITLE]": {"После отпуска"}}))
	if st := form(ivanHook, "task.stages.get", nil).(map[string]any)[mine].(map[string]any); st["ENTITY_TYPE"] != "U" || st["ENTITY_ID"] != num(ivanNum) {
		t.Errorf("a stage of my plan: %v", st)
	}
	form(ivanHook, "task.stages.movetask", url.Values{"id": {id}, "stageId": {mine}})
	var inPlan bool
	must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM plan_items pi JOIN plan_stages ps ON ps.id = pi.stage_id WHERE ps.num = $1)`, mine).Scan(&inPlan))
	if !inPlan {
		t.Error("task.stages.movetask to my plan")
	}
	if e := errOf(hook, "task.stages.update", "id="+mine+"&fields[TITLE]=x", 400); e != "ERROR_NOT_FOUND" {
		t.Errorf("another's plan changed: %v", e)
	}
	form(ivanHook, "task.stages.delete", url.Values{"id": {mine}})

	// task.dependence: the second depends on the first, finish-finish
	if form(hook, "task.dependence.add", url.Values{"taskIdFrom": {id}, "taskIdTo": {id2}, "linkType": {"3"}}) != true {
		t.Error("task.dependence.add")
	}
	var typ string
	must(pool.QueryRow(ctx, `SELECT type FROM task_links`).Scan(&typ))
	if typ != "finish_finish" {
		t.Errorf("the link: %s", typ)
	}
	if e := errOf(hook, "task.dependence.add", "taskIdFrom="+id2+"&taskIdTo="+id, 400); e != "ERROR_CORE" { // a cycle
		t.Errorf("a cycle: %v", e)
	}
	form(hook, "task.dependence.delete", url.Values{"taskIdFrom": {id}, "taskIdTo": {id2}})
	errOf(hook, "task.dependence.delete", "taskIdFrom="+id+"&taskIdTo="+id2, 400)

	// tasks.task.result: Ivan's comment becomes the result
	cm := str(form(ivanHook, "task.commentitem.add", url.Values{"TASKID": {id}, "FIELDS[POST_MESSAGE]": {"Обмен работает"}}))
	r := form(ivanHook, "tasks.task.result.addFromComment", url.Values{"commentId": {cm}}).(map[string]any)
	if str(r["commentId"]) != cm || r["text"] != "Обмен работает" || str(r["createdBy"]) != num(ivanNum) || str(r["taskId"]) != id {
		t.Errorf("result.addFromComment: %v", r)
	}
	if l := form(hook, "tasks.task.result.list", url.Values{"taskId": {id}}).([]any); len(l) != 1 {
		t.Errorf("result.list: %v", l)
	}
	if e := errOf(olgaHook, "tasks.task.result.deleteFromComment", "commentId="+cm, 403); e != "ACCESS_DENIED" {
		t.Errorf("Olga removes the result: %v", e)
	}
	form(ivanHook, "tasks.task.result.deleteFromComment", url.Values{"commentId": {cm}})
	if l := form(hook, "tasks.task.result.list", url.Values{"taskId": {id}}).([]any); len(l) != 0 {
		t.Errorf("result.list after delete: %v", l)
	}

	// history: the creation first, the status of the second task in numbers
	h := form(hook, "tasks.task.history.list", url.Values{"taskId": {id}}).(map[string]any)["list"].([]any)
	if first := h[0].(map[string]any); first["field"] != "NEW" || first["user"].(map[string]any)["id"] != num(annaNum) {
		t.Errorf("history: %v", h)
	}
	st := form(hook, "tasks.task.history.list", url.Values{"taskId": {id2}, "filter[FIELD]": {"STATUS"}}).(map[string]any)["list"].([]any)
	if len(st) != 1 || st[0].(map[string]any)["value"].(map[string]any)["to"] != "5" {
		t.Errorf("history of the status: %v", st)
	}

	// counters: Ivan's overdue task (the second is completed) and Anna's comment unread
	form(hook, "task.commentitem.add", url.Values{"TASKID": {id}, "FIELDS[POST_MESSAGE]": {"Посмотри @Иван"}})
	c := form(ivanHook, "tasks.task.counters.get", url.Values{"type": {"view_role_responsible"}}).(map[string]any)
	if c["expired"].(map[string]any)["counter"] != float64(1) {
		t.Errorf("counters: %v", c)
	}
	if c := form(ivanHook, "tasks.task.counters.get", url.Values{"type": {"view_role_auditor"}}).(map[string]any); c["expired"].(map[string]any)["counter"] != float64(0) {
		t.Errorf("counters of an auditor: %v", c)
	}
	if e := errOf(ivanHook, "tasks.task.counters.get", "userId="+num(annaNum), 403); e != "ACCESS_DENIED" {
		t.Errorf("another's counters: %v", e)
	}

	// the plan of the day
	tid, err := srv.Tasks.TaskIDByKey(ctx, "GO-1")
	must(err)
	must(srv.Tasks.SetDayPlan(ctx, ivan, tid, true))
	if l := form(ivanHook, "task.planner.getlist", nil).([]any); len(l) != 1 || l[0] != id {
		t.Errorf("task.planner.getlist: %v", l)
	}

	// startwatch: Olga sees no task; Anna's colleague in the project becomes an auditor of a task of the project
	if e := errOf(olgaHook, "tasks.task.startwatch", "taskId="+id, 403); e != "ACCESS_DENIED" {
		t.Errorf("Olga watches: %v", e)
	}
	form(hook, "tasks.task.startwatch", url.Values{"taskId": {id}})
	if a := task(form(hook, "tasks.task.get", url.Values{"taskId": {id}}))["auditors"].([]any); len(a) != 1 || a[0] != num(annaNum) {
		t.Errorf("startwatch: %v", a)
	}
	form(hook, "tasks.task.stopwatch", url.Values{"taskId": {id}})
	if a := task(form(hook, "tasks.task.get", url.Values{"taskId": {id}}))["auditors"].([]any); len(a) != 0 {
		t.Errorf("stopwatch: %v", a)
	}

	// tasks.task.getaccess (GO24DEV-1000): the caller's rights by default; the responsible's and a stranger's
	id3 := newTask(hook, "Права", num(ivanNum))
	acc := func(v any, user int64) map[string]any {
		t.Helper()
		return v.(map[string]any)["allowedActions"].(map[string]any)["U"+num(user)].(map[string]any)
	}
	if a := acc(form(hook, "tasks.task.getaccess", url.Values{"taskId": {id3}}), annaNum); a["ACTION_EDIT"] != true ||
		a["ACTION_REMOVE"] != true || a["ACTION_RATE"] != true || a["ACTION_DELEGATE"] != false || a["ACTION_APPROVE"] != false ||
		a["ACTION_ADD_FAVORITE"] != true || a["ACTION_DELETE_FAVORITE"] != false || len(a) != 21 {
		t.Errorf("Anna's access: %v", a)
	}
	both := form(hook, "tasks.task.getaccess", url.Values{"taskId": {id3}, "users[]": {num(ivanNum), num(olgaNum)}})
	if a := acc(both, ivanNum); a["ACTION_START"] != true || a["ACTION_DELEGATE"] != true || a["ACTION_REMOVE"] != false ||
		a["ACTION_RATE"] != false || a["ACTION_COMPLETE"] != true || a["ACTION_ELAPSED_TIME_ADD"] != true {
		t.Errorf("Ivan's access: %v", a)
	}
	for k, v := range acc(both, olgaNum) {
		if v != false {
			t.Errorf("Olga may %s", k)
		}
	}
	if e := errOf(olgaHook, "tasks.task.getaccess", "taskId="+id3, 403); e != "ACCESS_DENIED" {
		t.Errorf("Olga asks: %v", e)
	}

	// task.logitem.list: the old history, FIELD in the filter
	form(ivanHook, "tasks.task.start", url.Values{"taskId": {id3}})
	logs := form(hook, "task.logitem.list", url.Values{"TASKID": {id3}, "ORDER[ID]": {"desc"}, "FILTER[FIELD]": {"STATUS"}}).([]any)
	if len(logs) != 1 {
		t.Fatalf("task.logitem.list: %v", logs)
	}
	if l := logs[0].(map[string]any); l["FROM_VALUE"] != "2" || l["TO_VALUE"] != "3" || l["USER_ID"] != num(ivanNum) || l["TASK_ID"] != id3 {
		t.Errorf("a log item: %v", l)
	}
	if l := js(hook, "task.logitem.list", `[`+id3+`]`).([]any); len(l) < 2 {
		t.Errorf("all log items: %v", l)
	}

	// own fields of tasks (task.item.userfield.*): an administrator adds them; UF_* in select, filter, add and update
	errOf(hook, "task.item.userfield.add", "PARAMS[USER_TYPE_ID]=string&PARAMS[FIELD_NAME]=UF_X", 403)
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, anna)
	must(err)
	contract := str(js(hook, "task.item.userfield.add", `{"PARAMS": {"USER_TYPE_ID": "string", "FIELD_NAME": "UF_CONTRACT",
		"EDIT_FORM_LABEL": {"ru": "Договор", "en": "Contract"}}}`))
	sum := str(form(hook, "task.item.userfield.add", url.Values{"PARAMS[USER_TYPE_ID]": {"double"}, "PARAMS[LABEL]": {"Сумма"}}))
	stage := str(js(hook, "task.item.userfield.add", `[{"USER_TYPE_ID": "enumeration", "FIELD_NAME": "STAGE", "LABEL": "Этап",
		"LIST": [{"VALUE": "Черновик"}, {"VALUE": "Готово"}]}]`))
	errOf(hook, "task.item.userfield.add", "PARAMS[USER_TYPE_ID]=string&PARAMS[FIELD_NAME]=UF_AUTO_5", 400)
	errOf(hook, "task.item.userfield.add", "PARAMS[USER_TYPE_ID]=string&PARAMS[FIELD_NAME]=UF_CONTRACT", 400)
	ufs := form(hook, "task.item.userfield.getlist", nil).([]any)
	if len(ufs) != 3 || ufs[0].(map[string]any)["ENTITY_ID"] != "TASKS_TASK" {
		t.Fatalf("task.item.userfield.getlist: %v", ufs)
	}
	names := map[string]string{}
	for _, u := range ufs {
		names[str(u.(map[string]any)["ID"])] = str(u.(map[string]any)["FIELD_NAME"])
	}
	if names[contract] != "UF_CONTRACT" || names[sum] != "UF_AUTO_"+sum || names[stage] != "UF_STAGE" {
		t.Errorf("the names: %v", names)
	}
	if l := js(hook, "task.item.userfield.getlist", `[{}, {"USER_TYPE_ID": "double"}]`).([]any); len(l) != 1 {
		t.Errorf("getlist by type: %v", l)
	}
	if u := form(hook, "task.item.userfield.get", url.Values{"ID": {stage}}).(map[string]any); len(u["LIST"].([]any)) != 2 {
		t.Errorf("task.item.userfield.get: %v", u)
	}
	if r := js(hook, "task.item.userfield.update", `[`+contract+`, {"EDIT_FORM_LABEL": "Номер договора"}]`); r != true {
		t.Errorf("task.item.userfield.update: %v", r)
	}
	if f := form(hook, "tasks.task.getFields", nil).(map[string]any)["fields"].(map[string]any)["UF_CONTRACT"].(map[string]any); f["title"] != "Номер договора" || f["type"] != "string" {
		t.Errorf("getFields UF_CONTRACT: %v", f)
	}

	id4 := str(task(form(hook, "tasks.task.add", url.Values{"fields[TITLE]": {"Счёт"}, "fields[GROUP_ID]": {num(goNum)},
		"fields[RESPONSIBLE_ID]": {num(ivanNum)}, "fields[UF_CONTRACT]": {"Д-17"}, "fields[UF_AUTO_" + sum + "]": {"1500.5"},
		"fields[UF_STAGE]": {"2"}}))["id"])
	sel := url.Values{"taskId": {id4}, "select[]": {"ID", "UF_*"}}
	if g := task(form(hook, "tasks.task.get", sel)); g["ufContract"] != "Д-17" || g["ufAuto"+sum] == nil || g["ufStage"] != "2" || g["title"] != nil {
		t.Errorf("UF_* of the task: %v", g)
	}
	if g := task(form(hook, "tasks.task.get", url.Values{"taskId": {id4}})); g["ufContract"] != nil {
		t.Errorf("without select: %v", g)
	}
	l4 := form(hook, "tasks.task.list", url.Values{"filter[UF_CONTRACT]": {"Д-17"}, "select[]": {"ID", "UF_CONTRACT"}}).(map[string]any)["tasks"].([]any)
	if len(l4) != 1 || l4[0].(map[string]any)["id"] != id4 {
		t.Errorf("filter by UF_CONTRACT: %v", l4)
	}
	if e := errOf(ivanHook, "tasks.task.update", "taskId="+id4+"&fields[UF_CONTRACT]=X", 403); e != "ACCESS_DENIED" {
		t.Errorf("Ivan changes a field: %v", e)
	}
	form(hook, "tasks.task.update", url.Values{"taskId": {id4}, "fields[UF_CONTRACT]": {""}, "fields[UF_STAGE]": {"Черновик"}})
	if g := task(form(hook, "tasks.task.get", sel)); g["ufContract"] != nil || g["ufStage"] != "1" {
		t.Errorf("updated UF_*: %v", g)
	}
	form(hook, "task.item.userfield.delete", url.Values{"ID": {sum}})
	if l := form(hook, "task.item.userfield.getlist", nil).([]any); len(l) != 2 {
		t.Errorf("after delete: %v", l)
	}
}
