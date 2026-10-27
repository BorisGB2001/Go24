package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestPulse: «Пульс компании» — opening a page marks the employee's day in its tool (once; a phone marks «mobile» too,
// a guest nothing); the administrator sees the whole company, a head — their departments with sub-departments, others
// — nothing (no menu item either); the involvement counts 4 tools without «mobile»; periods and a department; REST.
func TestPulse(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	defer func(l *time.Location) { time.Local = l }(time.Local)
	time.Local = time.FixedZone("UTC+2", 2*3600) // dates from Postgres are UTC midnights: «today» must stay today
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position) VALUES ($1, $1, $1 || '@t', 'Менеджер') RETURNING id`,
			name).Scan(&id))
		return id
	}
	admin, head, worker, plain, guest := newUser("Админов"), newUser("Начальников"), newUser("Работягин"), newUser("Складов"), newUser("Гостев")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest)
	must(err)
	cs := company.NewStore(pool)
	must(cs.EnsureRoot(ctx, "Компания"))
	root, _, err := cs.Tree(ctx)
	must(err)
	sales, err := cs.CreateDepartment(ctx, root.ID, "Продажи", head)
	must(err)
	b2b, err := cs.CreateDepartment(ctx, sales, "Корпоративные", "")
	must(err)
	store, err := cs.CreateDepartment(ctx, root.ID, "Склад", "")
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2), ($3, $4), ($5, $2)`,
		worker, b2b, plain, store, head)
	must(err)
	keys := apikeys.NewStore(pool)
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read"}, time.Hour)
		must(err)
		return k
	}
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, Company: cs, Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{admin, head, worker, plain, guest} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Name: u, Admin: u == admin}))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, path, agent string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(cookie[who])
		if agent != "" {
			req.Header.Set("User-Agent", agent)
		}
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	tools := func(uid string) []string {
		t.Helper()
		var out []string
		rows, err := pool.Query(ctx, `SELECT tool FROM user_activity WHERE user_id = $1 ORDER BY tool`, uid)
		must(err)
		for rows.Next() {
			var s string
			must(rows.Scan(&s))
			out = append(out, s)
		}
		return out
	}
	today := time.Now().In(time.Local).Format(time.DateOnly)
	ago := func(d int) string { return time.Now().In(time.Local).AddDate(0, 0, -d).Format(time.DateOnly) }

	// pages mark the day of their tool, once; from a phone — «mobile» too; settings are no tool; a guest — nothing
	iphone := "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) Mobile/15E148"
	do(worker, "/my", iphone)
	do(worker, "/my", "")
	do(worker, "/company", "")
	do(worker, "/company/structure", "")
	if got := tools(worker); !slices.Equal(got, []string{"company", "mobile", "tasks"}) {
		t.Fatalf("the worker's tools: %v", got)
	}
	do(guest, "/my", iphone)
	if got := tools(guest); len(got) != 0 {
		t.Fatalf("a guest is marked: %v", got)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM user_activity WHERE user_id = $1 AND day = $2`, worker, today).Scan(&n))
	if n != 3 {
		t.Fatalf("rows of the worker today: %d", n)
	}
	// the worker also used the messenger and the feed today: 4 tools, involved; the store keeper used CRM 3 days ago
	_, err = pool.Exec(ctx, `INSERT INTO user_activity (day, user_id, tool) VALUES ($1, $2, 'im'), ($1, $2, 'feed'),
		($3, $4, 'crm'), ($5, $4, 'mail')`, today, worker, ago(3), plain, ago(60))
	must(err)

	// who sees it: an employee neither has the menu item nor the page
	if _, body := do(plain, "/company", ""); strings.Contains(body, `href="/company/pulse"`) {
		t.Fatal("an employee has the menu item")
	}
	if st, _ := do(plain, "/company/pulse", ""); st != http.StatusForbidden {
		t.Fatalf("an employee opens the pulse: %d", st)
	}
	// the head: their department with the sub-department, not the store
	if _, body := do(head, "/company", ""); !strings.Contains(body, `href="/company/pulse"`) {
		t.Fatal("the head has no menu item")
	}
	st, body := do(head, "/company/pulse?period=week", "")
	if st != http.StatusOK || !strings.Contains(body, "Работягин") || strings.Contains(body, "Складов") ||
		!strings.Contains(body, "Индекс вовлечённости") || !strings.Contains(body, "Корпоративные") || strings.Contains(body, ">Склад<") {
		t.Fatalf("the head's pulse: %d\n%s", st, body)
	}
	if st, _ := do(head, "/company/pulse?dept="+store, ""); st != http.StatusForbidden {
		t.Fatalf("the head opens another department: %d", st)
	}
	// the administrator: everybody; the week takes the CRM of 3 days ago, today does not, the quarter takes the mail
	if _, body := do(admin, "/company/pulse?period=week", ""); !strings.Contains(body, "Складов") || !strings.Contains(body, "CRM") {
		t.Fatal("the administrator's week has no store keeper with CRM")
	}
	api := func(k, path string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+k)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	userOf := func(out map[string]any, id string) map[string]any {
		for _, u := range out["users"].([]any) {
			if m := u.(map[string]any); m["id"] == id {
				return m
			}
		}
		return nil
	}
	adminKey := key(admin)
	st, out := api(adminKey, "/api/v1/pulse?period=today")
	if st != http.StatusOK || out["total"] != 4.0 || out["from"] != today {
		t.Fatalf("today: %d %v", st, out)
	}
	if w := userOf(out, worker); w == nil || w["involved"] != true || w["days"] != 1.0 {
		t.Fatalf("the worker today: %v", w)
	}
	if p := userOf(out, plain); p == nil || !slices.Equal(p["tools"].([]any), []any{"company"}) || p["involved"] != false {
		t.Fatalf("the store keeper today (only «Компания» he opened): %v", p)
	}
	if userOf(out, guest) != nil {
		t.Fatal("a guest in the pulse")
	}
	_, out = api(adminKey, "/api/v1/pulse?from="+ago(6)+"&to="+today)
	if p := userOf(out, plain); p == nil || !slices.Equal(p["tools"].([]any), []any{"crm", "company"}) || p["involved"] != false {
		t.Fatalf("the store keeper's week: %v", p)
	}
	if len(out["days"].([]any)) != 7 {
		t.Fatalf("days of the week: %v", out["days"])
	}
	for _, tl := range out["tools"].([]any) {
		if m := tl.(map[string]any); m["tool"] == "crm" && m["users"] != 1.0 {
			t.Fatalf("CRM of the week: %v", m)
		}
	}
	_, out = api(adminKey, "/api/v1/pulse?period=quarter")
	if p := userOf(out, plain); p == nil || len(p["tools"].([]any)) != 3 {
		t.Fatalf("the store keeper's quarter: %v", p)
	}
	// the head over REST: the department only; an employee — refused
	_, out = api(key(head), "/api/v1/pulse")
	if out["total"] != 2.0 || userOf(out, plain) != nil {
		t.Fatalf("the head over REST: %v", out)
	}
	if st, _ := api(key(plain), "/api/v1/pulse"); st != http.StatusForbidden {
		t.Fatalf("an employee over REST: %d", st)
	}
	if st, _ := api(adminKey, "/api/v1/pulse?from=2026-01-01&to=2025-01-01"); st == http.StatusOK {
		t.Fatal("a period backwards is taken")
	}
}
