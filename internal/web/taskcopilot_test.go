package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
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

// TestCopilotLines: the items of a model's answer lose their marks, headings and empty lines.
func TestCopilotLines(t *testing.T) {
	got := copilotLines("Шаги:\n1. Собрать требования\n2) **Согласовать** макет\n\n- [ ] Сверстать\n* Проверить\n## Итог\n• Выложить")
	want := []string{"Собрать требования", "Согласовать макет", "Сверстать", "Проверить", "Выложить"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("lines = %q", got)
	}
	if n := len(copilotLines(strings.Repeat("шаг\n", 50))); n != copilotItems {
		t.Fatalf("items = %d", n)
	}
}

// TestTaskCopilot: CoPilot in a task — with no AI connection the card has no buttons and the calls say ErrNone;
// connected, «CoPilot: чек-лист» adds a list «CoPilot» of the model's items (the title and description go to the
// model), «CoPilot: итоги» posts the summary of the chat to it; an empty chat is not summed up, an auditor makes no
// checklist; REST gives the items and the text.
func TestTaskCopilot(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, ivan := newUser("Анна"), newUser("Иван")

	var asked []string // the system prompt and the text of each call of the model
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			System   string `json:"system"`
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		asked = append(asked, in.System+"\n"+in.Messages[0].Content[0].Text)
		answer := "1. Собрать требования\n2. Сверстать страницу\n3. Выложить"
		if strings.Contains(in.System, "итоги") {
			answer = "Решили: макет утверждён. Иван верстает до пятницы."
		}
		if strings.Contains(in.System, "Напиши описание") {
			answer = "  Сверстать лендинг осенней акции и выложить его.\n"
		}
		if strings.Contains(in.System, "Исправь в описании") {
			answer = "Страница осенней акции."
		}
		json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": answer}},
			"usage": map[string]int{"input_tokens": 40, "output_tokens": 9}})
	}))
	defer prov.Close()

	bundle, err := i18n.Load("")
	must(err)
	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	aiStore := ai.NewStore(pool, "secret")
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, I18n: bundle, AI: aiStore, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		CRM: crm.NewStore(pool), Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()

	_, err = ts.CreateProject(ctx, anna, "CP", "Сайт", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "CP")
	must(err)
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Лендинг акции", Description: "Страница осенней акции", Priority: 1, Auditors: []string{ivan}})
	must(err)
	id, err := ts.TaskIDByKey(ctx, key)
	must(err)

	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(url.Values{}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	// no AI connection: no buttons, ErrNone
	if _, body := do(anna, "GET", "/t/"+key); strings.Contains(body, "/copilot/checklist") {
		t.Fatal("CoPilot offered with no AI connection")
	}
	if _, err := srv.taskCopilotChecklist(ctx, anna, id); !errors.Is(err, ai.ErrNone) {
		t.Fatalf("no connection: %v", err)
	}
	if _, body := do(anna, "GET", "/tasks/new"); strings.Contains(body, "data-copilot") {
		t.Fatal("CoPilot of the description offered with no AI connection")
	}
	if _, err := srv.taskCopilotText(ctx, anna, "write", "Лендинг", ""); !errors.Is(err, ai.ErrNone) {
		t.Fatalf("no connection: %v", err)
	}
	_, err = aiStore.Save(ctx, anna, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)

	// the checklist: offered, made of the title and the description, added as «CoPilot»
	if st, body := do(anna, "GET", "/t/"+key); st != http.StatusOK || !strings.Contains(body, `action="/t/`+key+`/copilot/checklist"`) ||
		strings.Contains(body, "/copilot/summary") {
		t.Fatalf("card: %d (summary offered for an empty chat?)", st)
	}
	if st, _ := do(anna, "POST", "/t/"+key+"/copilot/checklist"); st != http.StatusSeeOther {
		t.Fatalf("checklist: %d", st)
	}
	if len(asked) != 1 || !strings.Contains(asked[0], "Лендинг акции") || !strings.Contains(asked[0], "Страница осенней акции") {
		t.Fatalf("asked: %q", asked)
	}
	c, err := ts.GetCard(ctx, anna, id)
	must(err)
	if len(c.Checklists) != 1 || c.Checklists[0].Title != "CoPilot" || len(c.Checklist) != 3 || c.Checklist[1].Title != "Сверстать страницу" {
		t.Fatalf("checklist: %+v", c.Checklists)
	}
	// an auditor does not change the checklists
	if _, err := srv.taskCopilotChecklist(ctx, ivan, id); !errors.Is(err, tasks.ErrForbidden) {
		t.Fatalf("auditor: %v", err)
	}

	// the summary: nothing to sum up, then the chat goes to the model and the summary to the chat
	if _, err := srv.taskCopilotSummary(ctx, anna, id); err == nil {
		t.Fatal("an empty chat summed up")
	}
	must(ts.AddComment(ctx, anna, id, "Макет утверждаем?"))
	must(ts.AddComment(ctx, ivan, id, "Да, верстаю до пятницы"))
	if _, body := do(ivan, "GET", "/t/"+key); !strings.Contains(body, `action="/t/`+key+`/copilot/summary"`) {
		t.Fatal("no summary button")
	}
	if st, _ := do(ivan, "POST", "/t/"+key+"/copilot/summary"); st != http.StatusSeeOther {
		t.Fatalf("summary: %d", st)
	}
	last := asked[len(asked)-1]
	if !strings.Contains(last, "Анна: Макет утверждаем?") || !strings.Contains(last, "Иван: Да, верстаю до пятницы") {
		t.Fatalf("chat to the model: %q", last)
	}
	c, err = ts.GetCard(ctx, anna, id)
	must(err)
	if n := len(c.Comments); n != 3 || !strings.Contains(c.Comments[n-1].Body, "CoPilot") || !strings.Contains(c.Comments[n-1].Body, "Иван верстает до пятницы") ||
		c.Comments[n-1].AuthorID != ivan {
		t.Fatalf("comments: %+v", c.Comments)
	}

	// REST
	k, err := keys.Create(ctx, anna, "k", []string{"tasks:read", "tasks:write"}, time.Hour)
	must(err)
	call := func(path string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if st, out := call("/api/v1/tasks/" + key + "/copilot/checklist"); st != http.StatusOK || len(out["items"].([]any)) != 3 {
		t.Fatalf("REST checklist: %d %v", st, out)
	}
	if st, out := call("/api/v1/tasks/" + key + "/copilot/summary"); st != http.StatusOK || !strings.Contains(out["text"].(string), "макет утверждён") {
		t.Fatalf("REST summary: %d %v", st, out)
	}
	if st, _ := call("/api/v1/tasks/" + key + "/copilot/poem"); st != http.StatusNotFound {
		t.Fatalf("unknown: %d", st)
	}
	var agents []string
	rows, err := pool.Query(ctx, `SELECT agent FROM ai_usage`)
	must(err)
	for rows.Next() {
		var a string
		must(rows.Scan(&a))
		agents = append(agents, a)
	}
	if len(agents) != 4 || slices.ContainsFunc(agents, func(a string) bool { return a != "tasks" }) {
		t.Fatalf("usage: %q", agents)
	}

	// «CoPilot» of the description: in the new task, the card's «Изменить» and a template; the answer comes back as
	// JSON (the form decides where it goes), nothing is saved; mistakes are said in the answer
	for _, path := range []string{"/tasks/new", "/t/" + key, "/templates/new"} {
		if _, body := do(anna, "GET", path); !strings.Contains(body, `data-copilot="/tasks/copilot"`) || !strings.Contains(body, `data-copilot-mode="shorten"`) {
			t.Fatalf("%s: no CoPilot of the description", path)
		}
	}
	form := func(v url.Values) map[string]string {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
		req, _ := http.NewRequest(http.MethodPost, hs.URL+"/tasks/copilot", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var out map[string]string
		json.NewDecoder(res.Body).Decode(&out)
		return out
	}
	if out := form(url.Values{"mode": {"write"}, "title": {"Лендинг осенней акции"}}); out["text"] != "Сверстать лендинг осенней акции и выложить его." {
		t.Fatalf("write: %v", out)
	}
	if last := asked[len(asked)-1]; !strings.Contains(last, "Напиши описание") || !strings.Contains(last, "«Лендинг осенней акции»") {
		t.Fatalf("write asked: %q", last)
	}
	if out := form(url.Values{"mode": {"fix"}, "title": {"Лендинг"}, "text": {"Страница осеней акцыи"}}); out["text"] != "Страница осенней акции." {
		t.Fatalf("fix: %v", out)
	}
	if last := asked[len(asked)-1]; !strings.Contains(last, "Страница осеней акцыи") {
		t.Fatalf("fix asked: %q", last)
	}
	n := len(asked)
	if out := form(url.Values{"mode": {"shorten"}, "title": {"Лендинг"}, "text": {"  "}}); out["error"] == "" || out["text"] != "" {
		t.Fatalf("shorten nothing: %v", out)
	}
	if out := form(url.Values{"mode": {"write"}}); out["error"] == "" {
		t.Fatalf("write of nothing: %v", out)
	}
	if out := form(url.Values{"mode": {"poem"}, "text": {"x"}}); out["error"] == "" || len(asked) != n {
		t.Fatalf("unknown mode: %v (model asked %d)", out, len(asked)-n)
	}
	req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/v1/tasks/copilot/text", strings.NewReader(`{"mode":"write","title":"Лендинг"}`))
	req.Header.Set("Authorization", "Bearer "+k)
	res, err := http.DefaultClient.Do(req)
	must(err)
	var out map[string]string
	json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(out["text"], "Сверстать лендинг") {
		t.Fatalf("REST text: %d %v", res.StatusCode, out)
	}
	c, err = ts.GetCard(ctx, anna, id)
	must(err)
	if c.Description != "Страница осенней акции" {
		t.Fatalf("the description changed: %q", c.Description)
	}
}
