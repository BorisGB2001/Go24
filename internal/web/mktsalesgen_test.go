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

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

func TestSalesGenerator(t *testing.T) {
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
	var anna, boris, vera string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('vera', 'Вера', 'vera@t') RETURNING id`).Scan(&vera))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Marketing = marketing.NewStore(pool, srv.CRM)

	// clients: a company, its contact Иван (Бориса), Пётр with a deal in work, Мария
	co, err := srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "ООО Ромашка", Phone: "+7 900 000-00-01", Responsible: crm.Ref{ID: anna}})
	must(err)
	ivan, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Иван", Phone: "+7 900 111-11-11", Email: "ivan@x.test", Company: crm.Ref{ID: co},
		Responsible: crm.Ref{ID: boris}})
	must(err)
	petr, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Пётр", Responsible: crm.Ref{ID: anna}})
	must(err)
	_, err = srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Мария"})
	must(err)
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe := pipes[0]
	_, err = srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Открытая", PipelineID: pipe.ID, Contact: crm.Ref{ID: petr}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user}))
		return rec.Result().Cookies()[0]
	}
	annaC, borisC := cookieOf(anna), cookieOf(boris)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(c *http.Cookie, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(c)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(b)
	}
	idOf := func(loc string) string { return strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/marketing/salesgen/") }

	do(annaC, "POST", "/marketing/segments/new", url.Values{"name": {"Контакты"}, "entity": {"contact"}})
	do(annaC, "POST", "/marketing/segments/new", url.Values{"name": {"Компании"}, "entity": {"company"}})
	do(annaC, "POST", "/marketing/segments/new", url.Values{"name": {"Адреса"}, "entity": {"list"}, "emails": {"x@list.test"}})
	segs, err := srv.Marketing.Segments(ctx)
	must(err)
	seg := map[string]string{}
	for _, g := range segs {
		seg[g.Name] = g.ID
	}

	// the editor offers segments of contacts and companies only, and the deal pipelines' stages in work
	if code, _, body := do(annaC, "GET", "/marketing/salesgen/new", nil); code != http.StatusOK || !strings.Contains(body, "Контакты") ||
		strings.Contains(body, "Адреса") || !strings.Contains(body, `value="`+pipe.ID+":") {
		t.Fatalf("editor: %d", code)
	}
	stage := pipe.Stages[1] // the second stage of the pipeline
	// deals for the contacts: Пётр has a deal in work and is skipped; Иван's deal goes to Борис (his responsible) with Иван's company
	code, loc, _ := do(annaC, "POST", "/marketing/salesgen/new", url.Values{"title": {"Продление"}, "segment": {seg["Контакты"]}, "target": {"deal"},
		"place": {pipe.ID + ":" + stage.ID}, "card_title": {"Продление 2027 — {client}"}, "comment": {"Предложить тариф"}, "skip_open": {"1"}, "assign": {"owner"}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "/marketing/salesgen/") {
		t.Fatalf("save: %d %s", code, loc)
	}
	gid := idOf(loc)
	if _, _, body := do(annaC, "GET", "/marketing/salesgen/"+gid, nil); !strings.Contains(body, "Будет создано карточек: 2") {
		t.Fatal("the count of a draft")
	}
	if code, _, _ := do(borisC, "POST", "/marketing/salesgen/"+gid+"/delete", url.Values{}); code == http.StatusSeeOther {
		t.Fatal("another's generator deleted")
	}
	_, loc, _ = do(annaC, "POST", "/marketing/salesgen/"+gid, url.Values{"title": {"Продление"}, "segment": {seg["Контакты"]}, "target": {"deal"},
		"place": {pipe.ID + ":" + stage.ID}, "card_title": {"Продление 2027 — {client}"}, "comment": {"Предложить тариф"}, "skip_open": {"1"}, "assign": {"owner"},
		"go": {"start"}})
	if !strings.Contains(loc, "msg=") {
		t.Fatalf("start: %s", loc)
	}
	g, err := srv.Marketing.SalesGen(ctx, gid)
	must(err)
	if g.Status != "done" || g.Total != 2 || g.Made != 2 || g.Failed != 0 || g.InWork() != 2 {
		t.Fatalf("generator: %+v", g)
	}
	var title, stageID, resp, company, comment string
	must(pool.QueryRow(ctx, `SELECT d.title, d.stage_id::text, d.responsible_id::text, coalesce(d.company_id::text, ''), d.comment FROM crm_deals d
		WHERE d.contact_id::text = $1`, ivan).Scan(&title, &stageID, &resp, &company, &comment))
	if title != "Продление 2027 — Иван" || stageID != stage.ID || resp != boris || company != co || comment != "Предложить тариф" {
		t.Fatalf("Иван's deal: %s %s %s %s %s", title, stageID, resp, company, comment)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_deals WHERE contact_id::text = $1`, petr).Scan(&n))
	if n != 1 {
		t.Fatalf("Пётр with a deal in work got another: %d", n)
	}
	// Мария has no responsible: her deal is the starter's; Борис is told once
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id::text = $1 AND kind = 'crm_salesgen'`, boris).Scan(&n))
	if n != 1 {
		t.Fatalf("Борис told %d times", n)
	}
	do(annaC, "POST", "/marketing/salesgen/"+gid, url.Values{"title": {"Другое"}})
	if g2, _ := srv.Marketing.SalesGen(ctx, gid); g2.Title != "Продление" {
		t.Fatal("a started generator changed")
	}

	// one deal won: the progress shows it; the list of the won has Иван only
	var dealID, won string
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_deals WHERE contact_id::text = $1`, ivan).Scan(&dealID))
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_stages WHERE pipeline_id::text = $1 AND semantic = 'success'`, pipe.ID).Scan(&won))
	_, err = pool.Exec(ctx, `UPDATE crm_deals SET stage_id = $2 WHERE id::text = $1`, dealID, won)
	must(err)
	if g, _ = srv.Marketing.SalesGen(ctx, gid); g.Won != 1 || g.InWork() != 1 {
		t.Fatalf("won: %+v", g)
	}
	if _, _, body := do(annaC, "GET", "/marketing/salesgen/"+gid+"?state=won", nil); !strings.Contains(body, "Продление 2027 — Иван") ||
		strings.Contains(body, "Продление 2027 — Мария") {
		t.Fatal("the won list")
	}
	if _, _, body := do(annaC, "GET", "/marketing/salesgen", nil); !strings.Contains(body, "Продление") || !strings.Contains(body, "50%") {
		t.Fatal("the list of generators")
	}

	// repeat leads for the companies by the chosen employees: linked to the client, with its phone
	_, loc, _ = do(annaC, "POST", "/marketing/salesgen/new", url.Values{"title": {"Повтор"}, "segment": {seg["Компании"]}, "target": {"lead"},
		"skip_open": {"1"}, "assign": {"staff"}, "staff": {vera}, "go": {"start"}})
	g, err = srv.Marketing.SalesGen(ctx, idOf(loc))
	must(err)
	if g.Made != 1 || g.Target != "lead" {
		t.Fatalf("leads: %+v %s", g, loc)
	}
	var lt, lresp, lco, lphone string
	var repeat bool
	must(pool.QueryRow(ctx, `SELECT title, responsible_id::text, coalesce(company_id::text, ''), phone, is_repeat FROM crm_leads`).Scan(&lt, &lresp, &lco, &lphone, &repeat))
	if lt != "Повтор: ООО Ромашка" || lresp != vera || lco != co || !repeat || lphone == "" {
		t.Fatalf("lead: %s %s %s %s %v", lt, lresp, lco, lphone, repeat)
	}
	// the company now has a lead in work: a second generator skips it
	_, loc, _ = do(annaC, "POST", "/marketing/salesgen/new", url.Values{"title": {"Ещё"}, "segment": {seg["Компании"]}, "target": {"lead"},
		"skip_open": {"1"}, "assign": {"owner"}, "go": {"start"}})
	if !strings.Contains(loc, "/marketing/salesgen/") {
		t.Fatalf("a second generator: %s", loc)
	}
	if g, _ = srv.Marketing.SalesGen(ctx, idOf(loc)); g.Status != "draft" {
		t.Fatalf("nobody to sell to, yet started: %+v", g)
	}

	// the worker makes the rest: a draft started with more clients than a round
	gen := marketing.SalesGen{Title: "Много", SegmentID: seg["Контакты"], Target: "deal", PipelineID: pipe.ID}
	for i := 0; i < 120; i++ {
		_, err := srv.CRM.SaveContact(ctx, anna, crm.Contact{Name: "Клиент"})
		must(err)
	}
	id, err := srv.Marketing.SaveSalesGen(ctx, anna, false, gen)
	must(err)
	total, err := srv.Marketing.StartSalesGen(ctx, anna, false, id)
	must(err)
	if g, _ = srv.Marketing.SalesGen(ctx, id); g.Status != "started" || g.Made != 100 || total != 123 {
		t.Fatalf("a round at once: %d %+v", total, g)
	}
	_, err = srv.Marketing.MakeSalesGenCards(ctx, "")
	must(err)
	if g, _ = srv.Marketing.SalesGen(ctx, id); g.Status != "done" || g.Made != 123 {
		t.Fatalf("the worker's round: %+v", g)
	}
	// removing a generator keeps its deals
	if code, _, _ := do(annaC, "POST", "/marketing/salesgen/"+id+"/delete", url.Values{}); code != http.StatusSeeOther {
		t.Fatal("delete")
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_deals`).Scan(&n))
	if n != 1+2+123 {
		t.Fatalf("deals after delete: %d", n)
	}
}
