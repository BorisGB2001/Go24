package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestRestPortalDisk: disk.storage.*, disk.folder.*, disk.file.* by numbers — the storages, a folder made, files uploaded
// with fileContent and by uploadUrl, a name taken, DOWNLOAD_URL without a key, versions, rename, copy, move to a
// project's drive, the trash and back, deleted for good; another person's drive stays closed.
func TestRestPortalDisk(t *testing.T) {
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
	var anna, ivan string
	var annaNum, ivanNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна Петрова', 'anna@go24.test')
		RETURNING id, num`).Scan(&anna, &annaNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван Сидоров', 'ivan@go24.test')
		RETURNING id, num`).Scan(&ivan, &ivanNum))
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "DSK", "Запуск", "")
	must(err)
	var projNum int64
	must(pool.QueryRow(ctx, `SELECT num FROM projects WHERE key = 'DSK'`).Scan(&projNum))

	bundle, err := i18n.Load("")
	must(err)
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	srv := &Server{DB: pool, Tasks: ts, CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool), Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	srv.BaseURL = hs.URL
	decode := func(res *http.Response, want int) map[string]any {
		t.Helper()
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", res.Request.URL.Path, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	call := func(path string, v url.Values, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+path, "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
		must(err)
		return decode(res, want)
	}
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hookOf := func(id string, n int64) string {
		token, err := srv.Keys.Create(ctx, id, "n8n", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	hook, ivanHook := hookOf(anna, annaNum), hookOf(ivan, ivanNum)
	form := func(method string, v url.Values) any { t.Helper(); return call(hook+method, v, 200)["result"] }
	obj := func(v any) map[string]any { return v.(map[string]any) }
	get := func(u string) string {
		t.Helper()
		res, err := http.Get(u)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 {
			t.Fatalf("GET %s: %d %s", u, res.StatusCode, raw)
		}
		return string(raw)
	}
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

	// storages: «Мой диск», «Общий диск», the project's
	list := form("disk.storage.getlist", nil).([]any)
	if len(list) != 3 || obj(list[0])["ENTITY_TYPE"] != "user" || obj(list[0])["ENTITY_ID"] != num(annaNum) ||
		obj(list[1])["ENTITY_TYPE"] != "common" || obj(list[2])["ENTITY_TYPE"] != "group" || obj(list[2])["ENTITY_ID"] != num(projNum) ||
		obj(list[2])["NAME"] != "Запуск" {
		t.Fatalf("disk.storage.getlist: %v", list)
	}
	my, group := obj(list[0]), obj(list[2])
	if l := form("disk.storage.getlist", url.Values{"filter[ENTITY_TYPE]": {"group"}}).([]any); len(l) != 1 {
		t.Errorf("filter ENTITY_TYPE: %v", l)
	}
	if again := obj(form("disk.storage.get", url.Values{"id": {portalStr(my["ID"])}})); again["ROOT_OBJECT_ID"] != my["ROOT_OBJECT_ID"] {
		t.Errorf("disk.storage.get: %v", again)
	}

	// a folder at the top, a file in it by fileContent, a name taken, generateUniqueName
	f := obj(form("disk.storage.addfolder", url.Values{"id": {portalStr(my["ID"])}, "data[NAME]": {"Отчёты"}}))
	if f["TYPE"] != "folder" || f["PARENT_ID"] != my["ROOT_OBJECT_ID"] || f["STORAGE_ID"] != my["ID"] || f["CREATED_BY"] != num(annaNum) {
		t.Fatalf("disk.storage.addfolder: %v", f)
	}
	folder := portalStr(f["ID"])
	file := obj(form("disk.folder.uploadfile", url.Values{"id": {folder}, "data[NAME]": {"итоги.txt"},
		"fileContent[0]": {"x.txt"}, "fileContent[1]": {b64("первая")}}))
	if file["NAME"] != "итоги.txt" || file["PARENT_ID"] != folder || file["SIZE"] != strconv.Itoa(len("первая")) ||
		file["GLOBAL_CONTENT_VERSION"] != "1" {
		t.Fatalf("disk.folder.uploadfile: %v", file)
	}
	id := portalStr(file["ID"])
	if get(portalStr(file["DOWNLOAD_URL"])) != "первая" {
		t.Error("DOWNLOAD_URL")
	}
	if e := call(hook+"disk.folder.uploadfile", url.Values{"id": {folder}, "data[NAME]": {"итоги.txt"}, "fileContent[1]": {b64("2")}}, 400); e["error"] != "DISK_OBJ_22000" {
		t.Errorf("a name taken: %v", e)
	}
	twin := obj(form("disk.folder.uploadfile", url.Values{"id": {folder}, "data[NAME]": {"итоги.txt"}, "fileContent[1]": {b64("2")},
		"generateUniqueName": {"Y"}}))
	if twin["NAME"] != "итоги (2).txt" {
		t.Errorf("generateUniqueName: %v", twin["NAME"])
	}

	// uploadUrl: no fileContent — the address, then the form's «file» there
	up := obj(form("disk.storage.uploadfile", url.Values{"id": {portalStr(my["ID"])}, "data[NAME]": {"план.txt"}}))
	if up["field"] != "file" || !strings.HasPrefix(portalStr(up["uploadUrl"]), hs.URL+"/rest/upload?token=") {
		t.Fatalf("uploadUrl: %v", up)
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "любое.txt")
	fw.Write([]byte("план"))
	mw.Close()
	res, err := http.Post(portalStr(up["uploadUrl"]), mw.FormDataContentType(), &buf)
	must(err)
	plan := obj(decode(res, 200)["result"])
	if plan["NAME"] != "план.txt" || plan["PARENT_ID"] != my["ROOT_OBJECT_ID"] {
		t.Fatalf("upload by uploadUrl: %v", plan)
	}
	res, err = http.Post(portalStr(up["uploadUrl"])+"x", mw.FormDataContentType(), strings.NewReader(""))
	must(err)
	decode(res, 400)

	// children: of the storage's top and of the folder; ROOT_OBJECT_ID is a folder too
	top := form("disk.storage.getchildren", url.Values{"id": {portalStr(my["ID"])}}).([]any)
	if len(top) != 2 || obj(top[0])["ID"] != folder || obj(top[1])["NAME"] != "план.txt" {
		t.Fatalf("disk.storage.getchildren: %v", top)
	}
	if same := form("disk.folder.getchildren", url.Values{"id": {portalStr(my["ROOT_OBJECT_ID"])}}).([]any); len(same) != 2 {
		t.Errorf("disk.folder.getchildren of ROOT_OBJECT_ID: %v", same)
	}
	in := form("disk.folder.getchildren", url.Values{"id": {folder}, "filter[%NAME]": {"(2)"}}).([]any)
	if len(in) != 1 || obj(in[0])["ID"] != twin["ID"] {
		t.Errorf("disk.folder.getchildren filter: %v", in)
	}
	if g := obj(form("disk.folder.get", url.Values{"id": {folder}})); g["NAME"] != "Отчёты" {
		t.Errorf("disk.folder.get: %v", g)
	}
	call(hook+"disk.file.get", url.Values{"id": {folder}}, 400) // a folder is not a file

	// versions: a new one by fileContent, the history, the first one back
	v2 := obj(form("disk.file.uploadversion", url.Values{"id": {id}, "fileContent[0]": {"x.txt"}, "fileContent[1]": {b64("вторая")}}))
	if v2["GLOBAL_CONTENT_VERSION"] != "2" || v2["ID"] != id || get(portalStr(v2["DOWNLOAD_URL"])) != "вторая" {
		t.Fatalf("disk.file.uploadversion: %v", v2)
	}
	vers := form("disk.file.getversions", url.Values{"id": {id}}).([]any)
	if len(vers) != 2 || obj(vers[0])["ID"] != "2" || get(portalStr(obj(vers[1])["DOWNLOAD_URL"])) != "первая" {
		t.Fatalf("disk.file.getversions: %v", vers)
	}
	form("disk.file.restorefromversion", url.Values{"id": {id}, "versionId": {"1"}})
	if g := obj(form("disk.file.get", url.Values{"id": {id}})); get(portalStr(g["DOWNLOAD_URL"])) != "первая" || g["GLOBAL_CONTENT_VERSION"] != "3" {
		t.Errorf("disk.file.restorefromversion: %v", g)
	}

	// rename, copy into the top, move the folder into the project's drive
	if r := obj(form("disk.file.rename", url.Values{"id": {id}, "newName": {"итоги года.txt"}})); r["NAME"] != "итоги года.txt" {
		t.Errorf("disk.file.rename: %v", r)
	}
	cp := obj(form("disk.file.copyto", url.Values{"id": {id}, "targetFolderId": {portalStr(my["ROOT_OBJECT_ID"])}}))
	if cp["ID"] == id || cp["NAME"] != "итоги года.txt" || cp["PARENT_ID"] != my["ROOT_OBJECT_ID"] || get(portalStr(cp["DOWNLOAD_URL"])) != "первая" {
		t.Errorf("disk.file.copyto: %v", cp)
	}
	mv := obj(form("disk.folder.moveto", url.Values{"id": {folder}, "targetFolderId": {portalStr(group["ROOT_OBJECT_ID"])}}))
	if mv["STORAGE_ID"] != group["ID"] || mv["PARENT_ID"] != group["ROOT_OBJECT_ID"] {
		t.Errorf("disk.folder.moveto: %v", mv)
	}
	if g := obj(form("disk.file.get", url.Values{"id": {id}})); g["STORAGE_ID"] != group["ID"] || g["PARENT_ID"] != folder {
		t.Errorf("a file of the moved folder: %v", g)
	}

	// the trash: markdeleted, restore; delete for good
	del := obj(form("disk.file.markdeleted", url.Values{"id": {portalStr(cp["ID"])}}))
	if del["DELETED_TYPE"] != "3" || del["DELETED_BY"] != num(annaNum) || del["DELETE_TIME"] == nil {
		t.Errorf("disk.file.markdeleted: %v", del)
	}
	call(hook+"disk.file.get", url.Values{"id": {portalStr(cp["ID"])}}, 400)
	if back := obj(form("disk.file.restore", url.Values{"id": {portalStr(cp["ID"])}})); back["DELETED_TYPE"] != "0" {
		t.Errorf("disk.file.restore: %v", back)
	}
	if form("disk.file.delete", url.Values{"id": {portalStr(cp["ID"])}}) != true {
		t.Error("disk.file.delete")
	}
	call(hook+"disk.file.restore", url.Values{"id": {portalStr(cp["ID"])}}, 400)

	// Ivan: not Anna's «Мой диск», not the project's drive he is not in
	call(ivanHook+"disk.file.get", url.Values{"id": {portalStr(plan["ID"])}}, 400)
	call(ivanHook+"disk.storage.get", url.Values{"id": {portalStr(group["ID"])}}, 400)
	call(ivanHook+"disk.folder.addsubfolder", url.Values{"id": {portalStr(my["ROOT_OBJECT_ID"])}, "data[NAME]": {"x"}}, 400)
	if l := call(ivanHook+"disk.storage.getlist", nil, 200)["result"].([]any); len(l) != 2 || obj(l[0])["ENTITY_ID"] != num(ivanNum) {
		t.Errorf("Ivan's storages: %v", l)
	}

	// tasks.task.files.attach: a copy of the drive's file goes into the task, by its number
	tk := obj(obj(form("tasks.task.add", url.Values{"fields[TITLE]": {"Итоги"}, "fields[GROUP_ID]": {num(projNum)}}))["task"])
	att := obj(form("tasks.task.files.attach", url.Values{"taskId": {portalStr(tk["id"])}, "fileId": {id}}))
	var attName string
	must(pool.QueryRow(ctx, `SELECT f.name FROM files f JOIN tasks t ON t.id = f.task_id WHERE t.num = $1 AND f.num = $2`,
		portalStr(tk["id"]), portalStr(att["attachmentId"])).Scan(&attName))
	if attName != obj(form("disk.file.get", url.Values{"id": {id}}))["NAME"] {
		t.Errorf("tasks.task.files.attach: %v %q", att, attName)
	}
	call(hook+"tasks.task.files.attach", url.Values{"taskId": {portalStr(tk["id"])}, "fileId": {folder}}, 400) // a folder

	// the folder for good with what it holds
	if form("disk.folder.deletetree", url.Values{"id": {folder}}) != true {
		t.Error("disk.folder.deletetree")
	}
	call(hook+"disk.file.get", url.Values{"id": {id}}, 400)
}
