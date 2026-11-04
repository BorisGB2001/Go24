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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestWorktimeRightsHTTP: «Права доступа» of working time — by default a head sees and changes the days of their
// department only and the settings are the administrators'; the rights an administrator gives let the head see
// everyone's days but change only their own department's, and an employee change the settings; the REST reads the
// rights and only administrators set them.
func TestWorktimeRightsHTTP(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna, olga, adm := user("Борис"), user("Анна"), user("Ольга"), user("Админ")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, adm)
	must(err)
	dept := func(name, parent, head string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ($1, $2, $3) RETURNING id`,
			name, nn(parent), nn(head)).Scan(&id))
		return id
	}
	root := dept("Компания", "", "")
	sales := dept("Продажи", root, boss)
	it := dept("ИТ", root, "")
	for _, x := range [][2]string{{anna, sales}, {olga, it}} {
		_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, x[0], x[1])
		must(err)
	}

	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Keys: keys, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{boss, anna, adm} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == adm}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	day := time.Now().AddDate(0, 0, -1).Format(time.DateOnly)
	change := url.Values{"start": {"09:00"}, "end": {"18:00"}, "reason": {"забыл(а) отметиться"}}
	status := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		st, body := do(who, method, path, form)
		if st != want {
			t.Fatalf("%s %s: %d, want %d", method, path, st, want)
		}
		return body
	}

	// the defaults: the head — their department only, the settings and the rights — administrators
	status(boss, "GET", "/worktime/d/"+anna+"/"+day, nil, http.StatusOK)
	status(boss, "GET", "/worktime/d/"+olga+"/"+day, nil, http.StatusForbidden)
	status(boss, "GET", "/settings/worktime/rights", nil, http.StatusForbidden)
	status(anna, "GET", "/settings/worktime", nil, http.StatusForbidden)
	if body := status(boss, "GET", "/worktime/table", nil, http.StatusOK); strings.Contains(body, "Ольга") {
		t.Error("the time sheet shows a person of another department")
	}
	status(adm, "GET", "/settings/worktime/rights", nil, http.StatusOK)

	// heads see everyone, change their own departments; employees change the settings
	status(adm, "POST", "/settings/worktime/rights", url.Values{"head.view": {"all"}, "head.edit": {"own"},
		"employee.settings": {"all"}}, http.StatusSeeOther)
	if body := status(boss, "GET", "/worktime/d/"+olga+"/"+day, nil, http.StatusOK); strings.Contains(body, `name="reason"`) {
		t.Error("the day of a person one may only see offers the change form")
	}
	if body := status(boss, "GET", "/worktime/table", nil, http.StatusOK); !strings.Contains(body, "Ольга") {
		t.Error("the time sheet misses a person the head may see")
	}
	status(boss, "POST", "/worktime/d/"+olga+"/"+day, change, http.StatusForbidden)
	status(boss, "POST", "/worktime/d/"+anna+"/"+day, change, http.StatusSeeOther)
	var started bool
	must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM workdays WHERE user_id = $1 AND started_at IS NOT NULL)`, anna).Scan(&started))
	if !started {
		t.Error("the head's change of their person's day is not applied")
	}
	status(anna, "GET", "/settings/worktime", nil, http.StatusOK)
	status(anna, "GET", "/worktime/schedules/new", nil, http.StatusOK)
	status(anna, "GET", "/settings/worktime/rights", nil, http.StatusForbidden)

	// REST
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	bossKey, admKey := key(boss), key(adm)
	api := func(k, method, path, body string, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+k)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, res.StatusCode, raw, want)
		}
		return raw
	}
	var got company.StructRights
	must(json.Unmarshal(api(bossKey, "GET", "/api/v1/worktime/rights", "", http.StatusOK), &got))
	if got.Scope("head", "view") != "all" || got.Scope("employee", "settings") != "all" || got.Scope("deputy", "edit") != "" {
		t.Errorf("rights = %v", got)
	}
	api(bossKey, "GET", "/api/v1/worktime/days?user_id="+olga, "", http.StatusOK)
	api(bossKey, "PUT", "/api/v1/worktime/rights", `{"head":{"edit":"all"}}`, http.StatusForbidden)
	api(admKey, "PUT", "/api/v1/worktime/rights", `{"head":{"settings":"own"}}`, http.StatusUnprocessableEntity)
	must(json.Unmarshal(api(admKey, "PUT", "/api/v1/worktime/rights", `{"head":{"view":"own"}}`, http.StatusOK), &got))
	if got.Scope("head", "view") != "own" || got.Scope("head", "edit") != "" || got.Scope("employee", "settings") != "" {
		t.Errorf("rights after PUT = %v", got)
	}
	api(bossKey, "GET", "/api/v1/worktime/days?user_id="+olga, "", http.StatusForbidden)
}
