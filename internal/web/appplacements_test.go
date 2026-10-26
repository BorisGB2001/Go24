package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/apps"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestAppPlacementsHTTP: «Места встраивания», as placement.* — an application binds its handlers to the
// tabs of a deal's and a task's card with an administrator's token (only on its own host, only known places, only
// an application's token); the cards show the tabs to whom the application is for, the tab's frame posts the
// employee's tokens with PLACEMENT and PLACEMENT_OPTIONS of the card; unbinding and uninstalling take the tabs off.
func TestAppPlacementsHTTP(t *testing.T) {
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
	app := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer app.Close()

	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle, CRM: crm.NewStore(pool),
		Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool), Audit: audit.NewStore(pool), AdminGroups: []string{"go24-admins"},
		BaseURL: "https://go24.example", Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), anna: cookie(auth.Session{UserID: anna})}
	send := func(who, method, path, ctype, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if c := as[who]; c != nil {
			req.AddCookie(c)
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
	const js = "application/json"

	id, err := srv.Apps.Save(ctx, boss, apps.App{Name: "Доставка", HandlerURL: app.URL + "/page", MenuTitle: "Расчёт доставки", Scopes: []string{"crm:read", "placement"}})
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
	key, err := srv.Keys.Create(ctx, boss, "ключ", []string{"placement"}, 0)
	must(err)

	// placement.list; only an application calls the methods, only an administrator binds
	has(send(bossTok, "GET", "/api/v1/placements/list", "", "", 200), true, `"CRM_DEAL_DETAIL_TAB"`, `"TASK_VIEW_TAB"`)
	has(send(key, "GET", "/api/v1/placements", "", "", 403), true, "только приложению")
	send(annaTok, "POST", "/api/v1/placements", js, `{"placement": "CRM_DEAL_DETAIL_TAB", "handler": "`+app.URL+`/deal"}`, 403)
	has(send(bossTok, "POST", "/api/v1/placements", js, `{"placement": "CRM_DEAL_DETAIL_TAB", "handler": "https://evil.example/deal"}`, 422), true, "домене приложения")
	has(send(bossTok, "POST", "/api/v1/placements", js, `{"placement": "NOWHERE", "handler": "`+app.URL+`/deal"}`, 422), true, "NOWHERE")
	deal := send(bossTok, "POST", "/api/v1/placements", js, `{"placement": "crm_deal_detail_tab", "handler": "`+app.URL+`/deal", "title": "Доставка сделки"}`, 201)
	again := send(bossTok, "POST", "/api/v1/placements", js, `{"placement": "CRM_DEAL_DETAIL_TAB", "handler": "`+app.URL+`/deal", "title": "Расчёт по сделке", "description": "Стоимость и срок"}`, 201)
	dealTab := deal[strings.Index(deal, `"id": "`)+7:][:36]
	has(again, true, dealTab, "Расчёт по сделке")
	send(bossTok, "POST", "/api/v1/placements?placement=TASK_VIEW_TAB&handler="+url.QueryEscape(app.URL+"/task"), "", "", 201)
	list := send(bossTok, "GET", "/api/v1/placements", "", "", 200)
	if strings.Count(list, `"placement"`) != 2 || strings.Count(send(bossTok, "GET", "/api/v1/placements?placement=TASK_VIEW_TAB", "", "", 200), `"placement"`) != 1 {
		t.Fatalf("placement.get: %s", list)
	}
	var taskTab string
	must(pool.QueryRow(ctx, `SELECT id::text FROM app_placements WHERE placement = 'TASK_VIEW_TAB'`).Scan(&taskTab))
	has(send(boss, "GET", "/settings/apps", "", "", 200), true, "Места встраивания", "Вкладка карточки сделки", "Вкладка карточки задачи", app.URL+"/task")

	// the cards: a deal's and a task's, the tab opens the frame of the card
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	d, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Альфа", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	card := send(anna, "GET", "/crm/deals/"+d, "", "", 200)
	has(card, true, "data-app-tab", "Расчёт по сделке", "Стоимость и срок", `data-src="/apps/tab/`+dealTab+`?id=`+d+`"`)
	has(card, false, "/apps/tab/"+taskTab)
	frame := send(anna, "GET", "/apps/tab/"+dealTab+"?id="+d, "", "", 200)
	has(frame, true, `action="`+app.URL+`/deal?APP_SID=`, `name="PLACEMENT" value="CRM_DEAL_DETAIL_TAB"`,
		`name="PLACEMENT_OPTIONS" value="{&#34;ID&#34;:&#34;`+d+`&#34;}"`, `name="AUTH_ID" value="go24at_`, "data-submit-now")
	has(frame, false, `target="go24app"`, `class="nav`) // bare: no menu, the frame posts itself
	_, err = ts.CreateProject(ctx, boss, "DL", "Доставка", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "DL")
	must(err)
	must(ts.SetMember(ctx, p, anna, "member"))
	tk, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Отгрузка"})
	must(err)
	has(send(anna, "GET", "/t/"+tk, "", "", 200), true, `data-src="/apps/tab/`+taskTab+`?id=`+tk+`"`)
	has(send(anna, "GET", "/apps/tab/"+taskTab+"?id="+tk, "", "", 200), true, `{&#34;taskId&#34;:&#34;`+tk+`&#34;}`, app.URL+"/task?")

	// «Кому доступно» — only Борис: Анна sees no tab and cannot open it
	must(srv.Apps.SetAccess(ctx, a.ID, []string{boss}))
	has(send(anna, "GET", "/crm/deals/"+d, "", "", 200), false, "data-app-tab")
	send(anna, "GET", "/apps/tab/"+dealTab+"?id="+d, "", "", 404)
	has(send(boss, "GET", "/crm/deals/"+d, "", "", 200), true, "/apps/tab/"+dealTab)
	must(srv.Apps.SetAccess(ctx, a.ID, nil))

	// placement.unbind: by the handler, or all of the place; uninstalling takes the rest off
	bossTok = tokens(boss)
	has(send(bossTok, "DELETE", "/api/v1/placements?placement=CRM_DEAL_DETAIL_TAB&handler="+url.QueryEscape(app.URL+"/other"), "", "", 200), true, `"count": 0`)
	has(send(bossTok, "DELETE", "/api/v1/placements", js, `{"placement": "CRM_DEAL_DETAIL_TAB"}`, 200), true, `"count": 1`)
	has(send(anna, "GET", "/crm/deals/"+d, "", "", 200), false, "data-app-tab")
	send(anna, "GET", "/apps/tab/"+dealTab+"?id="+d, "", "", 404)
	must(srv.Apps.Uninstall(ctx, a.ID))
	if left, err := srv.Apps.Placements(ctx, ""); err != nil || len(left) != 0 {
		t.Fatalf("after uninstall: %v %v", left, err)
	}
}
