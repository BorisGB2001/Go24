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
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestPHPParams: PHP arrays of a query read as PHP reads them.
func TestPHPParams(t *testing.T) {
	v, _ := url.ParseQuery("filter[>ID]=5&filter[STAGE_ID]=NEW&select[]=ID&select[]=TITLE&start=50&" +
		"fields[PHONE][0][VALUE]=%2B7&fields[PHONE][0][VALUE_TYPE]=WORK&order[ID]=DESC&broken[=1")
	got := phpParams(v)
	want := map[string]any{
		"filter":  map[string]any{">ID": "5", "STAGE_ID": "NEW"},
		"select":  []any{"ID", "TITLE"},
		"start":   "50",
		"fields":  map[string]any{"PHONE": []any{map[string]any{"VALUE": "+7", "VALUE_TYPE": "WORK"}}},
		"order":   map[string]any{"ID": "DESC"},
		"broken[": "1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("phpParams:\n got %#v\nwant %#v", got, want)
	}
}

// TestPortalFill: $result[…] of a batch command is the value of an earlier result, escaped for the query.
func TestPortalFill(t *testing.T) {
	results := map[string]any{
		"me":    map[string]any{"ID": "7", "NAME": "Анна Мария"},
		"deals": []any{map[string]any{"ID": "12"}},
	}
	for cmd, want := range map[string]string{
		"user.get?ID=$result[me][ID]":                       "user.get?ID=7",
		"crm.deal.get?id=$result[deals][0][ID]":             "crm.deal.get?id=12",
		"im.notify?message=$result[me][NAME]":               "im.notify?message=%D0%90%D0%BD%D0%BD%D0%B0+%D0%9C%D0%B0%D1%80%D0%B8%D1%8F",
		"user.get?ID=$result[nobody][ID]&x=$result[me][NO]": "user.get?ID=&x=",
	} {
		if got := portalFill(cmd, results); got != want {
			t.Errorf("portalFill(%q) = %q, want %q", cmd, got, want)
		}
	}
	if portalRefsReady("a?x=$result[b][ID]", map[string]bool{}, map[string]string{"b": ""}) {
		t.Error("a command waits for the command it refers to")
	}
}

// TestRestPortalHTTP: methods by name — through the incoming webhook (the employee's number or uuid) and
// through /rest/<method>?auth=; the answer {result, time}, a list {result, total, next}, errors {error,
// error_description}; batch with $result and halt; scope, methods, method.get.
func TestRestPortalHTTP(t *testing.T) {
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
	var annaNum, ivanNum, deptNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, first_name, last_name, position, admin)
		VALUES ('anna', 'Анна Петрова', 'anna@go24.test', 'Анна', 'Петрова', 'Бухгалтер', true) RETURNING id, num`).Scan(&anna, &annaNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван Сидоров', 'ivan@go24.test')
		RETURNING id, num`).Scan(&ivan, &ivanNum))
	for i := range 55 {
		_, err := pool.Exec(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@go24.test')`, "u"+string(rune('a'+i/26))+string(rune('a'+i%26)))
		must(err)
	}
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id, num`).Scan(&dept, &deptNum))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, ivan, dept)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), I18n: bundle,
		Audit: audit.NewStore(pool), BaseURL: "https://go24.example", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	call := func(method, path, ctype, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.400s", method, path, res.StatusCode, want, raw)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s %s: not JSON: %.200s", method, path, raw)
		}
		return out
	}
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	token, err := srv.Keys.Create(ctx, anna, "n8n", apikeys.AllScopes, 0)
	must(err)
	hook := "/rest/" + num(annaNum) + "/" + token + "/"

	// the core
	if out := call("GET", hook+"server.time.json", "", "", 200); out["result"] == nil || out["time"].(map[string]any)["date_start"] == nil {
		t.Errorf("server.time: %v", out)
	}
	prof := call("GET", hook+"profile", "", "", 200)["result"].(map[string]any)
	if prof["ID"] != num(annaNum) || prof["NAME"] != "Анна" || prof["LAST_NAME"] != "Петрова" || prof["ADMIN"] != true {
		t.Errorf("profile: %v", prof)
	}
	cur := call("GET", "/rest/"+anna+"/"+token+"/user.current", "", "", 200)["result"].(map[string]any) // uuid in the address too
	if cur["ID"] != num(annaNum) || cur["EMAIL"] != "anna@go24.test" || cur["WORK_POSITION"] != "Бухгалтер" || cur["USER_TYPE"] != "employee" {
		t.Errorf("user.current: %v", cur)
	}
	// user.get: the filter in PHP arrays, in JSON; a page of 50 with total and next
	got := call("GET", hook+"user.get?FILTER[ID]="+num(ivanNum), "", "", 200)
	list := got["result"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["NAME"] != "Иван" || got["total"].(float64) != 1 {
		t.Errorf("user.get by ID: %v", got)
	}
	if deps := list[0].(map[string]any)["UF_DEPARTMENT"].([]any); len(deps) != 1 || deps[0].(float64) != float64(deptNum) {
		t.Errorf("UF_DEPARTMENT: %v", deps)
	}
	got = call("POST", hook+"user.get.json", "application/json", `{"filter": {"UF_DEPARTMENT": `+num(deptNum)+`}}`, 200)
	if got["total"].(float64) != 1 {
		t.Errorf("user.get by department: %v", got)
	}
	page := call("POST", hook+"user.get", "application/x-www-form-urlencoded", "start=0", 200)
	if len(page["result"].([]any)) != 50 || page["total"].(float64) != 57 || page["next"].(float64) != 50 {
		t.Errorf("user.get page: total %v next %v len %d", page["total"], page["next"], len(page["result"].([]any)))
	}
	page = call("GET", hook+"user.get?start=50", "", "", 200)
	if len(page["result"].([]any)) != 7 || page["next"] != nil {
		t.Errorf("user.get last page: next %v len %d", page["next"], len(page["result"].([]any)))
	}

	// an application's call: the token in «auth»
	if out := call("GET", "/rest/profile.json?auth="+url.QueryEscape(token), "", "", 200); out["result"].(map[string]any)["ID"] != num(annaNum) {
		t.Errorf("/rest/profile?auth=: %v", out)
	}
	call("POST", "/rest/user.current", "application/x-www-form-urlencoded", "auth="+url.QueryEscape(token), 200)

	// errors gives them
	if e := call("GET", "/rest/profile", "", "", 401); e["error"] != "NO_AUTH_FOUND" {
		t.Errorf("no auth: %v", e)
	}
	if e := call("GET", "/rest/"+num(ivanNum)+"/"+token+"/profile", "", "", 401); e["error"] != "NO_AUTH_FOUND" {
		t.Errorf("another employee's address: %v", e)
	}
	if e := call("GET", hook+"no.such.method", "", "", 404); e["error"] != "ERROR_METHOD_NOT_FOUND" || e["error_description"] == "" {
		t.Errorf("unknown method: %v", e)
	}
	narrow, err := srv.Keys.Create(ctx, anna, "tasks", []string{"tasks:read"}, 0)
	must(err)
	if e := call("GET", "/rest/user.get?auth="+narrow, "", "", 401); e["error"] != "insufficient_scope" {
		t.Errorf("scope: %v", e)
	}
	// go24 paths still go through the webhook
	call("GET", hook+"me", "", "", 200)

	// scope, methods, method.get
	if sc := call("GET", "/rest/scope?auth="+narrow, "", "", 200)["result"].([]any); !reflect.DeepEqual(sc, []any{"task", "user", "user_basic"}) {
		t.Errorf("scope: %v", sc)
	}
	ms := call("GET", "/rest/methods?auth="+narrow, "", "", 200)["result"].([]any)
	if !slicesHas(ms, "batch") || !slicesHas(ms, "profile") || slicesHas(ms, "user.get") {
		t.Errorf("methods: %v", ms)
	}
	if mg := call("GET", "/rest/method.get?name=user.get&auth="+narrow, "", "", 200)["result"].(map[string]any); mg["isExisting"] != true || mg["isAvailable"] != false {
		t.Errorf("method.get: %v", mg)
	}

	// batch: $result of an earlier command, the errors of each, halt
	form := url.Values{"cmd[me]": {"user.current"}, "cmd[a_ivan]": {"user.get?ID=$result[me][ID]"}, "cmd[bad]": {"no.such"},
		"cmd[loop]": {"profile?x=$result[loop][ID]"}}
	b := call("POST", hook+"batch", "application/x-www-form-urlencoded", form.Encode(), 200)["result"].(map[string]any)
	res, errs := b["result"].(map[string]any), b["result_error"].(map[string]any)
	if res["me"] == nil || len(res["a_ivan"].([]any)) != 1 || b["result_total"].(map[string]any)["a_ivan"].(float64) != 1 {
		t.Errorf("batch results: %v", b)
	}
	if errs["bad"].(map[string]any)["error"] != "ERROR_METHOD_NOT_FOUND" || errs["loop"].(map[string]any)["error"] != "ERROR_BATCH_REFERENCE" {
		t.Errorf("batch errors: %v", errs)
	}
	b = call("POST", hook+"batch.json", "application/json", `{"halt": 1, "cmd": {"a": "no.such", "b": "profile"}}`, 200)["result"].(map[string]any)
	if r, ok := b["result"].([]any); !ok || len(r) != 0 {
		t.Errorf("batch halt: %v", b)
	}
	if e := call("POST", hook+"batch", "application/json", `{"cmd": {"a": "batch?cmd[b]=profile"}}`, 200)["result"].(map[string]any)["result_error"].(map[string]any); e["a"].(map[string]any)["error"] != "ERROR_BATCH_METHOD_NOT_ALLOWED" {
		t.Errorf("batch in batch: %v", e)
	}
}

func slicesHas(list []any, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
