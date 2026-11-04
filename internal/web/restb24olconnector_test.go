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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/openlines"
	"go24/internal/tasks"
)

// TestRestPortalConnector: imconnector.* — an application registers its channel (another application may not take its
// code), it is in imconnector.list; a message before the channel is on a line is refused; activate, status,
// connector.data.set; the client's message becomes the line's dialogue of channel «app» with the client's name, phone
// and the message's id there; update.messages and delete.messages change it; the operator's answer goes to the
// application as ONIMCONNECTORMESSAGEADD (posted posts it), the closed dialogue as
// ONIMCONNECTORDIALOGFINISH — once; send.status.reading, chat.name.set; the administrators turn it off on the line —
// ONIMCONNECTORSTATUSDELETE, no more messages; unregister keeps the dialogue.
func TestRestPortalConnector(t *testing.T) {
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
	var mu sync.Mutex
	var posted []url.Values // what the application's handler got
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/file" {
			_, _ = w.Write([]byte("%PDF-1.4 test"))
			return
		}
		_ = r.ParseForm()
		mu.Lock()
		posted = append(posted, r.PostForm)
		mu.Unlock()
	}))
	defer app.Close()
	bundle, err := i18n.Load("")
	must(err)
	ol := openlines.NewStore(pool, crm.NewStore(pool))
	ol.Tr = func(key string, _ ...any) string { return key }
	ol.Key = []byte(strings.Repeat("k", 32))
	files := map[string][]byte{}
	ol.PutFile = func(_ context.Context, key string, r io.Reader, _ int64, _ string) error {
		b, err := io.ReadAll(r)
		files[key] = b
		return err
	}
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: ol.CRM, OpenLines: ol, Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool),
		I18n: bundle, AdminGroups: []string{"go24-admins"}, BaseURL: "https://go24.example", Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false)}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	token := func(name string) string {
		id, err := srv.Apps.Save(ctx, boss, apps.App{Name: name, HandlerURL: app.URL + "/page", Scopes: []string{"openlines:read", "openlines:write"}})
		must(err)
		a, err := srv.Apps.Get(ctx, id)
		must(err)
		must(srv.Apps.Install(ctx, a, boss, srv.BaseURL, "ru"))
		tk, err := srv.Apps.Issue(ctx, a.ID, boss)
		must(err)
		return tk.Access
	}
	tk, other := token("Мессенджер"), token("Другое")
	call := func(tk, method, body string, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+"/rest/"+method+"?auth="+tk, "application/json", strings.NewReader(body))
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

	line := portalStr(call(tk, "imopenlines.config.add", `{"PARAMS": {"LINE_NAME": "Поддержка"}}`, 200)["result"])
	if r := call(tk, "imconnector.register", `{"ID": "MyChat", "NAME": "Мой чат", "ICON": {"DATA_IMAGE": "data:image/svg+xml;base64,PHN2Zy8+"},
		"PLACEMENT_HANDLER": "`+app.URL+`/setup"}`, 200); r["result"] != true {
		t.Fatalf("register: %v", r)
	}
	if e := call(other, "imconnector.register", `{"ID": "mychat", "NAME": "Чужой"}`, 400); e["error"] == nil {
		t.Fatalf("another application's code: %v", e)
	}
	if l := obj(call(tk, "imconnector.list", `{}`, 200)["result"]); l["mychat"] != "Мой чат" || l["livechat"] == nil {
		t.Fatalf("list: %v", l)
	}
	if e := call(other, "imconnector.activate", `{"CONNECTOR": "mychat", "LINE": `+line+`, "ACTIVE": 1}`, 404); e["error"] != "CONNECTOR_NOT_FOUND" {
		t.Fatalf("another application's connector: %v", e)
	}
	msg := func(id, text string) string {
		return `{"CONNECTOR": "mychat", "LINE": ` + line + `, "MESSAGES": [{"user": {"id": "u1", "name": "Иван", "last_name": "Петров", "phone": "+79990001122"},
			"message": {"id": "` + id + `", "date": 1761700000, "text": "` + text + `"}, "chat": {"id": "c1", "url": "https://messenger.example/c1"}}]}`
	}
	if r := obj(call(tk, "imconnector.send.messages", msg("m0", "рано"), 200)["result"]); r["SUCCESS"] != false {
		t.Fatalf("a message before activate: %v", r)
	}
	call(tk, "imconnector.activate", `{"CONNECTOR": "mychat", "LINE": `+line+`, "ACTIVE": 1}`, 200)
	if st := obj(call(tk, "imconnector.status", `{"CONNECTOR": "mychat", "LINE": `+line+`}`, 200)["result"]); st["CONFIGURED"] != true || st["STATUS"] != true || portalStr(st["LINE"]) != line {
		t.Fatalf("status: %v", st)
	}
	call(tk, "imconnector.connector.data.set", `{"CONNECTOR": "mychat", "LINE": `+line+`, "DATA": {"id": "acc1", "url_im": "https://messenger.example/acc1", "name": "Наш аккаунт", "junk": "x"}}`, 200)
	var data map[string]any
	must(pool.QueryRow(ctx, `SELECT data FROM ol_connector_lines`).Scan(&data))
	if data["name"] != "Наш аккаунт" || data["url_im"] != "https://messenger.example/acc1" || data["junk"] != nil {
		t.Fatalf("data: %v", data)
	}
	call(tk, "event.bind", `{"event": "ONIMCONNECTORMESSAGEADD", "handler": "`+app.URL+`/ev"}`, 200)
	call(tk, "event.bind", `{"event": "ONIMCONNECTORDIALOGFINISH", "handler": "`+app.URL+`/ev"}`, 200)
	call(tk, "event.bind", `{"event": "ONIMCONNECTORSTATUSDELETE", "handler": "`+app.URL+`/ev"}`, 200)

	sent := obj(call(tk, "imconnector.send.messages", msg("m1", "Привет"), 200)["result"])
	res := obj(obj(sent["DATA"])["RESULT"].([]any)[0])
	if sent["SUCCESS"] != true || res["SUCCESS"] != true || len(res["message"].([]any)) != 1 {
		t.Fatalf("send: %v", sent)
	}
	chat := portalStr(obj(res["session"])["CHAT_ID"])
	var sid, name, phone, channel, page string
	must(pool.QueryRow(ctx, `SELECT id::text, name, phone, channel, page FROM ol_sessions WHERE num::text = $1`, chat).Scan(&sid, &name, &phone, &channel, &page))
	if name != "Иван Петров" || phone != "+79990001122" || channel != "app" || page != "https://messenger.example/c1" {
		t.Fatalf("the dialogue: %q %q %q %q", name, phone, channel, page)
	}
	// the next message of the chat — the same dialogue; a file by its link
	two := obj(call(tk, "imconnector.send.messages", `{"CONNECTOR": "mychat", "LINE": `+line+`, "MESSAGES": [{"user": {"id": "u1"},
		"message": {"id": "m2", "text": "Договор", "files": [{"url": "`+app.URL+`/file", "name": "dogovor.pdf"}]}, "chat": {"id": "c1"}}]}`, 200)["result"])
	if portalStr(obj(obj(obj(two["DATA"])["RESULT"].([]any)[0])["session"])["CHAT_ID"]) != chat {
		t.Fatalf("the same chat, another dialogue: %v", two)
	}
	var fileName string
	must(pool.QueryRow(ctx, `SELECT file_name FROM ol_messages WHERE app_id = 'm2'`).Scan(&fileName))
	if fileName != "dogovor.pdf" || len(files) != 1 {
		t.Fatalf("the file: %q, %d kept", fileName, len(files))
	}

	call(tk, "imconnector.update.messages", `{"CONNECTOR": "mychat", "LINE": `+line+`, "MESSAGES": [{"message": {"id": "m1", "text": "Привет!"}, "chat": {"id": "c1"}}]}`, 200)
	body := func(app string) string {
		var b string
		must(pool.QueryRow(ctx, `SELECT body FROM ol_messages WHERE app_id = $1`, app).Scan(&b))
		return b
	}
	if b := body("m1"); b != "Привет!" {
		t.Fatalf("updated: %q", b)
	}
	if r := obj(call(tk, "imconnector.update.messages", `{"CONNECTOR": "mychat", "LINE": `+line+`, "MESSAGES": [{"message": {"id": "nope", "text": "x"}, "chat": {"id": "c1"}}]}`, 200)["result"]); r["SUCCESS"] != false {
		t.Fatalf("an unknown message: %v", r)
	}
	call(tk, "imconnector.delete.messages", `{"CONNECTOR": "mychat", "LINE": `+line+`, "MESSAGES": [{"message": {"id": "m1"}, "chat": {"id": "c1"}}]}`, 200)
	if b := body("m1"); b != "ol.app_deleted" {
		t.Fatalf("deleted: %q", b)
	}

	// the operator's answer goes to the application
	must(ol.Reply(ctx, boss, sid, "Здравствуйте"))
	var answer int64
	must(pool.QueryRow(ctx, `SELECT max(id) FROM ol_messages WHERE session_id::text = $1 AND author_id IS NOT NULL`, sid).Scan(&answer))
	if _, err := ol.SendConnectors(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := ol.SendConnectors(ctx); err != nil || n != 0 {
		t.Fatalf("sent again: %d %v", n, err)
	}
	if n, errs := srv.Apps.DeliverEvents(ctx, time.Now(), srv.BaseURL); n == 0 || len(errs) > 0 {
		t.Fatalf("delivered %d: %v", n, errs)
	}
	find := func(event, key, value string) url.Values {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		for _, p := range posted {
			if p.Get("event") == event && (key == "" || p.Get(key) == value) {
				return p
			}
		}
		t.Fatalf("no %s with %s=%s among %v", event, key, value, posted)
		return nil
	}
	ev := find("ONIMCONNECTORMESSAGEADD", "data[MESSAGES][0][message][text]", "Здравствуйте")
	if ev.Get("data[CONNECTOR]") != "mychat" || ev.Get("data[LINE]") != line || ev.Get("data[MESSAGES][0][chat][id]") != "c1" ||
		ev.Get("data[MESSAGES][0][im][chat_id]") != chat || ev.Get("data[MESSAGES][0][im][message_id]") != strconv.FormatInt(answer, 10) ||
		ev.Get("data[MESSAGES][0][message][user_name]") != "Борис" || ev.Get("auth[application_token]") == "" {
		t.Fatalf("the answer's event: %v", ev)
	}

	call(tk, "imconnector.send.status.delivery", `{"CONNECTOR": "mychat", "LINE": `+line+`, "MESSAGES": [{"im": {"chat_id": `+chat+`, "message_id": `+strconv.FormatInt(answer, 10)+`}, "chat": {"id": "c1"}}]}`, 200)
	call(tk, "imconnector.send.status.reading", `{"CONNECTOR": "mychat", "LINE": `+line+`, "MESSAGES": [{"im": {"chat_id": `+chat+`, "message_id": `+strconv.FormatInt(answer, 10)+`}, "chat": {"id": "c1"}}]}`, 200)
	var read int64
	must(pool.QueryRow(ctx, `SELECT vis_read FROM ol_sessions WHERE id::text = $1`, sid).Scan(&read))
	if read != answer {
		t.Fatalf("read up to %d, want %d", read, answer)
	}
	call(tk, "imconnector.chat.name.set", `{"CONNECTOR": "mychat", "LINE": `+line+`, "CHAT_ID": "c1", "NAME": "Иван (VIP)"}`, 200)
	if d := obj(call(tk, "imopenlines.dialog.get", `{"CHAT_ID": `+chat+`}`, 200)["result"]); d["name"] != "Иван (VIP)" {
		t.Fatalf("dialog.get: %v", d)
	}

	// closed — the application is told once
	must(ol.SetStatus(ctx, boss, sid, false))
	must2 := func(n int, err error) int { t.Helper(); must(err); return n }
	must2(ol.SendConnectors(ctx))
	if n := must2(ol.SendConnectors(ctx)); n != 0 {
		t.Fatalf("the closing told again: %d", n)
	}
	srv.Apps.DeliverEvents(ctx, time.Now(), srv.BaseURL)
	if f := find("ONIMCONNECTORDIALOGFINISH", "data[CHAT_ID]", "c1"); f.Get("data[SESSION_ID]") != chat {
		t.Fatalf("finish: %v", f)
	}

	// an administrator turns it off on the line
	var connID, lineID string
	must(pool.QueryRow(ctx, `SELECT connector_id::text, widget_id::text FROM ol_connector_lines`).Scan(&connID, &lineID))
	must(ol.TurnOffConnector(ctx, connID, lineID))
	srv.Apps.DeliverEvents(ctx, time.Now(), srv.BaseURL)
	find("ONIMCONNECTORSTATUSDELETE", "data[connector]", "mychat")
	if st := obj(call(tk, "imconnector.status", `{"CONNECTOR": "mychat", "LINE": `+line+`}`, 200)["result"]); st["CONFIGURED"] != false {
		t.Fatalf("status off: %v", st)
	}
	if r := obj(call(tk, "imconnector.send.messages", msg("m3", "Ещё"), 200)["result"]); r["SUCCESS"] != false {
		t.Fatalf("a message when off: %v", r)
	}
	call(tk, "imconnector.unregister", `{"ID": "mychat"}`, 200)
	if e := call(tk, "imconnector.status", `{"CONNECTOR": "mychat", "LINE": `+line+`}`, 404); e["error"] != "CONNECTOR_NOT_FOUND" {
		t.Fatalf("unregistered: %v", e)
	}
	must(pool.QueryRow(ctx, `SELECT channel FROM ol_sessions WHERE id::text = $1`, sid).Scan(&channel))
	if channel != "app" {
		t.Fatalf("the dialogue after unregister: %q", channel)
	}
}
