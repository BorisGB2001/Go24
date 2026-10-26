package web

import (
	"context"
	"io"
	"log/slog"
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
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskAttachHTTP: «Файл с Диска» — the picker's drives (with a folder shared with the user), a drive's file
// attached to a task card and to a new post (a copy of the same stored bytes), and no file the user may not read.
func TestDiskAttachHTTP(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	_, err = ts.CreateProject(ctx, anna, "AT", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "AT")
	must(err)
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Отчёт"})
	must(err)
	task, err := ts.TaskIDByKey(ctx, key)
	must(err)
	my, _ := srv.Drive.Open(ctx, anna, "my")
	shared, err := srv.Drive.CreateFolder(ctx, anna, my, "", "Для Ивана")
	must(err)
	addFile := func(folder, name string) string {
		id, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + name, Name: name, ContentType: "application/pdf", // empty: no stored bytes to copy
			UploadedBy: anna, Drive: "user", DriveOwner: anna, FolderID: folder})
		must(err)
		return id
	}
	report, private := addFile("", "отчёт.pdf"), addFile("", "личное.pdf")
	inShared := addFile(shared, "общий.pdf")
	must(srv.Drive.SetShare(ctx, anna, my, shared, "", "user", ivan, "read"))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), ivan: cookie(ivan)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		req.Header.Set("Accept", "application/json")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}

	if page := do(anna, "GET", "/t/"+key, nil, 200); !strings.Contains(page, `data-to="/t/`+key+`/files/drive"`) || !strings.Contains(page, "js/drivepick.js") {
		t.Error("the task card has no «Файл с Диска»")
	}
	if page := do(anna, "GET", "/feed", nil, 200); !strings.Contains(page, `data-input="drive_file"`) {
		t.Error("the feed's form has no «Файл с Диска»")
	}
	if pick := do(ivan, "GET", "/disk/pick?which=sh:"+shared, nil, 200); !strings.Contains(pick, `"which": "sh:`+shared+`"`) || !strings.Contains(pick, "общий.pdf") {
		t.Errorf("ivan's picker = %s", pick)
	}

	// a task: Anna attaches her file; the copy is the task's, with the same stored bytes
	do(anna, "POST", "/t/"+key+"/files/drive", url.Values{"file": {report}}, 200)
	var objKey, name string
	must(pool.QueryRow(ctx, `SELECT object_key, name FROM files WHERE task_id = $1`, task).Scan(&objKey, &name))
	if objKey == "k/отчёт.pdf" || name != "отчёт.pdf" {
		t.Errorf("the task's file = %s %s", objKey, name)
	}
	if page := do(anna, "GET", "/t/"+key, nil, 200); !strings.Contains(page, "отчёт.pdf") {
		t.Error("the card does not show the attached file")
	}
	do(anna, "POST", "/t/"+key+"/files/drive", url.Values{"file": {"bad"}}, 404)
	do(ivan, "POST", "/t/"+key+"/files/drive", url.Values{"file": {report}}, 403) // not his task

	// a post: Ivan attaches a file of the folder shared with him, not one of Anna's own
	do(ivan, "POST", "/feed", url.Values{"body": {"Смотрите"}, "drive_file": {private}}, 403)
	do(ivan, "POST", "/feed", url.Values{"body": {"Смотрите"}, "drive_file": {inShared}}, 201)
	var posted int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files f JOIN feed_posts p ON p.id = f.post_id WHERE f.name = 'общий.pdf' AND f.uploaded_by = $1`, ivan).Scan(&posted))
	if posted != 1 {
		t.Errorf("the post's files = %d", posted)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'disk_share'`, ivan).Scan(&notes))
	if notes != 1 {
		t.Errorf("ivan's notifications = %d", notes)
	}
	if bell := do(ivan, "GET", "/notifications", nil, 200); !strings.Contains(bell, "открыл(а) вам папку на Диске «Для Ивана»") {
		t.Error("the bell does not say the folder was shared")
	}
	var nid string
	must(pool.QueryRow(ctx, `SELECT id::text FROM notifications WHERE user_id = $1`, ivan).Scan(&nid))
	req, _ := http.NewRequest("GET", hs.URL+"/notifications/"+nid, nil)
	req.AddCookie(as[ivan])
	res, err := client.Do(req)
	must(err)
	res.Body.Close()
	if loc := res.Header.Get("Location"); loc != "/disk/sh/"+shared {
		t.Errorf("the notification leads to %q", loc)
	}
}
