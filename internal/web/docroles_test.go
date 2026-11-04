package web

import (
	"bytes"
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
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestDocRoles: «Генератор документов → Права доступа» — by default an employee makes documents but neither changes
// templates nor settings; a role with «templates» opens the templates (not «Моя компания»), one with «settings» the
// numerators and the roles; without «modify» no documents are made, without «view» the card hides them; a department's
// role reaches its head; documentgenerator.role.* lists, adds, updates and gives roles (fillaccesses), the default
// role is not deleted.
func TestDocRoles(t *testing.T) {
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
	user := func(name string, admin bool) (string, int64) {
		var id string
		var n int64
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ($1, $1, $1 || '@t', $2) RETURNING id, num`,
			name, admin).Scan(&id, &n))
		return id, n
	}
	admin, adminNum := user("admin", true)
	bob, bobNum := user("bob", false)
	eve, _ := user("eve", false)
	var sales string
	var salesNum int64
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Продажи', $1) RETURNING id, num`, eve).Scan(&sales, &salesNum))

	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle,
		pages: pages, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, admin, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: bob}})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who, Admin: who == admin, AdminMode: who == admin}))
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hook := func(uid string, n int64) string {
		token, err := srv.Keys.Create(ctx, uid, "k", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	adminHook, bobHook := hook(admin, adminNum), hook(bob, bobNum)
	call := func(hook, method string, in any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(in)
		res, err := http.Post(hs.URL+hook+method+".json", "application/json", bytes.NewReader(raw))
		must(err)
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, body)
		}
		var out map[string]any
		must(json.Unmarshal(body, &out))
		return out
	}
	perms := func(who string) crm.DocPerms {
		t.Helper()
		p, err := srv.CRM.DocPermsOf(ctx, who)
		must(err)
		return p
	}

	// by default: documents, no templates or settings
	if p := perms(bob); !p.Can("view") || !p.Can("modify") || p.Can("templates") || p.Can("settings") {
		t.Fatalf("default: %v", p)
	}
	if code, _, _ := do(bob, "GET", "/crm/documents", nil); code != http.StatusForbidden {
		t.Fatalf("templates to bob: %d", code)
	}
	if _, page, _ := do(bob, "GET", "/crm/deals", nil); strings.Contains(page, `href="/crm/documents"`) {
		t.Fatal("bob sees the templates' link")
	}
	if code, page, _ := do(admin, "GET", "/crm/documents", nil); code != http.StatusOK || !strings.Contains(page, `href="/crm/documents/rights"`) {
		t.Fatalf("admin templates: %d", code)
	}
	code, page, _ := do(admin, "GET", "/crm/documents/rights", nil)
	if code != http.StatusOK || !strings.Contains(page, "Все сотрудники") || !strings.Contains(page, "Администратор") || !strings.Contains(page, `name="templates"`) {
		t.Fatalf("rights page: %d", code)
	}
	if code, _, _ := do(bob, "GET", "/crm/documents/rights", nil); code != http.StatusForbidden {
		t.Fatalf("rights to bob: %d", code)
	}

	// a role of the templates to bob
	code, _, loc := do(admin, "POST", "/crm/documents/rights", url.Values{"name": {"Шаблоны"}, "templates": {"all"}, "view": {"all"},
		"modify": {"all"}, "settings": {"none"}, "members": {bob}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("save: %d %s", code, loc)
	}
	code, page, _ = do(bob, "GET", "/crm/documents", nil)
	if code != http.StatusOK || !strings.Contains(page, `action="/crm/documents/builtin"`) || strings.Contains(page, `action="/crm/documents/my-company"`) ||
		strings.Contains(page, `href="/crm/documents/rights"`) {
		t.Fatalf("bob's templates: %d", code)
	}
	if _, page, _ := do(bob, "GET", "/crm/deals", nil); !strings.Contains(page, `href="/crm/documents"`) {
		t.Fatal("no templates' link to bob")
	}
	if code, _, _ := do(bob, "POST", "/crm/documents/my-company", url.Values{"company": {""}}); code != http.StatusForbidden {
		t.Fatalf("bob's my company: %d", code)
	}

	// no «modify», no «view» for everyone else: eve makes no documents and the card hides them
	roles, err := srv.CRM.DocRoles(ctx)
	must(err)
	if len(roles) != 3 || !roles[0].Default || roles[1].Code != "admin" || roles[2].Name != "Шаблоны" || len(roles[2].Members) != 1 {
		t.Fatalf("roles: %+v", roles)
	}
	code, _, loc = do(admin, "POST", "/crm/documents/rights", url.Values{"id": {roles[0].ID}, "view": {"none"}, "modify": {"none"}})
	if code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("save default: %d %s", code, loc)
	}
	if p := perms(eve); p.Can("view") || p.Can("modify") {
		t.Fatalf("eve: %v", p)
	}
	if code, _, _ := do(eve, "POST", "/crm/deals/"+deal+"/document", url.Values{"template": {"x"}}); code != http.StatusForbidden {
		t.Fatalf("eve makes a document: %d", code)
	}
	var made int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_documents`).Scan(&made))
	if made != 0 {
		t.Fatalf("documents: %d", made)
	}
	// «modify» brings «view»
	if p := (crm.DocPerms{"modify": "all"}).Clean(); !p.Can("view") {
		t.Fatalf("modify without view: %v", p)
	}

	// documentgenerator.role.*: the settings' holders
	call(bobHook, "documentgenerator.role.list", nil, http.StatusForbidden)
	list := call(adminHook, "documentgenerator.role.list", nil, http.StatusOK)["result"].(map[string]any)["roles"].([]any)
	if len(list) != 3 {
		t.Fatalf("role.list: %v", list)
	}
	first := list[0].(map[string]any)
	if first["code"] != "EVERYONE" || first["permissions"].(map[string]any)["DOCUMENTS"].(map[string]any)["VIEW"] != "" {
		t.Fatalf("everyone: %v", first)
	}
	added := call(adminHook, "documentgenerator.role.add", map[string]any{"fields": map[string]any{"name": "Настройки",
		"permissions": map[string]any{"SETTINGS": map[string]any{"MODIFY": "X"}, "DOCUMENTS": map[string]any{"VIEW": "X"}}}},
		http.StatusOK)["result"].(map[string]any)["role"].(map[string]any)
	if added["name"] != "Настройки" || added["permissions"].(map[string]any)["SETTINGS"].(map[string]any)["MODIFY"] != "X" ||
		added["permissions"].(map[string]any)["TEMPLATES"].(map[string]any)["MODIFY"] != "" {
		t.Fatalf("role.add: %v", added)
	}
	// given to the department: its head eve has it; bob keeps «Шаблоны» only when given anew
	call(adminHook, "documentgenerator.role.fillaccesses", map[string]any{"accesses": []any{
		map[string]any{"roleId": added["id"], "accessCode": "DR" + num(salesNum)}}}, http.StatusOK)
	if p := perms(eve); !p.Can("settings") || !p.Can("view") || p.Can("modify") {
		t.Fatalf("eve by the department: %v", p)
	}
	if p := perms(bob); p.Can("templates") {
		t.Fatalf("bob keeps the templates: %v", p)
	}
	got := call(adminHook, "documentgenerator.role.get", map[string]any{"id": added["id"]}, http.StatusOK)["result"].(map[string]any)["role"].(map[string]any)
	if codes := got["accessCodes"].([]any); len(codes) != 1 || codes[0] != "DR"+num(salesNum) {
		t.Fatalf("role.get: %v", got)
	}
	// eve with «settings»: the numerators and the roles
	if code, _, _ := do(eve, "GET", "/crm/documents/rights", nil); code != http.StatusOK {
		t.Fatalf("rights to eve: %d", code)
	}
	upd := call(adminHook, "documentgenerator.role.update", map[string]any{"id": added["id"], "fields": map[string]any{
		"permissions": map[string]any{"TEMPLATES": map[string]any{"MODIFY": "X"}}}}, http.StatusOK)["result"].(map[string]any)["role"].(map[string]any)
	if upd["name"] != "Настройки" || upd["permissions"].(map[string]any)["SETTINGS"].(map[string]any)["MODIFY"] != "X" ||
		upd["permissions"].(map[string]any)["TEMPLATES"].(map[string]any)["MODIFY"] != "X" {
		t.Fatalf("role.update: %v", upd)
	}
	call(adminHook, "documentgenerator.role.delete", map[string]any{"id": first["id"]}, http.StatusBadRequest)
	call(adminHook, "documentgenerator.role.delete", map[string]any{"id": added["id"]}, http.StatusOK)
	if p := perms(eve); p.Can("settings") {
		t.Fatalf("eve after delete: %v", p)
	}
}
