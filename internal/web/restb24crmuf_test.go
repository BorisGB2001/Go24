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
	"go24/internal/tasks"
)

// TestRestPortalCRMUF: user fields UF_CRM_* (crm.*.userfield.*, in add, get, list, update), the contacts of a deal and
// of a company, crm.duplicate.findbycomm.
func TestRestPortalCRMUF(t *testing.T) {
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
	var annaNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна Петрова', 'anna@go24.test', true)
		RETURNING id, num`).Scan(&anna, &annaNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван Сидоров', 'ivan@go24.test')
		RETURNING id`).Scan(&ivan))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), I18n: bundle,
		CRM: crm.NewStore(pool), Audit: audit.NewStore(pool), BaseURL: "https://go24.example",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(path, body string, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+path, "application/json", strings.NewReader(body))
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
	token, err := srv.Keys.Create(ctx, anna, "n8n", apikeys.AllScopes, 0)
	must(err)
	hook := "/rest/" + strconv.FormatInt(annaNum, 10) + "/" + token + "/"
	js := func(method, body string) any {
		t.Helper()
		return call(hook+method+".json", body, 200)["result"]
	}
	str := portalStr

	// a field added in go24 (no code) — UF_CRM_<number>
	must(srv.Tasks.AddFieldTo(ctx, tasks.CRMFieldSets["deal"], tasks.Field{Name: "Склад", Kind: "string"}))
	inn := str(js("crm.deal.userfield.add", `{"fields": {"FIELD_NAME": "UF_CRM_INN", "USER_TYPE_ID": "string",
		"EDIT_FORM_LABEL": {"ru": "ИНН", "en": "Tax ID"}, "MANDATORY": "N", "SORT": 10}}`))
	kind := str(js("crm.deal.userfield.add", `{"fields": {"FIELD_NAME": "KIND", "USER_TYPE_ID": "enumeration", "LABEL": "Вид",
		"LIST": [{"VALUE": "Опт"}, {"VALUE": "Розница"}]}}`))
	js("crm.deal.userfield.add", `{"fields": {"FIELD_NAME": "WEIGHT", "USER_TYPE_ID": "double", "LABEL": "Вес"}}`)
	js("crm.deal.userfield.add", `{"fields": {"FIELD_NAME": "VIP", "USER_TYPE_ID": "boolean", "LABEL": "VIP"}}`)
	js("crm.deal.userfield.add", `{"fields": {"FIELD_NAME": "SHIP", "USER_TYPE_ID": "date", "LABEL": "Отгрузка"}}`)
	if inn != "2" || kind != "3" {
		t.Errorf("userfield.add numbers: %s %s", inn, kind)
	}
	for _, bad := range []string{`{"FIELD_NAME": "INN", "USER_TYPE_ID": "string", "LABEL": "Ещё"}`,
		`{"FIELD_NAME": "12", "USER_TYPE_ID": "string", "LABEL": "Цифры"}`, `{"FIELD_NAME": "F", "USER_TYPE_ID": "file", "LABEL": "Файл"}`,
		`{"FIELD_NAME": "M", "USER_TYPE_ID": "string", "LABEL": "Много", "MULTIPLE": "Y"}`} {
		call(hook+"crm.deal.userfield.add.json", `{"fields": `+bad+`}`, 400)
	}
	ivanKey, err := srv.Keys.Create(ctx, ivan, "own", apikeys.AllScopes, 0)
	must(err)
	call("/rest/crm.deal.userfield.add?auth="+ivanKey, `{"fields": {"FIELD_NAME": "X", "USER_TYPE_ID": "string", "LABEL": "X"}}`, 403)

	uf := js("crm.deal.userfield.get", `{"id": `+kind+`}`).(map[string]any)
	if uf["FIELD_NAME"] != "UF_CRM_KIND" || uf["USER_TYPE_ID"] != "enumeration" || uf["ENTITY_ID"] != "CRM_DEAL" ||
		uf["LIST"].([]any)[1].(map[string]any)["VALUE"] != "Розница" {
		t.Errorf("userfield.get: %v", uf)
	}
	list := js("crm.deal.userfield.list", `{"filter": {"USER_TYPE_ID": "string"}}`).([]any)
	if len(list) != 2 || list[0].(map[string]any)["FIELD_NAME"] != "UF_CRM_1" || list[1].(map[string]any)["FIELD_NAME"] != "UF_CRM_INN" {
		t.Errorf("userfield.list: %v", list)
	}
	f := js("crm.deal.fields", `{}`).(map[string]any)
	if k := f["UF_CRM_KIND"].(map[string]any); k["isDynamic"] != true || k["type"] != "enumeration" || k["title"] != "Вид" ||
		len(k["items"].([]any)) != 2 || f["UF_CRM_INN"].(map[string]any)["title"] == "" {
		t.Errorf("crm.deal.fields: %v %v", f["UF_CRM_KIND"], f["UF_CRM_INN"])
	}

	// values in add, get, list and update
	deal := str(js("crm.deal.add", `{"fields": {"TITLE": "Поставка", "UF_CRM_INN": "7701", "UF_CRM_KIND": "2", "UF_CRM_WEIGHT": "12,5",
		"UF_CRM_VIP": "Y", "UF_CRM_SHIP": "2026-11-30", "UF_CRM_1": "Север"}}`))
	d := js("crm.deal.get", `{"id": `+deal+`}`).(map[string]any)
	for k, want := range map[string]string{"UF_CRM_INN": "7701", "UF_CRM_KIND": "2", "UF_CRM_WEIGHT": "12.50", "UF_CRM_VIP": "1", "UF_CRM_1": "Север"} {
		if str(d[k]) != want {
			t.Errorf("crm.deal.get %s = %v, want %s", k, d[k], want)
		}
	}
	if !strings.HasPrefix(str(d["UF_CRM_SHIP"]), "2026-11-30T00:00:00") {
		t.Errorf("crm.deal.get UF_CRM_SHIP = %v", d["UF_CRM_SHIP"])
	}
	js("crm.deal.add", `{"fields": {"TITLE": "Другая", "UF_CRM_KIND": "Опт"}}`)
	call(hook+"crm.deal.add.json", `{"fields": {"TITLE": "Плохая", "UF_CRM_KIND": "Мелкий опт"}}`, 400)
	l := call(hook+"crm.deal.list.json", `{"filter": {"UF_CRM_KIND": 2}, "select": ["TITLE", "UF_*"]}`, 200)
	rows := l["result"].([]any)
	if l["total"] != 1.0 || rows[0].(map[string]any)["UF_CRM_INN"] != "7701" {
		t.Errorf("list by UF_CRM_KIND: %v", l)
	}
	if r := js("crm.deal.list", `{"filter": {">UF_CRM_WEIGHT": 10}}`).([]any); len(r) != 1 || r[0].(map[string]any)["UF_CRM_INN"] != nil {
		t.Errorf("list by UF_CRM_WEIGHT, no UF selected: %v", r)
	}
	js("crm.deal.update", `{"id": `+deal+`, "fields": {"UF_CRM_INN": "", "UF_CRM_VIP": "N"}}`)
	if d := js("crm.deal.get", `{"id": `+deal+`}`).(map[string]any); d["UF_CRM_INN"] != nil || d["UF_CRM_VIP"] != "0" || d["TITLE"] != "Поставка" {
		t.Errorf("after update: %v %v", d["UF_CRM_INN"], d["UF_CRM_VIP"])
	}

	// userfield.update: renaming an option renames the values, removing one drops them
	js("crm.deal.userfield.update", `{"id": `+kind+`, "fields": {"EDIT_FORM_LABEL": "Канал", "LIST": [{"ID": 2, "VALUE": "Розн."},
		{"ID": 1, "DEL": "Y"}, {"VALUE": "Дилер"}]}}`)
	uf = js("crm.deal.userfield.get", `{"id": `+kind+`}`).(map[string]any)
	if uf["EDIT_FORM_LABEL"] != "Канал" || len(uf["LIST"].([]any)) != 2 || uf["LIST"].([]any)[0].(map[string]any)["VALUE"] != "Розн." {
		t.Errorf("userfield.update: %v", uf)
	}
	if d := js("crm.deal.get", `{"id": `+deal+`}`).(map[string]any); d["UF_CRM_KIND"] != "1" {
		t.Errorf("a renamed option: %v", d["UF_CRM_KIND"])
	}
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_deal_field_values WHERE value = 'Опт'`).Scan(&left))
	if left != 0 {
		t.Errorf("the values of a removed option stay: %d", left)
	}
	js("crm.deal.userfield.delete", `{"id": `+inn+`}`)
	if f := js("crm.deal.fields", `{}`).(map[string]any); f["UF_CRM_INN"] != nil {
		t.Error("a deleted field is still there")
	}

	// the contacts of a deal and of a company
	co := str(js("crm.company.add", `{"fields": {"TITLE": "ООО Ромашка"}}`))
	c1 := str(js("crm.contact.add", `{"fields": {"NAME": "Пётр", "PHONE": [{"VALUE": "+7 (900) 111-22-33"}]}}`))
	c2 := str(js("crm.contact.add", `{"fields": {"NAME": "Мария", "EMAIL": [{"VALUE": "Maria@Romashka.test"}]}}`))
	js("crm.deal.contact.items.set", `{"id": `+deal+`, "items": [{"CONTACT_ID": `+c1+`, "SORT": 20}, {"CONTACT_ID": `+c2+`, "SORT": 10}]}`)
	items := js("crm.deal.contact.items.get", `{"id": `+deal+`}`).([]any)
	if len(items) != 2 || str(items[0].(map[string]any)["CONTACT_ID"]) != c2 || items[0].(map[string]any)["IS_PRIMARY"] != "Y" {
		t.Errorf("deal.contact.items: %v", items)
	}
	js("crm.deal.contact.add", `{"id": `+deal+`, "fields": {"CONTACT_ID": `+c1+`, "IS_PRIMARY": "Y"}}`)
	if d := js("crm.deal.get", `{"id": `+deal+`}`).(map[string]any); str(d["CONTACT_ID"]) != c1 {
		t.Errorf("the main contact after add: %v", d["CONTACT_ID"])
	}
	js("crm.deal.contact.delete", `{"id": `+deal+`, "fields": {"CONTACT_ID": `+c1+`}}`)
	if items := js("crm.deal.contact.items.get", `{"id": `+deal+`}`).([]any); len(items) != 1 || str(items[0].(map[string]any)["CONTACT_ID"]) != c2 {
		t.Errorf("after delete: %v", items)
	}
	js("crm.deal.contact.items.delete", `{"id": `+deal+`}`)
	if items := js("crm.deal.contact.items.get", `{"id": `+deal+`}`).([]any); len(items) != 0 {
		t.Errorf("after items.delete: %v", items)
	}
	js("crm.company.contact.items.set", `{"id": `+co+`, "items": [{"CONTACT_ID": `+c1+`}, {"CONTACT_ID": `+c2+`}]}`)
	if items := js("crm.contact.company.items.get", `{"id": `+c2+`}`).([]any); len(items) != 1 || str(items[0].(map[string]any)["COMPANY_ID"]) != co {
		t.Errorf("contact.company.items: %v", items)
	}
	js("crm.contact.company.delete", `{"id": `+c1+`, "fields": {"COMPANY_ID": `+co+`}}`)
	if items := js("crm.company.contact.items.get", `{"id": `+co+`}`).([]any); len(items) != 1 || str(items[0].(map[string]any)["CONTACT_ID"]) != c2 {
		t.Errorf("company.contact.items: %v", items)
	}

	// duplicates
	dup := js("crm.duplicate.findbycomm", `{"type": "PHONE", "values": ["89001112233"]}`).(map[string]any)
	if c := dup["CONTACT"].([]any); len(c) != 1 || str(c[0]) != c1 || dup["LEAD"] != nil {
		t.Errorf("findbycomm PHONE: %v", dup)
	}
	if r := js("crm.duplicate.findbycomm", `{"type": "EMAIL", "values": ["maria@romashka.test"], "entity_type": "COMPANY"}`).([]any); len(r) != 0 {
		t.Errorf("findbycomm COMPANY: %v", r)
	}
	call(hook+"crm.duplicate.findbycomm.json", `{"type": "FAX", "values": ["1"]}`, 400)
}
