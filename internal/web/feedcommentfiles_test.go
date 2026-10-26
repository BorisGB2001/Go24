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

// TestFeedCommentFilesHTTP: files in comments of the feed (GO24DEV-463) — a comment of a drive's file
// only, shown under the comment and not among the post's files, its post's audience notified with the file's name;
// no file the author may not read; the files go away with the comment; such a comment may lose its text.
func TestFeedCommentFilesHTTP(t *testing.T) {
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
	post, err := srv.Feed.CreatePost(ctx, anna, "", "", "Новость", false)
	must(err)
	addFile := func(owner, name string) string {
		id, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + name, Name: name, ContentType: "application/pdf",
			UploadedBy: owner, Drive: "user", DriveOwner: owner})
		must(err)
		return id
	}
	mine, annas := addFile(ivan, "смета.pdf"), addFile(anna, "личное.pdf")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), ivan: cookie(ivan)}
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
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}

	if page := do(ivan, "GET", "/feed", nil, 200); !strings.Contains(page, `<form class="fcomment-form" data-comment>`) ||
		strings.Count(page, `data-input="drive_file"`) < 2 {
		t.Error("the comment form has no files")
	}
	do(ivan, "POST", "/feed/"+post+"/comments", url.Values{"body": {" "}}, 422)
	do(ivan, "POST", "/feed/"+post+"/comments", url.Values{"drive_file": {annas}}, 403)
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM feed_comments`).Scan(&n))
	if n != 0 {
		t.Errorf("a refused comment stayed: %d", n)
	}

	raw := do(ivan, "POST", "/feed/"+post+"/comments", url.Values{"drive_file": {mine}}, 201)
	var v map[string]string
	must(json.Unmarshal([]byte(raw), &v))
	if !strings.Contains(v["html"], "смета.pdf") || !strings.Contains(v["html"], `class="ffile"`) {
		t.Errorf("the comment has no file: %s", v["html"])
	}
	var cid string
	must(pool.QueryRow(ctx, `SELECT id FROM feed_comments`).Scan(&cid))
	var note string
	must(pool.QueryRow(ctx, `SELECT body FROM notifications WHERE user_id = $1 AND kind = 'post_comment'`, anna).Scan(&note))
	if note != "📎 смета.pdf" {
		t.Errorf("notification = %q", note)
	}
	it, err := srv.Feed.Item(ctx, anna, post)
	must(err)
	if len(it.Post.Files) != 0 || len(it.Post.Comments) != 1 || len(it.Post.Comments[0].Files) != 1 {
		t.Errorf("post files %v, comments %+v", it.Post.Files, it.Post.Comments)
	}
	var fid string
	must(pool.QueryRow(ctx, `SELECT id FROM files WHERE feed_comment_id = $1`, cid).Scan(&fid))
	m, err := srv.Storages.FileMeta(ctx, fid)
	must(err)
	if m.PostID == nil || *m.PostID != post {
		t.Error("the comment's file is not of its post: its audience could not download it")
	}

	// a comment of files keeps them without its text; a comment of text only may not lose it
	do(ivan, "POST", "/feed/comments/"+cid+"/edit", url.Values{"body": {"Смета готова"}}, 200)
	if raw := do(ivan, "POST", "/feed/comments/"+cid+"/edit", url.Values{"body": {""}}, 200); !strings.Contains(raw, "смета.pdf") {
		t.Errorf("the edited comment lost its file: %s", raw)
	}
	raw = do(ivan, "POST", "/feed/"+post+"/comments", url.Values{"body": {"Текст"}}, 201)
	var plain string
	must(pool.QueryRow(ctx, `SELECT id FROM feed_comments WHERE body = 'Текст'`).Scan(&plain))
	do(ivan, "POST", "/feed/comments/"+plain+"/edit", url.Values{"body": {""}}, 422)

	do(ivan, "POST", "/feed/comments/"+cid+"/delete", url.Values{}, 200)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE id = $1`, fid).Scan(&n))
	if n != 0 {
		t.Error("the comment's file stayed after the comment")
	}
}
