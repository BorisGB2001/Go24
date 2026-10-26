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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCRMPlanHTTP: «План продаж» — the page shows the company's line with the won deals before a plan is set; the
// administrator's form sets the metric and the company's, a department's and an employee's targets, the page shows
// them with the percent; another employee sees the plan but neither the form nor saving; REST reads and sets it
// (only administrators set), a bad period is refused; «Удалить план» removes it.
func TestCRMPlanHTTP(t *testing.T) {
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
	var anna, petr, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('anna', 'Анна', 'anna@t', true) RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Отдел продаж') RETURNING id`).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, petr, sales)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Company: company.NewStore(pool),
		Keys: apikeys.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe := pipes[0]
	var won string
	for _, st := range pipe.Stages {
		if st.Semantic == "success" {
			won = st.ID
		}
	}
	id, err := srv.CRM.SaveDeal(ctx, petr, crm.Deal{Title: "Поставка", PipelineID: pipe.ID, Amount: 60000, Currency: "RUB", Responsible: crm.Ref{ID: petr}})
	must(err)
	_, err = srv.CRM.MoveDeal(ctx, petr, id, won)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookieOf := func(user string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: user, Admin: admin}))
		return rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(cookie *http.Cookie, path string, form url.Values) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		resp, err := client.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Location")
	}
	admin, worker := cookieOf(anna, true), cookieOf(petr, false)
	month := time.Now().Format("2006-01") + "-01"

	code, body, _ := do(admin, "/crm/plan", nil)
	if code != http.StatusOK || !strings.Contains(body, "Пётр") || !strings.Contains(body, "edit=1") {
		t.Fatalf("no plan yet: %d %s", code, body)
	}
	if code, body, _ = do(admin, "/crm/plan?edit=1&period=quarter", nil); code != http.StatusOK || !strings.Contains(body, `name="dept_id" value="`+sales+`"`) || !strings.Contains(body, `name="user_id" value="`+petr+`"`) {
		t.Fatalf("the form: %d %s", code, body)
	}
	form := url.Values{"pipeline": {pipe.ID}, "period": {"month"}, "start": {month}, "metric": {"sum"}, "company": {"120 000"},
		"dept_id": {sales}, "dept_amount": {"100000"}, "user_id": {anna, petr}, "user_amount": {"", "80000,5"}}
	if code, _, loc := do(admin, "/crm/plan", form); code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("saving: %d %s", code, loc)
	}
	plan, err := srv.CRM.SalesPlan(ctx, pipe.ID, "month", time.Now())
	must(err)
	if plan.Company.Target != 120000 || plan.Company.Percent() != 50 || len(plan.Departments) != 1 || plan.Departments[0].Percent() != 60 ||
		len(plan.Users) != 1 || plan.Users[0].Target != 80000.5 {
		t.Fatalf("the plan saved: %+v", plan)
	}
	if code, body, _ = do(worker, "/crm/plan", nil); code != http.StatusOK || !strings.Contains(body, "50%") || !strings.Contains(body, "Отдел продаж") || strings.Contains(body, "edit=1") {
		t.Fatalf("an employee sees the plan: %d %s", code, body)
	}
	if code, _, _ = do(worker, "/crm/plan", form); code == http.StatusSeeOther {
		t.Fatal("an employee sets the plan")
	}

	// REST
	api := func(tok, method, path, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := http.DefaultClient.Do(req)
		must(err)
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}
	tokAnna, err := srv.Keys.Create(ctx, anna, "plan", []string{"crm:read", "crm:write"}, 0)
	must(err)
	tokPetr, err := srv.Keys.Create(ctx, petr, "plan", []string{"crm:read", "crm:write"}, 0)
	must(err)
	code, out := api(tokPetr, "GET", "/api/v1/crm/plan?period=month&start="+month, "")
	if c, _ := out["company"].(map[string]any); code != http.StatusOK || out["set"] != true || c["target"] != 120000.0 || c["percent"] != 50.0 || c["won"] != 1.0 {
		t.Fatalf("REST read: %d %v", code, out)
	}
	put := `{"period": "quarter", "start": "` + month + `", "metric": "count", "company": 3, "users": {"` + petr + `": 2}}`
	if code, _ = api(tokPetr, "PUT", "/api/v1/crm/plan", put); code != http.StatusForbidden {
		t.Fatalf("an employee sets the plan through REST: %d", code)
	}
	code, out = api(tokAnna, "PUT", "/api/v1/crm/plan", put)
	if us, _ := out["users"].([]any); code != http.StatusOK || out["metric"] != "count" || len(us) != 1 || us[0].(map[string]any)["percent"] != 50.0 {
		t.Fatalf("REST set: %d %v", code, out)
	}
	if code, _ = api(tokAnna, "GET", "/api/v1/crm/plan?period=week", ""); code != http.StatusUnprocessableEntity {
		t.Fatalf("a week: %d", code)
	}
	form.Set("op", "delete")
	if code, _, _ := do(admin, "/crm/plan", form); code != http.StatusSeeOther {
		t.Fatalf("deleting: %d", code)
	}
	if plan, _ = srv.CRM.SalesPlan(ctx, pipe.ID, "month", time.Now()); plan.ID != "" {
		t.Fatal("the plan stays")
	}
}
