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
	"go24/internal/hooks"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestAppEventsHTTP: events of applications, as event.* — an application subscribes its handlers with an
// administrator's token (only on its own host, only known events, only an application's token); adding and changing
// a deal, a smart process's card, a task and an employee queue the events once, the worker posts them signed with
// the tokens of the employee who bound the handler (without them when the application is not for them) and retries
// a failed one; unbinding stops the events, uninstalling sends ONAPPUNINSTALL and drops the subscriptions.
func TestAppEventsHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))

	type got struct {
		Path  string
		Event string `json:"event"`
		Data  struct {
			Fields      map[string]string `json:"FIELDS"`
			FieldsAfter map[string]string `json:"FIELDS_AFTER"`
			ID          string            `json:"ID"`
		} `json:"data"`
		Auth map[string]any `json:"auth"`
		Sig  bool
	}
	var (
		mu       sync.Mutex
		received []got
		secret   string
	)
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var g got
		_ = json.Unmarshal(raw, &g)
		g.Path = r.URL.Path
		mu.Lock()
		g.Sig = secret != "" && r.Header.Get("X-Go24-Signature") == hooks.Sign(secret, raw)
		if g.Event != "ONAPPINSTALL" {
			received = append(received, g)
		}
		mu.Unlock()
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer app.Close()
	take := func() []got {
		mu.Lock()
		defer mu.Unlock()
		out := received
		received = nil
		return out
	}

	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle, CRM: crm.NewStore(pool),
		AdminGroups: []string{"go24-admins"}, BaseURL: "https://go24.example", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: boss, Admin: true}))
	bossCookie := rec.Result().Cookies()[0]
	send := func(who, method, path, ctype, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if who == "" {
			req.AddCookie(bossCookie)
		} else {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("Accept-Language", "ru")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	has := func(page string, yes bool, want ...string) {
		t.Helper()
		for _, w := range want {
			if strings.Contains(page, w) != yes {
				t.Fatalf("page has %q: %v, want %v", w, !yes, yes)
			}
		}
	}
	deliver := func(at time.Time) {
		t.Helper()
		if _, errs := srv.Apps.DeliverEvents(ctx, at, srv.BaseURL); len(errs) != 0 {
			t.Fatal(errs)
		}
	}
	const js = "application/json"

	id, err := srv.Apps.Save(ctx, boss, apps.App{Name: "Учёт", HandlerURL: app.URL + "/page", Scopes: []string{"crm:read"}})
	must(err)
	a, err := srv.Apps.Get(ctx, id)
	must(err)
	secret = a.ClientSecret
	must(srv.Apps.Install(ctx, a, boss, srv.BaseURL, "ru"))
	tokens := func(user string) string {
		tk, err := srv.Apps.Issue(ctx, a.ID, user)
		must(err)
		return tk.Access
	}
	bossTok, annaTok := tokens(boss), tokens(anna)
	key, err := srv.Keys.Create(ctx, boss, "ключ", []string{"crm:read"}, 0)
	must(err)

	// events; only an application calls the methods, only an administrator binds, on its host, known events
	has(send(bossTok, "GET", "/api/v1/events/list", "", "", 200), true, `"ONCRMDEALADD"`, `"ONTASKUPDATE"`, `"ONAPPUNINSTALL"`)
	has(send(key, "GET", "/api/v1/events", "", "", 403), true, "только приложению")
	send(annaTok, "POST", "/api/v1/events", js, `{"event": "ONCRMDEALADD", "handler": "`+app.URL+`/deal"}`, 403)
	has(send(bossTok, "POST", "/api/v1/events", js, `{"event": "ONCRMDEALADD", "handler": "https://evil.example/deal"}`, 422), true, "домене приложения")
	has(send(bossTok, "POST", "/api/v1/events", js, `{"event": "ONNOTHING", "handler": "`+app.URL+`/deal"}`, 422), true, "ONNOTHING")
	has(send(bossTok, "POST", "/api/v1/events", js, `{"event": "ONCRMDEALADD", "handler": "`+app.URL+`/deal", "auth_type": "nobody"}`, 422), true, "уволен")
	for _, ev := range []string{"oncrmdealadd", "ONCRMDEALUPDATE", "ONCRMDYNAMICITEMADD", "ONTASKADD", "ONTASKDELETE", "ONAPPUNINSTALL"} {
		has(send(bossTok, "POST", "/api/v1/events", js, `{"event": "`+ev+`", "handler": "`+app.URL+`/h"}`, 201), true, `"auth_type": "`+boss+`"`)
	}
	send(bossTok, "POST", "/api/v1/events?event=ONUSERADD&auth_type="+anna+"&handler="+url.QueryEscape(app.URL+"/user"), "", "", 201)
	send(bossTok, "POST", "/api/v1/events", js, `{"event": "ONCRMCONTACTADD", "handler": "`+app.URL+`/fail"}`, 201)
	if list := send(bossTok, "GET", "/api/v1/events", "", "", 200); strings.Count(list, `"event"`) != 8 ||
		strings.Count(send(bossTok, "GET", "/api/v1/events?event=ONTASKADD", "", "", 200), `"event"`) != 1 {
		t.Fatalf("event.get: %s", list)
	}

	// a deal added and changed in one go — one ONCRMDEALADD with its ID and Борис's tokens, signed; then an update
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	d, err := srv.CRM.SaveDeal(ctx, boss, crm.Deal{Title: "Альфа", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: boss}})
	must(err)
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET title = 'Альфа-2' WHERE id::text = $1`, d)
	must(err)
	deliver(time.Now())
	evs := take()
	if len(evs) != 1 || evs[0].Event != "ONCRMDEALADD" || evs[0].Data.Fields["ID"] != d || !evs[0].Sig || evs[0].Path != "/h" ||
		evs[0].Auth["application_token"] != a.ApplicationToken || evs[0].Auth["user_id"] != boss {
		t.Fatalf("deal added: %+v", evs)
	}
	has(send(evs[0].Auth["access_token"].(string), "GET", "/api/v1/crm/deals/"+d, "", "", 200), true, "Альфа-2")
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET title = 'Альфа-3' WHERE id::text = $1`, d)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET amount = 10 WHERE id::text = $1`, d)
	must(err)
	deliver(time.Now())
	if evs = take(); len(evs) != 1 || evs[0].Event != "ONCRMDEALUPDATE" || evs[0].Data.Fields["ID"] != d {
		t.Fatalf("deal changed: %+v", evs)
	}

	// a smart process's card — ONCRMDYNAMICITEMADD with its type, not ONCRMDEALADD
	var typeID, pipe string
	must(pool.QueryRow(ctx, `INSERT INTO crm_types (name) VALUES ('Заявки') RETURNING id::text`).Scan(&typeID))
	must(pool.QueryRow(ctx, `INSERT INTO crm_pipelines (name, type_id) VALUES ('Заявки', $1) RETURNING id::text`, typeID).Scan(&pipe))
	_, err = pool.Exec(ctx, `INSERT INTO crm_stages (entity, pipeline_id, name, code, sort) VALUES ('deal', $1, 'Новая', 'NEW', 1)`, pipe)
	must(err)
	item, err := srv.CRM.SaveDeal(ctx, boss, crm.Deal{Title: "Заявка", PipelineID: pipe, Responsible: crm.Ref{ID: boss}})
	must(err)
	deliver(time.Now())
	if evs = take(); len(evs) != 1 || evs[0].Event != "ONCRMDYNAMICITEMADD" || evs[0].Data.Fields["ID"] != item || evs[0].Data.Fields["ENTITY_TYPE_ID"] != typeID {
		t.Fatalf("smart process: %+v", evs)
	}

	// a task added and sent to the trash — by its key; a new employee — to Анна's handler with her tokens
	_, err = ts.CreateProject(ctx, boss, "EV", "События", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "EV")
	must(err)
	tk, err := ts.CreateTask(ctx, boss, p, tasks.NewTask{Title: "Отгрузка"})
	must(err)
	deliver(time.Now())
	if evs = take(); len(evs) != 1 || evs[0].Event != "ONTASKADD" || evs[0].Data.FieldsAfter["ID"] != tk {
		t.Fatalf("task added: %+v", evs)
	}
	_, err = pool.Exec(ctx, `UPDATE tasks_all SET deleted_at = now(), deleted_root = id WHERE project_id = $1`, p.ID)
	must(err)
	var ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	deliver(time.Now())
	evs = take()
	if len(evs) != 2 || evs[0].Event != "ONTASKDELETE" || evs[1].Event != "ONUSERADD" || evs[1].Data.ID != ivan || evs[1].Auth["user_id"] != anna {
		t.Fatalf("task deleted, employee added: %+v", evs)
	}

	// «Кому доступно» — only Борис: Анна's handler gets no tokens, only application_token
	must(srv.Apps.SetAccess(ctx, a.ID, []string{boss}))
	_, err = pool.Exec(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('olga', 'Ольга', 'olga@go24.test')`)
	must(err)
	deliver(time.Now())
	if evs = take(); len(evs) != 1 || evs[0].Auth["access_token"] != nil || evs[0].Auth["application_token"] != a.ApplicationToken {
		t.Fatalf("not for the employee: %+v", evs)
	}
	must(srv.Apps.SetAccess(ctx, a.ID, nil))

	// a failed delivery is retried later; the settings show the subscriptions and the error
	_, err = pool.Exec(ctx, `INSERT INTO crm_contacts (name, responsible_id, created_by) VALUES ('Пётр', $1, $1)`, boss)
	must(err)
	deliver(time.Now())
	deliver(time.Now())
	if evs = take(); len(evs) != 1 || evs[0].Path != "/fail" {
		t.Fatalf("failed once: %+v", evs)
	}
	deliver(time.Now().Add(2 * time.Minute))
	if evs = take(); len(evs) != 1 {
		t.Fatalf("retried: %+v", evs)
	}
	has(send("", "GET", "/settings/apps", "", "", 200), true, "События", "ONCRMCONTACTADD", app.URL+"/fail", "HTTP 500", "ждут доставки: 1", "api/v1/events")

	// event.unbind: by the handler, or all of the event
	bossTok = tokens(boss)
	has(send(bossTok, "DELETE", "/api/v1/events?event=ONCRMDEALUPDATE&handler="+url.QueryEscape(app.URL+"/other"), "", "", 200), true, `"count": 0`)
	has(send(bossTok, "DELETE", "/api/v1/events", js, `{"event": "ONCRMDEALUPDATE"}`, 200), true, `"count": 1`)
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET title = 'Альфа-4' WHERE id::text = $1`, d)
	must(err)
	deliver(time.Now().Add(time.Hour))
	for _, e := range take() {
		if e.Event == "ONCRMDEALUPDATE" {
			t.Fatalf("unbound: %+v", e)
		}
	}

	// uninstalling: ONAPPUNINSTALL at once, without tokens; the subscriptions are gone, nothing more is queued
	must(srv.Apps.Uninstall(ctx, a.ID, srv.BaseURL, "ru"))
	if evs = take(); len(evs) != 1 || evs[0].Event != "ONAPPUNINSTALL" || !evs[0].Sig || evs[0].Auth["access_token"] != nil ||
		evs[0].Auth["application_token"] != a.ApplicationToken {
		t.Fatalf("uninstalled: %+v", evs)
	}
	if left, err := srv.Apps.AppEvents(ctx, ""); err != nil || len(left) != 0 {
		t.Fatalf("after uninstall: %v %v", left, err)
	}
}
