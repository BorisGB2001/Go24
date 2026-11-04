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
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/marketing"
	"go24/internal/tasks"
)

// TestPortalStage: a stage's id — built-in codes, custom UC_<n>, C<pipeline>: of the other pipelines.
func TestPortalStage(t *testing.T) {
	for st, want := range map[portalStage]string{
		{Code: "new", Entity: "deal"}:                                  "NEW",
		{Code: "prepayment", Entity: "deal", Category: 2}:              "C2:PREPAYMENT_INVOICE",
		{Num: 15, Entity: "deal"}:                                      "UC_15",
		{Num: 16, Entity: "deal", Category: 1}:                         "C1:UC_16",
		{Code: "lead_in_process", Entity: "lead"}:                      "IN_PROCESS",
		{Code: "lead_junk", Entity: "lead", Category: 0}:               "JUNK",
		{Code: "sp_client", Entity: "deal", Category: 5, TypeNum: 128}: "DT128_5:CLIENT",
	} {
		if got := st.Portal(); got != want {
			t.Errorf("%+v: %q, want %q", st, got, want)
		}
	}
	stages := []portalStage{{ID: "a", Code: "new", Entity: "deal", PipelineID: "p0"}, {ID: "b", Code: "new", Entity: "deal", Category: 1, PipelineID: "p1"}}
	for _, c := range []struct{ code, pipeline, want string }{{"NEW", "", "a"}, {"c1:new", "", "b"}, {"NEW", "p1", "b"}, {"C1:NEW", "p0", ""}} {
		st, _ := portalStageOf(stages, c.code, c.pipeline)
		if st.ID != c.want {
			t.Errorf("portalStageOf(%q, %q) = %q, want %q", c.code, c.pipeline, st.ID, c.want)
		}
	}
}

// TestPortalMulti: PHONE and EMAIL of update — ID changes or (DELETE=Y) removes that one, no ID adds one.
func TestPortalMulti(t *testing.T) {
	cur := []crm.Comm{{Type: "work", Value: "+7 1"}, {Type: "home", Value: "+7 2"}}
	got := portalMulti(cur, []any{map[string]any{"ID": "1", "DELETE": "Y"}, map[string]any{"ID": "2", "VALUE": "+7 22", "VALUE_TYPE": "MOBILE"},
		map[string]any{"VALUE": "+7 3"}})
	want := []crm.Comm{{Type: "mobile", Value: "+7 22"}, {Value: "+7 3"}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("portalMulti: %v", got)
	}
	if got := portalMulti(nil, "a@b.test"); len(got) != 1 || got[0].Value != "a@b.test" {
		t.Errorf("a value alone: %v", got)
	}
}

// TestRestPortalCRM: crm.deal/lead/contact/company — fields, add (a form of PHP arrays and JSON), get, list with the
// filter's prefixes, select, order and pages, update of the fields given, delete; the rights of CRM; batch.
func TestRestPortalCRM(t *testing.T) {
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
	var annaNum, ivanNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна Петрова', 'anna@go24.test', true)
		RETURNING id, num`).Scan(&anna, &annaNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван Сидоров', 'ivan@go24.test')
		RETURNING id, num`).Scan(&ivan, &ivanNum))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), I18n: bundle,
		CRM: crm.NewStore(pool), Audit: audit.NewStore(pool), BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.Marketing = marketing.NewStore(pool, srv.CRM)
	defer bundle.SetFilter(nil)
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Свои", Perms: crm.Perms{"deal": {"read": "own", "write": "own"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, ivan, true))
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(path, ctype, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", ctype)
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
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	token, err := srv.Keys.Create(ctx, anna, "n8n", apikeys.AllScopes, 0)
	must(err)
	hook := "/rest/" + num(annaNum) + "/" + token + "/"
	form := func(method string, v url.Values) any {
		t.Helper()
		return call(hook+method, "application/x-www-form-urlencoded", v.Encode(), 200)["result"]
	}
	js := func(method, body string) any {
		t.Helper()
		return call(hook+method+".json", "application/json", body, 200)["result"]
	}
	str := func(v any) string { return portalStr(v) }

	// fields
	f := form("crm.deal.fields", nil).(map[string]any)
	if st := f["STAGE_ID"].(map[string]any); st["type"] != "crm_status" || st["statusType"] != "DEAL_STAGE" || st["title"] == "" {
		t.Errorf("crm.deal.fields STAGE_ID: %v", st)
	}
	if id := f["ID"].(map[string]any); id["isReadOnly"] != true || f["PHONE"] != nil {
		t.Errorf("crm.deal.fields: %v", f)
	}
	// «Режим работы CRM»
	if m := form("crm.settings.mode.get", nil); m != 1.0 {
		t.Errorf("crm.settings.mode.get, classic: %v", m)
	}
	_, _, err = srv.CRM.SetSimple(ctx, anna, true, false)
	must(err)
	if m := form("crm.settings.mode.get", nil); m != 2.0 {
		t.Errorf("crm.settings.mode.get, simple: %v", m)
	}
	_, _, err = srv.CRM.SetSimple(ctx, anna, false, false)
	must(err)

	// add: a company, its contact, a deal of them — numbers in, numbers out
	co := str(js("crm.company.add", `{"fields": {"TITLE": "ООО Ромашка", "COMPANY_TYPE": "CLIENT", "WEB": [{"VALUE": "romashka.test"}],
		"PHONE": [{"VALUE": "+7 (900) 111-22-33", "VALUE_TYPE": "MOBILE"}]}}`))
	ct := str(form("crm.contact.add", url.Values{"fields[NAME]": {"Пётр"}, "fields[LAST_NAME]": {"Иванов"}, "fields[COMPANY_ID]": {co},
		"fields[EMAIL][0][VALUE]": {"petr@romashka.test"}, "fields[UF_CRM_X]": {"skipped"}}))
	deal := str(form("crm.deal.add", url.Values{"fields[TITLE]": {"Поставка"}, "fields[OPPORTUNITY]": {"1500.5"}, "fields[COMPANY_ID]": {co},
		"fields[CONTACT_ID]": {ct}, "fields[STAGE_ID]": {"PREPARATION"}, "fields[ASSIGNED_BY_ID]": {num(ivanNum)},
		"fields[CLOSEDATE]": {"2026-11-30"}, "fields[UTM_SOURCE]": {"yandex"}, "fields[SOURCE_ID]": {"CALL"}}))
	if co != "1" || ct != "1" || deal != "1" {
		t.Errorf("numbers: company %s contact %s deal %s", co, ct, deal)
	}
	d := form("crm.deal.get", url.Values{"id": {deal}}).(map[string]any)
	for k, want := range map[string]string{"TITLE": "Поставка", "OPPORTUNITY": "1500.50", "STAGE_ID": "PREPARATION", "CATEGORY_ID": "0",
		"COMPANY_ID": co, "CONTACT_ID": ct, "ASSIGNED_BY_ID": num(ivanNum), "CREATED_BY_ID": num(annaNum), "UTM_SOURCE": "yandex",
		"SOURCE_ID": "CALL", "CLOSED": "N", "STAGE_SEMANTIC_ID": "P", "CURRENCY_ID": "RUB"} {
		if str(d[k]) != want {
			t.Errorf("crm.deal.get %s = %v, want %s", k, d[k], want)
		}
	}
	if !strings.HasPrefix(str(d["CLOSEDATE"]), "2026-11-30T00:00:00") || d["CONTACT_IDS"].([]any)[0] != ct {
		t.Errorf("crm.deal.get: CLOSEDATE %v CONTACT_IDS %v", d["CLOSEDATE"], d["CONTACT_IDS"])
	}
	c := form("crm.contact.get", url.Values{"ID": {ct}}).(map[string]any)
	if c["NAME"] != "Пётр Иванов" || c["COMPANY_ID"] != co || c["HAS_EMAIL"] != "Y" || c["EMAIL"].([]any)[0].(map[string]any)["VALUE"] != "petr@romashka.test" {
		t.Errorf("crm.contact.get: %v", c)
	}
	cg := js("crm.company.get", `{"id": `+co+`}`).(map[string]any)
	phone := cg["PHONE"].([]any)[0].(map[string]any)
	if cg["COMPANY_TYPE"] != "CLIENT" || cg["WEB"].([]any)[0].(map[string]any)["VALUE"] != "romashka.test" || phone["VALUE_TYPE"] != "MOBILE" || phone["ID"] != "1" {
		t.Errorf("crm.company.get: %v", cg)
	}

	// a deal of another pipeline: C<n>:
	pipe, err := srv.CRM.AddPipeline(ctx, "Тендеры")
	must(err)
	var cat int64
	must(pool.QueryRow(ctx, `SELECT num FROM crm_pipelines WHERE id = $1`, pipe).Scan(&cat))
	tender := str(js("crm.deal.add", `{"fields": {"TITLE": "Тендер", "STAGE_ID": "C`+num(cat)+`:PREPARATION", "OPPORTUNITY": 99}}`))
	if t2 := form("crm.deal.get", url.Values{"id": {tender}}).(map[string]any); t2["CATEGORY_ID"] != num(cat) || t2["STAGE_ID"] != "C"+num(cat)+":PREPARATION" {
		t.Errorf("a deal of pipeline %d: %v %v", cat, t2["CATEGORY_ID"], t2["STAGE_ID"])
	}

	// list: prefixes of the filter, select, order
	got := call(hook+"crm.deal.list", "application/x-www-form-urlencoded", "filter[>OPPORTUNITY]=1000&select[]=TITLE", 200)
	list := got["result"].([]any)
	if got["total"].(float64) != 1 || len(list) != 1 || len(list[0].(map[string]any)) != 2 || list[0].(map[string]any)["ID"] != deal {
		t.Errorf("crm.deal.list >OPPORTUNITY: %v", got)
	}
	for q, want := range map[string]int{"filter[STAGE_ID]=PREPARATION": 1, "filter[%25TITLE]=тенд": 1, "filter[!STAGE_ID]=PREPARATION": 1,
		"filter[@ID][]=1&filter[@ID][]=2": 2, "filter[COMPANY_ID]=" + co: 1, "filter[COMPANY_ID]=": 1, "filter[ASSIGNED_BY_ID]=" + num(ivanNum): 1,
		"filter[CATEGORY_ID]=" + num(cat): 1, "filter[>=DATE_CREATE]=2020-01-01": 2, "filter[UNKNOWN]=1": 2} {
		if n := len(call(hook+"crm.deal.list", "application/x-www-form-urlencoded", q, 200)["result"].([]any)); n != want {
			t.Errorf("crm.deal.list %s: %d, want %d", q, n, want)
		}
	}
	ordered := js("crm.deal.list", `{"order": {"OPPORTUNITY": "ASC"}, "select": ["ID"]}`).([]any)
	if ordered[0].(map[string]any)["ID"] != tender {
		t.Errorf("order: %v", ordered)
	}
	// pages of 50
	_, err = pool.Exec(ctx, `INSERT INTO crm_companies (title, responsible_id, created_by) SELECT 'К' || g, $1, $1 FROM generate_series(1, 55) g`, anna)
	must(err)
	page := call(hook+"crm.company.list", "application/x-www-form-urlencoded", "start=50", 200)
	if page["total"].(float64) != 56 || len(page["result"].([]any)) != 6 || page["next"] != nil {
		t.Errorf("crm.company.list start=50: total %v, %d", page["total"], len(page["result"].([]any)))
	}
	if page = call(hook+"crm.company.list", "application/x-www-form-urlencoded", "", 200); page["next"].(float64) != 50 {
		t.Errorf("crm.company.list next: %v", page["next"])
	}

	// update: only the fields given; PHONE and EMAIL by their ID
	if form("crm.deal.update", url.Values{"id": {deal}, "fields[STAGE_ID]": {"EXECUTING"}, "fields[UTM_MEDIUM]": {"cpc"}}) != true {
		t.Error("crm.deal.update is not true")
	}
	d = form("crm.deal.get", url.Values{"id": {deal}}).(map[string]any)
	if d["STAGE_ID"] != "EXECUTING" || d["TITLE"] != "Поставка" || d["OPPORTUNITY"] != "1500.50" || d["UTM_SOURCE"] != "yandex" || d["UTM_MEDIUM"] != "cpc" ||
		d["COMPANY_ID"] != co {
		t.Errorf("after crm.deal.update: %v", d)
	}
	lead := str(js("crm.lead.add", `{"fields": {"TITLE": "Заявка", "NAME": "Ольга", "STATUS_ID": "IN_PROCESS",
		"PHONE": [{"VALUE": "8 900 555-44-33", "VALUE_TYPE": "WORK"}, {"VALUE": "+7 900 000-00-01"}]}}`))
	if n := len(form("crm.lead.list", url.Values{"filter[PHONE]": {"+79005554433"}}).([]any)); n != 1 {
		t.Errorf("crm.lead.list by PHONE: %d", n)
	}
	js("crm.lead.update", `{"id": `+lead+`, "fields": {"PHONE": [{"ID": "1", "DELETE": "Y"}, {"VALUE": "+7 900 000-00-02"}], "STATUS_ID": "JUNK"}}`)
	l := form("crm.lead.get", url.Values{"id": {lead}}).(map[string]any)
	phones := l["PHONE"].([]any)
	if len(phones) != 2 || phones[0].(map[string]any)["VALUE"] != "+7 900 000-00-01" || l["STATUS_ID"] != "JUNK" || l["STATUS_SEMANTIC_ID"] != "F" || l["NAME"] != "Ольга" {
		t.Errorf("after crm.lead.update: %v", l)
	}

	// errors: a wrong stage, no card, a card of a reader without the right
	if e := call(hook+"crm.deal.update", "application/json", `{"id": `+deal+`, "fields": {"STAGE_ID": "NO_SUCH"}}`, 400); e["error"] != "ERROR_CORE" {
		t.Errorf("a wrong stage: %v", e)
	}
	if e := call(hook+"crm.deal.get", "application/json", `{"id": 999}`, 400); e["error"] != "ERROR_NOT_FOUND" {
		t.Errorf("no deal: %v", e)
	}
	ivanKey, err := srv.Keys.Create(ctx, ivan, "own", apikeys.AllScopes, 0)
	must(err)
	if mine := call("/rest/crm.deal.list?auth="+ivanKey, "application/json", `{}`, 200); mine["total"].(float64) != 1 {
		t.Errorf("ivan's deals: %v", mine)
	}
	if e := call("/rest/crm.deal.get?auth="+ivanKey, "application/json", `{"id": `+tender+`}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("another's deal: %v", e)
	}

	// batch: a new deal and its card by $result
	b := js("batch", `{"cmd": {"a": "crm.deal.add?fields[TITLE]=Из пакета", "b": "crm.deal.get?id=$result[a]"}}`).(map[string]any)
	if b["result"].(map[string]any)["b"].(map[string]any)["TITLE"] != "Из пакета" {
		t.Errorf("batch: %v", b)
	}

	// delete: to the recycle bin
	if form("crm.deal.delete", url.Values{"id": {deal}}) != true {
		t.Error("crm.deal.delete is not true")
	}
	call(hook+"crm.deal.get", "application/json", `{"id": `+deal+`}`, 400)
	var trashed int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_recycle WHERE entity = 'deal'`).Scan(&trashed))
	if trashed != 1 {
		t.Errorf("the recycle bin: %d", trashed)
	}
}
