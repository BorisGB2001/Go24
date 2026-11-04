package web

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestChecklistFilesHTTP: a file goes into a checklist item from the computer, shows under the item and not among the
// task's attachments, is written to the history and given by task.checklistitem.getlist as ATTACHMENTS; it is
// detached back; an auditor neither attaches nor detaches, a stranger cannot read it.
func TestChecklistFilesHTTP(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna, auditor, stranger := newUser("Борис"), newUser("Анна"), newUser("Наблюдатель"), newUser("Посторонний")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, boss, "AA", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "AA")
	must(err)
	key, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Собрать отчёт", ResponsibleID: anna, Priority: 1,
		Auditors: []string{auditor}, Checklist: []string{"Цифры", "Графики"}})
	must(err)
	id, err := ts.TaskIDByKey(ctx, key)
	must(err)
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who, method, path, ct string, body io.Reader) (string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return string(b), res.Header.Get("Location")
	}
	upload := func(who, path, name string) string {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write([]byte("цифры за квартал"))
		_ = mw.Close()
		_, loc := send(who, "POST", path, mw.FormDataContentType(), &b)
		return loc
	}
	itemFiles := func() map[string][]storages.File {
		t.Helper()
		m, err := srv.Storages.ChecklistFiles(ctx, id)
		must(err)
		return m
	}
	c, err := ts.GetCard(ctx, boss, id)
	must(err)
	item := c.Checklists[0].Items[0].ID

	// an auditor does not attach: 403, no way back to the card
	if loc := upload(auditor, "/t/"+key+"/checklist/"+item+"/files", "чужое.txt"); loc != "" || len(itemFiles()) != 0 {
		t.Fatalf("an auditor attached: %q", loc)
	}
	if loc := upload(anna, "/t/"+key+"/checklist/"+item+"/files", "цифры.txt"); loc != "/t/"+key+"#checklist" {
		t.Fatalf("attach: %q", loc)
	}
	files := itemFiles()[item]
	if len(files) != 1 || files[0].Name != "цифры.txt" {
		t.Fatalf("item files: %+v", itemFiles())
	}
	file := files[0].ID
	if att, err := srv.Storages.TaskFiles(ctx, id); err != nil || len(att) != 0 {
		t.Fatalf("an item's file among the attachments: %v %v", att, err)
	}
	page, _ := send(anna, "GET", "/t/"+key, "", nil)
	for _, want := range []string{`class="ck-files"`, "цифры.txt", "/checklist/" + item + "/files/" + file + "/delete",
		`action="/t/` + key + `/checklist/` + item + `/files"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("card lacks %q", want)
		}
	}
	if page, _ := send(auditor, "GET", "/t/"+key, "", nil); !strings.Contains(page, "цифры.txt") || strings.Contains(page, "/files/"+file+"/delete") {
		t.Fatal("an auditor: sees the file, without detaching")
	}
	if body, _ := send(anna, "GET", "/files/"+file, "", nil); body != "цифры за квартал" {
		t.Fatalf("download: %q", body)
	}
	if body, _ := send(stranger, "GET", "/files/"+file, "", nil); body == "цифры за квартал" {
		t.Fatal("a stranger downloads an item's file")
	}
	var hist string
	must(pool.QueryRow(ctx, `SELECT new_value FROM task_history WHERE task_id = $1 AND field = 'checklist_file'`, id).Scan(&hist))
	if hist != "Цифры: цифры.txt" {
		t.Fatalf("history: %q", hist)
	}
	checks, err := srv.portalChecks(ctx, id, "")
	must(err)
	if a, ok := checks[0]["ATTACHMENTS"].(map[string]any); !ok || len(a) != 1 {
		t.Fatalf("ATTACHMENTS of the item: %v", checks[0]["ATTACHMENTS"])
	}
	if a, ok := checks[1]["ATTACHMENTS"].([]any); !ok || len(a) != 0 {
		t.Fatalf("ATTACHMENTS of another item: %v", checks[1]["ATTACHMENTS"])
	}
	if _, loc := send(anna, "POST", "/t/"+key+"/checklist/"+item+"/files/drive", "application/x-www-form-urlencoded",
		strings.NewReader(url.Values{"file": {file}}.Encode())); loc != "" && !strings.Contains(loc, "msg=") { // refused: 403 or a message
		t.Fatalf("an attachment taken as a drive's file: %q", loc)
	}

	other := c.Checklists[0].Items[1].ID // the file is not of that item
	_, _ = send(anna, "POST", "/t/"+key+"/checklist/"+other+"/files/"+file+"/delete", "", nil)
	_, _ = send(auditor, "POST", "/t/"+key+"/checklist/"+item+"/files/"+file+"/delete", "", nil)
	if len(itemFiles()[item]) != 1 {
		t.Fatal("detached through another item or by an auditor")
	}
	if _, loc := send(anna, "POST", "/t/"+key+"/checklist/"+item+"/files/"+file+"/delete", "", nil); loc != "/t/"+key+"#checklist" {
		t.Fatalf("detach: %q", loc)
	}
	if len(itemFiles()) != 0 {
		t.Fatal("the file stays in the item")
	}
	must(pool.QueryRow(ctx, `SELECT old_value FROM task_history WHERE task_id = $1 AND field = 'checklist_file' AND new_value = ''`, id).Scan(&hist))
	if hist != "Цифры: цифры.txt" {
		t.Fatalf("history of detaching: %q", hist)
	}

	// a deleted item leaves its files to the task
	_ = upload(anna, "/t/"+key+"/checklist/"+other+"/files", "графики.txt")
	must(ts.ChangeChecklistItem(ctx, anna, id, other, true))
	if att, err := srv.Storages.TaskFiles(ctx, id); err != nil || len(att) != 1 || att[0].Name != "графики.txt" {
		t.Fatalf("files of a deleted item: %v %v", att, err)
	}
}
