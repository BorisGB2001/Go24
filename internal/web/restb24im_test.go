package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestRestPortalNotify: im.notify.system.add and im.notify.personal.add put a notification of the integration in the
// employee's bell (BB codes dropped), one with the same TAG replaces the earlier one, a kind turned off is not written,
// im.notify.delete takes back the caller's own; a chat number unknown and a message number unknown are not found.
func TestRestPortalNotify(t *testing.T) {
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
	var boss, anna string
	var bossNum, annaNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Борис Гусев', 'boss@go24.test') RETURNING id, num`).Scan(&boss, &bossNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id, num`).Scan(&anna, &annaNum))

	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Notify: notify.NewStore(pool), I18n: bundle,
		BaseURL: "https://go24.example", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	key, err := srv.Keys.Create(ctx, boss, "ключ", apikeys.AllScopes, 0)
	must(err)
	readKey, err := srv.Keys.Create(ctx, boss, "чтение", []string{"im:read"}, 0)
	must(err)
	call := func(key, method, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/rest/"+strconv.FormatInt(bossNum, 10)+"/"+key+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
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
	bell := func() []notify.Item {
		t.Helper()
		list, err := srv.Notify.List(ctx, anna, 0, 50)
		must(err)
		return list
	}
	text := func(i notify.Item) string {
		return notify.Text(i, func(k string, _ ...any) string { return k }, nil)
	}

	// a system one: no author, the BB codes dropped; a personal one: from the caller
	sys := call(key, "im.notify.system.add", `{"USER_ID": `+strconv.FormatInt(annaNum, 10)+`, "MESSAGE": "Счёт [b]№5[/b] оплачен", "TAG": "inv5"}`, 200)
	if _, ok := sys["result"].(float64); !ok {
		t.Fatalf("im.notify.system.add: %v", sys)
	}
	call(key, "im.notify.personal.add", `{"USER_ID": "`+strconv.FormatInt(annaNum, 10)+`", "MESSAGE": "Зайди ко мне"}`, 200)
	list := bell()
	if len(list) != 2 || list[1].Kind != "app" || text(list[1]) != "🔔 Счёт №5 оплачен" || list[0].Kind != "app_personal" ||
		text(list[0]) != "Борис Гусев: Зайди ко мне" {
		t.Fatalf("the bell: %+v", list)
	}
	// the same tag replaces it
	call(key, "im.notify.system.add", `{"USER_ID": `+strconv.FormatInt(annaNum, 10)+`, "MESSAGE": "Счёт №5 отгружен", "TAG": "inv5"}`, 200)
	if list = bell(); len(list) != 2 || list[0].Body != "Счёт №5 отгружен" {
		t.Fatalf("a tag replaces: %+v", list)
	}
	// errors: no employee, no text, a key without im:write
	if e := call(key, "im.notify.system.add", `{"MESSAGE": "x"}`, 400); e["error"] != "USER_ID_EMPTY" {
		t.Errorf("no USER_ID: %v", e)
	}
	if e := call(key, "im.notify.personal.add", `{"USER_ID": `+strconv.FormatInt(annaNum, 10)+`, "MESSAGE": " "}`, 400); e["error"] != "MESSAGE_EMPTY" {
		t.Errorf("no MESSAGE: %v", e)
	}
	if e := call(readKey, "im.notify", `{"USER_ID": 1, "MESSAGE": "x"}`, 401); e["error"] != "insufficient_scope" {
		t.Errorf("im:read only: %v", e)
	}
	// turned off — not written, false
	_, err = pool.Exec(ctx, `UPDATE users SET notify_off = '{app}' WHERE id = $1`, anna)
	must(err)
	if r := call(key, "im.notify.system.add", `{"USER_ID": `+strconv.FormatInt(annaNum, 10)+`, "MESSAGE": "y"}`, 200); r["result"] != false {
		t.Errorf("turned off: %v", r)
	}
	// delete: by tag, by number
	if r := call(key, "im.notify.delete", `{"TAG": "inv5"}`, 200); r["result"] != true {
		t.Errorf("delete by tag: %v", r)
	}
	if list = bell(); len(list) != 1 {
		t.Fatalf("after delete by tag: %+v", list)
	}
	if r := call(key, "im.notify.delete", `{"ID": `+strconv.FormatInt(list[0].ID, 10)+`}`, 200); r["result"] != true || len(bell()) != 0 {
		t.Errorf("delete by id: %v", r)
	}

	// the messenger: without Matrix the methods are not there; with it, chatN and MESSAGE_ID unknown are not found
	if e := call(key, "im.message.add", `{"DIALOG_ID": "chat1", "MESSAGE": "x"}`, 404); e["error"] != "ERROR_METHOD_NOT_FOUND" {
		t.Errorf("no Matrix: %v", e)
	}
	srv.Matrix = &matrixsync.Service{DB: pool}
	if e := call(key, "im.message.add", `{"DIALOG_ID": "chat77", "MESSAGE": "x"}`, 400); e["error"] != "ERROR_NOT_FOUND" {
		t.Errorf("chat77: %v", e)
	}
	if e := call(key, "im.message.add", `{"MESSAGE": "x"}`, 400); e["error"] != "DIALOG_ID_EMPTY" {
		t.Errorf("no DIALOG_ID: %v", e)
	}
	if e := call(key, "im.message.delete", `{"MESSAGE_ID": 5}`, 400); e["error"] != "ERROR_NOT_FOUND" {
		t.Errorf("message 5: %v", e)
	}
	// numbers: given once, the same for the same room and event
	a, err := srv.portalChatNum(ctx, "!a:go24.test")
	must(err)
	b, err := srv.portalChatNum(ctx, "!b:go24.test")
	must(err)
	if again, _ := srv.portalChatNum(ctx, "!a:go24.test"); again != a || b == a {
		t.Errorf("chat numbers: %d %d %d", a, b, again)
	}
	m, err := srv.portalMessageNum(ctx, "!a:go24.test", "$e1")
	must(err)
	if again, _ := srv.portalMessageNum(ctx, "!a:go24.test", "$e1"); again != m {
		t.Errorf("message numbers: %d %d", m, again)
	}
}

// TestPortalBefore: the position of a message among those people wrote (lines about the chat are left out).
func TestPortalBefore(t *testing.T) {
	msgs := portalSaid([]matrixsync.Message{{ID: "$1"}, {ID: "$2", Event: "renamed"}, {ID: "$3"}, {ID: "$4", Change: "edit"}})
	if len(msgs) != 2 || portalBefore(msgs, "$3") != 1 || portalBefore(msgs, "$2") != 2 || portalBefore(msgs, "") != 2 {
		t.Errorf("portalSaid/portalBefore: %+v", msgs)
	}
}
