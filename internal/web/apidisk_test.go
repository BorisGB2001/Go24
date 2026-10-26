package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskAPI: «Диск» through REST API v1, as disk.* — the drives, a folder made, files uploaded (the same
// name — a new version), listed, downloaded, renamed, locked, copied, moved, put into the trash and restored; the
// key's scope and the drive's rights are kept.
func TestDiskAPI(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	anna, boris := user("Анна"), user("Борис")
	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	annaKey, readOnly, borisKey := key(anna, "disk:read", "disk:write"), key(anna, "disk:read"), key(boris, "disk:read", "disk:write")
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	send := func(req *http.Request, token string, want int) []byte {
		t.Helper()
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", req.Method, req.URL.Path, res.StatusCode, want, raw)
		}
		return raw
	}
	call := func(method, path, token string, body any, want int, out any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		raw := send(req, token, want)
		if out != nil {
			must(json.Unmarshal(raw, out))
		}
	}
	upload := func(folder, name, text, token string, want int) []apiDiskFile {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		mw.WriteField("folder", folder)
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write([]byte(text))
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/api/v1/disk/drives/my/files", &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		var out []apiDiskFile
		if raw := send(req, token, want); want == http.StatusCreated {
			must(json.Unmarshal(raw, &out))
		}
		return out
	}
	type listing struct {
		Path    []apiDiskFolder
		Folders []apiDiskFolder
		Files   []apiDiskFile
	}
	list := func(folder string) listing {
		t.Helper()
		var l listing
		call("GET", "/api/v1/disk/drives/my?folder="+folder, annaKey, nil, 200, &l)
		return l
	}

	var drives []apiDrive
	call("GET", "/api/v1/disk/drives", annaKey, nil, 200, &drives)
	if len(drives) != 2 || drives[0].ID != "my" || !drives[0].CanWrite || drives[1].ID != "company" {
		t.Fatalf("drives %+v", drives)
	}
	call("POST", "/api/v1/disk/drives/my/folders", readOnly, map[string]string{"name": "Отчёты"}, 403, nil)
	var folder apiDiskFolder
	call("POST", "/api/v1/disk/drives/my/folders", annaKey, map[string]string{"name": "Отчёты"}, 201, &folder)

	up := upload(folder.ID, "итоги.txt", "1", annaKey, 201)
	if len(up) != 1 || up[0].Name != "итоги.txt" || up[0].Size != 1 {
		t.Fatalf("uploaded %+v", up)
	}
	file := up[0].ID
	if again := upload(folder.ID, "итоги.txt", "22", annaKey, 201); len(again) != 1 || again[0].ID != file || again[0].Size != 2 {
		t.Fatalf("uploaded again %+v", again)
	}
	upload(folder.ID, "x.txt", "1", readOnly, 403)
	var versions []apiVersion
	call("GET", "/api/v1/disk/files/"+file+"/versions", annaKey, nil, 200, &versions)
	if len(versions) != 2 || versions[0].Size != 2 {
		t.Fatalf("versions %+v", versions)
	}
	if l := list(folder.ID); len(l.Path) != 1 || l.Path[0].Name != "Отчёты" || len(l.Files) != 1 || l.Files[0].URL != "/api/v1/disk/files/"+file {
		t.Fatalf("the folder %+v", l)
	}
	req, _ := http.NewRequest("GET", hs.URL+"/api/v1/disk/files/"+file, nil)
	if got := send(req, readOnly, 200); string(got) != "22" {
		t.Fatalf("downloaded %q", got)
	}
	req, _ = http.NewRequest("GET", hs.URL+"/api/v1/disk/files/"+file, nil)
	send(req, borisKey, 403) // not his
	call("GET", "/api/v1/disk/drives/sh:"+folder.ID, borisKey, nil, 404, nil)
	call("GET", "/api/v1/disk/drives/NOPE", annaKey, nil, 404, nil)

	call("PATCH", "/api/v1/disk/drives/my/files/"+file, annaKey, map[string]string{"name": "итоги-2026.txt"}, 204, nil)
	call("PATCH", "/api/v1/disk/drives/my/folders/"+folder.ID, annaKey, map[string]string{"name": "Отчёты 2026"}, 204, nil)
	call("POST", "/api/v1/disk/drives/my/files/"+file+"/lock", annaKey, nil, 204, nil)
	if l := list(folder.ID); l.Path[0].Name != "Отчёты 2026" || l.Files[0].Name != "итоги-2026.txt" || l.Files[0].LockedBy == nil || l.Files[0].LockedBy.ID != anna {
		t.Fatalf("renamed and locked %+v", l)
	}
	call("DELETE", "/api/v1/disk/drives/my/files/"+file+"/lock", annaKey, nil, 204, nil)

	call("POST", "/api/v1/disk/drives/my/copy", annaKey, map[string]any{"files": []string{file}}, 204, nil)
	top := list("")
	if len(top.Folders) != 1 || len(top.Files) != 1 || top.Files[0].Name != "итоги-2026.txt" || top.Files[0].ID == file {
		t.Fatalf("copied to the top %+v", top)
	}
	call("POST", "/api/v1/disk/drives/my/move", annaKey, map[string]any{"files": []string{top.Files[0].ID}, "to": "company"}, 204, nil)
	if top = list(""); len(top.Files) != 0 {
		t.Fatalf("moved away %+v", top)
	}
	call("POST", "/api/v1/disk/drives/my/move", annaKey, map[string]any{"bogus": 1}, 422, nil)

	call("DELETE", "/api/v1/disk/drives/my/folders/"+folder.ID, annaKey, nil, 204, nil)
	var trash []apiTrashed
	call("GET", "/api/v1/disk/drives/my/trash", annaKey, nil, 200, &trash)
	if len(trash) != 1 || trash[0].Kind != "folder" || trash[0].DeletedBy.ID != anna {
		t.Fatalf("trash %+v", trash)
	}
	call("POST", "/api/v1/disk/drives/my/restore", annaKey, map[string]any{"folders": []string{folder.ID}}, 204, nil)
	if top = list(""); len(top.Folders) != 1 {
		t.Fatalf("restored %+v", top)
	}
	call("GET", "/api/v1/disk/drives", "go24_bad_key", nil, 401, nil)
}
