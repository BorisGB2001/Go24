package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCollabCreatorsHTTP: «Настройки → Коллабы» is the administrators' page; the rule it saves hides «Создать
// коллаб» and refuses making a collab on the page and over the REST API to whom it does not let.
func TestCollabCreatorsHTTP(t *testing.T) {
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
	newUser := func(name string, groups ...string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, username, groups) VALUES ($1, $1, $1 || '@t', $1, $2)
			RETURNING id`, name, append([]string{}, groups...)).Scan(&id))
		return id
	}
	admin, plain, chosen := newUser("admin", "go24-admins"), newUser("plain"), newUser("chosen")
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&dept))

	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), AdminGroups: []string{"go24-admins"},
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for u, a := range map[string]bool{admin: true, plain: false, chosen: false} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: a}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.AddCookie(cookie[who])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	api := func(who, body string) int {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"collabs:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest("POST", hs.URL+"/api/v1/collabs", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		res.Body.Close()
		return res.StatusCode
	}
	canCreate := func(who string) bool {
		t.Helper()
		st, body := do(who, "GET", "/collabs", nil)
		if st != http.StatusOK {
			t.Fatalf("/collabs: %d", st)
		}
		return strings.Contains(body, `method="post" action="/collabs"`)
	}

	if st, _ := do(plain, "GET", "/settings/collabs", nil); st != http.StatusForbidden {
		t.Fatalf("an employee opened the settings: %d", st)
	}
	if st, _ := do(plain, "POST", "/settings/collabs", url.Values{"enabled": {"1"}, "who": {"admins"}}); st != http.StatusForbidden {
		t.Fatalf("an employee saved the settings: %d", st)
	}
	if st, body := do(admin, "GET", "/settings/collabs", nil); st != http.StatusOK || !strings.Contains(body, `value="all" checked`) {
		t.Fatalf("the settings: %d", st)
	}
	if _, body := do(admin, "GET", "/settings", nil); !strings.Contains(body, `href="/settings/collabs"`) {
		t.Error("no «Коллабы» among the company's settings")
	}
	if !canCreate(plain) {
		t.Fatal("by default an employee makes collabs")
	}

	// the chosen employee and the department
	st, _ := do(admin, "POST", "/settings/collabs", url.Values{"enabled": {"1"}, "who": {"some"}, "creators": {chosen, "dept:" + dept}})
	if st != http.StatusSeeOther {
		t.Fatalf("save: %d", st)
	}
	if st, body := do(admin, "GET", "/settings/collabs", nil); st != http.StatusOK || !strings.Contains(body, `value="dept:`+dept+`"`) ||
		!strings.Contains(body, `value="`+chosen+`"`) || !strings.Contains(body, "Продажи") {
		t.Fatalf("the chosen are not shown: %d", st)
	}
	if canCreate(plain) || !canCreate(chosen) || !canCreate(admin) {
		t.Fatal("«Создать коллаб» does not follow the rule")
	}
	if st, _ := do(plain, "POST", "/collabs", url.Values{"name": {"Нельзя"}}); st == http.StatusSeeOther {
		t.Fatal("an employee made a collab on the page")
	}
	if st, _ := do(chosen, "POST", "/collabs", url.Values{"name": {"Можно"}}); st != http.StatusSeeOther {
		t.Fatalf("the chosen made no collab: %d", st)
	}
	if st := api(plain, `{"name":"Нельзя"}`); st != http.StatusForbidden {
		t.Fatalf("an employee made a collab over the API: %d", st)
	}

	// administrators only: the administrator's key makes one even without admin mode
	if st, _ := do(admin, "POST", "/settings/collabs", url.Values{"enabled": {"1"}, "who": {"admins"}}); st != http.StatusSeeOther {
		t.Fatalf("save admins: %d", st)
	}
	if canCreate(chosen) || !canCreate(admin) {
		t.Fatal("administrators only")
	}
	if st := api(chosen, `{"name":"Нельзя"}`); st != http.StatusForbidden {
		t.Fatalf("an employee over the API, admins only: %d", st)
	}
	if st := api(admin, `{"name":"Админский"}`); st != http.StatusCreated {
		t.Fatalf("an administrator over the API: %d", st)
	}

	// off: nobody
	if st, _ := do(admin, "POST", "/settings/collabs", url.Values{"who": {"all"}}); st != http.StatusSeeOther {
		t.Fatalf("save off: %d", st)
	}
	if canCreate(admin) || canCreate(plain) {
		t.Fatal("collabs are off, yet «Создать коллаб» is there")
	}
}
