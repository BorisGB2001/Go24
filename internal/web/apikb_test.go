package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/kb"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestKBAPI: «База знаний» through REST API v1 — bases made (empty and from a template), renamed, closed by «Права
// доступа»; pages written, changed (the history, a version compared and restored), moved in the tree and into
// another base, copied, found, put into the trash and restored; export and import; pictures uploaded and read; the
// key's scope and the base's rights are kept.
func TestKBAPI(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name) VALUES ($1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	anna, boris, eve := user("Анна"), user("Борис"), user("Ева")
	keys := apikeys.NewStore(pool)
	key := func(uid string, scopes ...string) string {
		k, err := keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	annaKey, readOnly, borisKey, eveKey := key(anna, "kb:read", "kb:write"), key(anna, "kb:read"), key(boris, "kb:read", "kb:write"),
		key(eve, "kb:read", "kb:write")
	noKB := key(anna, "disk:read")
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Drive: drive.NewStore(pool, ts), KB: kb.NewStore(pool), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s3ep := os.Getenv("GO24_TEST_S3")
	if s3ep != "" {
		s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
	}
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
	multi := func(path, token string, fields map[string]string, name string, data []byte, want int) []byte {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+path, &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		return send(req, token, want)
	}

	// a base for everyone: the others see it and write in it
	call("POST", "/api/v1/kb/spaces", readOnly, map[string]string{"name": "Регламенты"}, 403, nil)
	call("GET", "/api/v1/kb/spaces", noKB, nil, 403, nil)
	call("POST", "/api/v1/kb/spaces", borisKey, map[string]string{"name": "Регламенты", "project": "NOPE"}, 404, nil)
	call("POST", "/api/v1/kb/spaces", borisKey, map[string]any{"name": "Регламенты", "bogus": 1}, 422, nil)
	var sp apiKBSpace
	call("POST", "/api/v1/kb/spaces", borisKey, map[string]string{"name": "Регламенты", "description": "для всех"}, 201, &sp)
	if sp.ID == "" || !sp.CanManage || sp.Pages != 0 {
		t.Fatalf("space: %+v", sp)
	}
	base := "/api/v1/kb/spaces/" + sp.ID
	var spaces []apiKBSpace
	call("GET", "/api/v1/kb/spaces", annaKey, nil, 200, &spaces)
	if len(spaces) != 1 || spaces[0].Name != "Регламенты" || !spaces[0].CanEdit || spaces[0].CanManage {
		t.Fatalf("anna's spaces: %+v", spaces)
	}
	call("PATCH", base, annaKey, map[string]string{"name": "Чужая"}, 403, nil)
	call("PATCH", base, borisKey, map[string]string{"name": "Правила"}, 204, nil)

	// pages
	var top, sub apiKBPage
	call("POST", base+"/pages", readOnly, map[string]string{"title": "Отпуск"}, 403, nil)
	call("POST", base+"/pages", annaKey, map[string]string{"title": " "}, 422, nil)
	call("POST", base+"/pages", annaKey, map[string]string{"title": "Отпуск", "body": "за **две** недели"}, 201, &top)
	call("POST", base+"/pages", annaKey, map[string]string{"title": "Перенос", "body": "по заявлению", "parent": top.ID}, 201, &sub)
	if top.Author.ID != anna || top.Author.Name != "Анна" || !strings.Contains(top.HTML, "<b>две</b>") || !strings.HasSuffix(top.Link, "/kb/"+sp.ID+"/"+top.ID) {
		t.Fatalf("page: %+v", top)
	}
	if sub.Parent != top.ID || len(sub.Path) != 1 || sub.Path[0].Title != "Отпуск" {
		t.Fatalf("subpage: %+v", sub)
	}
	var got apiKBSpace
	call("GET", base, eveKey, nil, 200, &got)
	if got.Name != "Правила" || got.Description != "для всех" || got.Pages != 2 || len(got.Tree) != 1 || len(got.Tree[0].Children) != 1 || got.Tree[0].Children[0].ID != sub.ID {
		t.Fatalf("space tree: %+v", got)
	}
	// «Избранное» and «Следить»: reading is enough; the author watches the page
	var marked apiKBPage
	call("GET", "/api/v1/kb/pages/"+sub.ID, annaKey, nil, 200, &marked)
	if marked.Favorite || !marked.Watching {
		t.Fatalf("author's marks: %+v", marked)
	}
	call("PUT", "/api/v1/kb/pages/"+sub.ID+"/marks/favorite", readOnly, nil, 204, nil)
	call("PUT", base+"/marks/watch", eveKey, nil, 204, nil)
	call("PUT", base+"/marks/like", eveKey, nil, 404, nil)
	var favs []apiKBItem
	call("GET", "/api/v1/kb/favorites", annaKey, nil, 200, &favs)
	if len(favs) != 1 || favs[0].Page != sub.ID || favs[0].Title != "Перенос" || favs[0].SpaceName != "Правила" {
		t.Fatalf("favorites: %+v", favs)
	}
	call("GET", "/api/v1/kb/pages/"+top.ID, eveKey, nil, 200, &marked)
	if marked.Favorite || !marked.Watching {
		t.Fatalf("watched through the base: %+v", marked)
	}
	var marks struct {
		Favorite, Watching bool
		Pages              map[string][]string
	}
	call("GET", base+"/marks", annaKey, nil, 200, &marks)
	if marks.Favorite || marks.Watching || len(marks.Pages["favorite"]) != 1 || len(marks.Pages["watching"]) != 2 {
		t.Fatalf("marks of the base: %+v", marks)
	}
	call("DELETE", "/api/v1/kb/pages/"+sub.ID+"/marks/favorite", annaKey, nil, 204, nil)
	call("GET", "/api/v1/kb/favorites", annaKey, nil, 200, &favs)
	var recent []apiKBItem
	call("GET", "/api/v1/kb/recent", annaKey, nil, 200, &recent)
	if len(favs) != 0 || recent == nil {
		t.Fatalf("unstarred: %+v %+v", favs, recent)
	}
	call("DELETE", base+"/marks/watch", eveKey, nil, 204, nil)
	var pg apiKBPage
	call("GET", "/api/v1/kb/pages/"+top.ID, annaKey, nil, 200, &pg)
	if len(pg.Children) != 1 || pg.Children[0].Title != "Перенос" || len(pg.Path) != 0 {
		t.Fatalf("page children: %+v", pg)
	}

	// changes go to the history; a version is compared and restored
	call("PATCH", "/api/v1/kb/pages/"+top.ID, eveKey, map[string]string{"body": "за три недели"}, 200, &pg)
	if pg.Title != "Отпуск" || pg.Body != "за три недели" || pg.Editor.ID != eve || pg.Author.ID != anna {
		t.Fatalf("saved: %+v", pg)
	}
	call("PATCH", "/api/v1/kb/pages/"+top.ID, readOnly, map[string]string{"title": "x"}, 403, nil)
	var versions []apiKBVersion
	call("GET", "/api/v1/kb/pages/"+top.ID+"/versions", annaKey, nil, 200, &versions)
	if len(versions) != 1 || versions[0].Editor.ID != anna {
		t.Fatalf("versions: %+v", versions)
	}
	var old struct {
		apiKBVersion
		Diff []apiKBDiffLine
	}
	v := "/api/v1/kb/pages/" + top.ID + "/versions/" + strconv.FormatInt(versions[0].ID, 10)
	call("GET", v+"?diff=1", annaKey, nil, 200, &old)
	if old.Body != "за **две** недели" || len(old.Diff) != 3 || old.Diff[1] != (apiKBDiffLine{"-", "за **две** недели"}) || old.Diff[2] != (apiKBDiffLine{"+", "за три недели"}) {
		t.Fatalf("version: %+v", old)
	}
	call("GET", "/api/v1/kb/pages/"+top.ID+"/versions/x", annaKey, nil, 404, nil)
	call("POST", v+"/restore", annaKey, nil, 200, &pg)
	if pg.Body != "за **две** недели" {
		t.Fatalf("restored: %+v", pg)
	}

	// search
	var hits []apiKBHit
	call("GET", "/api/v1/kb/search?q=ЗАЯВЛ", eveKey, nil, 200, &hits)
	if len(hits) != 1 || hits[0].ID != sub.ID || hits[0].SpaceName != "Правила" {
		t.Fatalf("hits: %+v", hits)
	}
	call("GET", "/api/v1/kb/search?q=x&space=00000000-0000-0000-0000-000000000000", eveKey, nil, 404, nil)

	// the tree: to the top before the page, into another base with the pages under it, a copy
	call("POST", "/api/v1/kb/pages/"+sub.ID+"/move", annaKey, map[string]string{"before": top.ID}, 200, &pg)
	call("GET", base, annaKey, nil, 200, &got)
	if len(got.Tree) != 2 || got.Tree[0].ID != sub.ID || pg.Parent != "" {
		t.Fatalf("placed: %+v", got.Tree)
	}
	call("POST", "/api/v1/kb/pages/"+sub.ID+"/move", annaKey, map[string]string{"parent": top.ID}, 200, nil)
	var arch apiKBSpace
	call("POST", "/api/v1/kb/spaces", annaKey, map[string]string{"name": "Архив"}, 201, &arch)
	call("POST", "/api/v1/kb/pages/"+top.ID+"/move", annaKey, map[string]string{"space": arch.ID}, 200, &pg)
	call("GET", "/api/v1/kb/spaces/"+arch.ID, annaKey, nil, 200, &got)
	if pg.Space != arch.ID || got.Pages != 2 || len(got.Tree) != 1 || len(got.Tree[0].Children) != 1 {
		t.Fatalf("moved: %+v", got)
	}
	var cp apiKBPage
	call("POST", "/api/v1/kb/pages/"+top.ID+"/copy", annaKey, map[string]string{"title": "Отпуск 2027"}, 201, &cp)
	if cp.Title != "Отпуск 2027" || cp.Body != "за **две** недели" || len(cp.Children) != 0 {
		t.Fatalf("copy: %+v", cp)
	}
	call("POST", "/api/v1/kb/pages/"+sub.ID+"/copy", annaKey, nil, 201, &cp)
	if cp.Title == "" || cp.Parent != top.ID {
		t.Fatalf("copy without a title: %+v", cp)
	}

	// «Права доступа»: closed to Eve, only the managers see the lines
	ab := "/api/v1/kb/spaces/" + arch.ID
	call("GET", ab+"/rights", borisKey, nil, 403, nil)
	call("PUT", ab+"/rights", annaKey, map[string]string{"kind": "user", "whom_id": boris, "access": "read"}, 204, nil)
	call("PUT", ab+"/rights", annaKey, map[string]string{"kind": "user", "whom_id": boris, "access": "boss"}, 404, nil)
	var rights []apiKBRight
	call("GET", ab+"/rights", annaKey, nil, 200, &rights)
	if len(rights) != 1 || rights[0].WhomID != boris || rights[0].Access != "read" {
		t.Fatalf("rights: %+v", rights)
	}
	call("GET", ab, eveKey, nil, 404, nil)
	call("GET", "/api/v1/kb/pages/"+top.ID, eveKey, nil, 404, nil)
	call("GET", "/api/v1/kb/search?q=недел", eveKey, nil, 200, &hits)
	if len(hits) != 0 {
		t.Fatalf("eve finds a closed page: %+v", hits)
	}
	call("GET", "/api/v1/kb/pages/"+top.ID, borisKey, nil, 200, nil)
	call("PATCH", "/api/v1/kb/pages/"+top.ID, borisKey, map[string]string{"body": "x"}, 403, nil)
	call("DELETE", ab+"/rights/"+rights[0].ID, annaKey, nil, 204, nil)
	call("GET", ab, eveKey, nil, 200, nil)

	// templates
	var tpls []apiKBTemplate
	call("GET", "/api/v1/kb/templates", annaKey, nil, 200, &tpls)
	if len(tpls) < 4 || tpls[0].ID != "b:onboarding" || !tpls[0].Builtin || tpls[0].Pages == 0 {
		t.Fatalf("templates: %+v", tpls)
	}
	var faq apiKBSpace
	call("POST", "/api/v1/kb/spaces", annaKey, map[string]string{"name": "Вопросы", "template": "b:faq"}, 201, &faq)
	if faq.Pages == 0 || len(faq.Tree) == 0 {
		t.Fatalf("from a template: %+v", faq)
	}

	// the trash
	call("DELETE", "/api/v1/kb/pages/"+top.ID, readOnly, nil, 403, nil)
	call("DELETE", "/api/v1/kb/pages/"+top.ID, annaKey, nil, 204, nil)
	call("GET", "/api/v1/kb/pages/"+top.ID, annaKey, nil, 404, nil)
	call("DELETE", "/api/v1/kb/spaces/"+faq.ID, annaKey, nil, 204, nil)
	var trash []apiKBTrashed
	call("GET", "/api/v1/kb/trash", annaKey, nil, 200, &trash)
	if len(trash) != 2 {
		t.Fatalf("trash: %+v", trash)
	}
	call("POST", "/api/v1/kb/trash/restore", annaKey, map[string]string{"page": top.ID}, 204, nil)
	call("POST", "/api/v1/kb/trash/restore", annaKey, map[string]string{"space": faq.ID}, 204, nil)
	call("POST", "/api/v1/kb/trash/restore", annaKey, map[string]string{}, 404, nil)
	call("GET", "/api/v1/kb/pages/"+sub.ID, annaKey, nil, 200, nil)

	// export and import
	req, _ := http.NewRequest("GET", hs.URL+ab+"/export", nil)
	raw := send(req, annaKey, 200)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	must(err)
	if len(zr.File) < 3 {
		t.Fatalf("export: %d files", len(zr.File))
	}
	var imp map[string]int
	must(json.Unmarshal(multi(base+"/import", annaKey, nil, "архив.zip", raw, 201), &imp))
	if imp["pages"] != len(zr.File) {
		t.Fatalf("import: %+v of %d", imp, len(zr.File))
	}
	multi(base+"/import", readOnly, nil, "a.md", []byte("# A"), 403)
	must(json.Unmarshal(multi(ab+"/import", annaKey, map[string]string{"parent": sub.ID}, "a.md", []byte("# Приказ\nтекст"), 201), &imp))
	call("GET", "/api/v1/kb/pages/"+sub.ID, annaKey, nil, 200, &pg)
	if imp["pages"] != 1 || len(pg.Children) != 1 || pg.Children[0].Title != "Приказ" {
		t.Fatalf("import under a page: %+v %+v", imp, pg.Children)
	}

	// pictures of the pages
	if s3ep == "" {
		return
	}
	var f kbFile
	multi(ab+"/files", readOnly, nil, "схема.png", []byte("png"), 403)
	must(json.Unmarshal(multi(ab+"/files", annaKey, nil, "схема.png", []byte("png"), 201), &f))
	if f.ID == "" || !f.Image || f.MD != "![схема.png](/files/"+f.ID+"?inline=1)" {
		t.Fatalf("upload: %+v", f)
	}
	req, _ = http.NewRequest("GET", hs.URL+"/api/v1/kb/files/"+f.ID, nil)
	if got := send(req, borisKey, 200); string(got) != "png" {
		t.Fatalf("download: %q", got)
	}
	call("PUT", ab+"/rights", annaKey, map[string]string{"kind": "user", "whom_id": boris, "access": "read"}, 204, nil)
	req, _ = http.NewRequest("GET", hs.URL+"/api/v1/kb/files/"+f.ID, nil)
	send(req, eveKey, 404)
	req, _ = http.NewRequest("GET", hs.URL+"/api/v1/kb/files/00000000-0000-0000-0000-000000000000", nil)
	send(req, annaKey, 404)
}
