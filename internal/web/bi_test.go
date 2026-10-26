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
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/bi"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/portal"
	"go24/internal/tasks"
)

// TestBIHTTP: «BI-аналитика» — a dashboard made, its widgets built in the editor (the chart redrawn by each change of
// the form) and saved, shown over the period picked, ordered, widened, copied and removed; others see it and copy
// it, but only the author and the administrators change it.
func TestBIHTTP(t *testing.T) {
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
	var boss, anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	bundle, err := i18n.Load("")
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, I18n: bundle, Portal: portal.NewStore(pool, bundle), Drive: drive.NewStore(pool, ts),
		Company: company.NewStore(pool), CRM: crm.NewStore(pool),
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.BI = bi.NewStore(pool, srv.CRM)
	defer bundle.SetFilter(nil)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	for _, a := range []float64{1000, 500.5} {
		_, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Сделка", PipelineID: pipes[0].ID, Amount: a, Responsible: crm.Ref{ID: anna}})
		must(err)
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), anna: cookie(anna, false), ivan: cookie(ivan, false)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var where string // the Location of the last redirect
	send := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("Accept-Language", "ru")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		where = res.Header.Get("Location")
		return string(raw)
	}
	has := func(page string, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if !strings.Contains(page, p) {
				t.Fatalf("no %q on the page", p)
			}
		}
	}

	has(send(anna, "GET", "/bi", nil, 200), "BI-аналитика", "Дашбордов пока нет")
	send(anna, "POST", "/bi", url.Values{"title": {" "}}, 303)
	if !strings.Contains(where, "msg=") {
		t.Fatalf("no title: %s", where)
	}
	send(anna, "POST", "/bi", url.Values{"title": {"Продажи"}, "period": {"year"}}, 303)
	id := regexp.MustCompile(`^/bi/([0-9a-f-]{36})\?edit=1$`).FindStringSubmatch(where)
	if id == nil {
		t.Fatalf("created: %s", where)
	}
	dash := "/bi/" + id[1]
	has(send(anna, "GET", dash+"?edit=1", nil, 200), "На дашборде пока нет виджетов", dash+"/widget?kind=kpi")

	// the editor: a new widget of a kind; a function picked takes its first field; the chart is drawn as typed
	has(send(anna, "GET", dash+"/widget?kind=pie", nil, 200), `value="pie" checked`, "bi-pie")
	ed := send(anna, "GET", dash+"/widget?dataset=crm_deal&kind=bar&dim=stage&agg0=sum&agg1=count&ffield0=amount&fop0=eq", nil, 200)
	has(ed, `<option value="amount" selected>`, "<rect", `name="fvalue0"`, "Сделки: Сумма")
	has(send(anna, "GET", dash+"/widget?dataset=crm_deal&kind=kpi&agg0=sum&mfield0=amount&ffield0=amount&fop0=gt&fvalue0=600", nil, 200), "<b>1 000</b>")

	save := func(form url.Values) string {
		t.Helper()
		send(anna, "POST", dash+"/widget", form, 303)
		m := regexp.MustCompile(`#w-([0-9a-f-]{36})$`).FindStringSubmatch(where)
		if m == nil {
			t.Fatalf("saved: %s", where)
		}
		return m[1]
	}
	kpi := save(url.Values{"dataset": {"crm_deal"}, "kind": {"kpi"}, "width": {"3"}, "title": {"Сделок"}, "agg0": {"count"}, "agg1": {"sum"}, "mfield1": {"amount"}})
	bar := save(url.Values{"dataset": {"crm_deal"}, "kind": {"bar"}, "width": {"6"}, "dim": {"created_at"}, "grain": {"month"}, "agg0": {"sum"}, "mfield0": {"amount"}})
	all := save(url.Values{"dataset": {"crm_deal"}, "kind": {"table"}, "width": {"12"}, "dim": {"stage"}, "agg0": {"count"}, "no_period": {"1"}})
	w, err := srv.BI.Widget(ctx, bar)
	must(err)
	if w.Query.Dims[0] != "created_at:month" || w.Query.Measures[0] != (bi.Measure{Agg: "sum", Field: "amount"}) || w.Width != 6 {
		t.Fatalf("bar %+v", w)
	}
	page := send(anna, "GET", dash, nil, 200)
	has(page, "Сделок", "<b>2</b>", "1 500,5", "<svg", `id="w-`+all+`"`, "Этот год")
	if strings.Contains(page, "bi-tools") {
		t.Fatal("tools without ?edit=1")
	}
	// another period: the widgets with the period count nothing, the one without counts all
	page = send(anna, "GET", dash+"?period=prev_year", nil, 200)
	has(page, "<b>0</b>", "Нет данных за период", "<td class=\"num\">2</td>")
	send(anna, "GET", dash+"?period=range&from=2020-01-01&to=2020-12-31", nil, 200)

	// «Экспорт в CSV» of a widget: over the period of the page, the measures as numbers; «Печать», «Скопировать
	// ссылку» with the period
	has(page, `href="/bi/w/`+bar+`/csv?period=prev_year"`, "data-bi-print", `data-copy="`+dash+`?period=prev_year"`)
	csvBar := send(anna, "GET", "/bi/w/"+bar+"/csv", nil, 200)
	has(csvBar, "\uFEFF", "Сумма", "1500,5")
	if strings.Contains(send(anna, "GET", "/bi/w/"+bar+"/csv?period=prev_year", nil, 200), "1500") {
		t.Fatal("csv ignores the period")
	}
	if c := send(anna, "GET", "/bi/w/"+all+"/csv?period=prev_year", nil, 200); !strings.Contains(c, ";2\n") {
		t.Fatalf("csv %q", c)
	}
	send(ivan, "GET", "/bi/w/"+bar+"/csv", nil, 404) // a draft of someone else

	// changing a widget in the editor keeps it in its place
	has(send(anna, "GET", dash+"/widget?w="+kpi, nil, 200), `value="Сделок"`)
	send(anna, "POST", dash+"/widget?w="+kpi, url.Values{"dataset": {"crm_deal"}, "kind": {"kpi"}, "width": {"4"}, "title": {"Всего"}, "agg0": {"count"}}, 303)
	if w, _ := srv.BI.Widget(ctx, kpi); w.Title != "Всего" || w.Width != 4 || w.Pos != 0 {
		t.Fatalf("kpi %+v", w)
	}

	// the tools: width, order, copy, delete; the order dragged
	send(anna, "POST", "/bi/w/"+bar+"/width", url.Values{"width": {"12"}}, 303)
	send(anna, "POST", "/bi/w/"+all+"/up", nil, 303)
	order := func() string {
		list, err := srv.BI.Widgets(ctx, id[1])
		must(err)
		var ids []string
		for _, x := range list {
			ids = append(ids, x.ID)
		}
		return strings.Join(ids, ",")
	}
	if got := order(); got != kpi+","+all+","+bar {
		t.Fatalf("order %s", got)
	}
	send(anna, "POST", dash+"/order", url.Values{"ids": {bar, kpi, all}}, 303)
	if got := order(); got != bar+","+kpi+","+all {
		t.Fatalf("dragged %s", got)
	}
	send(anna, "POST", "/bi/w/"+kpi+"/copy", nil, 303)
	send(anna, "POST", "/bi/w/"+kpi+"/delete", nil, 303)
	send(anna, "POST", "/bi/w/"+kpi+"/spin", nil, 404)
	if got := strings.Count(order(), ","); got != 2 {
		t.Fatalf("widgets after copy and delete: %s", order())
	}

	// a draft is its author's (and the administrators'); published, everyone sees it
	send(ivan, "GET", dash, nil, 404)
	has(send(anna, "GET", "/bi", nil, 200), "Черновик")
	has(send(anna, "GET", dash+"?settings=1", nil, 200), "Права доступа", `action="`+dash+`/publish"`, `action="`+dash+`/rights"`)
	send(ivan, "POST", dash+"/publish", url.Values{"status": {"published"}}, 404)
	send(anna, "POST", dash+"/publish", url.Values{"status": {"published"}}, 303)

	// others: see and copy, not change; administrators change
	has(send(ivan, "GET", dash, nil, 200), "Продажи")
	if strings.Contains(send(ivan, "GET", dash+"?edit=1", nil, 200), "bi-tools") {
		t.Fatal("tools for someone else")
	}
	send(ivan, "GET", dash+"/widget", nil, 403)
	send(ivan, "POST", dash, url.Values{"title": {"Моё"}}, 403)
	send(ivan, "POST", "/bi/w/"+bar+"/delete", nil, 403)
	send(ivan, "POST", dash+"/delete", nil, 403)
	send(ivan, "POST", dash+"/copy", nil, 303)
	if !strings.HasSuffix(where, "?edit=1") || strings.Contains(where, id[1]) {
		t.Fatalf("copy: %s", where)
	}
	has(send(ivan, "GET", strings.TrimSuffix(where, "?edit=1"), nil, 200), "Продажи (копия)", "bi-w")
	send(boss, "POST", dash, url.Values{"title": {"Продажи 2026"}, "period": {"range"}, "date_from": {"2026-01-01"}, "date_to": {"2026-12-31"}}, 303)
	if d, _ := srv.BI.Dashboard(ctx, bi.Viewer{Admin: true}, id[1]); d.Title != "Продажи 2026" || d.Period != "range" || d.DateTo == nil {
		t.Fatalf("settings %+v", d)
	}
	has(send(anna, "GET", "/bi?q=2026", nil, 200), "Продажи 2026")

	// a group: its maker manages it; its lines close its dashboards to the others; a line of the dashboard lets
	// an employee change it, not manage it
	send(ivan, "POST", "/bi/groups", url.Values{"title": {"Финансы"}}, 303)
	gpage := where
	gid := strings.TrimPrefix(gpage, "/bi/groups?id=")
	has(send(ivan, "GET", "/bi/groups", nil, 200), "Финансы")
	has(send(ivan, "GET", gpage, nil, 200), "Права доступа", "/bi/groups/"+gid+"/rights")
	send(anna, "GET", gpage, nil, 403)
	send(anna, "POST", "/bi/groups/"+gid+"/rename", url.Values{"title": {"x"}}, 403)
	send(anna, "POST", dash+"/publish", url.Values{"status": {"published"}, "group": {gid}}, 303)
	has(send(anna, "GET", "/bi?group="+gid, nil, 200), "Продажи 2026", "Финансы")
	send(ivan, "POST", "/bi/groups/"+gid+"/rights", url.Values{"whom": {"user:" + anna}, "access": {"view"}}, 303)
	send(ivan, "GET", dash, nil, 404)
	send(anna, "POST", dash+"/rights", url.Values{"whom": {"user:" + ivan}, "access": {"edit"}}, 303)
	has(send(ivan, "GET", dash+"?edit=1", nil, 200), "bi-tools")
	send(ivan, "POST", dash, url.Values{"title": {"Продажи 2026"}, "period": {"year"}}, 303)
	send(ivan, "POST", dash+"/delete", nil, 403)
	send(ivan, "POST", dash+"/rights", url.Values{"clear": {"1"}}, 403)
	send(anna, "POST", dash+"/rights", url.Values{"clear": {"1"}}, 303)
	send(ivan, "GET", dash, nil, 404)
	send(ivan, "POST", "/bi/groups/"+gid+"/delete", nil, 303)
	has(send(ivan, "GET", dash, nil, 200), "Продажи 2026")
	send(anna, "POST", dash+"/delete", nil, 303)
	send(anna, "GET", dash, nil, 404)

	// the ready dashboards: in the list with their titles, counted, only copied — with the widgets' titles in words
	must(srv.BI.EnsureSystem(ctx))
	has(send(anna, "GET", "/bi", nil, 200), "Готовые дашборды", "Лиды и источники", "Рабочее время")
	list, err := srv.BI.Dashboards(ctx, bi.Viewer{}, "", "")
	must(err)
	sales := "/bi/" + list[0].ID
	page = send(boss, "GET", sales, nil, 200)
	has(page, "<h1>Продажи</h1>", "Новые сделки", "Средний чек", "Копировать в свои", "1 500,5")
	if strings.Contains(page, "?edit=1") {
		t.Fatal("a ready dashboard offers editing")
	}
	send(boss, "POST", sales, url.Values{"title": {"x"}}, 403)
	send(boss, "POST", sales+"/delete", nil, 403)
	send(boss, "GET", sales+"/widget", nil, 403)
	send(anna, "POST", sales+"/copy", nil, 303)
	cp := strings.TrimSuffix(where, "?edit=1")
	has(send(anna, "GET", cp, nil, 200), "<h1>Продажи <span class=\"pill\">Черновик</span></h1>", "Новые сделки", `href="`+cp+`?edit=1"`)
	ws, err := srv.BI.Widgets(ctx, strings.TrimPrefix(cp, "/bi/"))
	must(err)
	if ws[0].Title != "Новые сделки" {
		t.Errorf("copied title %q", ws[0].Title)
	}

	// «Показывать в разделах»: the ready ones are in their sections at first; an administrator pins them, the author
	// pins their own; the section's «BI-аналитика» shows those the viewer sees, ?d= the one open
	crmBI := send(anna, "GET", "/crm/bi", nil, 200)
	has(crmBI, "<h1>CRM</h1>", `href="/crm/bi" class="on"`, "Продажи", "Лиды и источники", "1\u00a0500,5", `href="/bi/w/`)
	if strings.Contains(crmBI, "?d="+list[3].ID) { // «Рабочее время»
		t.Fatal("timeman in the CRM")
	}
	has(send(anna, "GET", "/tasks/bi", nil, 200), "Задачи и эффективность")
	has(send(anna, "GET", "/company/bi", nil, 200), "Рабочее время")
	send(anna, "POST", sales+"/pin", url.Values{"sections": {"tasks"}}, 403)
	has(send(boss, "GET", sales+"?pin=1", nil, 200), `action="`+sales+`/pin"`, `value="crm" checked`)
	send(boss, "POST", sales+"/pin", url.Values{"sections": {"tasks", "nowhere"}}, 303)
	if d, _ := srv.BI.Dashboard(ctx, bi.Viewer{}, list[0].ID); strings.Join(d.Sections, ",") != "tasks" {
		t.Fatalf("pinned %v", d.Sections)
	}
	has(send(anna, "GET", "/tasks/bi?d="+list[0].ID+"&period=year", nil, 200), `class="on">Продажи</a>`, `name="d" value="`+list[0].ID+`"`,
		`data-copy="/tasks/bi?d=`+list[0].ID+`&amp;period=year"`)
	send(anna, "POST", cp+"/pin", url.Values{"sections": {"crm"}}, 303) // a draft: only its author sees it there
	has(send(anna, "GET", "/crm/bi?d="+strings.TrimPrefix(cp, "/bi/"), nil, 200), "Черновик", "Новые сделки")
	if strings.Contains(send(ivan, "GET", "/crm/bi", nil, 200), strings.TrimPrefix(cp, "/bi/")) {
		t.Fatal("someone else's draft in the CRM")
	}
	send(boss, "POST", sales+"/pin", nil, 303)
	has(send(ivan, "GET", "/tasks/bi", nil, 200), "Задачи и эффективность")

	// keys for external BI: administrators only; the token shown once; the export by it in JSON and CSV, its sets
	// only; a personal key with bi:read reads with its owner's rights; a revoked key and that of a former
	// administrator read nothing
	send(anna, "GET", "/bi/keys", nil, 403)
	send(anna, "POST", "/bi/keys", url.Values{"name": {"x"}}, 403)
	has(send(boss, "GET", "/bi", nil, 200), `href="/bi/keys"`)
	has(send(boss, "GET", "/bi/keys", nil, 200), "Ключей пока нет", "filter=won:yes")
	page = send(boss, "POST", "/bi/keys", url.Values{"name": {"Power BI"}, "datasets": {"crm_deal"}}, 200)
	token := regexp.MustCompile(`value="(go24bi_[0-9a-f]{12}_[0-9a-f]{64})"`).FindStringSubmatch(page)
	if token == nil {
		t.Fatalf("no token: %.500s", page)
	}
	srv.Keys = apikeys.NewStore(pool)
	get := func(path, token string, want int) (string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("GET %s: %d, want %d: %.300s", path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header
	}
	sets, _ := get("/api/v1/bi/export?key="+token[1], "", 200)
	if !strings.Contains(sets, `"code": "crm_deal"`) || strings.Contains(sets, `"code": "task"`) {
		t.Fatalf("sets: %s", sets)
	}
	body, _ := get("/api/v1/bi/export/crm_deal?fields=title,amount,won&key="+token[1], "", 200)
	var rows []map[string]any
	must(json.Unmarshal([]byte(body), &rows))
	if len(rows) != 2 || rows[0]["title"] != "Сделка" || rows[0]["won"] != false || rows[0]["id"] == "" {
		t.Fatalf("json: %s", body)
	}
	csvBody, hdr := get("/api/v1/bi/export/crm_deal?format=csv&names=1&fields=title,amount&filter=amount:gt:600", token[1], 200)
	if !strings.HasPrefix(hdr.Get("Content-Type"), "text/csv") || csvBody != "Идентификатор строки,Название,Сумма\n"+rows0(rows, "amount", 1000)+",Сделка,1000\n" {
		t.Fatalf("csv: %q", csvBody)
	}
	_, hdr = get("/api/v1/bi/export/crm_deal?limit=1&key="+token[1], "", 200)
	if hdr.Get("X-Next-Offset") != "1" {
		t.Fatalf("next offset: %v", hdr)
	}
	get("/api/v1/bi/export/crm_deal?period=prev_year&key="+token[1], "", 200)
	get("/api/v1/bi/export/task?key="+token[1], "", 403)
	get("/api/v1/bi/export/crm_deal?filter=amount:drop&key="+token[1], "", 422)
	get("/api/v1/bi/export/crm_deal", "", 401)
	get("/api/v1/bi/export/crm_deal?key=go24bi_000000000000_"+strings.Repeat("0", 64), "", 401)
	// a personal key with bi:read; one without — refused
	full, err := srv.Keys.Create(ctx, anna, "bi", []string{"bi:read"}, 0)
	must(err)
	if body, _ := get("/api/v1/bi/export/crm_deal", full, 200); !strings.Contains(body, "Сделка") {
		t.Fatalf("personal: %s", body)
	}
	other, err := srv.Keys.Create(ctx, anna, "crm", []string{"crm:read"}, 0)
	must(err)
	get("/api/v1/bi/export/crm_deal", other, 403)
	// the author is no longer an administrator
	_, err = pool.Exec(ctx, `UPDATE users SET groups = '{}' WHERE id = $1`, boss)
	must(err)
	get("/api/v1/bi/export/crm_deal?key="+token[1], "", 401)
	_, err = pool.Exec(ctx, `UPDATE users SET groups = '{go24-admins}' WHERE id = $1`, boss)
	must(err)
	keys, err := srv.BI.Keys(ctx)
	must(err)
	has(send(boss, "GET", "/bi/keys", nil, 200), "Power BI", "Сделки", "127.0.0.1")
	send(boss, "POST", "/bi/keys/"+keys[0].ID+"/revoke", nil, 303)
	get("/api/v1/bi/export/crm_deal?key="+token[1], "", 401)
	send(boss, "POST", "/bi/keys/"+keys[0].ID+"/delete", nil, 303)
	if keys, _ = srv.BI.Keys(ctx); len(keys) != 0 {
		t.Fatalf("deleted: %v", keys)
	}
}

// rows0 is the id of the row whose field is v.
func rows0(rows []map[string]any, field string, v float64) string {
	for _, r := range rows {
		if r[field] == v {
			s, _ := r["id"].(string)
			return s
		}
	}
	return ""
}
