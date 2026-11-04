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

// TestPortalItemNames: the fields of crm.item.* are those of crm.*.* in camelCase, a few by their own names.
func TestPortalItemNames(t *testing.T) {
	for upper, want := range map[string]string{"ASSIGNED_BY_ID": "assignedById", "STAGE_ID": "stageId", "STATUS_ID": "stageId",
		"DATE_CREATE": "createdTime", "CREATED_BY_ID": "createdBy", "CONTACT_IDS": "contactIds", "UTM_SOURCE": "utmSource",
		"BEGINDATE": "begindate"} {
		if got := portalItemName(upper); got != want {
			t.Errorf("%s: %q, want %q", upper, got, want)
		}
	}
	e := portalEntities["lead"]
	in := portalItemIn(e, map[string]any{"filter": map[string]any{"=stageId": "NEW", "!assignedById": 1, "unknown": 1},
		"select": []any{"id", "title", "fm"}, "fields": map[string]any{"title": "Лид", "fm": []any{
			map[string]any{"typeId": "PHONE", "valueType": "WORK", "value": "+7 1"}}}})
	if f := in["filter"].(map[string]any); f["=STATUS_ID"] != "NEW" || f["!ASSIGNED_BY_ID"] != 1 || len(f) != 2 {
		t.Errorf("filter: %v", f)
	}
	if sel := in["select"].([]any); len(sel) != 5 || sel[2] != "PHONE" {
		t.Errorf("select: %v", sel)
	}
	if f := in["fields"].(map[string]any); f["TITLE"] != "Лид" || len(f["PHONE"].([]any)) != 1 {
		t.Errorf("fields: %v", f)
	}
	out := portalItemOut(e, map[string]any{"ID": "7", "ASSIGNED_BY_ID": "3", "OPPORTUNITY": "10.5", "STATUS_ID": "NEW",
		"PHONE": []map[string]any{{"ID": "1", "VALUE_TYPE": "WORK", "VALUE": "+7 1", "TYPE_ID": "PHONE"}}})
	if out["id"] != int64(7) || out["assignedById"] != int64(3) || out["opportunity"] != 10.5 || out["stageId"] != "NEW" {
		t.Errorf("out: %v", out)
	}
	if fm := out["fm"].([]map[string]any); len(fm) != 1 || fm[0]["typeId"] != "PHONE" || fm[0]["value"] != "+7 1" {
		t.Errorf("fm: %v", out["fm"])
	}
}

// TestRestPortalItems: crm.item.* over the four kinds and a smart process (its own numbers, stages DT…, pipelines),
// crm.type.* by the administrator only, crm.category.* and crm.status.* of a smart process, the rights of the CRM.
func TestRestPortalItems(t *testing.T) {
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
	str := func(v any) string { return portalStr(v) }

	// crm.type.*: the administrator adds a smart process numbered from 128; an employee may not
	call(ivanHook+"crm.type.add.json", `{"fields": {"title": "Договоры"}}`, 403)
	typ := js("crm.type.add", `{"fields": {"title": "Договоры"}}`)["type"].(map[string]any)
	if typ["entityTypeId"] != float64(128) || typ["title"] != "Договоры" {
		t.Fatalf("crm.type.add: %v", typ)
	}
	js("crm.type.update", `{"id": 128, "fields": {"title": "Договоры поставки"}}`)
	if got := js("crm.type.get", `{"id": 128}`)["type"].(map[string]any); got["title"] != "Договоры поставки" {
		t.Errorf("crm.type.get: %v", got)
	}
	if list := js("crm.type.list", `{}`)["types"].([]any); len(list) != 1 {
		t.Errorf("crm.type.list: %v", list)
	}
	cats := js("crm.category.list", `{"entityTypeId": 128}`)["categories"].([]any)
	if len(cats) != 1 || cats[0].(map[string]any)["isDefault"] != "Y" || cats[0].(map[string]any)["entityTypeId"] != float64(128) {
		t.Fatalf("crm.category.list 128: %v", cats)
	}
	cat := str(cats[0].(map[string]any)["id"])
	call(hook+"crm.category.list.json", `{"entityTypeId": 3}`, 400)

	// crm.item.*: a smart process's card in its pipeline, the stage DT128_<cat>:…
	fields := js("crm.item.fields", `{"entityTypeId": 128}`)["fields"].(map[string]any)
	if _, ok := fields["stageId"]; !ok || fields["title"].(map[string]any)["upperName"] != "TITLE" {
		t.Errorf("crm.item.fields 128: %v", fields)
	}
	if _, ok := fields["typeId"]; ok {
		t.Errorf("crm.item.fields 128: a smart process has no typeId")
	}
	item := js("crm.item.add", `{"entityTypeId": 128, "fields": {"title": "Договор 1", "opportunity": 500, "assignedById": `+num(ivanNum)+`}}`)["item"].(map[string]any)
	if item["title"] != "Договор 1" || item["stageId"] != "DT128_"+cat+":NEW" || item["categoryId"] != float64(atoi(cat)) ||
		item["opportunity"] != float64(500) || item["assignedById"] != float64(ivanNum) {
		t.Fatalf("crm.item.add 128: %v", item)
	}
	id := str(item["id"])
	item = js("crm.item.update", `{"entityTypeId": 128, "id": `+id+`, "fields": {"stageId": "DT128_`+cat+`:CLIENT"}}`)["item"].(map[string]any)
	if item["stageId"] != "DT128_"+cat+":CLIENT" || item["title"] != "Договор 1" {
		t.Errorf("crm.item.update 128: %v", item)
	}
	js("crm.item.add", `{"entityTypeId": 128, "fields": {"title": "Договор 2", "stageId": "PREPARATION"}}`)
	list := js("crm.item.list", `{"entityTypeId": 128, "filter": {"stageId": "DT128_`+cat+`:CLIENT"}, "select": ["id", "title"]}`)["items"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["title"] != "Договор 1" || len(list[0].(map[string]any)) != 2 {
		t.Errorf("crm.item.list 128 by stage: %v", list)
	}
	// a smart process's cards are not deals, and a deal is not one of them
	if deals := js("crm.item.list", `{"entityTypeId": 2}`)["items"].([]any); len(deals) != 0 {
		t.Errorf("crm.item.list 2 gives the smart process's cards: %v", deals)
	}
	call(hook+"crm.deal.get.json", `{"id": `+id+`}`, 400)
	statuses := call(hook+"crm.status.list.json", `{"filter": {"ENTITY_ID": "DYNAMIC_128_STAGE_`+cat+`"}}`, 200)["result"].([]any)
	if len(statuses) != 5 || statuses[0].(map[string]any)["STATUS_ID"] != "DT128_"+cat+":NEW" {
		t.Errorf("crm.status.list DYNAMIC_128_STAGE_%s: %v", cat, statuses)
	}
	// the rights of the CRM: Ivan sees his own card only
	if mine := call(ivanHook+"crm.item.list.json", `{"entityTypeId": 128}`, 200)["result"].(map[string]any)["items"].([]any); len(mine) != 1 {
		t.Errorf("Ivan's crm.item.list 128: %v", mine)
	}
	call(hook+"crm.type.delete.json", `{"id": 128}`, 400) // it has cards
	call(hook+"crm.item.delete.json", `{"entityTypeId": 128, "id": `+id+`}`, 200)
	call(hook+"crm.item.get.json", `{"entityTypeId": 128, "id": `+id+`}`, 400)

	// the four kinds: a contact with fm, a deal with contactIds
	contact := js("crm.item.add", `{"entityTypeId": 3, "fields": {"name": "Пётр", "post": "Директор",
		"fm": [{"typeId": "PHONE", "valueType": "WORK", "value": "+7 900 1"}, {"typeId": "EMAIL", "valueType": "WORK", "value": "p@x.test"}]}}`)["item"].(map[string]any)
	if contact["name"] != "Пётр" || contact["post"] != "Директор" || len(contact["fm"].([]any)) != 2 {
		t.Fatalf("crm.item.add 3: %v", contact)
	}
	deal := js("crm.item.add", `{"entityTypeId": 2, "fields": {"title": "Поставка", "contactIds": [`+str(contact["id"])+`]}}`)["item"].(map[string]any)
	if deal["stageId"] != "NEW" || deal["contactIds"].([]any)[0] != contact["id"] {
		t.Errorf("crm.item.add 2: %v", deal)
	}
	if got := js("crm.item.list", `{"entityTypeId": 3, "filter": {"%name": "пёт"}}`)["items"].([]any); len(got) != 1 {
		t.Errorf("crm.item.list 3 %%name: %v", got)
	}
	lead := js("crm.item.add", `{"entityTypeId": 1, "fields": {"title": "Заявка"}}`)["item"].(map[string]any)
	if lead["stageId"] != "NEW" {
		t.Errorf("crm.item.add 1: %v", lead)
	}
	call(hook+"crm.item.list.json", `{"entityTypeId": 7}`, 400)
	call(hook+"crm.item.list.json", `{"entityTypeId": 130}`, 400)
}

