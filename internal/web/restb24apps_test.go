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
	"go24/internal/tasks"
)

// TestPHPForm: an event is a form of PHP arrays.
func TestPHPForm(t *testing.T) {
	got := apps.PHPForm(map[string]any{"event": "ONCRMDEALADD", "data": map[string]any{"FIELDS": map[string]any{"ID": json.Number("5")}},
		"auth": map[string]any{"user_id": int64(3), "scope": []any{"crm"}, "ok": true}})
	want := url.Values{"event": {"ONCRMDEALADD"}, "data[FIELDS][ID]": {"5"}, "auth[user_id]": {"3"}, "auth[scope][0]": {"crm"}, "auth[ok]": {"1"}}
	if got.Encode() != want.Encode() {
		t.Errorf("PHPForm: %s, want %s", got.Encode(), want.Encode())
	}
}

// TestRestPortalEvents: events, event.bind/get/unbind and placement.* by the names — an application's token
// only, binding by an administrator; a handler bound so gets the event as a form of PHP arrays with the record's
// number and «auth» for /rest/, while one bound over go24 REST keeps JSON with the go24 id.
func TestRestPortalEvents(t *testing.T) {
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
	var bossNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}')
		RETURNING id, num`).Scan(&boss, &bossNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))

	type got struct {
		Path, Type string
		Form       url.Values
		JSON       map[string]any
	}
	var (
		mu       sync.Mutex
		received []got
	)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := got{Path: r.URL.Path, Type: r.Header.Get("Content-Type")}
		if g.Type == "application/json" {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &g.JSON)
			if g.JSON["event"] == "ONAPPINSTALL" {
				return
			}
		} else {
			_ = r.ParseForm()
			g.Form = r.PostForm
		}
		mu.Lock()
		received = append(received, g)
		mu.Unlock()
	}))
	defer app.Close()

	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle,
		CRM: crm.NewStore(pool), AdminGroups: []string{"go24-admins"}, BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", path, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}

	id, err := srv.Apps.Save(ctx, boss, apps.App{Name: "Учёт", HandlerURL: app.URL + "/page", Scopes: []string{"crm:read", "placement"}})
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
	key, err := srv.Keys.Create(ctx, boss, "ключ", apikeys.AllScopes, 0)
	must(err)

	// an incoming webhook may not; an employee may not bind
	if e := call("/rest/"+strconv.FormatInt(bossNum, 10)+"/"+key+"/event.get", `{}`, 401); e["error"] != "WRONG_AUTH_TYPE" {
		t.Errorf("event.get of a webhook: %v", e)
	}
	if e := call("/rest/event.bind?auth="+annaTok, `{"event": "ONCRMDEALADD", "handler": "`+app.URL+`/portal"}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("event.bind of an employee: %v", e)
	}
	if evs := call("/rest/events?auth="+bossTok, `{}`, 200)["result"].([]any); len(evs) == 0 || evs[0] != "ONCRMLEADADD" {
		t.Errorf("events: %v", evs)
	}
	if call("/rest/event.bind?auth="+bossTok, `{"event": "ONCRMDEALADD", "handler": "`+app.URL+`/portal", "auth_type": 0}`, 200)["result"] != true {
		t.Error("event.bind is not true")
	}
	if _, err := srv.Apps.BindEvent(ctx, a, apps.Event{Event: "ONCRMDEALADD", Handler: app.URL + "/json", UserID: boss}); err != nil {
		t.Fatal(err)
	}
	list := call("/rest/event.get?auth="+bossTok, `{}`, 200)["result"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["auth_type"] != strconv.FormatInt(bossNum, 10) {
		t.Errorf("event.get: %v", list)
	}

	// a deal: the handler gets a form with its number, the other JSON with its go24 id
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	d, err := srv.CRM.SaveDeal(ctx, boss, crm.Deal{Title: "Альфа", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boss}})
	must(err)
	var dealNum string
	must(pool.QueryRow(ctx, `SELECT num::text FROM crm_deals WHERE id::text = $1`, d).Scan(&dealNum))
	if _, errs := srv.Apps.DeliverEvents(ctx, time.Now(), srv.BaseURL); len(errs) != 0 {
		t.Fatal(errs)
	}
	mu.Lock()
	evs := received
	mu.Unlock()
	if len(evs) != 2 {
		t.Fatalf("delivered: %+v", evs)
	}
	for _, g := range evs {
		switch g.Path {
		case "/portal":
			f := g.Form
			if !strings.HasPrefix(g.Type, "application/x-www-form-urlencoded") || f.Get("event") != "ONCRMDEALADD" || f.Get("data[FIELDS][ID]") != dealNum ||
				f.Get("data[FIELDS][Portal_ID]") != "" || f.Get("auth[user_id]") != strconv.FormatInt(bossNum, 10) ||
				f.Get("auth[client_endpoint]") != "https://go24.example/rest/" || f.Get("auth[access_token]") == "" {
				t.Errorf("the event: %s %v", g.Type, f)
			}
		case "/json":
			fields := g.JSON["data"].(map[string]any)["FIELDS"].(map[string]any)
			if fields["ID"] != d || fields["Portal_ID"] != nil {
				t.Errorf("the event of go24: %v", g.JSON)
			}
		}
	}
	if n := call("/rest/event.unbind?auth="+bossTok, `{"event": "ONCRMDEALADD", "handler": "`+app.URL+`/portal"}`, 200)["result"].(map[string]any)["count"]; n != 1.0 {
		t.Errorf("event.unbind: %v", n)
	}

	// placements
	places := call("/rest/placement.list?auth="+bossTok, `{}`, 200)["result"].([]any)
	if len(places) == 0 {
		t.Fatal("placement.list is empty")
	}
	place := places[0].(string)
	if call("/rest/placement.bind?auth="+bossTok, `{"PLACEMENT": "`+place+`", "HANDLER": "`+app.URL+`/tab", "TITLE": "Учёт"}`, 200)["result"] != true {
		t.Error("placement.bind is not true")
	}
	if got := call("/rest/placement.get?auth="+bossTok, `{}`, 200)["result"].([]any); len(got) != 1 || got[0].(map[string]any)["title"] != "Учёт" {
		t.Errorf("placement.get: %v", got)
	}
	if n := call("/rest/placement.unbind?auth="+bossTok, `{"PLACEMENT": "`+place+`"}`, 200)["result"].(map[string]any)["count"]; n != 1.0 {
		t.Errorf("placement.unbind: %v", n)
	}

	// a widget of the messenger (GO24DEV-977): the title of LANG_ALL, OPTIONS kept as given; a wrong one is refused
	if call("/rest/placement.bind?auth="+bossTok, `{"PLACEMENT": "IM_TEXTAREA", "HANDLER": "`+app.URL+`/ta", "LANG_ALL": {"ru": {"TITLE": "Шаблоны"}, "en": {"TITLE": "Templates"}},
		"OPTIONS": {"iconName": "fa-bars", "context": "user;chat", "role": "USER", "color": "GRAPHITE", "width": "200", "height": 100, "extranet": "N", "other": "x"}}`, 200)["result"] != true {
		t.Error("placement.bind of IM_TEXTAREA is not true")
	}
	imGot := call("/rest/placement.get?auth="+bossTok, `{}`, 200)["result"].([]any)
	if len(imGot) != 1 || imGot[0].(map[string]any)["title"] != "Шаблоны" {
		t.Fatalf("placement.get of IM_TEXTAREA: %v", imGot)
	}
	if o := imGot[0].(map[string]any)["options"].(map[string]any); o["context"] != "USER;CHAT" || o["width"] != "200" || o["height"] != "100" || o["iconName"] != "fa-bars" || o["other"] != nil {
		t.Errorf("OPTIONS of IM_TEXTAREA: %v", o)
	}
	call("/rest/placement.bind?auth="+bossTok, `{"PLACEMENT": "IM_SIDEBAR", "HANDLER": "`+app.URL+`/sb", "OPTIONS": {"context": "GROUPS"}}`, 400)
}
