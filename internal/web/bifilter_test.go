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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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

// TestBIFilters: «Фильтры дашборда» picked in its settings show beside the period with the values of the data; a
// value picked, or clicked on a chart, narrows every widget whose set has the field (the others are marked), in the
// CSV and the link too, and is taken off by its chip.
func TestBIFilters(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	bundle, err := i18n.Load("")
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, I18n: bundle, Portal: portal.NewStore(pool, bundle), Drive: drive.NewStore(pool, ts),
		Company: company.NewStore(pool), CRM: crm.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.BI = bi.NewStore(pool, srv.CRM)
	defer bundle.SetFilter(nil)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	for who, a := range map[string]float64{anna: 1000, ivan: 250} {
		_, err := srv.CRM.SaveDeal(ctx, who, crm.Deal{Title: "Сделка", PipelineID: pipes[0].ID, Amount: a, Responsible: crm.Ref{ID: who}})
		must(err)
	}
	dash, err := srv.BI.SaveDashboard(ctx, anna, bi.Dashboard{Title: "Продажи", Period: "year"})
	must(err)
	widget := func(kind string, q bi.Query) string {
		id, err := srv.BI.SaveWidget(ctx, bi.Widget{DashboardID: dash, Kind: kind, Width: 6, Query: q})
		must(err)
		return id
	}
	sum := []bi.Measure{{Agg: "sum", Field: "amount"}}
	widget("kpi", bi.Query{Dataset: "crm_deal", Measures: sum})
	bar := widget("bar", bi.Query{Dataset: "crm_deal", Dims: []string{"responsible"}, Measures: sum})
	widget("line", bi.Query{Dataset: "crm_deal", Dims: []string{"created_at:month"}, Measures: sum})
	widget("kpi", bi.Query{Dataset: "timeman", Measures: []bi.Measure{{Agg: "count"}}})

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
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
	hasnt := func(page string, parts ...string) {
		t.Helper()
		for _, p := range parts {
			if strings.Contains(page, p) {
				t.Fatalf("%q on the page", p)
			}
		}
	}
	path := "/bi/" + dash

	// the settings offer the fields of the widgets' sets, no dates; the filters picked are saved
	settings := send("GET", path+"?settings=1", nil, 200)
	has(settings, "Фильтры дашборда", `name="filters" value="responsible"`, `name="filters" value="employee"`)
	hasnt(settings, `name="filters" value="created_at"`)
	send("POST", path, url.Values{"title": {"Продажи"}, "period": {"year"}, "filters": {"responsible", "pipeline"}}, 303)

	// the dashboard: the filters with the values of the data; a chart's columns filter it by a click
	page := send("GET", path, nil, 200)
	has(page, `data-bi-filter="f_responsible"`, `<option value="Анна" >Анна</option>`, `data-bi-filter="f_pipeline"`,
		`data-bi-f="f_responsible" data-bi-v="Анна"`, `data-bi-f="f_created_at" data-bi-v="month:`, "1 250")
	hasnt(page, "Сбросить фильтры", "Без фильтра")

	// a value picked: the deals' widgets count Анна's, the working time is not filtered; the chip takes it off
	page = send("GET", path+"?f_responsible=Анна", nil, 200)
	has(page, "<b>1 000</b>", "Ответственный: Анна", `<option value="Анна" selected>`, "Без фильтра: Ответственный",
		"Сбросить фильтры", `href="/bi/`+dash+`"`, `/csv?f_responsible=%D0%90%D0%BD%D0%BD%D0%B0"`,
		`name="f_responsible" value="Анна"`)
	hasnt(page, "1 250")
	csv := send("GET", "/bi/w/"+bar+"/csv?f_responsible=Анна", nil, 200)
	if !strings.Contains(csv, "Анна;1000") || strings.Contains(csv, "Иван") {
		t.Fatalf("csv %q", csv)
	}
	// two values clicked: both; a month clicked: the date's chip
	has(send("GET", path+"?f_responsible=Анна&f_responsible=Иван", nil, 200), "<b>1 250</b>", "выбрано 2")
	month, _ := bi.Span("month", nil, nil, time.Now())
	has(send("GET", path+"?f_created_at=month:"+month.Format("2006-01-02"), nil, 200), "Дата создания: ", "<b>1 250</b>")
	has(send("GET", path+"?f_created_at=month:2000-01-01", nil, 200), "<b>0</b>")
	// a filter of a field no widget has is dropped
	hasnt(send("GET", path+"?f_nothing=1", nil, 200), "Сбросить фильтры")
}
