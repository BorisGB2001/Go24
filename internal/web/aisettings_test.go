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
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/ai"
	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestAISettings: «Подключения ИИ» — an administrator keeps the company's connections, an employee sees them and keeps
// their own; the key never comes back, only its tail; the check asks the provider; the choice; REST does the same.
func TestAISettings(t *testing.T) {
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
	var boss, anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	prov := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer sk-company-1234" {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"bad key"}}`))
			return
		}
		w.Write([]byte(`{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`))
	}))
	defer prov.Close()
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, AI: ai.NewStore(pool, "secret"),
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept-Language", "ru")
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who, Admin: who == boss}))
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}

	// an employee cannot add a company connection; an administrator can
	company := url.Values{"scope": {"company"}, "name": {"Общий"}, "provider": {"openai"}, "base_url": {prov.URL + "/v1"}, "key": {"sk-company-1234"}}
	if st, _ := do(anna, "POST", "/settings/ai", company); st != http.StatusForbidden {
		t.Fatalf("employee added a company connection: %d", st)
	}
	if st, body := do(boss, "POST", "/settings/ai", company); st != http.StatusSeeOther {
		t.Fatalf("admin add: %d %s", st, body)
	}
	list, err := srv.AI.List(ctx, anna)
	must(err)
	if len(list) != 1 || list[0].Owner != "" || list[0].KeyTail != "1234" {
		t.Fatalf("list = %+v", list)
	}
	cid := list[0].ID
	// the employee sees it, cannot change it, adds their own
	_, page := do(anna, "GET", "/settings/ai", nil)
	if !strings.Contains(page, "Общий") || strings.Contains(page, `name="id" value="`+cid) || strings.Contains(page, "sk-company") {
		t.Fatalf("employee page: %s", page)
	}
	if st, _ := do(anna, "POST", "/settings/ai/"+cid+"/delete", url.Values{}); st == http.StatusSeeOther {
		t.Fatal("employee deleted the company connection")
	}
	if st, body := do(anna, "POST", "/settings/ai", url.Values{"scope": {"mine"}, "name": {"Свой"}, "provider": {"compatible"}, "base_url": {"http://ollama:11434/v1"}}); st != http.StatusSeeOther {
		t.Fatalf("own add: %d %s", st, body)
	}
	_, page = do(boss, "GET", "/settings/ai", nil)
	if !strings.Contains(page, `name="id" value="`+cid) || strings.Contains(page, "Свой") || !strings.Contains(page, "…1234") || strings.Contains(page, "sk-company") {
		t.Fatalf("admin page: %s", page)
	}
	if c, _, _ := srv.AI.For(ctx, anna); c.Name != "Свой" {
		t.Fatalf("anna works through %q", c.Name)
	}
	// the choice and the check
	if st, _ := do(anna, "POST", "/settings/ai/choice", url.Values{"connection": {cid}}); st != http.StatusSeeOther {
		t.Fatalf("choice: %d", st)
	}
	if c, _, _ := srv.AI.For(ctx, anna); c.ID != cid {
		t.Fatalf("anna works through %q after choice", c.Name)
	}
	st, _ := do(anna, "POST", "/settings/ai/"+cid+"/check", url.Values{})
	if c, _ := srv.AI.Get(ctx, anna, cid); st != http.StatusSeeOther || c.CheckedAt == nil || c.CheckError != "" {
		t.Fatalf("check: %d %+v", st, c)
	}

	// REST
	key := func(user string, scopes ...string) string {
		tok, err := srv.Keys.Create(ctx, user, "k", scopes, 0)
		must(err)
		return tok
	}
	call := func(tok, method, path, body string, want int, out any) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		if strings.Contains(string(raw), "sk-") {
			t.Fatalf("%s %s gave the key: %s", method, path, raw)
		}
		if out != nil {
			must(json.Unmarshal(raw, out))
		}
	}
	annaKey, bossKey := key(anna, "settings:read", "settings:write"), key(boss, "settings:read", "settings:write")
	var got struct {
		Connections []apiAIConn `json:"connections"`
	}
	call(key(anna, "settings:read"), "GET", "/api/v1/ai/connections", "", 200, &got)
	if len(got.Connections) != 2 || !got.Connections[0].Company || got.Connections[0].KeyTail != "1234" || got.Connections[1].Company {
		t.Fatalf("connections = %+v", got.Connections)
	}
	call(key(anna, "settings:read"), "POST", "/api/v1/ai/connections", `{"name":"x","provider":"openai","key":"sk-1"}`, 403, nil)
	call(annaKey, "POST", "/api/v1/ai/connections", `{"company":true,"name":"x","provider":"openai","key":"sk-1"}`, 403, nil)
	call(annaKey, "PATCH", "/api/v1/ai/connections/"+cid, `{"name":"y"}`, 403, nil)
	call(annaKey, "DELETE", "/api/v1/ai/connections/"+cid, "", 403, nil)
	call(annaKey, "POST", "/api/v1/ai/connections", `{"name":"","provider":"openai","key":"sk-1"}`, 422, nil)
	var c apiAIConn
	call(annaKey, "POST", "/api/v1/ai/connections", `{"name":"Claude","provider":"anthropic","model":"claude-sonnet-5-5","key":"sk-ant-5678"}`, 201, &c)
	if c.Company || c.KeyTail != "5678" || c.Default {
		t.Fatalf("created = %+v", c)
	}
	call(annaKey, "PATCH", "/api/v1/ai/connections/"+c.ID, `{"default":true}`, 200, &c)
	if !c.Default || c.Model != "claude-sonnet-5-5" || c.KeyTail != "5678" {
		t.Fatalf("patched = %+v", c)
	}
	call(bossKey, "PATCH", "/api/v1/ai/connections/"+c.ID, `{"name":"z"}`, 404, nil)
	var models struct {
		Models []string `json:"models"`
	}
	call(annaKey, "POST", "/api/v1/ai/connections/"+cid+"/check", "", 200, &models)
	if len(models.Models) != 2 {
		t.Fatalf("models = %v", models)
	}
	var mine struct {
		Connection string     `json:"connection"`
		Using      *apiAIConn `json:"using"`
	}
	call(annaKey, "PUT", "/api/v1/ai/mine", `{"connection":""}`, 200, &mine)
	if mine.Connection != "" || mine.Using == nil || mine.Using.ID != c.ID {
		t.Fatalf("mine = %+v", mine)
	}
	call(annaKey, "PUT", "/api/v1/ai/mine", `{"connection":"`+cid+`"}`, 200, &mine)
	if mine.Connection != cid || mine.Using.ID != cid {
		t.Fatalf("mine after choice = %+v", mine)
	}
	call(annaKey, "DELETE", "/api/v1/ai/connections/"+c.ID, "", 204, nil)
	call(bossKey, "DELETE", "/api/v1/ai/connections/"+cid, "", 204, nil)
	call(annaKey, "GET", "/api/v1/ai/mine", "", 200, &mine)
	if mine.Connection != "" || mine.Using == nil || mine.Using.Name != "Свой" {
		t.Fatalf("mine after deletes = %+v", mine)
	}
}
