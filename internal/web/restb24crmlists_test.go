package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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

// TestRestPortalLists: crm.status.add/update/delete over the stages and the dictionaries, crm.dealcategory.* and
// crm.category.* changed by the administrator only, crm.stagehistory.list of deals and leads within the rights.
func TestRestPortalLists(t *testing.T) {
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
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept-Language", "ru")
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
	js := func(method, body string) map[string]any {
		t.Helper()
		return call(hook+method+".json", body, 200)["result"].(map[string]any)
	}
	ivanToken, err := srv.Keys.Create(ctx, ivan, "n8n", apikeys.AllScopes, 0)
	must(err)
	ivanHook := "/rest/" + num(ivanNum) + "/" + ivanToken + "/"
	list := func(hook, method, body string) []any {
		t.Helper()
		return call(hook+method+".json", body, 200)["result"].([]any)
	}
	val := func(method, body string) any {
		t.Helper()
		return call(hook+method+".json", body, 200)["result"]
	}
	// the names of the deal stages of a list, in their order
	stages := func(entity string) []string {
		t.Helper()
		var out []string
		for _, x := range list(hook, "crm.status.list", `{"filter": {"ENTITY_ID": "`+entity+`"}, "order": {"SORT": "asc"}}`) {
			out = append(out, x.(map[string]any)["NAME"].(string))
		}
		return out
	}

	// crm.status.*: a dictionary item and stages at their places; an employee may not
	call(ivanHook+"crm.status.add.json", `{"fields": {"ENTITY_ID": "SOURCE", "NAME": "Выставка"}}`, 403)
	src := int64(val("crm.status.add", `{"fields": {"ENTITY_ID": "SOURCE", "NAME": "Выставка"}}`).(float64))
	if got := val("crm.status.get", `{"id": `+num(src)+`}`).(map[string]any); got["NAME"] != "Выставка" || got["ENTITY_ID"] != "SOURCE" {
		t.Fatalf("crm.status.get source: %v", got)
	}
	val("crm.status.update", `{"id": `+num(src)+`, "fields": {"NAME": "Выставки"}}`)
	call(hook+"crm.status.add.json", `{"fields": {"ENTITY_ID": "NOPE", "NAME": "x"}}`, 400)
	stage := int64(val("crm.status.add", `{"fields": {"ENTITY_ID": "DEAL_STAGE", "NAME": "Переговоры", "SORT": 25, "COLOR": "#FF0000"}}`).(float64))
	if got := stages("DEAL_STAGE"); len(got) < 4 || got[2] != "Переговоры" {
		t.Fatalf("SORT 25: %v", got)
	}
	got := val("crm.status.get", `{"id": `+num(stage)+`}`).(map[string]any)
	if got["COLOR"] != "#FF0000" || got["SEMANTICS"] != nil || !strings.HasPrefix(got["STATUS_ID"].(string), "UC_") {
		t.Errorf("crm.status.get stage: %v", got)
	}
	val("crm.status.update", `{"id": `+num(stage)+`, "fields": {"NAME": "Торг", "SORT": 5}}`)
	if got := stages("DEAL_STAGE"); got[0] != "Торг" {
		t.Errorf("SORT 5: %v", got)
	}
	fail := int64(val("crm.status.add", `{"fields": {"ENTITY_ID": "DEAL_STAGE", "NAME": "Дорого", "SEMANTICS": "F"}}`).(float64))
	if got := val("crm.status.get", `{"id": `+num(fail)+`}`).(map[string]any); got["SEMANTICS"] != "F" {
		t.Errorf("SEMANTICS F: %v", got)
	}
	lead := int64(val("crm.status.add", `{"fields": {"ENTITY_ID": "STATUS", "NAME": "Перезвонить"}}`).(float64))
	builtin := list(hook, "crm.status.list", `{"filter": {"ENTITY_ID": "SOURCE", "SYSTEM": "Y"}}`)[0].(map[string]any)["ID"].(string)
	call(hook+"crm.status.delete.json", `{"id": `+builtin+`}`, 400)
	val("crm.status.delete", `{"id": `+builtin+`, "params": {"FORCED": "Y"}}`)
	for _, id := range []int64{src, stage, fail, lead} {
		val("crm.status.delete", `{"id": `+num(id)+`}`)
		call(hook+"crm.status.get.json", `{"id": `+num(id)+`}`, 400)
	}

	// crm.dealcategory.*: a pipeline with its stages; the main one stays
	call(ivanHook+"crm.dealcategory.add.json", `{"fields": {"NAME": "Опт"}}`, 403)
	cat := int64(val("crm.dealcategory.add", `{"fields": {"NAME": "Опт", "SORT": 500}}`).(float64))
	if got := val("crm.dealcategory.get", `{"id": `+num(cat)+`}`).(map[string]any); got["NAME"] != "Опт" || got["SORT"] != "500" {
		t.Errorf("crm.dealcategory.get: %v", got)
	}
	if got := stages("DEAL_STAGE_" + num(cat)); len(got) != 7 {
		t.Errorf("the new pipeline's stages: %v", got)
	}
	val("crm.status.add", `{"fields": {"ENTITY_ID": "DEAL_STAGE_`+num(cat)+`", "NAME": "Отгрузка"}}`)
	val("crm.dealcategory.update", `{"id": `+num(cat)+`, "fields": {"NAME": "Оптовые"}}`)
	if got := val("crm.dealcategory.get", `{"id": `+num(cat)+`}`).(map[string]any); got["NAME"] != "Оптовые" {
		t.Errorf("crm.dealcategory.update: %v", got)
	}
	call(hook+"crm.dealcategory.delete.json", `{"id": 0}`, 400)
	val("crm.dealcategory.delete", `{"id": `+num(cat)+`}`)
	call(hook+"crm.dealcategory.get.json", `{"id": `+num(cat)+`}`, 400)

	// crm.category.*
	c := js("crm.category.add", `{"entityTypeId": 2, "fields": {"name": "Розница"}}`)["category"].(map[string]any)
	if c["name"] != "Розница" || c["entityTypeId"] != float64(2) || c["isDefault"] != "N" {
		t.Fatalf("crm.category.add: %v", c)
	}
	cid := num(int64(c["id"].(float64)))
	if c := js("crm.category.update", `{"entityTypeId": 2, "id": `+cid+`, "fields": {"name": "Магазины", "sort": 900}}`)["category"].(map[string]any); c["name"] != "Магазины" || c["sort"] != float64(900) {
		t.Errorf("crm.category.update: %v", c)
	}
	call(hook+"crm.category.delete.json", `{"entityTypeId": 2, "id": 0}`, 400)
	call(hook+"crm.category.delete.json", `{"entityTypeId": 2, "id": `+cid+`}`, 200)
	if f := js("crm.category.fields", `{}`)["fields"].(map[string]any); f["name"] == nil {
		t.Errorf("crm.category.fields: %v", f)
	}

	// crm.stagehistory.list: a deal made, won, moved to another pipeline; a lead made
	deal := int64(val("crm.deal.add", `{"fields": {"TITLE": "Поставка"}}`).(float64))
	val("crm.deal.update", `{"id": `+num(deal)+`, "fields": {"STAGE_ID": "WON"}}`)
	other := int64(val("crm.dealcategory.add", `{"fields": {"NAME": "Сервис"}}`).(float64))
	_, err = pool.Exec(ctx, `UPDATE crm_deals d SET pipeline_id = p.id, stage_id = st.id FROM crm_pipelines p JOIN crm_stages st
		ON st.pipeline_id = p.id AND st.code = 'new' WHERE p.num = $1 AND d.num = $2`, other, deal)
	must(err)
	res := js("crm.stagehistory.list", `{"entityTypeId": 2, "filter": {"OWNER_ID": `+num(deal)+`}, "order": {"ID": "ASC"}}`)
	items := res["items"].([]any)
	want := [][]any{{float64(1), "NEW", "P", float64(0)}, {float64(3), "WON", "S", float64(0)}, {float64(5), "C" + num(other) + ":NEW", "P", float64(other)}}
	if len(items) != len(want) {
		t.Fatalf("crm.stagehistory.list 2: %v", items)
	}
	for i, w := range want {
		it := items[i].(map[string]any)
		if it["TYPE_ID"] != w[0] || it["STAGE_ID"] != w[1] || it["STAGE_SEMANTIC_ID"] != w[2] || it["CATEGORY_ID"] != w[3] ||
			it["OWNER_ID"] != float64(deal) || it["CREATED_TIME"] == nil {
			t.Errorf("history %d: %v, want %v", i, it, w)
		}
	}
	if got := js("crm.stagehistory.list", `{"entityTypeId": 2, "filter": {"OWNER_ID": `+num(deal)+`, "TYPE_ID": 3}, "select": ["ID", "STAGE_ID"]}`)["items"].([]any); len(got) != 1 || len(got[0].(map[string]any)) != 2 {
		t.Errorf("filter TYPE_ID, select: %v", got)
	}
	if got := js("crm.stagehistory.list", `{"entityTypeId": 2, "filter": {">=CREATED_TIME": "2999-01-01T00:00:00"}}`)["items"].([]any); len(got) != 0 {
		t.Errorf(">=CREATED_TIME: %v", got)
	}
	if got := call(ivanHook+"crm.stagehistory.list.json", `{"entityTypeId": 2}`, 200)["result"].(map[string]any)["items"].([]any); len(got) != 0 {
		t.Errorf("Ivan sees Anna's deals' history: %v", got)
	}
	ld := int64(val("crm.lead.add", `{"fields": {"TITLE": "Заявка"}}`).(float64))
	items = js("crm.stagehistory.list", `{"entityTypeId": 1, "filter": {"OWNER_ID": `+num(ld)+`}}`)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["STATUS_ID"] != "NEW" || items[0].(map[string]any)["TYPE_ID"] != float64(1) {
		t.Errorf("crm.stagehistory.list 1: %v", items)
	}
	call(hook+"crm.stagehistory.list.json", `{"entityTypeId": 3}`, 400)
}
