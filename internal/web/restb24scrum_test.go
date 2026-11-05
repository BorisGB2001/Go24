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

// TestRestPortalScrum: tasks.api.scrum.* — the backlog, epics, sprints (add, update, start, complete, delete), the kanban
// of a sprint and the scrum fields of a task; a stranger sees no secret scrum, a plain group is not a scrum.
func TestRestPortalScrum(t *testing.T) {
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
	var anna, olga string
	var annaNum, olgaNum int64
	for _, u := range []struct {
		sub, name string
		id        *string
		num       *int64
	}{{"anna", "Анна Петрова", &anna, &annaNum}, {"olga", "Ольга Белова", &olga, &olgaNum}} {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id, num`,
			u.sub, u.name).Scan(u.id, u.num))
	}
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), I18n: bundle,
		Audit: audit.NewStore(pool), BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	_, err = srv.Tasks.CreateProject(ctx, anna, "SC", "Скрам", "")
	must(err)
	pr, err := srv.Tasks.ProjectByKey(ctx, anna, "SC")
	must(err)
	must(srv.Tasks.SetProjectKind(ctx, pr, "scrum"))
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	must2(pool.Exec(ctx, `UPDATE projects SET privacy = 'secret' WHERE key = 'SC'`))
	_, err = srv.Tasks.CreateProject(ctx, anna, "PL", "Обычная", "")
	must(err)
	var scNum, plNum int64
	must(pool.QueryRow(ctx, `SELECT num FROM projects WHERE key = 'SC'`).Scan(&scNum))
	must(pool.QueryRow(ctx, `SELECT num FROM projects WHERE key = 'PL'`).Scan(&plNum))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want && !(want == 400 && res.StatusCode >= 400 && res.StatusCode < 500) {
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
	hook, olgaHook := hookOf(anna, annaNum), hookOf(olga, olgaNum)
	form := func(hook, method string, v url.Values) any {
		t.Helper()
		return call(hook+method, v.Encode(), 200)["result"]
	}
	fail := func(hook, method string, v url.Values) string {
		t.Helper()
		return portalStr(call(hook+method, v.Encode(), 400)["error"])
	}
	str := func(v any) string { return portalStr(v) }
	obj := func(v any) map[string]any { t.Helper(); return v.(map[string]any) }

	// The backlog: one number for get and add, a plain group is not a scrum, a stranger does not see a secret one.
	bl := obj(form(hook, "tasks.api.scrum.backlog.get", url.Values{"id": {num(scNum)}}))
	if bl["entityType"] != "backlog" || str(bl["groupId"]) != num(scNum) || str(bl["createdBy"]) != num(annaNum) {
		t.Fatalf("backlog: %v", bl)
	}
	if bl2 := obj(form(hook, "tasks.api.scrum.backlog.add", url.Values{"fields[groupId]": {num(scNum)}})); str(bl2["id"]) != str(bl["id"]) {
		t.Fatalf("backlog.add gave another backlog: %v", bl2)
	}
	fail(hook, "tasks.api.scrum.backlog.get", url.Values{"id": {num(plNum)}})
	fail(olgaHook, "tasks.api.scrum.backlog.get", url.Values{"id": {num(scNum)}})

	// Epics.
	ep := obj(form(hook, "tasks.api.scrum.epic.add", url.Values{"fields[groupId]": {num(scNum)}, "fields[name]": {"Оплата"},
		"fields[color]": {"#69DC00"}, "fields[description]": {"Платёжный модуль"}}))
	if ep["name"] != "Оплата" || ep["color"] != "#69dc00" || str(ep["createdBy"]) != num(annaNum) {
		t.Fatalf("epic.add: %v", ep)
	}
	epID := str(ep["id"])
	ep = obj(form(hook, "tasks.api.scrum.epic.update", url.Values{"id": {epID}, "fields[name]": {"Оплата картой"}}))
	if ep["name"] != "Оплата картой" || ep["description"] != "Платёжный модуль" {
		t.Fatalf("epic.update: %v", ep)
	}
	if list := form(hook, "tasks.api.scrum.epic.list", url.Values{"filter[GROUP_ID]": {num(scNum)}}).([]any); len(list) != 1 {
		t.Fatalf("epic.list: %v", list)
	}
	if list := form(olgaHook, "tasks.api.scrum.epic.list", nil).([]any); len(list) != 0 {
		t.Fatalf("a stranger sees the epics: %v", list)
	}
	fail(olgaHook, "tasks.api.scrum.epic.get", url.Values{"id": {epID}})
	if f := obj(obj(form(hook, "tasks.api.scrum.epic.getfields", nil))["fields"]); f["color"] == nil {
		t.Fatalf("epic.getFields: %v", f)
	}

	// Sprints: numbered in one row with the backlog.
	sp := obj(form(hook, "tasks.api.scrum.sprint.add", url.Values{"fields[groupId]": {num(scNum)}, "fields[name]": {"Спринт 1"},
		"fields[goal]": {"Оплата"}, "fields[dateStart]": {"2026-11-02"}, "fields[dateEnd]": {"2026-11-15"}}))
	if sp["status"] != "planned" || sp["goal"] != "Оплата" || !strings.HasPrefix(str(sp["dateStart"]), "2026-11-02T00:00:00") ||
		!strings.HasPrefix(str(sp["dateEnd"]), "2026-11-15T00:00:00") || str(sp["id"]) == str(bl["id"]) {
		t.Fatalf("sprint.add: %v", sp)
	}
	spID := str(sp["id"])
	fail(hook, "tasks.api.scrum.sprint.add", url.Values{"fields[groupId]": {num(scNum)}, "fields[name]": {"Плохой"},
		"fields[dateStart]": {"когда-нибудь"}})
	if list := form(hook, "tasks.api.scrum.sprint.list", nil).([]any); len(list) != 1 {
		t.Fatalf("a sprint with wrong fields is left: %v", list)
	}
	sp = obj(form(hook, "tasks.api.scrum.sprint.update", url.Values{"id": {spID}, "fields[name]": {"Спринт первый"}}))
	if sp["name"] != "Спринт первый" || sp["goal"] != "Оплата" {
		t.Fatalf("sprint.update: %v", sp)
	}

	// Tasks: in the backlog, then into the sprint with story points and the epic.
	newTask := func(title string) string {
		t.Helper()
		return str(form(hook, "tasks.task.add", url.Values{"fields[TITLE]": {title}, "fields[GROUP_ID]": {num(scNum)},
			"fields[RESPONSIBLE_ID]": {num(annaNum)}}).(map[string]any)["task"].(map[string]any)["id"])
	}
	t1, t2 := newTask("Форма оплаты"), newTask("Чек")
	st := obj(form(hook, "tasks.api.scrum.task.get", url.Values{"id": {t1}}))
	if str(st["entityId"]) != str(bl["id"]) || st["storyPoints"] != "" || str(st["epicId"]) != "0" {
		t.Fatalf("task.get in the backlog: %v", st)
	}
	form(hook, "tasks.api.scrum.task.update", url.Values{"id": {t1}, "fields[entityId]": {spID}, "fields[storyPoints]": {"3,5"},
		"fields[epicId]": {epID}, "fields[sort]": {"7"}})
	st = obj(form(hook, "tasks.api.scrum.task.get", url.Values{"id": {t1}}))
	if str(st["entityId"]) != spID || st["storyPoints"] != "3.5" || str(st["epicId"]) != epID || str(st["sort"]) != "7" {
		t.Fatalf("task.update: %v", st)
	}
	fail(hook, "tasks.api.scrum.task.update", url.Values{"id": {t1}, "fields[entityId]": {"999999"}})
	fail(hook, "tasks.api.scrum.task.update", url.Values{"id": {t1}, "fields[storyPoints]": {"много"}})

	// The kanban: stages of the group, addTask onto a stage of the active sprint, deleteTask back to the backlog.
	stages := form(hook, "tasks.api.scrum.kanban.getstages", url.Values{"sprintId": {spID}}).([]any)
	if len(stages) < 2 || obj(stages[0])["type"] != "NEW" || obj(stages[len(stages)-1])["type"] != "FINISH" ||
		str(obj(stages[0])["sprintId"]) != spID {
		t.Fatalf("kanban.getStages: %v", stages)
	}
	stageNum := str(form(hook, "tasks.api.scrum.kanban.addstage", url.Values{"fields[sprintId]": {spID}, "fields[name]": {"Ревью"},
		"fields[color]": {"#00a2e8"}}))
	form(hook, "tasks.api.scrum.kanban.updatestage", url.Values{"stageId": {stageNum}, "fields[name]": {"Code review"}})
	stages = form(hook, "tasks.api.scrum.kanban.getstages", url.Values{"sprintId": {spID}}).([]any)
	found := false
	for _, s := range stages {
		if str(obj(s)["id"]) == stageNum && obj(s)["name"] == "Code review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("kanban.addStage/updateStage: %v", stages)
	}
	sp = obj(form(hook, "tasks.api.scrum.sprint.start", url.Values{"id": {spID}}))
	if sp["status"] != "active" {
		t.Fatalf("sprint.start: %v", sp)
	}
	fail(hook, "tasks.api.scrum.sprint.start", url.Values{"id": {spID}})
	form(hook, "tasks.api.scrum.kanban.addtask", url.Values{"sprintId": {spID}, "taskId": {t2}, "stageId": {stageNum}})
	var stageOf string
	must(pool.QueryRow(ctx, `SELECT coalesce(ps.num::text, '') FROM tasks t LEFT JOIN project_stages ps ON ps.id = t.stage_id
		WHERE t.num::text = $1`, t2).Scan(&stageOf))
	if stageOf != stageNum {
		t.Fatalf("kanban.addTask: the stage %q, want %q", stageOf, stageNum)
	}
	if str(obj(form(hook, "tasks.api.scrum.task.get", url.Values{"id": {t2}}))["entityId"]) != spID {
		t.Fatal("kanban.addTask: not in the sprint")
	}
	form(hook, "tasks.api.scrum.kanban.deletetask", url.Values{"sprintId": {spID}, "taskId": {t2}})
	if str(obj(form(hook, "tasks.api.scrum.task.get", url.Values{"id": {t2}}))["entityId"]) != str(bl["id"]) {
		t.Fatal("kanban.deleteTask: not in the backlog")
	}
	fail(hook, "tasks.api.scrum.kanban.deletetask", url.Values{"sprintId": {spID}, "taskId": {t2}})

	// Complete: the unfinished task goes back to the backlog; a planned sprint is deleted.
	sp = obj(form(hook, "tasks.api.scrum.sprint.complete", url.Values{"id": {spID}}))
	if sp["status"] != "completed" {
		t.Fatalf("sprint.complete: %v", sp)
	}
	if str(obj(form(hook, "tasks.api.scrum.task.get", url.Values{"id": {t1}}))["entityId"]) != str(bl["id"]) {
		t.Fatal("sprint.complete: the unfinished task is not in the backlog")
	}
	sp2 := str(obj(form(hook, "tasks.api.scrum.sprint.add", url.Values{"fields[groupId]": {num(scNum)}, "fields[name]": {"Спринт 2"}}))["id"])
	form(hook, "tasks.api.scrum.sprint.delete", url.Values{"id": {sp2}})
	fail(hook, "tasks.api.scrum.sprint.get", url.Values{"id": {sp2}})
	form(hook, "tasks.api.scrum.epic.delete", url.Values{"id": {epID}})
	if st := obj(form(hook, "tasks.api.scrum.task.get", url.Values{"id": {t1}})); str(st["epicId"]) != "0" {
		t.Fatalf("epic.delete: the task keeps it: %v", st)
	}
}
