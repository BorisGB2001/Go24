package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/ai"
	"go24/internal/calls"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestMeetingSummary: with no AI connection the task of the call is told the meeting could not be summed up and why;
// with one, the AI of the one who recorded reads the transcript, the task gets the summary — agreements, tasks with
// «Создать задачу» (a subtask of the call's task), the next meeting — and the file beside the recording; the tokens
// go to the usage. An answer that is not JSON is told as it is.
func TestMeetingSummary(t *testing.T) {
	dbURL, s3ep := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_S3")
	if dbURL == "" || s3ep == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_S3 not set")
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
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "CR", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "CR")
	must(err)
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Планёрка", ResponsibleID: anna})
	must(err)
	taskID, err := ts.TaskIDByKey(ctx, key)
	must(err)

	answer := "```json\n" + `{"summary": "Обсудили запуск сайта.", "agreements": ["Запуск 1 ноября", " "],
		"tasks": [{"title": "Подготовить тексты", "who": "Анна", "due": "пятница"}, {"title": "Проверить формы", "who": "", "due": ""}],
		"next": "Через неделю"}` + "\n```"
	var asked []string
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
		json.NewEncoder(w).Encode(map[string]any{"content": []any{map[string]any{"type": "text", "text": answer}},
			"usage": map[string]int{"input_tokens": 70, "output_tokens": 20}})
	}))
	defer prov.Close()

	bundle, err := i18n.Load("")
	must(err)
	aiStore := ai.NewStore(pool, "secret")
	sts := storages.NewStore(pool, nil, s3)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: sts, I18n: bundle, AI: aiStore,
		Recordings: &calls.Recordings{DB: pool}, BaseURL: "http://go24", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// the recording of the task's call, saved on Анна's Drive
	must(sts.Put(ctx, anna, false, "", "rec/1.mp4", strings.NewReader("mp4"), 3, "video/mp4"))
	fileID, err := sts.AddFile(ctx, storages.NewFile{Key: "rec/1.mp4", Name: "CR-1 Планёрка 2026-10-28.mp4", ContentType: "video/mp4",
		Size: 3, UploadedBy: anna, Drive: "user", DriveOwner: anna, TaskID: taskID})
	must(err)
	rec, err := srv.Recordings.Add(ctx, calls.Recording{Room: "task-" + key, Title: key + " Планёрка", Query: "task=" + key, EgressID: "EG_S", StartedBy: anna})
	must(err)
	must(srv.Recordings.Finish(ctx, rec.ID, fileID, "", false))
	lastComment := func() string {
		t.Helper()
		var c string
		must(pool.QueryRow(ctx, `SELECT body FROM task_comments WHERE task_id = $1 ORDER BY created_at DESC LIMIT 1`, taskID).Scan(&c))
		return c
	}
	transcript := "Расшифровка\n\n[00:01] Запускаем сайт первого ноября.\n"

	if srv.summaryOn(ctx) {
		t.Fatal("summing up is on before the administrators let it")
	}
	must(srv.Recordings.SetSummarizing(ctx, anna, true))
	if !srv.summaryOn(ctx) {
		t.Fatal("the setting is not read")
	}

	// no AI connection: told why
	srv.summarizeMeeting(ctx, rec, fileID, transcript)
	if c := lastComment(); c != "Не удалось подвести итоги встречи «CR-1 Планёрка»: Нет подключения ИИ: добавьте его в «Настройки → Подключения ИИ»" {
		t.Fatalf("no connection told %q", c)
	}

	_, err = aiStore.Save(ctx, anna, ai.Connection{Name: "Claude", Provider: "anthropic", BaseURL: prov.URL, Model: "claude-x"}, "k")
	must(err)
	srv.summarizeMeeting(ctx, rec, fileID, transcript)
	if len(asked) != 1 || !strings.Contains(asked[0], "CoPilot") || !strings.Contains(asked[0], "первого ноября") {
		t.Fatalf("asked %q", asked)
	}
	c := lastComment()
	newTask := "[url=http://go24/tasks/new?parent=" + key + "&title=%D0%9F%D0%BE%D0%B4%D0%B3%D0%BE%D1%82%D0%BE%D0%B2%D0%B8%D1%82%D1%8C+%D1%82%D0%B5%D0%BA%D1%81%D1%82%D1%8B]Создать задачу[/url]"
	for _, want := range []string{"[b]Итоги встречи «CR-1 Планёрка», ", "\n\nОбсудили запуск сайта.\n", "[b]Договорённости[/b]\n• Запуск 1 ноября\n\n[b]",
		"[b]Задачи[/b]\n• Подготовить тексты — Анна, срок: пятница · " + newTask + "\n• Проверить формы · [url=",
		"[b]Следующая встреча[/b]\nЧерез неделю\n\nФайл: CR-1 Планёрка 2026-10-28 — итоги встречи.txt — http://go24/files/"} {
		if !strings.Contains(c, want) {
			t.Fatalf("the task is told %q, no %q", c, want)
		}
	}
	var summary, fname, folder, ftask, fkey string
	must(pool.QueryRow(ctx, `SELECT c.summary, f.name, fo.name, f.task_id::text, f.object_key FROM call_recordings c
		JOIN files f ON f.id = c.summary_file JOIN drive_folders fo ON fo.id = f.folder_id WHERE c.id = $1`, rec.ID).
		Scan(&summary, &fname, &folder, &ftask, &fkey))
	if fname != "CR-1 Планёрка 2026-10-28 — итоги встречи.txt" || folder != "Записи звонков" || ftask != taskID ||
		strings.Contains(summary, "[b]") || strings.Contains(summary, "[url") || !strings.Contains(summary, "Договорённости\n• Запуск 1 ноября") {
		t.Fatalf("the summary kept: %q %q %q %s", summary, fname, folder, ftask)
	}
	obj, err := sts.Open(ctx, nil, fkey)
	must(err)
	body, _ := io.ReadAll(obj)
	obj.Close()
	if string(body) != summary {
		t.Fatalf("the file %q", body)
	}
	var used int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage WHERE user_id = $1 AND agent = 'calls' AND in_tokens = 70`, anna).Scan(&used))
	if used != 1 {
		t.Fatalf("usage %d", used)
	}

	// not JSON: told as it is
	answer = "Встреча короткая, договорились созвониться."
	srv.summarizeMeeting(ctx, rec, fileID, transcript)
	if c := lastComment(); !strings.HasPrefix(c, "[b]Итоги встречи «CR-1 Планёрка», ") || !strings.Contains(c, "[/b]\n\nВстреча короткая, договорились созвониться.\n\nФайл: ") {
		t.Fatalf("a plain answer told %q", c)
	}
}

// TestParseSummary: the JSON is found in a fence or among words; an empty or broken one is not a summary.
func TestParseSummary(t *testing.T) {
	sm, ok := parseSummary("Вот итоги:\n```json\n{\"summary\":\"Коротко\",\"tasks\":[{\"title\":\"Сделать\"}]}\n```")
	if !ok || sm.Summary != "Коротко" || len(sm.Tasks) != 1 || sm.Tasks[0].Title != "Сделать" {
		t.Fatalf("%v %+v", ok, sm)
	}
	for _, s := range []string{"просто текст", "{\"summary\": \"\"}", "{\"summary\": \"x\""} {
		if _, ok := parseSummary(s); ok {
			t.Fatalf("%q read as a summary", s)
		}
	}
}
