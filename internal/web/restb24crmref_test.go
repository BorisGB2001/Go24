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

// TestRestPortalCRMRefs: crm.status.* (stages and dictionaries in one list with unique ids), crm.dealcategory.* and
// crm.category.*, crm.currency.*, crm.enum.*, crm.activity.* and crm.timeline.comment.* with the rights of CRM.
func TestRestPortalCRMRefs(t *testing.T) {
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
	js := func(method, body string) any {
		t.Helper()
		return call(hook+method+".json", body, 200)["result"]
	}
	str := func(v any) string { return portalStr(v) }
	items := func(v any) []map[string]any {
		var out []map[string]any
		for _, x := range v.([]any) {
			out = append(out, x.(map[string]any))
		}
		return out
	}

	// crm.status.*: the stages of leads and deals and the dictionaries, ids unique
	pipe, err := srv.CRM.AddPipeline(ctx, "Тендеры")
	must(err)
	var cat int64
	must(pool.QueryRow(ctx, `SELECT num FROM crm_pipelines WHERE id = $1`, pipe).Scan(&cat))
	all := items(js("crm.status.list", `{}`))
	ids := map[string]bool{}
	byEntity := map[string]int{}
	for _, x := range all {
		if ids[str(x["ID"])] {
			t.Errorf("crm.status.list: ID %v twice", x["ID"])
		}
		ids[str(x["ID"])] = true
		byEntity[str(x["ENTITY_ID"])]++
	}
	for _, e := range []string{"STATUS", "SOURCE", "CONTACT_TYPE", "COMPANY_TYPE", "INDUSTRY", "DEAL_TYPE", "DEAL_STAGE", "DEAL_STAGE_" + num(cat)} {
		if byEntity[e] == 0 {
			t.Errorf("crm.status.list: no %s in %v", e, byEntity)
		}
	}
	src := items(js("crm.status.list", `{"filter": {"ENTITY_ID": "SOURCE", "STATUS_ID": "CALL"}}`))
	if len(src) != 1 || src[0]["NAME"] != "Звонок" || src[0]["SYSTEM"] != "Y" {
		t.Errorf("crm.status.list SOURCE CALL: %v", src)
	}
	if got := js("crm.status.get", `{"id": `+str(src[0]["ID"])+`}`).(map[string]any); got["STATUS_ID"] != "CALL" {
		t.Errorf("crm.status.get: %v", got)
	}
	won := items(js("crm.status.list", `{"filter": {"ENTITY_ID": "DEAL_STAGE_`+num(cat)+`"}, "order": {"SORT": "DESC"}}`))
	if len(won) < 2 || won[0]["CATEGORY_ID"] != num(cat) || !strings.HasPrefix(str(won[0]["STATUS_ID"]), "C"+num(cat)+":") || won[0]["SORT"] == won[len(won)-1]["SORT"] {
		t.Errorf("the stages of pipeline %d, the last first: %v", cat, won[0])
	}
	if lead := items(js("crm.status.entity.items", `{"entityId": "STATUS"}`)); lead[0]["STATUS_ID"] != "NEW" {
		t.Errorf("crm.status.entity.items STATUS: %v", lead)
	}
	types := items(js("crm.status.entity.types", `{}`))
	if types[len(types)-1]["ID"] != "DEAL_STAGE_"+num(cat) || !strings.Contains(str(types[len(types)-1]["NAME"]), "Тендеры") {
		t.Errorf("crm.status.entity.types: %v", types)
	}
	// a stage added later takes a number no item of a dictionary has
	must(srv.CRM.AddStage(ctx, "deal", pipe, "Переговоры", "00C4FB", "process"))
	var stageNum int64
	must(pool.QueryRow(ctx, `SELECT num FROM crm_stages WHERE name = 'Переговоры'`).Scan(&stageNum))
	if ids[num(stageNum)] {
		t.Errorf("a new stage numbered %d as an item already listed", stageNum)
	}

	// pipelines
	cats := items(js("crm.dealcategory.list", `{}`))
	if len(cats) != 1 || cats[0]["ID"] != num(cat) || cats[0]["NAME"] != "Тендеры" {
		t.Errorf("crm.dealcategory.list: %v", cats)
	}
	if d := js("crm.dealcategory.default.get", `{}`).(map[string]any); d["NAME"] == "" || str(d["ID"]) != "0" {
		t.Errorf("crm.dealcategory.default.get: %v", d)
	}
	if st := items(js("crm.dealcategory.stage.list", `{"id": `+num(cat)+`}`)); st[0]["STATUS_ID"] != "C"+num(cat)+":NEW" {
		t.Errorf("crm.dealcategory.stage.list: %v", st)
	}
	cl := js("crm.category.list", `{"entityTypeId": 2}`).(map[string]any)["categories"].([]any)
	if len(cl) != 2 || cl[0].(map[string]any)["isDefault"] != "Y" {
		t.Errorf("crm.category.list: %v", cl)
	}
	if c := js("crm.category.get", `{"entityTypeId": 2, "id": `+num(cat)+`}`).(map[string]any)["category"].(map[string]any); c["name"] != "Тендеры" {
		t.Errorf("crm.category.get: %v", c)
	}
	call(hook+"crm.category.list", `{"entityTypeId": 1}`, 400)

	// currencies and enums
	if js("crm.currency.base.get", `{}`) != "RUB" {
		t.Error("crm.currency.base.get is not RUB")
	}
	if c := js("crm.currency.get", `{"id": "usd"}`).(map[string]any); c["CURRENCY"] != "USD" || c["BASE"] != "N" || c["FULL_NAME"] != "Доллар США" {
		t.Errorf("crm.currency.get: %v", c)
	}
	if o := items(js("crm.enum.ownertype", `{}`)); str(o[1]["ID"]) != "2" || o[1]["SYMBOL_CODE"] != "DEAL" {
		t.Errorf("crm.enum.ownertype: %v", o)
	}

	// activities of a deal: add, get, list by the owner, update, complete, reopen, delete
	deal := str(js("crm.deal.add", `{"fields": {"TITLE": "Поставка", "ASSIGNED_BY_ID": `+num(ivanNum)+`}}`))
	other := str(js("crm.deal.add", `{"fields": {"TITLE": "Чужая"}}`))
	act := str(js("crm.activity.add", `{"fields": {"OWNER_TYPE_ID": 2, "OWNER_ID": `+deal+`, "TYPE_ID": 2, "SUBJECT": "Позвонить",
		"DESCRIPTION": "Уточнить объём", "RESPONSIBLE_ID": `+num(ivanNum)+`, "END_TIME": "2026-11-05T10:00:00+01:00"}}`))
	a := js("crm.activity.get", `{"id": `+act+`}`).(map[string]any)
	for k, want := range map[string]string{"OWNER_TYPE_ID": "2", "OWNER_ID": deal, "TYPE_ID": "2", "PROVIDER_ID": "VOXIMPLANT_CALL",
		"SUBJECT": "Позвонить", "RESPONSIBLE_ID": num(ivanNum), "AUTHOR_ID": num(annaNum), "COMPLETED": "N"} {
		if str(a[k]) != want {
			t.Errorf("crm.activity.get %s = %v, want %s", k, a[k], want)
		}
	}
	if !strings.HasPrefix(str(a["DEADLINE"]), "2026-11-05T") {
		t.Errorf("crm.activity.get DEADLINE: %v", a["DEADLINE"])
	}
	js("crm.activity.add", `{"fields": {"OWNER_TYPE_ID": "D", "OWNER_ID": `+other+`, "SUBJECT": "Встретиться", "PROVIDER_ID": "CRM_MEETING",
		"COMPLETED": "Y"}}`)
	got := call(hook+"crm.activity.list.json", `{"filter": {"OWNER_TYPE_ID": 2, "OWNER_ID": `+deal+`}}`, 200)
	if got["total"].(float64) != 1 || items(got["result"])[0]["ID"] != act {
		t.Errorf("crm.activity.list of the deal: %v", got)
	}
	if done := items(js("crm.activity.list", `{"filter": {"COMPLETED": "Y"}, "select": ["ID", "TYPE_ID"]}`)); len(done) != 1 || str(done[0]["TYPE_ID"]) != "1" {
		t.Errorf("crm.activity.list COMPLETED=Y: %v", done)
	}
	js("crm.activity.update", `{"id": `+act+`, "fields": {"SUBJECT": "Перезвонить", "COMPLETED": "Y"}}`)
	a = js("crm.activity.get", `{"id": `+act+`}`).(map[string]any)
	if a["SUBJECT"] != "Перезвонить" || a["COMPLETED"] != "Y" || a["DESCRIPTION"] != "Уточнить объём" {
		t.Errorf("after crm.activity.update: %v", a)
	}
	js("crm.activity.update", `{"id": `+act+`, "fields": {"COMPLETED": "N"}}`)
	if a = js("crm.activity.get", `{"id": `+act+`}`).(map[string]any); a["COMPLETED"] != "N" {
		t.Errorf("reopened: %v", a)
	}
	// ivan reads the deeds of his deal only
	ivanKey, err := srv.Keys.Create(ctx, ivan, "own", apikeys.AllScopes, 0)
	must(err)
	if mine := call("/rest/crm.activity.list?auth="+ivanKey, `{}`, 200); mine["total"].(float64) != 1 {
		t.Errorf("ivan's deeds: %v", mine)
	}
	if e := call("/rest/crm.activity.add?auth="+ivanKey, `{"fields": {"OWNER_TYPE_ID": 2, "OWNER_ID": `+other+`, "SUBJECT": "x"}}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("a deed on another's deal: %v", e)
	}
	call(hook+"crm.activity.add", `{"fields": {"SUBJECT": "без карточки"}}`, 400)

	// timeline comments
	com := str(js("crm.timeline.comment.add", `{"fields": {"ENTITY_TYPE": "deal", "ENTITY_ID": `+deal+`, "COMMENT": "Клиент согласен"}}`))
	js("crm.timeline.comment.add", `{"fields": {"ENTITY_TYPE": "deal", "ENTITY_ID": `+deal+`, "COMMENT": "Счёт отправлен"}}`)
	list := items(js("crm.timeline.comment.list", `{"filter": {"ENTITY_TYPE": "deal", "ENTITY_ID": `+deal+`}}`))
	if len(list) != 2 || list[0]["COMMENT"] != "Счёт отправлен" || list[1]["ID"] != com || list[1]["ENTITY_ID"] != deal || list[1]["AUTHOR_ID"] != num(annaNum) {
		t.Errorf("crm.timeline.comment.list: %v", list)
	}
	if str(js("crm.timeline.comment.update", `{"id": `+com+`, "fields": {"COMMENT": "Клиент согласен на 100 шт."}}`)) != com {
		t.Error("crm.timeline.comment.update does not give the id")
	}
	if c := js("crm.timeline.comment.get", `{"id": `+com+`}`).(map[string]any); c["COMMENT"] != "Клиент согласен на 100 шт." || c["ENTITY_TYPE"] != "deal" {
		t.Errorf("crm.timeline.comment.get: %v", c)
	}
	if e := call("/rest/crm.timeline.comment.update?auth="+ivanKey, `{"id": `+com+`, "fields": {"COMMENT": "моё"}}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("another's comment: %v", e)
	}
	if js("crm.timeline.comment.delete", `{"id": `+com+`}`) != true {
		t.Error("crm.timeline.comment.delete is not true")
	}
	if n := len(items(js("crm.timeline.comment.list", `{"filter": {"ENTITY_TYPE": "D", "ENTITY_ID": `+deal+`}}`))); n != 1 {
		t.Errorf("after crm.timeline.comment.delete: %d", n)
	}
	if js("crm.activity.delete", `{"id": `+act+`}`) != true {
		t.Error("crm.activity.delete is not true")
	}
	call(hook+"crm.activity.get", `{"id": `+act+`}`, 400)
}
