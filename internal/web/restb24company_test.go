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
	"go24/internal/authentik"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestRestPortalCompany: department.add/update/delete by numbers, user.add — an invitation and the employee's number at
// once, taken over by the first sign-in; user.search, user.update with the rights of the pages.
func TestRestPortalCompany(t *testing.T) {
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

	ak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/api/v3/flows/instances/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"pk": "flow-1"})
		case r.Method == "POST" && r.URL.Path == "/api/v3/stages/invitation/invitations/":
			_ = json.NewEncoder(w).Encode(map[string]any{"pk": "inv-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ak.Close()
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), I18n: bundle,
		Audit: audit.NewStore(pool), BaseURL: "https://go24.example", Authentik: authentik.New(ak.URL, "tok"),
		AuthentikPublic: "https://sso.test", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(path string, v url.Values, want int) map[string]any {
		t.Helper()
		res, err := http.Post(hs.URL+path, "application/x-www-form-urlencoded", strings.NewReader(v.Encode()))
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
	hookOf := func(id string, n int64) string {
		token, err := srv.Keys.Create(ctx, id, "n8n", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	hook, ivanHook := hookOf(anna, annaNum), hookOf(ivan, ivanNum)
	form := func(hook, method string, v url.Values) any { t.Helper(); return call(hook+method, v, 200)["result"] }
	str := func(v any) string { return portalStr(v) }

	if f := form(hook, "user.fields", nil).(map[string]any); f["WORK_POSITION"] == "" || f["WORK_POSITION"] == "portal.user.WORK_POSITION" {
		t.Errorf("user.fields: %v", f)
	}
	if f := form(hook, "department.fields", nil).(map[string]any); len(f) != 5 {
		t.Errorf("department.fields: %v", f)
	}

	// departments: under the company by default, a sub-department with a head and SORT, renamed, moved, deleted
	sales := str(form(hook, "department.add", url.Values{"NAME": {"Продажи"}}))
	shop := str(form(hook, "department.add", url.Values{"NAME": {"Магазин"}, "PARENT": {sales}, "UF_HEAD": {num(ivanNum)},
		"SORT": {"200"}}))
	d := form(hook, "department.get", url.Values{"ID": {shop}}).([]any)
	if len(d) != 1 || d[0].(map[string]any)["PARENT"] != sales || d[0].(map[string]any)["UF_HEAD"] != num(ivanNum) ||
		d[0].(map[string]any)["SORT"] != "200" {
		t.Fatalf("department.add: %v", d)
	}
	if form(hook, "department.update", url.Values{"ID": {shop}, "NAME": {"Розница"}}) != true {
		t.Error("department.update")
	}
	if d := form(hook, "department.get", url.Values{"ID": {shop}}).([]any); d[0].(map[string]any)["NAME"] != "Розница" ||
		d[0].(map[string]any)["UF_HEAD"] != num(ivanNum) {
		t.Errorf("department.update: only NAME changes: %v", d)
	}
	if e := call(ivanHook+"department.add", url.Values{"NAME": {"Своё"}}, 403); e["error"] != "ACCESS_DENIED" {
		t.Errorf("department.add by an employee: %v", e)
	}
	if e := call(hook+"department.add", url.Values{"NAME": {"x"}, "PARENT": {"999"}}, 400); e["error"] != "ERROR_CORE" {
		t.Errorf("department.add, no such parent: %v", e)
	}
	tmp := str(form(hook, "department.add", url.Values{"NAME": {"Временный"}}))
	if form(hook, "department.delete", url.Values{"ID": {tmp}}) != true || len(form(hook, "department.get", url.Values{"ID": {tmp}}).([]any)) != 0 {
		t.Error("department.delete")
	}

	// user.add: invited, there at once with the number, in the department
	maria := str(form(hook, "user.add", url.Values{"EMAIL": {"Maria@go24.test"}, "NAME": {"Мария"}, "LAST_NAME": {"Ковалёва"},
		"WORK_POSITION": {"Кассир"}, "UF_DEPARTMENT[]": {shop}}))
	u := form(hook, "user.get", url.Values{"ID": {maria}}).([]any)
	if len(u) != 1 || u[0].(map[string]any)["NAME"] != "Мария" || u[0].(map[string]any)["WORK_POSITION"] != "Кассир" ||
		str(u[0].(map[string]any)["UF_DEPARTMENT"].([]any)[0]) != shop || u[0].(map[string]any)["EMAIL"] != "maria@go24.test" {
		t.Fatalf("user.add: %v", u)
	}
	var invited int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM employee_invites WHERE email = 'maria@go24.test'`).Scan(&invited))
	if invited != 1 {
		t.Errorf("user.add: %d invitations", invited)
	}
	if e := call(hook+"user.add", url.Values{"EMAIL": {"maria@go24.test"}}, 400); e["error"] != "ERROR_CORE" {
		t.Errorf("user.add twice: %v", e)
	}
	if e := call(hook+"user.add", url.Values{"EMAIL": {"g@go24.test"}, "EXTRANET": {"Y"}}, 400); !strings.Contains(str(e["error_description"]), "user.add") {
		t.Errorf("user.add EXTRANET: %v", e)
	}

	// user.search
	if r := form(hook, "user.search", url.Values{"FIND": {"ковал"}}).([]any); len(r) != 1 || str(r[0].(map[string]any)["ID"]) != maria {
		t.Errorf("user.search FIND: %v", r)
	}
	if r := form(hook, "user.search", url.Values{"UF_DEPARTMENT_NAME": {"розн"}}).([]any); len(r) != 1 {
		t.Errorf("user.search UF_DEPARTMENT_NAME: %v", r)
	}
	if r := form(hook, "user.search", url.Values{"FILTER[WORK_POSITION]": {"касс"}}).([]any); len(r) != 1 {
		t.Errorf("user.search FILTER: %v", r)
	}

	// user.update: one's own card yes, another's or the departments — administrators
	if form(ivanHook, "user.update", url.Values{"ID": {num(ivanNum)}, "PERSONAL_CITY": {"Казань"}}) != true {
		t.Error("user.update own")
	}
	p, err := srv.Company.Profile(ctx, ivan)
	must(err)
	if p.City != "Казань" || p.FirstName != "Иван" || p.LastName != "Сидоров" {
		t.Errorf("user.update own: the rest of the card kept: %+v", p)
	}
	call(ivanHook+"user.update", url.Values{"ID": {maria}, "WORK_POSITION": {"x"}}, 403)
	call(ivanHook+"user.update", url.Values{"ID": {num(ivanNum)}, "UF_DEPARTMENT[]": {sales}}, 403)
	if form(hook, "user.update", url.Values{"ID": {maria}, "ACTIVE": {"N"}, "UF_DEPARTMENT[]": {sales}}) != true {
		t.Error("user.update ACTIVE")
	}
	u = form(hook, "user.get", url.Values{"ID": {maria}}).([]any)
	if u[0].(map[string]any)["ACTIVE"] != false || str(u[0].(map[string]any)["UF_DEPARTMENT"].([]any)[0]) != sales {
		t.Errorf("user.update ACTIVE, UF_DEPARTMENT: %v", u)
	}
	if list, _ := srv.Audit.List(ctx, audit.Filter{Kind: "user_fired"}); len(list) != 1 {
		t.Errorf("user.update ACTIVE=N not in the events: %v", list)
	}
	form(hook, "user.update", url.Values{"ID": {maria}, "ACTIVE": {"Y"}})

	// user.userfield.*: the profile's own fields UF_USR_* (an administrator), in user.get and user.update
	must(srv.Tasks.AddFieldTo(ctx, tasks.UserFieldSet, tasks.Field{Name: "Табельный номер", Kind: "string"}))
	shirt := str(form(hook, "user.userfield.add", url.Values{"fields[FIELD_NAME]": {"UF_USR_SHIRT"},
		"fields[USER_TYPE_ID]": {"enumeration"}, "fields[EDIT_FORM_LABEL][ru]": {"Размер"}, "fields[LIST][0][VALUE]": {"S"},
		"fields[LIST][1][VALUE]": {"M"}}))
	form(hook, "user.userfield.add", url.Values{"fields[FIELD_NAME]": {"REMOTE"}, "fields[USER_TYPE_ID]": {"boolean"},
		"fields[LABEL]": {"Удалённо"}})
	call(ivanHook+"user.userfield.add", url.Values{"fields[FIELD_NAME]": {"X"}, "fields[USER_TYPE_ID]": {"string"}}, 403)
	ufs := form(ivanHook, "user.userfield.list", nil).([]any)
	if len(ufs) != 3 || ufs[0].(map[string]any)["FIELD_NAME"] != "UF_USR_1" || ufs[0].(map[string]any)["ENTITY_ID"] != "USER" {
		t.Fatalf("user.userfield.list: %v", ufs)
	}
	if uf := form(hook, "user.userfield.get", url.Values{"id": {shirt}}).(map[string]any); uf["FIELD_NAME"] != "UF_USR_SHIRT" ||
		len(uf["LIST"].([]any)) != 2 {
		t.Errorf("user.userfield.get: %v", uf)
	}
	if form(ivanHook, "user.update", url.Values{"ID": {num(ivanNum)}, "UF_USR_SHIRT": {"2"}, "UF_USR_REMOTE": {"Y"},
		"UF_USR_1": {"T-17"}}) != true {
		t.Error("user.update UF_USR_*")
	}
	call(ivanHook+"user.update", url.Values{"ID": {maria}, "UF_USR_1": {"x"}}, 403)
	u = form(hook, "user.get", url.Values{"ID": {num(ivanNum)}}).([]any)
	if x := u[0].(map[string]any); x["UF_USR_SHIRT"] != "2" || x["UF_USR_REMOTE"] != "1" || x["UF_USR_1"] != "T-17" {
		t.Errorf("user.get UF_USR_*: %v", x)
	}
	size := func() string { // the value kept, by the field's name
		vals, err := srv.Tasks.FieldValuesOf(ctx, tasks.UserFieldSet, ivan)
		must(err)
		for _, v := range vals {
			if v.Name == "Размер" {
				return v.Value
			}
		}
		return "?"
	}
	if size() != "M" {
		t.Error("UF_USR_SHIRT kept as the option")
	}
	if x := form(hook, "user.current", nil).(map[string]any); x["UF_USR_SHIRT"] != nil || x["UF_USR_REMOTE"] != "0" {
		t.Errorf("user.current UF_USR_* not filled: %v", x)
	}
	form(hook, "user.userfield.update", url.Values{"id": {shirt}, "fields[LIST][0][ID]": {"2"}, "fields[LIST][0][VALUE]": {"L"}})
	if size() != "L" {
		t.Error("user.userfield.update renames the option with its values")
	}
	if form(hook, "user.userfield.delete", url.Values{"id": {shirt}}) != true ||
		len(form(hook, "user.userfield.list", nil).([]any)) != 2 {
		t.Error("user.userfield.delete")
	}

	// the first sign-in with that e-mail takes the record over
	var mariaID string
	must(pool.QueryRow(ctx, `SELECT id FROM users WHERE num::text = $1`, maria).Scan(&mariaID))
	got, active, guest, err := srv.signIn(ctx, auth.Claims{Sub: "ak-maria", Email: "maria@go24.test", Name: "Мария К"}, "")
	must(err)
	var sub string
	must(pool.QueryRow(ctx, `SELECT oidc_sub FROM users WHERE id = $1`, got).Scan(&sub))
	if got != mariaID || !active || guest || sub != "ak-maria" {
		t.Errorf("sign-in after user.add: %s %v %v %s, want %s", got, active, guest, sub, mariaID)
	}
	if p, _ := srv.Company.Profile(ctx, got); p.Name != "Мария Ковалёва" {
		t.Errorf("sign-in after user.add: the name of the card kept: %q", p.Name)
	}
}
