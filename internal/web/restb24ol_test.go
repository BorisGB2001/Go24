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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/openlines"
	"go24/internal/tasks"
)

// TestRestPortalOL: imopenlines.* — an administrator adds a line with the settings (an operator may not),
// reads and changes it; a client's dialogue goes to the first of a «strictly» queue, the other operator does not see
// it; the operator skips it to the next one, who answers and passes it back; pin, pinAll/unpinAll; a lead of the
// dialogue and the card's dialogues; the head's rating (not an operator's); finish — one's own, another.finish —
// the administrators'; session.start opens it again; spam; history with the users by number.
func TestRestPortalOL(t *testing.T) {
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
	user := func(name string, admin bool) (string, int64) {
		var id string
		var n int64
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ($1, $1, $1 || '@t', $2) RETURNING id, num`, name, admin).Scan(&id, &n))
		return id, n
	}
	boss, bossNum := user("Босс", true)
	anna, annaNum := user("Анна", false)
	eve, eveNum := user("Ева", false)
	bundle, err := i18n.Load("")
	must(err)
	ol := openlines.NewStore(pool, crm.NewStore(pool))
	ol.Tr = func(key string, _ ...any) string { return key }
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: ol.CRM, OpenLines: ol, Keys: apikeys.NewStore(pool), I18n: bundle,
		BaseURL: "https://go24.example", Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()

	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hook := func(uid string, n int64) string {
		token, err := srv.Keys.Create(ctx, uid, "ol", apikeys.AllScopes, time.Hour)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	bossHook, annaHook, eveHook := hook(boss, bossNum), hook(anna, annaNum), hook(eve, eveNum)
	call := func(h, method, body string, want int) any {
		t.Helper()
		res, err := http.Post(hs.URL+h+method+".json", "application/json", strings.NewReader(body))
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out["result"]
	}
	obj := func(v any) map[string]any { return v.(map[string]any) }
	str := func(v any) string { return portalStr(v) }

	// the lines: the administrators'
	add := `{"PARAMS": {"LINE_NAME": "Сайт", "QUEUE": [` + num(annaNum) + `, ` + num(eveNum) + `], "QUEUE_TYPE": "strictly",
		"WORKTIME_FROM": "9.30", "WORKTIME_TO": "18", "WORKTIME_DAYOFF": ["SA", "SU"], "WORKTIME_TIMEZONE": "Asia/Vladivostok", "WORKTIME_HOLIDAYS": ["8.03", "01.01"], "AUTO_CLOSE_TIME": 7200, "KPI_FIRST_ANSWER_TIME": 300}}`
	call(annaHook, "imopenlines.config.add", add, 403)
	line := str(call(bossHook, "imopenlines.config.add", add, 200))
	cfg := obj(call(annaHook, "imopenlines.config.get", `{"CONFIG_ID": `+line+`}`, 200))
	if cfg["LINE_NAME"] != "Сайт" || cfg["QUEUE_TYPE"] != "strictly" || cfg["WORKTIME_FROM"] != "9.30" || cfg["WORKTIME_TO"] != "18.00" ||
		str(cfg["AUTO_CLOSE_TIME"]) != "7200" || str(cfg["KPI_FIRST_ANSWER_TIME"]) != "300" || str(cfg["QUEUE"]) != "["+num(annaNum)+" "+num(eveNum)+"]" ||
		str(cfg["WORKTIME_DAYOFF"]) != "[SU SA]" || cfg["WORKTIME_TIMEZONE"] != "Asia/Vladivostok" || str(cfg["WORKTIME_HOLIDAYS"]) != "[01.01 08.03]" || cfg["ACTIVE"] != "Y" {
		t.Fatalf("config.get: %v", cfg)
	}
	call(bossHook, "imopenlines.config.update", `{"CONFIG_ID": `+line+`, "PARAMS": {"QUEUE_TYPE": "random"}}`, 400)
	call(bossHook, "imopenlines.config.update", `{"CONFIG_ID": `+line+`, "PARAMS": {"LINE_NAME": "Сайт магазина"}}`, 200)
	other := str(call(bossHook, "imopenlines.config.add", `{"PARAMS": {"LINE_NAME": "Склад", "ACTIVE": "N"}}`, 200))
	list := call(eveHook, "imopenlines.config.list.get", `{"PARAMS": {"filter": {"ACTIVE": "Y"}}}`, 200).([]any)
	if len(list) != 1 || obj(list[0])["LINE_NAME"] != "Сайт магазина" || obj(list[0])["QUEUE_TYPE"] != "strictly" || obj(list[0])["QUEUE"] != nil {
		t.Fatalf("config.list.get: %v", list)
	}
	call(bossHook, "imopenlines.config.delete", `{"CONFIG_ID": `+other+`}`, 200)
	call(bossHook, "imopenlines.config.get", `{"CONFIG_ID": `+other+`}`, 400)
	if p := obj(call(annaHook, "imopenlines.config.path.get", `{}`, 200)); p["PUBLIC_PATH"] != "https://go24.example/openlines/widgets" {
		t.Errorf("config.path.get: %v", p)
	}

	// a client writes: Anna is the first of the queue, Eve does not see her dialogue
	var wid string
	must(pool.QueryRow(ctx, `SELECT id FROM ol_widgets WHERE num = $1`, line).Scan(&wid))
	_, tok, err := ol.Visit(ctx, wid, "", "https://shop.example/")
	must(err)
	must(ol.Contact(ctx, wid, tok, "Пётр", "+79001112233", "", "", "widget"))
	must(ol.VisitorSay(ctx, wid, tok, "Есть доставка?"))
	var chat string
	must(pool.QueryRow(ctx, `SELECT num FROM ol_sessions WHERE widget_id = $1`, wid).Scan(&chat))
	c := `{"CHAT_ID": ` + chat + `}`
	d := obj(call(annaHook, "imopenlines.dialog.get", c, 200))
	if str(d["owner"]) != num(annaNum) || d["dialog_id"] != "chat"+chat || !strings.HasPrefix(str(d["entity_id"]), "livechat|"+line+"|"+chat) {
		t.Fatalf("dialog.get: %v", d)
	}
	call(eveHook, "imopenlines.dialog.get", c, 400)
	call(eveHook, "imopenlines.operator.skip", c, 400)

	// Anna skips it to Eve; Eve answers and passes it back
	call(annaHook, "imopenlines.operator.skip", c, 200)
	if d := obj(call(eveHook, "imopenlines.dialog.get", `{"DIALOG_ID": "chat`+chat+`"}`, 200)); str(d["owner"]) != num(eveNum) {
		t.Fatalf("skip: %v", d)
	}
	call(eveHook, "imopenlines.crm.message.add", `{"CHAT_ID": `+chat+`, "MESSAGE": ""}`, 400)
	call(eveHook, "imopenlines.crm.message.add", `{"CHAT_ID": `+chat+`, "USER_ID": `+num(annaNum)+`, "MESSAGE": "Есть"}`, 403)
	msg := str(call(eveHook, "imopenlines.crm.message.add", `{"CHAT_ID": `+chat+`, "MESSAGE": "Да, по городу бесплатно"}`, 200))
	call(eveHook, "imopenlines.operator.transfer", `{"CHAT_ID": `+chat+`, "TRANSFER_ID": `+num(annaNum)+`}`, 200)
	h := obj(call(annaHook, "imopenlines.session.history.get", c, 200))
	m := obj(obj(h["message"])[msg])
	if m["text"] != "Да, по городу бесплатно" || str(m["author_id"]) != num(eveNum) || obj(obj(h["users"])[num(eveNum)])["name"] != "Ева" ||
		h["canVoteAsHead"] != "N" || len(obj(h["usersMessage"])[chat].([]any)) < 2 {
		t.Fatalf("session.history.get: %v", h)
	}

	// pin
	call(annaHook, "imopenlines.session.mode.pin", `{"CHAT_ID": `+chat+`, "ACTIVATE": "Y"}`, 200)
	if got := call(annaHook, "imopenlines.session.mode.pinAll", `{}`, 200).([]any); len(got) != 0 {
		t.Errorf("pinAll: %v", got)
	}
	if got := call(annaHook, "imopenlines.session.mode.unpinAll", `{}`, 200).([]any); len(got) != 1 || str(got[0]) != chat {
		t.Errorf("unpinAll: %v", got)
	}

	// a lead of the dialogue and the lead's dialogues
	call(annaHook, "imopenlines.crm.lead.create", c, 200)
	d = obj(call(annaHook, "imopenlines.dialog.get", `{"SESSION_ID": `+chat+`}`, 200))
	parts := strings.Split(str(d["entity_data_1"]), "|")
	if parts[0] != "Y" || parts[1] != "LEAD" || parts[2] == "0" {
		t.Fatalf("crm.lead.create: %v", d)
	}
	chats := call(annaHook, "imopenlines.crm.chat.get", `{"CRM_ENTITY_TYPE": "LEAD", "CRM_ENTITY": `+parts[2]+`}`, 200).([]any)
	if len(chats) != 1 || str(obj(chats[0])["CHAT_ID"]) != chat || obj(chats[0])["CONNECTOR_ID"] != "livechat" {
		t.Errorf("crm.chat.get: %v", chats)
	}
	if got := call(eveHook, "imopenlines.crm.chat.get", `{"CRM_ENTITY_TYPE": "LEAD", "CRM_ENTITY": `+parts[2]+`}`, 200).([]any); len(got) != 0 {
		t.Errorf("crm.chat.get of another's dialogue: %v", got)
	}

	lead := `"CRM_ENTITY_TYPE": "LEAD", "CRM_ENTITY": ` + parts[2]
	if got := str(call(annaHook, "imopenlines.crm.chat.getLastId", `{`+lead+`}`, 200)); got != chat {
		t.Errorf("crm.chat.getLastId: %v", got)
	}
	call(eveHook, "imopenlines.crm.chat.getLastId", `{`+lead+`}`, 400)

	// Anna brings Eve into the lead's dialogue and takes her out; the boss joins it himself
	call(eveHook, "imopenlines.crm.chat.user.add", `{`+lead+`, "USER_ID": `+num(eveNum)+`}`, 400) // she does not see it
	call(annaHook, "imopenlines.crm.chat.user.add", `{`+lead+`, "USER_ID": `+num(eveNum)+`, "CHAT_ID": 999999}`, 400)
	if got := str(call(annaHook, "imopenlines.crm.chat.user.add", `{`+lead+`, "USER_ID": `+num(eveNum)+`}`, 200)); got != chat {
		t.Errorf("crm.chat.user.add: %v", got)
	}
	call(eveHook, "imopenlines.dialog.get", c, 200)
	call(annaHook, "imopenlines.crm.chat.user.add", `{`+lead+`, "USER_ID": `+num(eveNum)+`}`, 400) // already there
	call(annaHook, "imopenlines.crm.chat.user.delete", `{`+lead+`, "USER_ID": `+num(eveNum)+`, "CHAT_ID": `+chat+`}`, 200)
	call(eveHook, "imopenlines.dialog.get", c, 400)
	call(eveHook, "imopenlines.session.join", c, 400)
	call(bossHook, "imopenlines.session.join", c, 200)
	var joined bool
	must(pool.QueryRow(ctx, `SELECT $2::uuid = ANY(invited) FROM ol_sessions WHERE num::text = $1`, chat, boss).Scan(&joined))
	if !joined {
		t.Error("session.join: the boss is not in the dialogue")
	}

	// Eve's answer becomes Anna's quick answer
	call(annaHook, "imopenlines.message.quick.save", `{"CHAT_ID": `+chat+`, "MESSAGE_ID": 0}`, 400)
	call(annaHook, "imopenlines.message.quick.save", `{"CHAT_ID": `+chat+`, "MESSAGE_ID": `+msg+`}`, 200)
	quick, err := ol.Quick(ctx, anna)
	must(err)
	if len(quick) != 1 || quick[0].Title != "Да, по городу бесплатно" || quick[0].Common {
		t.Errorf("message.quick.save: %+v", quick)
	}

	// the head's rating
	call(annaHook, "imopenlines.session.head.vote", `{"SESSION_ID": `+chat+`, "RATING": 5}`, 403)
	call(bossHook, "imopenlines.session.head.vote", `{"SESSION_ID": `+chat+`, "RATING": 3}`, 400)
	call(bossHook, "imopenlines.session.head.vote", `{"SESSION_ID": `+chat+`, "RATING": 5, "COMMENT": "Быстро"}`, 200)
	if h := obj(call(bossHook, "imopenlines.session.history.get", c, 200)); str(h["sessionVoteHead"]) != "5" || h["sessionCommentHead"] != "Быстро" {
		t.Errorf("head.vote: %v", h)
	}

	// finish, start, another.finish
	call(eveHook, "imopenlines.operator.finish", c, 400) // not hers and not seen after the transfer
	call(annaHook, "imopenlines.operator.finish", c, 200)
	if d := obj(call(annaHook, "imopenlines.dialog.get", c, 200)); d["status"] != "closed" {
		t.Fatalf("finish: %v", d)
	}
	call(annaHook, "imopenlines.session.start", c, 200)
	call(annaHook, "imopenlines.operator.another.finish", c, 403)
	call(bossHook, "imopenlines.operator.another.finish", c, 200)

	// another client: Anna takes it by answer, it is spam
	_, tok2, err := ol.Visit(ctx, wid, "", "https://shop.example/")
	must(err)
	must(ol.VisitorSay(ctx, wid, tok2, "Купите рекламу"))
	var chat2 string
	must(pool.QueryRow(ctx, `SELECT max(num) FROM ol_sessions`).Scan(&chat2))
	call(bossHook, "imopenlines.operator.answer", `{"CHAT_ID": `+chat2+`}`, 200)
	call(bossHook, "imopenlines.operator.answer", `{"CHAT_ID": `+chat2+`}`, 200) // their own already
	call(bossHook, "imopenlines.operator.spam", `{"CHAT_ID": `+chat2+`}`, 200)
	if d := obj(call(bossHook, "imopenlines.dialog.get", `{"CHAT_ID": `+chat2+`}`, 200)); d["spam"] != true || str(d["owner"]) != num(bossNum) {
		t.Errorf("answer and spam: %v", d)
	}
	call(bossHook, "imopenlines.dialog.get", `{"CHAT_ID": 0}`, 400)
	call(bossHook, "imopenlines.dialog.get", `{}`, 400)
}
