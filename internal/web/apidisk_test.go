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
	// «Документы»: office documents only; «Недавние» — those opened in the editor
	docx := upload("", "План.docx", "d", annaKey, 201)[0].ID
	var docs []apiDoc
	call("GET", "/api/v1/disk/docs", readOnly, nil, 200, &docs)
	if len(docs) != 1 || docs[0].ID != docx || docs[0].Drive != "my" || docs[0].Kind != "doc" || docs[0].Opened != nil {
		t.Fatalf("documents %+v", docs)
	}
	call("GET", "/api/v1/disk/docs?scope=recent", annaKey, nil, 200, &docs)
	if len(docs) != 0 {
		t.Fatalf("recent before opening %+v", docs)
	}
	must(srv.Drive.Opened(ctx, anna, docx))
	call("GET", "/api/v1/disk/docs?scope=recent&type=doc", annaKey, nil, 200, &docs)
	if len(docs) != 1 || docs[0].Opened == nil {
		t.Fatalf("recent %+v", docs)
	}
	call("GET", "/api/v1/disk/docs?type=sheet", annaKey, nil, 200, &docs)
	if len(docs) != 0 {
		t.Fatalf("spreadsheets %+v", docs)
	}
	_, err = pool.Exec(ctx, `DELETE FROM files WHERE id = $1`, docx) // not to be in the top folder and the trash below
	must(err)
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

	// the public link, as disk.file.getExternalLink
	fileLink, folderLink := "/api/v1/disk/drives/my/files/"+file+"/link", "/api/v1/disk/drives/my/folders/"+folder.ID+"/link"
	call("GET", fileLink, annaKey, nil, 404, nil)
	call("PUT", fileLink, readOnly, map[string]any{}, 403, nil)
	var link apiDiskLinkOut
	call("PUT", fileLink, annaKey, map[string]any{"password": "секрет", "expires": "2099-12-31", "edit": true}, 200, &link)
	if !strings.Contains(link.URL, "/s/") || !link.Password || !link.Edit || link.ExpiresAt == nil || link.ExpiresAt.Year() != 2099 {
		t.Fatalf("the link %+v", link)
	}
	var again apiDiskLinkOut
	call("GET", fileLink, readOnly, nil, 200, &again) // a read-only key of the one who writes there
	call("PUT", fileLink, annaKey, map[string]any{"clear_password": true}, 200, &again)
	if again.URL != link.URL || again.Password || again.Edit || again.ExpiresAt != nil {
		t.Fatalf("the link changed %+v", again)
	}
	call("PUT", fileLink, annaKey, map[string]any{"expires": "вчера"}, 422, nil)
	call("GET", "/api/v1/disk/drives/my/files/"+file+"/link", borisKey, nil, 404, nil) // not on his drive
	call("PUT", folderLink, annaKey, map[string]any{"edit": true}, 200, &link)
	if link.Edit || link.URL == again.URL {
		t.Fatalf("the folder's link %+v", link)
	}
	call("DELETE", fileLink, annaKey, nil, 204, nil)
	call("GET", fileLink, annaKey, nil, 404, nil)

	// «Общий доступ», as disk.folder.sharetouser
	fileShares := "/api/v1/disk/drives/my/files/" + file + "/shares"
	var shares []apiDiskShare
	call("POST", fileShares, annaKey, map[string]string{"whom": "user:" + boris, "access": "full"}, 404, nil) // a file: read or edit
	call("POST", fileShares, annaKey, map[string]string{"whom": "user:" + boris, "access": "edit"}, 200, &shares)
	if len(shares) != 1 || shares[0].Kind != "user" || shares[0].Whom.ID != boris || shares[0].Whom.Name != "Борис" || shares[0].Access != "edit" {
		t.Fatalf("shares %+v", shares)
	}
	call("GET", "/api/v1/disk/drives", borisKey, nil, 200, &drives)
	if last := drives[len(drives)-1]; last.ID != "sf:"+file || last.Access != "edit" {
		t.Fatalf("boris's drives %+v", drives)
	}
	call("POST", "/api/v1/disk/drives/my/folders/"+folder.ID+"/shares", annaKey, map[string]string{"whom": "user:" + boris, "access": "read"}, 200, &shares)
	if len(shares) != 1 || shares[0].Access != "read" {
		t.Fatalf("the folder's shares %+v", shares)
	}
	call("DELETE", "/api/v1/disk/drives/my/shares/"+shares[0].ID, annaKey, nil, 204, nil)
	call("GET", "/api/v1/disk/drives/my/folders/"+folder.ID+"/shares", annaKey, nil, 200, &shares)
	if len(shares) != 0 {
		t.Fatalf("unshared %+v", shares)
	}
	call("GET", "/api/v1/disk/drives/company/files/"+file+"/shares", annaKey, nil, 403, nil) // «Мой диск» only

	// «Обсуждение» of the file
	comments := "/api/v1/disk/files/" + file + "/comments"
	var cm struct{ ID string }
	call("POST", comments, annaKey, map[string]string{"body": "Проверь итоги"}, 201, &cm)
	call("POST", comments, borisKey, map[string]string{"body": "Проверил"}, 201, nil) // the file is shared with him
	call("POST", comments, annaKey, map[string]string{"body": " "}, 422, nil)
	var list2 []apiDiskComment
	call("GET", comments, readOnly, nil, 200, &list2)
	if len(list2) != 2 || list2[0].Body != "Проверь итоги" || list2[0].Author.ID != anna || list2[1].Author.Name != "Борис" {
		t.Fatalf("comments %+v", list2)
	}
	call("DELETE", comments+"/"+cm.ID, borisKey, nil, 404, nil) // not his
	call("DELETE", comments+"/"+cm.ID, annaKey, nil, 204, nil)
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'disk_comment'`, anna).Scan(&notes))
	if notes != 1 {
		t.Fatalf("anna heard of %d comments", notes)
	}
	call("DELETE", "/api/v1/disk/drives/my/shares/"+func() string {
		call("GET", fileShares, annaKey, nil, 200, &shares)
		return shares[0].ID
	}(), annaKey, nil, 204, nil)
	call("GET", comments, borisKey, nil, 403, nil)

	// a version brought back, as disk.file.restoreFromVersion
	call("POST", "/api/v1/disk/drives/my/files/"+file+"/versions/"+versions[1].ID+"/restore", readOnly, nil, 403, nil)
	call("POST", "/api/v1/disk/drives/my/files/"+file+"/versions/"+versions[1].ID+"/restore", annaKey, nil, 204, nil)
	req, _ = http.NewRequest("GET", hs.URL+"/api/v1/disk/files/"+file, nil)
	if got := send(req, annaKey, 200); string(got) != "1" {
		t.Fatalf("restored %q", got)
	}
	call("GET", "/api/v1/disk/files/"+file+"/versions", annaKey, nil, 200, &versions)
	if len(versions) != 3 || versions[0].Size != 1 {
		t.Fatalf("versions after the restore %+v", versions)
	}
	call("POST", "/api/v1/disk/drives/my/files/"+file+"/versions/"+anna+"/restore", annaKey, nil, 404, nil)

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
