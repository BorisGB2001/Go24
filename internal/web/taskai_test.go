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
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestTaskAIDate: a deadline as the model writes it — a day is its end, a time is kept, anything else is an error.
func TestTaskAIDate(t *testing.T) {
	d, err := taskAIDate("2026-11-05")
	if err != nil || d.Format("2006-01-02 15:04") != "2026-11-05 23:59" {
		t.Fatalf("a day: %v %v", d, err)
	}
	if d, err = taskAIDate("2026-11-05 15:30"); err != nil || d.Format("15:04") != "15:30" {
		t.Fatalf("a time: %v %v", d, err)
	}
	if d, err = taskAIDate(" "); err != nil || d != nil {
		t.Fatalf("none: %v %v", d, err)
	}
	if _, err = taskAIDate("в пятницу"); err == nil {
		t.Fatal("words taken for a date")
	}
}

// TestTaskAgentHTTP drives CoPilot of tasks against the test database with a fake Anthropic: no AI connection — the
// pane says how to connect one; the task lists and the card have the button; «Итоги по задаче» — the model is told
// the open task and reads it with its chat; subtasks wait for «Подтвердить» and are made under the open task with the
// responsible and the deadline; a rejected deadline changes nothing; a checklist, a message and a status change go
// through; the agent does not read another's task nor make tasks where one may not; another sees neither the
// conversation nor the tasks; REST gives the conversations.
func TestTaskAgentHTTP(t *testing.T) {
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
	user := func(sub, name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	anna, ivan, eve := user("anna", "Анна Петрова"), user("ivan", "Иван Сидоров"), user("eve", "Ева")
	bundle, err := i18n.Load("")
	must(err)
	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	aiStore := ai.NewStore(pool, "secret")
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, I18n: bundle, AI: aiStore, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		CRM: crm.NewStore(pool), Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	_, err = ts.CreateProject(ctx, anna, "WEB", "Сайт", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "WEB")
	must(err)
	must(ts.SetMember(ctx, p, ivan, "member"))
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Лендинг акции", Description: "Страница осенней акции", Priority: 1})
	must(err)
	id, err := ts.TaskIDByKey(ctx, key)
	must(err)
	_, err = ts.PostComment(ctx, anna, id, "Договорились: макет к среде, тексты пишет Иван")
	must(err)
	_, err = ts.CreateProject(ctx, eve, "EVE", "Личное Евы", "")
	must(err)
	pe, err := ts.ProjectByKey(ctx, eve, "EVE")
	must(err)
	secret, err := ts.CreateTask(ctx, eve, pe, tasks.NewTask{Title: "Тайный план", Priority: 1})
	must(err)

	// the fake provider answers in turn; each request is kept
	var asked []string
	replies := []string{
		`{"type":"tool_use","id":"r1","name":"read_task","input":{"key":"` + key + `"}}`,
		`{"type":"text","text":"Макет — **к среде**, тексты пишет Иван."}`,
		`{"type":"tool_use","id":"p1","name":"find_people","input":{"query":"иван"}}`,
		`{"type":"tool_use","id":"c1","name":"create_task","input":{"title":"Макет","parent":"` + key + `","deadline":"2026-11-04","checklist":["- Шапка","2) Подвал"]}},` +
			`{"type":"tool_use","id":"c2","name":"create_task","input":{"title":"Тексты","parent":"` + key + `","responsible":"` + ivan + `","important":true}}`,
		`{"type":"text","text":"Подзадачи поставлены."}`,
		`{"type":"tool_use","id":"d1","name":"set_deadline","input":{"key":"` + key + `","deadline":"2026-11-10 18:00"}}`,
		`{"type":"text","text":"Хорошо, срок не трогаю."}`,
		`{"type":"tool_use","id":"k1","name":"add_checklist","input":{"key":"` + key + `","title":"Запуск","items":["Проверить формы","Включить счётчик"]}},` +
			`{"type":"tool_use","id":"m1","name":"add_comment","input":{"key":"` + key + `","text":"Подзадачи готовы"}},` +
			`{"type":"tool_use","id":"s1","name":"change_status","input":{"key":"` + key + `","action":"start"}}`,
		`{"type":"text","text":"Готово."}`,
		`{"type":"tool_use","id":"l1","name":"list_tasks","input":{"role":"any","status":"any"}}`,
		`{"type":"text","text":"Три задачи."}`,
	}
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		asked = append(asked, string(b))
		if len(asked) > len(replies) {
			http.Error(w, `{"error":{"message":"no more"}}`, http.StatusBadRequest)
			return
		}
		io.WriteString(w, `{"content":[`+replies[len(asked)-1]+`],"usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer prov.Close()

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, ivan, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "text/html")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, b)
		}
		return string(b)
	}

	for _, path := range []string{"/my", "/tasks", "/t/" + key} {
		if page := do(anna, "GET", path, nil, 200); !strings.Contains(page, "data-ai-toggle") || !strings.Contains(page, `data-ai-url="/tasks/agent"`) || !strings.Contains(page, "js/aipane.js") {
			t.Fatalf("%s has no CoPilot", path)
		}
	}
	if page := do(anna, "GET", "/t/"+key, nil, 200); !strings.Contains(page, `data-ai-task="`+key+`"`) {
		t.Fatal("the card does not give its task to CoPilot")
	}
	if pane := do(anna, "GET", "/tasks/agent", nil, 200); !strings.Contains(pane, "подключите нейросеть") || !strings.Contains(pane, `href="/settings/ai"`) || strings.Contains(pane, "data-ai-form") {
		t.Fatalf("CoPilot with no AI connection:\n%s", pane)
	}
	_, err = aiStore.Save(ctx, anna, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)
	pane := do(anna, "GET", "/tasks/agent", nil, 200)
	for _, want := range []string{"Я CoPilot", "data-ai-form", `data-ai-say="Что у меня горит`, `data-ai-say="Подведи итоги по открытой задаче`, `data-ai-open`, `data-ai-fill="Поставь задачу: "`} {
		if !strings.Contains(pane, want) {
			t.Fatalf("a new CoPilot pane has no %q:\n%s", want, pane)
		}
	}

	// «Итоги по задаче»: the open task is in the system, read with its chat
	pane = do(anna, "POST", "/tasks/agent", url.Values{"task": {key}, "text": {"Подведи итоги по открытой задаче"}}, 200)
	if len(asked) != 2 || !strings.Contains(asked[0], "Анна Петрова") || !strings.Contains(asked[0], key+` «Лендинг акции»`) || !strings.Contains(asked[0], `"name":"create_task"`) {
		t.Fatalf("asked %d:\n%.2000s", len(asked), asked[0])
	}
	if !strings.Contains(asked[1], `Страница осенней акции`) || !strings.Contains(asked[1], `макет к среде`) || !strings.Contains(asked[1], `\"actions\":[`) {
		t.Fatalf("the task read:\n%s", asked[1])
	}
	for _, want := range []string{"Подведи итоги по открытой задаче", "<b>к среде</b>", "Прочитал задачу " + key} {
		if !strings.Contains(pane, want) {
			t.Fatalf("the pane after a summary has no %q:\n%s", want, pane)
		}
	}
	run := regexp.MustCompile(`data-run="([^"]+)"`).FindStringSubmatch(pane)
	if run == nil || run[1] == "" {
		t.Fatalf("no run in the pane:\n%s", pane)
	}

	// subtasks wait for «Подтвердить», then are made under the task
	pane = do(anna, "POST", "/tasks/agent", url.Values{"run": {run[1]}, "task": {key}, "text": {"Разбей на подзадачи: макет мне к 4 ноября, тексты Ивану"}}, 200)
	acts := regexp.MustCompile(`data-ai-decide="([^"]+)" data-yes="1"`).FindAllStringSubmatch(pane, -1)
	if len(acts) != 2 || !strings.Contains(pane, "Поставить задачу") || !strings.Contains(pane, "Подзадача "+key) || !strings.Contains(pane, "Срок: 2026-11-04") ||
		!strings.Contains(pane, "☐ Шапка") || !strings.Contains(pane, "Ответственный: Иван Сидоров") || !strings.Contains(pane, "Важная задача") || !strings.Contains(pane, "Нашёл сотрудников") {
		t.Fatalf("the subtasks do not wait for a confirmation:\n%s", pane)
	}
	if !strings.Contains(asked[2], `Иван Сидоров`) && !strings.Contains(asked[3], `Иван Сидоров`) {
		t.Fatal("the people found were not given to the model")
	}
	var subs int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM tasks WHERE parent_id = $1`, id).Scan(&subs))
	if subs != 0 {
		t.Fatal("a subtask before «Подтвердить»")
	}
	if p := do(eve, "POST", "/tasks/agent/"+run[1]+"/"+acts[0][1], url.Values{"yes": {"1"}}, 200); strings.Contains(p, "Макет") {
		t.Fatal("another confirms one's action")
	}
	do(anna, "POST", "/tasks/agent/"+run[1]+"/"+acts[0][1], url.Values{"yes": {"1"}, "task": {key}}, 200)
	pane = do(anna, "POST", "/tasks/agent/"+run[1]+"/"+acts[1][1], url.Values{"yes": {"1"}, "task": {key}}, 200)
	if !strings.Contains(pane, "Подзадачи поставлены.") || strings.Contains(pane, `data-yes="1"`) || !strings.Contains(pane, `href="/t/WEB-2"`) {
		t.Fatalf("the pane after confirmed subtasks:\n%s", pane)
	}
	type sub struct {
		title, resp string
		prio        int
		deadline    *time.Time
		items       int
	}
	rows, err := pool.Query(ctx, `SELECT t.title, t.responsible_id::text, t.priority, t.deadline, (SELECT count(*) FROM task_checklist_items c WHERE c.task_id = t.id)
		FROM tasks t WHERE t.parent_id = $1 ORDER BY t.number`, id)
	must(err)
	var got []sub
	for rows.Next() {
		var x sub
		must(rows.Scan(&x.title, &x.resp, &x.prio, &x.deadline, &x.items))
		got = append(got, x)
	}
	must(rows.Err())
	if len(got) != 2 || got[0].title != "Макет" || got[0].resp != anna || got[0].deadline == nil || got[0].deadline.Local().Format("2006-01-02") != "2026-11-04" || got[0].items != 2 ||
		got[1].title != "Тексты" || got[1].resp != ivan || got[1].prio != 2 || got[1].deadline != nil {
		t.Fatalf("subtasks %+v", got)
	}

	// a rejected deadline changes nothing, the model learns of it
	pane = do(anna, "POST", "/tasks/agent", url.Values{"run": {run[1]}, "text": {"Перенеси срок на 10-е"}}, 200)
	act := regexp.MustCompile(`data-ai-decide="([^"]+)" data-yes="0"`).FindStringSubmatch(pane)
	if act == nil || !strings.Contains(pane, "Изменить срок") || !strings.Contains(pane, key+" · 2026-11-10 18:00") {
		t.Fatalf("the deadline does not wait for a confirmation:\n%s", pane)
	}
	pane = do(anna, "POST", "/tasks/agent/"+run[1]+"/"+act[1], url.Values{"yes": {"0"}}, 200)
	var deadline *time.Time
	must(pool.QueryRow(ctx, `SELECT deadline FROM tasks WHERE id = $1`, id).Scan(&deadline))
	if deadline != nil || !strings.Contains(pane, "Отклонено") || !strings.Contains(pane, "срок не трогаю") {
		t.Fatalf("a rejected deadline: %v\n%s", deadline, pane)
	}
	if !strings.Contains(asked[6], `"tool_use_id":"d1"`) || !strings.Contains(asked[6], `"is_error":true`) {
		t.Fatalf("the model was not told of the rejection:\n%s", asked[6])
	}

	// a checklist, a message and the start of the work
	pane = do(anna, "POST", "/tasks/agent", url.Values{"run": {run[1]}, "task": {key}, "text": {"Добавь чек-лист запуска, напиши в чат и начни задачу"}}, 200)
	acts = regexp.MustCompile(`data-ai-decide="([^"]+)" data-yes="1"`).FindAllStringSubmatch(pane, -1)
	if len(acts) != 3 || !strings.Contains(pane, "☐ Включить счётчик") || !strings.Contains(pane, "Подзадачи готовы") || !strings.Contains(pane, key+" · Начать выполнение") {
		t.Fatalf("three actions do not wait:\n%s", pane)
	}
	for _, a := range acts {
		pane = do(anna, "POST", "/tasks/agent/"+run[1]+"/"+a[1], url.Values{"yes": {"1"}}, 200)
	}
	var status string
	var lists, comments int
	must(pool.QueryRow(ctx, `SELECT status, (SELECT count(*) FROM task_checklists l WHERE l.task_id = t.id AND l.title = 'Запуск'),
		(SELECT count(*) FROM task_comments c WHERE c.task_id = t.id AND c.body = 'Подзадачи готовы') FROM tasks t WHERE t.id = $1`, id).Scan(&status, &lists, &comments))
	if status != "in_progress" || lists != 1 || comments != 1 || !strings.Contains(pane, "Готово.") {
		t.Fatalf("after the actions: %s %d %d\n%s", status, lists, comments, pane)
	}

	// the agent works with the employee's rights
	if _, err := srv.taskAIRead(ctx, anna, json.RawMessage(`{"key":"`+secret+`"}`)); err == nil {
		t.Fatal("the agent of anna reads eve's task")
	}
	if _, err := srv.taskAICreate(ctx, anna, json.RawMessage(`{"title":"Чужое","project":"EVE"}`)); err == nil {
		t.Fatal("the agent of anna creates a task in eve's project")
	}
	if _, err := srv.taskAIComment(ctx, eve, json.RawMessage(`{"key":"`+key+`","text":"Привет"}`)); err == nil {
		t.Fatal("the agent of eve writes to anna's task")
	}
	if _, err := srv.taskAIStatus(ctx, anna, json.RawMessage(`{"key":"`+key+`","action":"delete"}`)); err == nil {
		t.Fatal("the agent does a status change it may not")
	}
	if out, err := srv.taskAIList(ctx, eve, json.RawMessage(`{"role":"any","status":"any"}`)); err != nil || strings.Contains(out, "Лендинг") || !strings.Contains(out, "Тайный план") {
		t.Fatalf("eve's tasks: %v %s", err, out)
	}
	if out, err := srv.taskAIProjects(ctx, ivan, nil); err != nil || !strings.Contains(out, `"key":"WEB"`) || strings.Contains(out, "EVE") {
		t.Fatalf("ivan's projects: %v %s", err, out)
	}
	if out, err := srv.taskAIList(ctx, ivan, json.RawMessage(`{}`)); err != nil || !strings.Contains(out, `"title":"Тексты"`) || strings.Contains(out, "Макет") {
		t.Fatalf("ivan's tasks: %v %s", err, out)
	}

	// REST: a new conversation, the past one with its actions; another's key does not see it
	api := func(who, method, path, body string, want int, out any) {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"tasks:read", "tasks:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, b)
		}
		if out != nil {
			must(json.Unmarshal(b, out))
		}
	}
	var got1 apiAIRun
	api(anna, "POST", "/api/v1/agents/tasks", `{"text":"Сколько задач?","task":"`+key+`"}`, 200, &got1)
	if got1.ID == "" || got1.ID == run[1] || got1.Status != ai.RunDone || len(got1.Messages) != 2 || got1.Messages[1].Text != "Три задачи." {
		t.Fatalf("REST: a new conversation %+v", got1)
	}
	api(anna, "GET", "/api/v1/agents/tasks/"+run[1], "", 200, &got1)
	if len(got1.Actions) != 8 || got1.Actions[2].Tool != "create_task" || got1.Actions[2].Status != ai.ActDone || got1.Actions[4].Status != ai.ActRejected {
		t.Fatalf("REST: the conversation %+v", got1.Actions)
	}
	var runs []apiAIRun
	api(anna, "GET", "/api/v1/agents/tasks", "", 200, &runs)
	if len(runs) != 2 {
		t.Fatalf("REST: conversations %+v", runs)
	}
	api(eve, "GET", "/api/v1/agents/tasks/"+run[1], "", 404, nil)

	// the past conversations: anna's, not eve's; a deleted one is gone
	if p := do(anna, "GET", "/tasks/agent", nil, 200); !strings.Contains(p, `data-ai-run="`+run[1]+`"`) {
		t.Fatalf("anna's conversations:\n%s", p)
	}
	if p := do(eve, "GET", "/tasks/agent?run="+run[1], nil, 200); strings.Contains(p, "Лендинг") || strings.Contains(p, run[1]) {
		t.Fatalf("eve sees anna's conversation:\n%s", p)
	}
	if p := do(anna, "POST", "/tasks/agent/"+run[1]+"/delete", url.Values{}, 200); strings.Contains(p, run[1]) || !strings.Contains(p, "Я CoPilot") {
		t.Fatalf("a deleted conversation:\n%s", p)
	}
}
