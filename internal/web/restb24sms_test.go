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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sms"
	"go24/internal/tasks"
)

// TestRestPortalMessageService: an application's administrator adds an SMS provider (messageservice.sender.add), it is
// one of the CRM's providers; a message sent through it goes to the handler posts it (type, code,
// message_id, message_to, message_body, auth of the sender), the application tells its status
// (messageservice.message.status.update); sender.list, sender.delete, and the uninstalled application loses its
// providers.
func TestRestPortalMessageService(t *testing.T) {
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
	var annaNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}')
		RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id, num`).Scan(&anna, &annaNum))

	var (
		mu       sync.Mutex
		received []url.Values
	)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sms" {
			return
		}
		_ = r.ParseForm()
		mu.Lock()
		received = append(received, r.PostForm)
		mu.Unlock()
		if strings.Contains(r.PostForm.Get("message_body"), "отказ") {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer app.Close()

	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle,
		AdminGroups: []string{"go24-admins"}, BaseURL: "https://go24.example", SMS: sms.NewStore(pool, strings.Repeat("k", 32)),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.SMS.App = srv.SMSAppCall
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(method, tok, body string, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+"/rest/"+method+"?auth="+tok, "application/json", strings.NewReader(body))
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

	id, err := srv.Apps.Save(ctx, boss, apps.App{Name: "SMS-шлюз", HandlerURL: app.URL + "/page", Scopes: []string{"crm:read", "crm:write"}})
	must(err)
	a, err := srv.Apps.Get(ctx, id)
	must(err)
	must(srv.Apps.Install(ctx, a, boss, srv.BaseURL, "ru"))
	tokens := func(user string) string {
		tk, err := srv.Apps.Issue(ctx, a.ID, user)
		must(err)
		return tk.Access
	}
	bossTok, annaTok := tokens(boss), tokens(anna)

	add := `{"CODE": "gate", "TYPE": "SMS", "HANDLER": "` + app.URL + `/sms", "NAME": {"ru": "Шлюз", "en": "Gate"}, "DESCRIPTION": "Через шлюз"}`
	if e := call("messageservice.sender.add", annaTok, add, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("add by an employee: %v", e)
	}
	if call("messageservice.sender.add", bossTok, add, 200)["result"] != true {
		t.Fatal("add is not true")
	}
	if e := call("messageservice.sender.add", bossTok, add, 400); e["error"] != "ERROR_SENDER_ALREADY_INSTALLED" {
		t.Errorf("add twice: %v", e)
	}
	if e := call("messageservice.sender.add", bossTok, `{"CODE": "x", "TYPE": "SMS", "HANDLER": "https://evil.example/sms", "NAME": "X"}`, 400); e["error"] != "ERROR_HANDLER_URL_MATCH" {
		t.Errorf("a handler elsewhere: %v", e)
	}
	if e := call("messageservice.sender.add", bossTok, `{"CODE": "x", "TYPE": "SMS", "HANDLER": "`+app.URL+`/sms"}`, 400); e["error"] != "ERROR_SENDER_VALIDATION_FAILURE" {
		t.Errorf("no name: %v", e)
	}
	if l, _ := call("messageservice.sender.list", annaTok, `{}`, 200)["result"].([]any); len(l) != 1 || l[0] != "gate" {
		t.Errorf("list: %v", l)
	}
	list, err := srv.SMS.Senders(ctx)
	must(err)
	if len(list) != 1 || list[0].Kind != "app" || list[0].Name != "Шлюз" || !list[0].Default || list[0].Description != "Через шлюз" {
		t.Fatalf("the CRM's providers: %+v", list)
	}

	// a message from the card goes to the handler with the sender's tokens
	m, err := srv.SMS.Send(ctx, sms.Out{Phone: "8 912 345-67-89", Text: "Ваш заказ готов", AuthorID: anna})
	must(err)
	if m.Status != "sent" || m.ID == "" {
		t.Fatalf("sent: %+v", m)
	}
	mu.Lock()
	got := received[len(received)-1]
	mu.Unlock()
	for k, v := range map[string]string{"type": "SMS", "code": "gate", "message_id": m.ID, "message_to": "+79123456789",
		"message_body": "Ваш заказ готов", "auth[user_id]": strconv.FormatInt(annaNum, 10), "auth[client_endpoint]": "https://go24.example/rest/",
		"auth[application_token]": a.ApplicationToken} {
		if got.Get(k) != v {
			t.Errorf("handler got %s = %q, want %q", k, got.Get(k), v)
		}
	}
	if got.Get("auth[access_token]") == "" {
		t.Error("no access_token for the handler")
	}

	// the application tells how it went
	status := func(msg, st string, want int) map[string]any {
		return call("messageservice.message.status.update", bossTok, `{"CODE": "gate", "MESSAGE_ID": "`+msg+`", "STATUS": "`+st+`"}`, want)
	}
	if status(m.ID, "delivered", 200)["result"] != true {
		t.Fatal("status is not true")
	}
	var st string
	must(pool.QueryRow(ctx, `SELECT status FROM sms_messages WHERE id::text = $1`, m.ID).Scan(&st))
	if st != "delivered" {
		t.Errorf("status: %s", st)
	}
	if e := status(m.ID, "lost", 400); e["error"] != "ERROR_MESSAGE_STATUS_INCORRECT" {
		t.Errorf("a wrong status: %v", e)
	}
	if e := status("00000000-0000-0000-0000-000000000000", "failed", 400); e["error"] != "ERROR_MESSAGE_NOT_FOUND" {
		t.Errorf("another message: %v", e)
	}

	// the handler refuses: the message is an error; a robot's message (no employee) has no employee's tokens
	if _, err := srv.SMS.Send(ctx, sms.Out{Phone: "+79123456789", Text: "отказ"}); err == nil {
		t.Error("a refused message is no error")
	}
	mu.Lock()
	got = received[len(received)-1]
	mu.Unlock()
	if got.Get("auth[access_token]") != "" || got.Get("auth[application_token]") != a.ApplicationToken {
		t.Errorf("a robot's message auth: %v", got)
	}
	must(pool.QueryRow(ctx, `SELECT status FROM sms_messages ORDER BY created_at DESC LIMIT 1`).Scan(&st))
	if st != "error" {
		t.Errorf("refused: %s", st)
	}

	// delete; the uninstalled application's providers are gone
	if call("messageservice.sender.delete", bossTok, `{"CODE": "gate"}`, 200)["result"] != true {
		t.Fatal("delete is not true")
	}
	if e := call("messageservice.sender.delete", bossTok, `{"CODE": "gate"}`, 400); e["error"] != "ERROR_SENDER_NOT_FOUND" {
		t.Errorf("delete twice: %v", e)
	}
	call("messageservice.sender.add", bossTok, add, 200)
	must(srv.Apps.Uninstall(ctx, a.ID, srv.BaseURL, "ru"))
	if list, _ := srv.SMS.Senders(ctx); len(list) != 0 {
		t.Errorf("after uninstall: %+v", list)
	}
}
