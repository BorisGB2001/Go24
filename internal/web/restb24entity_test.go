package web

import (
	"context"
	"encoding/base64"
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
	"go24/internal/apps"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestRestPortalEntity: app.option.*, user.option.*, access.name, user.access and the storage entity.* by the names of
// the portal — rights by access codes, sections, items with properties and files, filters and pages; a webhook keeps
// its data apart from the application's.
func TestRestPortalEntity(t *testing.T) {
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
	var bossNum, annaNum, top, sales int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}')
		RETURNING id, num`).Scan(&boss, &bossNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id, num`).Scan(&anna, &annaNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	var topID string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id, num`).Scan(&topID, &top))
	var salesID string
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Продажи') RETURNING id, num`, topID).Scan(&salesID, &sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, salesID)
	must(err)

	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Apps: apps.NewStore(pool), I18n: bundle,
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	srv.BaseURL = hs.URL

	id, err := srv.Apps.Save(ctx, boss, apps.App{Name: "Меню", APIOnly: true, Scopes: []string{"crm:read"}})
	must(err)
	a, err := srv.Apps.Get(ctx, id)
	must(err)
	must(srv.Apps.Install(ctx, a, boss, srv.BaseURL, "ru"))
	tok := func(user string) string {
		tk, err := srv.Apps.Issue(ctx, a.ID, user)
		must(err)
		return tk.Access
	}
	bossTok, annaTok, ivanTok := tok(boss), tok(anna), tok(ivan)
	call := func(method, token, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/rest/"+method+"?auth="+token, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	num := func(n int64) string { return strconv.FormatInt(n, 10) }

	// settings: the application's — by administrators, the employee's — their own
	if e := call("app.option.set", annaTok, `{"options": {"color": "red"}}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("app.option.set of an employee: %v", e)
	}
	call("app.option.set", bossTok, `{"options": {"color": "red", "limit": 5}}`, 200)
	if v := call("app.option.get", annaTok, `{"option": "limit"}`, 200)["result"]; v != 5.0 {
		t.Errorf("app.option.get limit: %v", v)
	}
	if v := call("app.option.get", annaTok, `{}`, 200)["result"].(map[string]any); v["color"] != "red" {
		t.Errorf("app.option.get: %v", v)
	}
	call("user.option.set", annaTok, `{"options": {"view": "list"}}`, 200)
	if v := call("user.option.get", annaTok, `{"option": "view"}`, 200)["result"]; v != "list" {
		t.Errorf("user.option.get of Anna: %v", v)
	}
	if v := call("user.option.get", ivanTok, `{"option": "view"}`, 200)["result"]; v != nil {
		t.Errorf("user.option.get of Ivan: %v", v)
	}

	// access codes
	access := func(token, codes string) any {
		return call("user.access", token, `{"ACCESS": `+codes+`}`, 200)["result"]
	}
	if access(annaTok, `["G1"]`) != false || access(bossTok, `["G1"]`) != true || access(annaTok, `["U`+num(annaNum)+`"]`) != true ||
		access(annaTok, `["D`+num(sales)+`"]`) != true || access(annaTok, `["DR`+num(top)+`"]`) != true || access(annaTok, `["D`+num(top)+`"]`) != false ||
		access(ivanTok, `["DR`+num(top)+`", "D`+num(sales)+`"]`) != false || access(ivanTok, `["AU"]`) != true {
		t.Error("user.access")
	}
	names := call("access.name", annaTok, `{"ACCESS": ["U`+num(bossNum)+`", "DR`+num(sales)+`", "G2", "X9"]}`, 200)["result"].(map[string]any)
	if len(names) != 3 || names["U"+num(bossNum)].(map[string]any)["name"] != "Борис" ||
		names["DR"+num(sales)].(map[string]any)["provider_id"] != "intranet" || !strings.Contains(names["DR"+num(sales)].(map[string]any)["name"].(string), "Продажи") {
		t.Errorf("access.name: %v", names)
	}

	// entities: by administrators; everyone reads by default
	if e := call("entity.add", annaTok, `{"ENTITY": "menu", "NAME": "Меню"}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("entity.add of an employee: %v", e)
	}
	call("entity.add", bossTok, `{"ENTITY": "menu", "NAME": "Меню"}`, 200)
	if e := call("entity.add", bossTok, `{"ENTITY": "MENU", "NAME": "Ещё"}`, 400); e["error"] != "ERROR_ENTITY_ALREADY_EXISTS" {
		t.Errorf("entity.add twice: %v", e)
	}
	if e := call("entity.add", bossTok, `{"ENTITY": "очень-длинный-код", "NAME": "x"}`, 400); e["error"] != "ERROR_ARGUMENT" {
		t.Errorf("entity.add of a bad code: %v", e)
	}
	if got := call("entity.get", annaTok, `{"ENTITY": "menu"}`, 200)["result"].(map[string]any); got["NAME"] != "Меню" || got["ENTITY"] != "menu" {
		t.Errorf("entity.get: %v", got)
	}
	if e := call("entity.get", annaTok, `{"ENTITY": "none"}`, 400); e["error"] != "ERROR_ENTITY_NOT_FOUND" {
		t.Errorf("entity.get of none: %v", e)
	}
	for _, p := range []string{`"PROPERTY": "PRICE", "NAME": "Цена", "TYPE": "N"`, `"PROPERTY": "COLOR", "NAME": "Цвет"`,
		`"PROPERTY": "PHOTO", "NAME": "Фото", "TYPE": "F"`} {
		call("entity.item.property.add", bossTok, `{"ENTITY": "menu", `+p+`}`, 200)
	}
	if e := call("entity.item.property.add", bossTok, `{"ENTITY": "menu", "PROPERTY": "price", "NAME": "x"}`, 400); e["error"] != "ERROR_PROPERTY_ALREADY_EXISTS" {
		t.Errorf("property.add twice: %v", e)
	}
	if ps := call("entity.item.property.get", annaTok, `{"ENTITY": "menu"}`, 200)["result"].([]any); len(ps) != 3 ||
		ps[0].(map[string]any)["PROPERTY"] != "PRICE" || ps[0].(map[string]any)["TYPE"] != "N" || ps[1].(map[string]any)["TYPE"] != "S" {
		t.Errorf("property.get: %v", ps)
	}

	// Anna reads only — until the department gets W
	if e := call("entity.item.add", annaTok, `{"ENTITY": "menu", "NAME": "Суп"}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("item.add of a reader: %v", e)
	}
	if e := call("entity.rights", annaTok, `{"ENTITY": "menu", "ACCESS": {"AU": "X"}}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("entity.rights of a reader: %v", e)
	}
	rights := call("entity.rights", bossTok, `{"ENTITY": "menu", "ACCESS": {"AU": "R", "dr`+num(sales)+`": "W"}}`, 200)["result"].(map[string]any)
	if rights["DR"+num(sales)] != "W" || rights["AU"] != "R" {
		t.Errorf("entity.rights: %v", rights)
	}
	if e := call("entity.item.add", ivanTok, `{"ENTITY": "menu", "NAME": "Суп"}`, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("item.add of Ivan: %v", e)
	}

	// sections and items
	hot := call("entity.section.add", annaTok, `{"ENTITY": "menu", "NAME": "Горячее"}`, 200)["result"].(float64)
	soups := call("entity.section.add", annaTok, `{"ENTITY": "menu", "NAME": "Супы", "SECTION": `+strconv.Itoa(int(hot))+`}`, 200)["result"].(float64)
	if e := call("entity.section.update", annaTok, `{"ENTITY": "menu", "ID": `+strconv.Itoa(int(hot))+`, "SECTION": `+strconv.Itoa(int(soups))+`}`, 400); e["error"] != "ERROR_CORE" {
		t.Errorf("a section under itself: %v", e)
	}
	secs := call("entity.section.get", annaTok, `{"ENTITY": "menu", "SORT": {"ID": "ASC"}}`, 200)["result"].([]any)
	if len(secs) != 2 || secs[1].(map[string]any)["SECTION"] != strconv.Itoa(int(hot)) || secs[1].(map[string]any)["DEPTH_LEVEL"] != "2" {
		t.Errorf("section.get: %v", secs)
	}
	photo := base64.StdEncoding.EncodeToString([]byte("PNG!"))
	borsch := call("entity.item.add", annaTok, `{"ENTITY": "menu", "NAME": "Борщ", "SECTION": `+strconv.Itoa(int(soups))+`,
		"PROPERTY_VALUES": {"PRICE": "350", "COLOR": "красный", "PHOTO": ["borsch.png", "`+photo+`"], "NOPE": "x"}}`, 200)["result"].(float64)
	call("entity.item.add", annaTok, `{"ENTITY": "menu", "NAME": "Щи", "SECTION": `+strconv.Itoa(int(soups))+`, "PROPERTY_VALUES": {"PRICE": 250}}`, 200)
	call("entity.item.add", annaTok, `{"ENTITY": "menu", "NAME": "Котлета", "ACTIVE": "N", "SORT": 10, "PROPERTY_VALUES": {"PRICE": "420.5"}}`, 200)
	if e := call("entity.item.add", annaTok, `{"ENTITY": "menu", "NAME": "x", "PROPERTY_VALUES": {"PRICE": "дорого"}}`, 400); e["error"] != "ERROR_CORE" {
		t.Errorf("a number that is not: %v", e)
	}
	items := func(body string) []any {
		t.Helper()
		return call("entity.item.get", ivanTok, `{"ENTITY": "menu", `+body+`}`, 200)["result"].([]any)
	}
	names2 := func(list []any) string {
		var out []string
		for _, x := range list {
			out = append(out, x.(map[string]any)["NAME"].(string))
		}
		return strings.Join(out, ",")
	}
	if got := names2(items(`"SORT": {"PROPERTY_PRICE": "DESC"}`)); got != "Котлета,Борщ,Щи" {
		t.Errorf("sorted by a number: %s", got)
	}
	if got := names2(items(`"FILTER": {">=PROPERTY_PRICE": 300}, "SORT": {"SORT": "ASC"}`)); got != "Котлета,Борщ" {
		t.Errorf(">=PROPERTY_PRICE: %s", got)
	}
	if got := names2(items(`"FILTER": {"SECTION": `+strconv.Itoa(int(soups))+`, "%NAME": "щ"}`)); got != "Борщ,Щи" {
		t.Errorf("SECTION and %%NAME: %s", got)
	}
	if got := names2(items(`"FILTER": {"ACTIVE": "Y", "PROPERTY_COLOR": "красный"}`)); got != "Борщ" {
		t.Errorf("ACTIVE and PROPERTY_COLOR: %s", got)
	}
	if got := names2(items(`"FILTER": {"PROPERTY_COLOR": false}, "SORT": {"ID": "ASC"}`)); got != "Щи,Котлета" {
		t.Errorf("no PROPERTY_COLOR: %s", got)
	}
	got := items(`"FILTER": {"ID": `+strconv.Itoa(int(borsch))+`}`)[0].(map[string]any)
	pv := got["PROPERTY_VALUES"].(map[string]any)
	if pv["PRICE"] != "350" || pv["COLOR"] != "красный" || got["CREATED_BY"] != num(annaNum) || got["IBLOCK_SECTION_ID"] != strconv.Itoa(int(soups)) {
		t.Errorf("item.get: %v", got)
	}
	res, err := http.Get(pv["PHOTO"].(string))
	must(err)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || string(body) != "PNG!" || !strings.Contains(res.Header.Get("Content-Disposition"), "borsch.png") {
		t.Errorf("the file: %d %q", res.StatusCode, body)
	}

	// changing: the properties given, the others stay; renaming a property moves its values
	call("entity.item.update", annaTok, `{"ENTITY": "menu", "ID": `+strconv.Itoa(int(borsch))+`, "NAME": "Борщ с пампушками",
		"PROPERTY_VALUES": {"PRICE": 390, "PHOTO": ""}}`, 200)
	call("entity.item.property.update", bossTok, `{"ENTITY": "menu", "PROPERTY": "COLOR", "PROPERTY_NEW": "TINT"}`, 200)
	got = items(`"FILTER": {"ID": `+strconv.Itoa(int(borsch))+`}`)[0].(map[string]any)
	pv = got["PROPERTY_VALUES"].(map[string]any)
	if got["NAME"] != "Борщ с пампушками" || pv["PRICE"] != "390" || pv["TINT"] != "красный" || pv["PHOTO"] != nil || got["MODIFIED_BY"] != num(annaNum) {
		t.Errorf("item after update: %v", got)
	}
	res, err = http.Get(hs.URL + "/rest/entityfile?token=forged.x")
	must(err)
	res.Body.Close()
	if res.StatusCode == 200 {
		t.Error("a forged file address works")
	}

	// pages of 50
	for i := range 52 {
		call("entity.item.add", annaTok, `{"ENTITY": "menu", "NAME": "Блюдо `+strconv.Itoa(i)+`"}`, 200)
	}
	page := call("entity.item.get", ivanTok, `{"ENTITY": "menu", "start": 50}`, 200)
	if page["total"] != 55.0 || len(page["result"].([]any)) != 5 || page["next"] != nil {
		t.Errorf("the second page: total %v, next %v", page["total"], page["next"])
	}

	// a section goes with what is in it; a deleted entity is gone
	call("entity.section.delete", annaTok, `{"ENTITY": "menu", "ID": `+strconv.Itoa(int(hot))+`}`, 200)
	if got := names2(items(`"FILTER": {"%NAME": "щ"}`)); got != "" {
		t.Errorf("items of a deleted section: %s", got)
	}
	if e := call("entity.item.delete", annaTok, `{"ENTITY": "menu", "ID": `+strconv.Itoa(int(borsch))+`}`, 400); e["error"] != "ERROR_ITEM_NOT_FOUND" {
		t.Errorf("item.delete of a gone one: %v", e)
	}

	// a webhook keeps its own storage
	key, err := srv.Keys.Create(ctx, boss, "интеграция", apikeys.AllScopes, 0)
	must(err)
	hook := hs.URL + "/rest/" + num(bossNum) + "/" + key + "/"
	hookCall := func(method, body string) map[string]any {
		t.Helper()
		res, err := http.Post(hook+method, "application/json", strings.NewReader(body))
		must(err)
		defer res.Body.Close()
		var out map[string]any
		must(json.NewDecoder(res.Body).Decode(&out))
		return out
	}
	if l := hookCall("entity.get", `{}`)["result"].([]any); len(l) != 0 {
		t.Errorf("entities of a webhook: %v", l)
	}
	hookCall("entity.add", `{"ENTITY": "menu", "NAME": "Своё"}`)
	if l := call("entity.get", bossTok, `{}`, 200)["result"].([]any); len(l) != 1 || l[0].(map[string]any)["NAME"] != "Меню" {
		t.Errorf("entities of the application: %v", l)
	}
	call("entity.delete", bossTok, `{"ENTITY": "menu"}`, 200)
	if e := call("entity.item.get", bossTok, `{"ENTITY": "menu"}`, 400); e["error"] != "ERROR_ENTITY_NOT_FOUND" {
		t.Errorf("a deleted entity: %v", e)
	}
}
