package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/matrix"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// TestRestPortalBotsV2 (GO24DEV-954): imbot.v2.Bot.* — register (again by the same code — the same bot), get by code,
// list with a filter, update, a «webhook» bot's address subscribed to ONIMBOTV2*; imbot.v2.Command.*; Event.get by
// offset; the errors of the bot's chats; imbot.v2.Revision.get; an incoming webhook with botToken (GO24DEV-962).
func TestRestPortalBotsV2(t *testing.T) {
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
	var boss string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}')
		RETURNING id`).Scan(&boss))
	synapse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer synapse.Close()
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer app.Close()
	bundle, err := i18n.Load("")
	must(err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle,
		AdminGroups: []string{"go24-admins"}, BaseURL: "https://go24.example", Log: log,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Matrix:   matrixsync.New(&matrixsync.Service{DB: pool, M: matrix.New(synapse.URL, "t.lan", "bot"), Log: log})}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	id, err := srv.Apps.Save(ctx, boss, apps.App{Name: "Support", HandlerURL: app.URL + "/page", Scopes: []string{"imbot", "im"}})
	must(err)
	a, err := srv.Apps.Get(ctx, id)
	must(err)
	must(srv.Apps.Install(ctx, a, boss, srv.BaseURL, "ru"))
	tk, err := srv.Apps.Issue(ctx, a.ID, boss)
	must(err)
	key, err := srv.Keys.Create(ctx, boss, "ключ", apikeys.AllScopes, 0)
	must(err)
	call := func(method, body string, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+"/rest/"+method+"?auth="+tk.Access, "application/json", strings.NewReader(body))
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

	reg := obj(call("imbot.v2.Bot.register", `{"fields": {"code": "support_bot", "botToken": "tok", "type": "supervisor",
		"properties": {"name": "Support Bot", "workPosition": "AI Assistant"}}}`, 200)["result"])
	bot, users := obj(reg["bot"]), reg["users"].([]any)
	botID := bot["id"].(float64)
	if bot["code"] != "support_bot" || bot["type"] != "supervisor" || bot["eventMode"] != "fetch" || bot["isReactionsEnabled"] != true ||
		len(users) != 1 || obj(users[0])["name"] != "Support Bot" || obj(users[0])["workPosition"] != "AI Assistant" || obj(users[0])["bot"] != true {
		t.Fatalf("register: %v", reg)
	}
	if again := obj(obj(call("imbot.v2.Bot.register", `{"fields": {"code": "SUPPORT_BOT", "properties": {"name": "Другой"}}}`, 200)["result"])["bot"]); again["id"] != botID {
		t.Fatalf("the same code again: %v", again)
	}
	if e := call("imbot.v2.Bot.register", `{"fields": {"code": "x", "type": "robot"}}`, 400); e["error"] != "BOT_INVALID_TYPE" {
		t.Fatalf("an unknown type: %v", e)
	}
	if e := call("imbot.v2.Bot.register", `{"fields": {"code": "y", "eventMode": "webhook"}}`, 400); e["error"] != "BOT_WEBHOOK_URL_REQUIRED" {
		t.Fatalf("webhook without its address: %v", e)
	}
	hooked := obj(obj(call("imbot.v2.Bot.register", `{"fields": {"code": "hooked", "eventMode": "webhook", "webhookUrl": "`+app.URL+`/bot",
		"properties": {"name": "Hooked"}}}`, 200)["result"])["bot"])
	var bound int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM app_events WHERE handler_url = $1 AND event LIKE 'ONIMBOTV2%' AND portal`, app.URL+"/bot").Scan(&bound))
	if hooked["eventMode"] != "webhook" || bound != len(matrixsync.BotV2Events) {
		t.Fatalf("a webhook bot %v, its address bound to %d events", hooked, bound)
	}

	if got := obj(obj(call("imbot.v2.Bot.get", `{"code": "support_bot"}`, 200)["result"])["bot"]); got["id"] != botID {
		t.Fatalf("get by code: %v", got)
	}
	list := obj(call("imbot.v2.Bot.list", `{"filter": {"type": "bot"}}`, 200)["result"])
	if bots := list["bots"].([]any); len(bots) != 1 || obj(bots[0])["code"] != "hooked" || list["hasNextPage"] != false {
		t.Fatalf("list of type bot: %v", list)
	}
	if page := obj(call("imbot.v2.Bot.list", `{"limit": 1}`, 200)["result"]); len(page["bots"].([]any)) != 1 || page["hasNextPage"] != true {
		t.Fatalf("a page of one: %v", page)
	}
	up := obj(call("imbot.v2.Bot.update", `{"botId": `+portalStr(botID)+`, "fields": {"isHidden": true, "properties": {"name": "Helpdesk"}}}`, 200)["result"])
	if obj(up["bot"])["isHidden"] != true || obj(up["users"].([]any)[0])["name"] != "Helpdesk" || obj(up["bot"])["type"] != "supervisor" {
		t.Fatalf("update: %v", up)
	}
	if e := call("imbot.v2.Bot.update", `{"botId": `+portalStr(botID)+`, "fields": {"eventMode": "push"}}`, 400); e["error"] != "BOT_INVALID_EVENT_MODE" {
		t.Fatalf("an unknown event mode: %v", e)
	}

	// commands: register (fields), list, update of a title, unregister
	cmd := obj(obj(call("imbot.v2.Command.register", `{"botId": `+portalStr(botID)+`, "fields": {"command": "help", "title": {"en": "Help", "ru": "Помощь"},
		"params": {"en": "topic"}, "common": true}}`, 200)["result"])["command"])
	if cmd["command"] != "/help" || cmd["botId"] != botID || cmd["common"] != true || obj(cmd["title"])["ru"] != "Помощь" || obj(cmd["params"])["en"] != "topic" {
		t.Fatalf("command register: %v", cmd)
	}
	upd := obj(obj(call("imbot.v2.Command.update", `{"botId": `+portalStr(botID)+`, "commandId": `+portalStr(cmd["id"])+`, "fields": {"title": {"ru": "Справка"}, "hidden": true}}`, 200)["result"])["command"])
	if obj(upd["title"])["ru"] != "Справка" || obj(upd["title"])["en"] != "Help" || upd["hidden"] != true {
		t.Fatalf("command update: %v", upd)
	}
	if cmds := obj(call("imbot.v2.Command.list", `{"botId": `+portalStr(botID)+`}`, 200)["result"])["commands"].([]any); len(cmds) != 1 {
		t.Fatalf("command list: %v", cmds)
	}
	if r := obj(call("imbot.v2.Command.unregister", `{"botId": `+portalStr(botID)+`, "commandId": `+portalStr(cmd["id"])+`}`, 200)["result"]); r["result"] != true {
		t.Fatalf("command unregister: %v", r)
	}

	// Event.get: a page, then the next from nextOffset
	b, err := srv.Matrix.AppBot(ctx, a.ID, "support_bot")
	must(err)
	for _, text := range []string{"one", "two", "three"} {
		must(srv.Matrix.QueueV2(ctx, b, matrixsync.BotV2MessageAdd, map[string]any{"message": map[string]any{"text": text}}))
	}
	page := obj(call("imbot.v2.Event.get", `{"botId": `+portalStr(botID)+`, "limit": 2}`, 200)["result"])
	evs := page["events"].([]any)
	if len(evs) != 2 || page["hasMore"] != true || obj(evs[0])["type"] != "ONIMBOTV2MESSAGEADD" ||
		obj(obj(obj(evs[1])["data"])["message"])["text"] != "two" || obj(obj(evs[0])["data"])["bot"].(map[string]any)["id"] != botID {
		t.Fatalf("events: %v", page)
	}
	page = obj(call("imbot.v2.Event.get", `{"botId": `+portalStr(botID)+`, "offset": `+portalStr(page["nextOffset"])+`}`, 200)["result"])
	if evs := page["events"].([]any); len(evs) != 1 || page["hasMore"] != false || obj(obj(obj(evs[0])["data"])["message"])["text"] != "three" {
		t.Fatalf("the next events: %v", page)
	}

	// the bot's chats: a reaction with no such code, a private chat is not updated
	if e := call("imbot.v2.Chat.Message.Reaction.add", `{"botId": `+portalStr(botID)+`, "messageId": 1, "reaction": "unicorn"}`, 400); e["error"] != "REACTION_NOT_FOUND" {
		t.Fatalf("an unknown reaction: %v", e)
	}
	if e := call("imbot.v2.Chat.update", `{"botId": `+portalStr(botID)+`, "dialogId": "1", "fields": {"title": "x"}}`, 400); e["error"] != "WRONG_MESSAGE_TYPE" {
		t.Fatalf("update of a private chat: %v", e)
	}

	// files (GO24DEV-963): no such file; a downloadUrl of another token is refused
	if e := call("imbot.v2.File.download", `{"botId": `+portalStr(botID)+`, "fileId": 999}`, 404); e["error"] != "FILE_NOT_FOUND" {
		t.Fatalf("download of no file: %v", e)
	}
	link := srv.portalChatFileLink(boss, "!r:t.lan", "$e")
	for _, u := range []string{link, strings.Replace(link, "token=", "token=x", 1), hs.URL + "/rest/imfile?token=abc.def"} {
		res, err := http.Get(strings.Replace(u, srv.BaseURL, hs.URL, 1))
		must(err)
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			t.Fatalf("%s: %d", u, res.StatusCode)
		}
	}

	if r := obj(call("imbot.v2.Revision.get", `{}`, 200)["result"]); r["rest"] == nil {
		t.Fatalf("revision: %v", r)
	}
	if r := obj(call("imbot.v2.Bot.unregister", `{"botId": `+portalStr(botID)+`}`, 200)["result"]); r["result"] != true {
		t.Fatalf("unregister: %v", r)
	}
	if e := call("imbot.v2.Bot.get", `{"botId": `+portalStr(botID)+`}`, 400); e["error"] != "BOT_ID_ERROR" {
		t.Fatalf("an unregistered bot: %v", e)
	}

	// an incoming webhook (GO24DEV-962): imbot.v2 with botToken, the bots of its own application
	hook := func(method, body string, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+"/rest/"+method+"?auth="+key, "application/json", strings.NewReader(body))
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("webhook %s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	if e := hook("imbot.v2.Bot.list", `{}`, 400); e["error"] != "BOT_TOKEN_NOT_SPECIFIED" {
		t.Fatalf("a webhook without botToken: %v", e)
	}
	hb := obj(obj(hook("imbot.v2.Bot.register", `{"fields": {"code": "hook_bot", "botToken": "secret1", "properties": {"name": "Hook Bot"}}}`, 200)["result"])["bot"])
	hbID := portalStr(hb["id"])
	if got := obj(obj(hook("imbot.v2.Bot.get", `{"botId": `+hbID+`, "botToken": "secret1"}`, 200)["result"])["bot"]); got["code"] != "hook_bot" {
		t.Fatalf("a webhook's bot: %v", got)
	}
	if e := hook("imbot.v2.Bot.get", `{"botId": `+hbID+`, "botToken": "other"}`, 401); e["error"] != "BOT_OWNERSHIP_ERROR" {
		t.Fatalf("a wrong botToken: %v", e)
	}
	if e := hook("imbot.v2.Bot.register", `{"fields": {"code": "hook_bot", "botToken": "other"}}`, 401); e["error"] != "BOT_OWNERSHIP_ERROR" {
		t.Fatalf("the same code with another botToken: %v", e)
	}
	if e := hook("imbot.v2.Bot.get", `{"code": "hooked", "botToken": "tok"}`, 400); e["error"] != "BOT_ID_ERROR" {
		t.Fatalf("an application's bot by a webhook: %v", e)
	}
	if l := obj(hook("imbot.v2.Bot.list", `{"botToken": "secret1"}`, 200)["result"])["bots"].([]any); len(l) != 1 {
		t.Fatalf("the webhook's bots: %v", l)
	}
	var owner string
	must(pool.QueryRow(ctx, `SELECT a.name FROM apps a JOIN im_bots b ON b.app_id = a.id WHERE b.code = 'hook_bot' AND a.webhook_key IS NOT NULL`).Scan(&owner))
	if owner != "ключ" {
		t.Fatalf("the webhook's application: %q", owner)
	}
	// imbot.* of the first version stays an application's
	res, err := http.Post(hs.URL+"/rest/imbot.bot.list?auth="+key, "application/json", strings.NewReader(`{}`))
	must(err)
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("imbot.bot.list by a key of an employee: %d", res.StatusCode)
	}
}
