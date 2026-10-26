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
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestOwnDriveFolders: a file attached to a task from the computer gets a copy in «Загруженные файлы» of the uploader's
// drive (deleting the copy keeps the attachment); «Сохранить на Диск» puts a copy of someone else's attachment into
// «Сохранённые файлы»; who does not see the task saves nothing.
func TestOwnDriveFolders(t *testing.T) {
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
	var anna, petr, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "OD", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "OD")
	must(err)
	must(ts.SetMember(ctx, p, petr, "member"))
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Отчёт", ResponsibleID: petr})
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Notify: notify.NewStore(pool), I18n: bundle,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who string, req *http.Request) *http.Response {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req.AddCookie(rec.Result().Cookies()[0])
		req.Header.Set("Accept-Language", "ru-RU")
		res, err := client.Do(req)
		must(err)
		res.Body.Close()
		return res
	}
	// on the drive: the folder named so at the top of whose drive and the files in it
	inFolder := func(who, name string) []string {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT f.id::text FROM files f JOIN drive_folders d ON d.id = f.folder_id
			WHERE f.drive = 'user' AND f.drive_owner = $1 AND d.name = $2 AND d.parent_id IS NULL AND f.deleted_at IS NULL
			AND f.task_id IS NULL`, who, name)
		must(err)
		var ids []string
		for rows.Next() {
			var id string
			must(rows.Scan(&id))
			ids = append(ids, id)
		}
		return ids
	}

	// Анна attaches a file to the task from the computer
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "смета.txt")
	fw.Write([]byte("итого: 100"))
	mw.Close()
	req, _ := http.NewRequest("POST", hs.URL+"/files?task="+key, &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if res := send(anna, req); res.StatusCode != http.StatusSeeOther {
		t.Fatalf("upload: %d", res.StatusCode)
	}
	var attached string
	taskID, err := ts.TaskIDByKey(ctx, key)
	must(err)
	must(pool.QueryRow(ctx, `SELECT id::text FROM files WHERE task_id = $1`, taskID).Scan(&attached))
	up := inFolder(anna, "Загруженные файлы")
	if len(up) != 1 || up[0] == attached {
		t.Fatalf("«Загруженные файлы» of the uploader: %v (the attachment %s)", up, attached)
	}
	m, err := srv.Storages.FileMeta(ctx, up[0])
	must(err)
	if m.Name != "смета.txt" || m.Size != int64(len("итого: 100")) {
		t.Errorf("the copy: %+v", m)
	}
	my, _ := srv.Drive.Open(ctx, anna, "my")
	must(srv.Drive.DeleteFile(ctx, anna, my, up[0]))
	if _, err := srv.Storages.FileMeta(ctx, attached); err != nil {
		t.Errorf("the attachment is gone with its copy: %v", err)
	}

	// Пётр saves it to his drive; Ева, who does not see the task, cannot
	save := func(who string) *http.Response {
		req, _ := http.NewRequest("POST", hs.URL+"/files/"+attached+"/drive", strings.NewReader(url.Values{"back": {"/t/" + key + "#files"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return send(who, req)
	}
	res := save(petr)
	if loc := res.Header.Get("Location"); res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/t/"+key+"?msg=") || !strings.HasSuffix(loc, "#files") {
		t.Errorf("«Сохранить на Диск»: %d %q", res.StatusCode, loc)
	}
	if got := inFolder(petr, "Сохранённые файлы"); len(got) != 1 {
		t.Errorf("«Сохранённые файлы» of Пётр: %v", got)
	}
	if got := inFolder(petr, "Загруженные файлы"); len(got) != 0 {
		t.Errorf("saved files are not uploaded ones: %v", got)
	}
	save(petr) // the folder is not made twice
	var folders int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM drive_folders WHERE drive = 'user' AND owner_id = $1`, petr).Scan(&folders))
	if folders != 1 || len(inFolder(petr, "Сохранённые файлы")) != 2 {
		t.Errorf("Пётр's drive after saving twice: %d folders", folders)
	}
	if res := save(eve); res.StatusCode == http.StatusSeeOther && !strings.Contains(res.Header.Get("Location"), "err") {
		t.Errorf("Ева saved a file of a task she does not see: %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	if got := inFolder(eve, "Сохранённые файлы"); len(got) != 0 {
		t.Errorf("Ева's drive: %v", got)
	}
}
