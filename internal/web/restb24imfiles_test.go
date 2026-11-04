package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
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
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/notify"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestRestPortalIMFiles (GO24DEV-937): im.notify.answer comments the notification's post; im.disk.file.save copies a file of the drive into «Сохранённые файлы» of the
// caller's «Мой диск» (a name met there is «name (2)»), another's file stays closed; the chat's file methods and
// im.chat.updateColor want Matrix and a chat; imbot.chat.* and the like are an application's only.
func TestRestPortalIMFiles(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id, num`).Scan(&anna, &annaNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id, num`).Scan(&ivan, &ivanNum))
	ts := tasks.NewStore(pool)
	bundle, err := i18n.Load("")
	must(err)
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	srv.BaseURL = hs.URL
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hookOf := func(id string, n int64) string {
		token, err := srv.Keys.Create(ctx, id, "n8n", apikeys.AllScopes, 0)
		must(err)
		return hs.URL + "/rest/" + num(n) + "/" + token + "/"
	}
	hook, ivanHook := hookOf(anna, annaNum), hookOf(ivan, ivanNum)
	call := func(u, method string, v url.Values, want int) map[string]any {
		t.Helper()
		res, err := http.Post(u+method, "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	obj := func(v any) map[string]any { return v.(map[string]any) }

	// a file at the top of Anna's drive
	st := obj(call(hook, "disk.storage.getlist", nil, 200)["result"].([]any)[0])
	file := obj(call(hook, "disk.folder.uploadfile", url.Values{"id": {portalStr(st["ROOT_OBJECT_ID"])}, "data[NAME]": {"план.txt"},
		"fileContent[0]": {"x.txt"}, "fileContent[1]": {base64.StdEncoding.EncodeToString([]byte("план"))}}, 200)["result"])

	// saved twice: «Сохранённые файлы», the second copy «план (2).txt»
	saved := obj(call(hook, "im.disk.file.save", url.Values{"FILE_ID": {portalStr(file["ID"])}}, 200)["result"])
	folder, copied := obj(saved["folder"]), obj(saved["file"])
	if folder["name"] != "Сохранённые файлы" || copied["name"] != "план.txt" || portalStr(copied["id"]) == portalStr(file["ID"]) {
		t.Fatalf("saved: %v", saved)
	}
	again := obj(obj(call(hook, "im.disk.file.save", url.Values{"FILE_ID": {portalStr(file["ID"])}}, 200)["result"])["file"])
	if again["name"] != "план (2).txt" {
		t.Errorf("saved again: %v", again)
	}
	var in int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files f JOIN drive_folders d ON d.id = f.folder_id
		WHERE d.name = 'Сохранённые файлы' AND d.owner_id = $1 AND f.deleted_at IS NULL`, anna).Scan(&in))
	if in != 2 {
		t.Errorf("in «Сохранённые файлы»: %d", in)
	}
	if e := call(ivanHook, "im.disk.file.save", url.Values{"FILE_ID": {portalStr(file["ID"])}}, 400); e["error"] != "ERROR_NOT_FOUND" { // not his to see
		t.Errorf("another's file: %v", e)
	}

	// im.notify.answer: the answer to a notification of a post is the post's comment by the one notified
	post, err := srv.Feed.CreatePost(ctx, ivan, "", "", "Кто идёт на обед?", false)
	must(err)
	var nPost, nBare int64
	must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, actor_id, kind, post_id) VALUES ($1, $2, 'post_comment', $3) RETURNING id`,
		anna, ivan, post).Scan(&nPost))
	must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body) VALUES ($1, $2, 'like', 'x') RETURNING id`,
		anna, ivan).Scan(&nBare))
	r := obj(call(hook, "im.notify.answer", url.Values{"NOTIFY_ID": {num(nPost)}, "ANSWER_TEXT": {"Я иду"}}, 200)["result"])
	if r["result_message"].([]any)[0] != "Ваш ответ отправлен" {
		t.Errorf("answered: %v", r)
	}
	var comment string
	must(pool.QueryRow(ctx, `SELECT body FROM feed_comments WHERE post_id = $1 AND author_id = $2`, post, anna).Scan(&comment))
	if comment != "Я иду" {
		t.Errorf("the comment: %q", comment)
	}
	for _, x := range []struct {
		hook string
		v    url.Values
		want int
		code string
	}{
		{hook, url.Values{"ANSWER_TEXT": {"да"}}, 400, "NOTIFY_ID_ERROR"},
		{hook, url.Values{"NOTIFY_ID": {num(nPost)}}, 400, "ANSWER_TEXT_ERROR"},
		{hook, url.Values{"NOTIFY_ID": {num(nBare)}, "ANSWER_TEXT": {"да"}}, 400, "NOTIFY_ANSWER_ERROR"},
		{ivanHook, url.Values{"NOTIFY_ID": {num(nPost)}, "ANSWER_TEXT": {"да"}}, 400, "ERROR_NOT_FOUND"}, // Anna's
	} {
		if e := call(x.hook, "im.notify.answer", x.v, x.want); e["error"] != x.code {
			t.Errorf("im.notify.answer %v: %v", x.v, e)
		}
	}

	// the chat's methods: without Matrix they are not there; with it, the chat must be known
	if e := call(hook, "im.disk.folder.get", url.Values{"CHAT_ID": {"5"}}, 404); e["error"] != "ERROR_METHOD_NOT_FOUND" {
		t.Errorf("no Matrix: %v", e)
	}
	srv.Matrix = &matrixsync.Service{DB: pool}
	if e := call(hook, "im.message.share", url.Values{"TYPE": {"TASK"}}, 400); e["error"] != "MESSAGE_ID_EMPTY" {
		t.Errorf("im.message.share without a message: %v", e)
	}
	for _, m := range []string{"im.disk.folder.get", "im.disk.file.commit", "im.disk.file.delete", "im.chat.updateColor"} {
		if e := call(hook, m, url.Values{}, 400); e["error"] != "DIALOG_ID_EMPTY" {
			t.Errorf("%s without a chat: %v", m, e)
		}
		if e := call(hook, m, url.Values{"CHAT_ID": {"77"}, "FILE_ID": {portalStr(file["ID"])}, "COLOR": {"RED"}}, 400); e["error"] != "ERROR_NOT_FOUND" {
			t.Errorf("%s of chat 77: %v", m, e)
		}
	}

	// a chat bound to an object: found by ENTITY_TYPE and ENTITY_ID only by its people
	_, err = pool.Exec(ctx, `INSERT INTO portal_chats (room_id, color, entity_type, entity_id) VALUES ('!x:go24', 'PINK', 'ORDER', '12')`)
	must(err)
	if r := call(hook, "im.chat.get", url.Values{"ENTITY_TYPE": {"ORDER"}, "ENTITY_ID": {"12"}}, 200); r["result"] != nil {
		t.Errorf("not in the chat: %v", r)
	}

	// the bots' methods: a key of an employee is not an application's
	for _, m := range []string{"imbot.chat.add", "imbot.chat.get", "imbot.chat.setManager", "imbot.chat.user.list", "imbot.dialog.get",
		"imbot.message.like", "imbot.chat.updateColor"} {
		if e := call(hook, m, url.Values{"CHAT_ID": {"1"}}, 401); e["error"] != "WRONG_AUTH_TYPE" {
			t.Errorf("%s by an employee's key: %v", m, e)
		}
	}

	// im.notify.read.list: the caller's notifications unread and read again, another's untouched
	var nIvan int64
	must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body) VALUES ($1, $2, 'like', 'y') RETURNING id`,
		ivan, anna).Scan(&nIvan))
	unread := func(id int64) (u bool) {
		must(pool.QueryRow(ctx, `SELECT read_at IS NULL FROM notifications WHERE id = $1`, id).Scan(&u))
		return
	}
	call(hook, "im.notify.read.list", url.Values{"IDS[]": {num(nPost), num(nIvan)}, "ACTION": {"N"}}, 200)
	if !unread(nPost) {
		t.Error("read.list N: still read")
	}
	call(hook, "im.notify.read.list", url.Values{"IDS[]": {num(nPost), num(nBare), num(nIvan)}}, 200)
	if unread(nPost) || unread(nBare) || !unread(nIvan) {
		t.Errorf("read.list Y: %v %v %v", unread(nPost), unread(nBare), unread(nIvan))
	}
	if e := call(hook, "im.notify.read.list", url.Values{}, 400); e["error"] != "PARAMS_ERROR" {
		t.Errorf("read.list without IDS: %v", e)
	}

	// im.notify.schema.get: the kinds by module, a kind turned off is off on the site
	_, err = pool.Exec(ctx, `UPDATE users SET notify_off = '{task_new}' WHERE id = $1`, anna)
	must(err)
	schema := obj(call(hook, "im.notify.schema.get", nil, 200)["result"])
	tk := obj(obj(obj(schema["tasks"])["NOTIFY"])["task_new"])
	if tk["SITE"] != false || obj(obj(obj(schema["tasks"])["NOTIFY"])["comment"])["SITE"] != true {
		t.Errorf("schema tasks: %v", schema["tasks"])
	}
	if _, ok := obj(obj(schema["im"])["NOTIFY"])["im_remind"]; !ok {
		t.Errorf("schema im: %v", schema["im"])
	}

	// im.smile.get: Unicode smileys in sets
	sm := obj(call(hook, "im.smile.get", nil, 200)["result"])
	if smiles := sm["SMILE"].([]any); len(sm["SETS"].([]any)) != 4 || obj(smiles[0])["TYPING"] != "😀" {
		t.Errorf("smiles: %v", sm["SETS"])
	}
}

// TestPortalFileNums: FILE_ID, UPLOAD_ID or DISK_ID, one or a list.
func TestPortalFileNums(t *testing.T) {
	for _, c := range []struct {
		in   map[string]any
		want string
	}{
		{map[string]any{"FILE_ID": "5"}, "5"},
		{map[string]any{"upload_id": []any{"5", "7"}}, "5,7"},
		{map[string]any{"DISK_ID": map[string]any{"0": "9"}}, "9"},
		{map[string]any{}, ""},
	} {
		if got := strings.Join(portalFileNums(c.in), ","); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
}
