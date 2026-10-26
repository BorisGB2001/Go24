package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/marketing"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestMarketingAds: the advertising channels, their expenses (by hand, from a CSV, by REST), the UTM tags of the
// cards (the card's form, REST, a deal of a lead) and «Сквозная аналитика» counting them.
func TestMarketingAds(t *testing.T) {
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
	var boss, anna, boris string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Босс', 'boss@t', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boris', 'Борис', 'boris@t') RETURNING id`).Scan(&boris))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool), AdminGroups: []string{"go24-admins"},
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)), BaseURL: "http://go24.test/"}
	srv.Marketing = marketing.NewStore(pool, srv.CRM)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(s auth.Session) string {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, s))
		return "cookie:" + rec.Result().Cookies()[0].String()
	}
	bossC, annaC, borisC := cookieOf(auth.Session{UserID: boss, Admin: true, AdminMode: true}), cookieOf(auth.Session{UserID: anna}), cookieOf(auth.Session{UserID: boris})
	key, err := srv.Keys.Create(ctx, anna, "ads", []string{"crm:read", "crm:write"}, 0)
	must(err)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	// send: who is a cookie or an API key; body — url.Values, a JSON value or a ready request body (with ctype)
	send := func(who, method, path string, body any, ctype string) (int, string, string) {
		t.Helper()
		var rd io.Reader
		switch b := body.(type) {
		case nil:
		case url.Values:
			rd, ctype = strings.NewReader(b.Encode()), "application/x-www-form-urlencoded"
		case *bytes.Buffer:
			rd = b
		default:
			raw, _ := json.Marshal(b)
			rd, ctype = bytes.NewReader(raw), "application/json"
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		if c, ok := strings.CutPrefix(who, "cookie:"); ok {
			req.Header.Set("Cookie", c)
		} else {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(raw)
	}
	page := func(who, path string) string {
		t.Helper()
		code, loc, body := send(who, "GET", path, nil, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s %.300s", path, code, loc, body)
		}
		return body
	}
	var advertising string
	must(pool.QueryRow(ctx, `SELECT id::text FROM crm_dict WHERE kind = 'source' AND code = 'ADVERTISING'`).Scan(&advertising))

	// the channels: an administrator's; an employee may not add one
	send(annaC, "POST", "/marketing/ads/sources/new", url.Values{"name": {"Чужой"}}, "")
	send(bossC, "POST", "/marketing/ads/sources/new", url.Values{"name": {"Яндекс Директ"}, "utm": {"Yandex, ya.direct yandex"}, "color": {"#ff0000"}}, "")
	send(bossC, "POST", "/marketing/ads/sources/new", url.Values{"name": {"VK Реклама"}, "crm_source": {advertising}}, "")
	if code, _, _ := send(bossC, "POST", "/marketing/ads/sources/new", url.Values{"name": {"Плохой"}, "color": {"red"}}, ""); code != http.StatusSeeOther {
		t.Fatalf("a wrong color: %d", code)
	}
	srcs, err := srv.Marketing.AdSources(ctx)
	must(err)
	if len(srcs) != 2 || srcs[0].Name != "Яндекс Директ" || strings.Join(srcs[0].UTM, ",") != "yandex,ya.direct" || srcs[1].CRMSource != advertising {
		t.Fatalf("sources: %+v", srcs)
	}
	direct, vk := srcs[0].ID, srcs[1].ID

	// the expenses: by hand, from a CSV (a wrong file writes nothing), by REST
	today := time.Now().Format(time.DateOnly)
	send(annaC, "POST", "/marketing/ads/expenses", url.Values{"source": {direct}, "day": {today}, "amount": {"1 000,50"}, "campaign": {"autumn"}}, "")
	upload := func(csv string) string {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", "x.csv")
		fw.Write([]byte(csv))
		mw.Close()
		_, loc, _ := send(annaC, "POST", "/marketing/ads/expenses", &b, mw.FormDataContentType())
		return loc
	}
	count := func() int {
		t.Helper()
		l, err := srv.Marketing.Expenses(ctx, "", time.Time{}, time.Time{}, 100)
		must(err)
		return len(l)
	}
	if loc := upload("Дата;Источник;Сумма\n" + today + ";VK Реклама;300\n" + today + ";нет такого;5\n"); count() != 1 || !strings.Contains(loc, url.QueryEscape("строке 3")) {
		t.Fatalf("a wrong line: %d %s", count(), loc)
	}
	if loc := upload("\ufeffДата;Источник;Сумма;Валюта;Кампания\n" + time.Now().Format("02.01.2006") + ";vk реклама;300;RUB;\n" + today + ";YANDEX;499,50;;\n"); count() != 3 {
		t.Fatalf("import: %s", loc)
	}
	if code, _, body := send(key, "POST", "/api/v1/marketing/expenses", map[string]any{"source_id": direct, "day": "2001-01-01", "amount": 1e6}, ""); code != http.StatusCreated {
		t.Fatalf("REST expense: %d %s", code, body)
	}
	if code, _, _ := send(key, "POST", "/api/v1/marketing/expenses", map[string]any{"source_id": direct, "day": today, "amount": -1}, ""); code == http.StatusCreated {
		t.Fatal("a negative expense")
	}
	list, err := srv.Marketing.Expenses(ctx, "", time.Time{}, time.Time{}, 100)
	must(err)
	if len(list) != 4 {
		t.Fatalf("expenses: %+v", list)
	}
	// not the author's: boris may not delete it
	send(borisC, "POST", "/marketing/ads/expenses/"+list[len(list)-1].ID+"/delete", url.Values{}, "")
	if l, _ := srv.Marketing.Expenses(ctx, "", time.Time{}, time.Time{}, 100); len(l) != 4 {
		t.Fatal("boris deleted anna's expense")
	}
	if !strings.Contains(page(annaC, "/marketing/ads?source="+direct), "autumn") {
		t.Fatal("the expenses page")
	}

	// the cards: a lead by REST with tags, a lead of the card's form, a lead of the «Реклама» source, one of nothing
	code, _, body := send(key, "POST", "/api/v1/crm/leads", map[string]any{"title": "Из Директа", "utm": map[string]string{"utm_source": "Yandex", "utm_medium": "cpc", "utm_campaign": "autumn"}}, "")
	var card struct {
		ID  string
		UTM *apiUTM
	}
	must(json.Unmarshal([]byte(body), &card))
	if code != http.StatusCreated || card.UTM == nil || card.UTM.Source != "Yandex" || card.UTM.Campaign != "autumn" {
		t.Fatalf("REST lead: %d %s", code, body)
	}
	_, loc, _ := send(annaC, "POST", "/crm/leads/new", url.Values{"title": {"С формы"}, "responsible": {anna}, "with_utm": {"1"}, "utm_source": {"ya.direct"}}, "")
	lead2 := strings.TrimPrefix(strings.SplitN(loc, "?", 2)[0], "/crm/leads/")
	if u, _, _ := srv.Marketing.UTMOf(ctx, "lead", lead2); u.Source != "ya.direct" {
		t.Fatalf("the card's form: %s %+v", loc, u)
	}
	_, err = srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Из VK", Origin: crm.Dict{ID: advertising}, Responsible: crm.Ref{ID: anna}})
	must(err)
	_, err = srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Просто", Responsible: crm.Ref{ID: anna}})
	must(err)
	// a deal of the REST lead, won; a deal of the form's lead takes its tags
	deal1, err := srv.CRM.ConvertLead(ctx, anna, card.ID)
	must(err)
	var won string
	must(pool.QueryRow(ctx, `SELECT st.id::text FROM crm_stages st JOIN crm_pipelines p ON p.id = st.pipeline_id WHERE st.semantic = 'success' AND p.type_id IS NULL LIMIT 1`).Scan(&won))
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	must2(pool.Exec(ctx, `UPDATE crm_deals SET amount = 3000, currency = 'RUB', stage_id = $2 WHERE id::text = $1`, deal1, won))
	deal2, err := srv.CRM.ConvertLead(ctx, anna, lead2)
	must(err)
	if u, inherited, _ := srv.Marketing.UTMOf(ctx, "deal", deal2); u.Source != "ya.direct" || !inherited {
		t.Fatalf("the deal's tags: %+v %v", u, inherited)
	}
	if b := page(annaC, "/crm/deals/"+deal2); !strings.Contains(b, "utm_source=ya.direct") || !strings.Contains(b, "из лида") {
		t.Fatal("the deal's card has no tags of its lead")
	}
	// tags of its own by REST, then none again
	if code, _, body := send(key, "PUT", "/api/v1/crm/deals/"+deal2+"/utm", map[string]string{"utm_source": "yandex", "utm_campaign": "winter"}, ""); code != http.StatusOK || !strings.Contains(body, `"utm_campaign": "winter"`) {
		t.Fatalf("PUT utm: %d %s", code, body)
	}
	if u, inherited, _ := srv.Marketing.UTMOf(ctx, "deal", deal2); u.Campaign != "winter" || inherited {
		t.Fatalf("own tags: %+v", u)
	}

	// «Сквозная аналитика»
	var rep struct {
		Currency string
		Channels []apiChannel
		Total    apiChannel
	}
	code, _, body = send(key, "GET", "/api/v1/marketing/analytics?from="+today+"&to="+today, nil, "")
	must(json.Unmarshal([]byte(body), &rep))
	if code != http.StatusOK || len(rep.Channels) != 3 {
		t.Fatalf("analytics: %d %s", code, body)
	}
	d, v, none := rep.Channels[0], rep.Channels[1], rep.Channels[2]
	if d.Name != "Яндекс Директ" || d.Expenses != 1500 || d.Leads != 2 || d.Deals != 2 || d.Won != 1 || d.Revenue != 3000 || d.ROI == nil || *d.ROI != 100 || d.CostPerLead != 750 {
		t.Fatalf("Директ: %+v", d)
	}
	if len(d.Campaigns) != 3 || d.Campaigns[0].Name != "autumn" || d.Campaigns[0].Expenses != 1000.5 || d.Campaigns[0].Won != 1 {
		t.Fatalf("the campaigns of Директ: %+v", d.Campaigns)
	}
	if v.Expenses != 300 || v.Leads != 1 || v.Deals != 0 || *v.ROI != -100 {
		t.Fatalf("VK: %+v", v)
	}
	if none.ID != "" || none.Leads != 1 || none.Expenses != 0 || none.ROI != nil {
		t.Fatalf("no channel: %+v", none)
	}
	if rep.Total.Expenses != 1800 || rep.Total.Leads != 4 || rep.Total.Revenue != 3000 {
		t.Fatalf("total: %+v", rep.Total)
	}
	if b := page(annaC, "/marketing/analytics?from="+today+"&to="+today); !strings.Contains(b, "Яндекс Директ") || !strings.Contains(b, "&#43;100%") || !strings.Contains(b, "winter") {
		t.Fatal("the analytics page")
	}

	// a channel deleted takes its expenses
	send(bossC, "POST", "/marketing/ads/sources/"+vk, url.Values{"delete": {"1"}}, "")
	if l, _ := srv.Marketing.Expenses(ctx, "", time.Time{}, time.Time{}, 100); len(l) != 3 {
		t.Fatalf("expenses after the channel: %d", len(l))
	}
}
