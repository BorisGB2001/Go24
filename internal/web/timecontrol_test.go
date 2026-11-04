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

// TestTimeControlHTTP: «Контроль времени» is switched on in the settings; after a gap the page's report of activity
// returns the absence, the question is on top of the pages until answered; the manager sees it in the report and
// on the day, a stranger does not; REST reads the absences and the settings, answers, and the settings are for
// administrators only.
func TestTimeControlHTTP(t *testing.T) {
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
	user := func(sub, name string) (id string) {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $2, $1 || '@go24.test') RETURNING id`, sub, name).Scan(&id))
		return id
	}
	boss, anna, olga, admin := user("boss", "Борис Начальников"), user("anna", "Анна Сотрудникова"), user("olga", "Ольга Посторонняя"), user("adm", "Админ")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, admin)
	must(err)
	var dept string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Отдел', $1) RETURNING id`, boss).Scan(&dept))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, anna, dept)
	must(err)
	keys := apikeys.NewStore(pool)
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	bossKey, annaKey, olgaKey, adminKey := key(boss), key(anna), key(olga), key(admin)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, Company: company.NewStore(pool), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, id := range []string{boss, anna, olga, admin} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: id == admin}))
		as[id] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(req *http.Request, want int) string {
		t.Helper()
		resp, err := noRedirect.Do(req)
		must(err)
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", req.Method, req.URL.Path, resp.StatusCode, want, b)
		}
		if loc := resp.Header.Get("Location"); loc != "" {
			return loc
		}
		return string(b)
	}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("Accept", "application/json, text/html")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return send(req, want)
	}
	api := func(k, method, path, body string, want int) string {
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
		return send(req, want)
	}

	now := time.Now()
	if now.Hour() < 2 { // the day must have room for an hour's gap
		t.Skip("too early in the day")
	}
	must(srv.Company.WorkAction(ctx, anna, "start", "", now.Add(-90*time.Minute), company.Place{}))
	// off: activity is not watched
	if got := do(anna, "POST", "/worktime/alive", nil, 200); got != "{}\n" {
		t.Fatalf("alive when off: %q", got)
	}
	if page := do(anna, "GET", "/worktime", nil, 200); strings.Contains(page, "timecontrol.js") {
		t.Fatal("the script runs when off")
	}
	// an employee does not change the settings
	do(anna, "POST", "/settings/worktime", url.Values{"start": {"09:00"}, "end": {"18:00"}, "time_control": {"on"}}, 403)
	api(annaKey, "PUT", "/api/v1/worktime/timecontrol/settings", `{"active":true}`, 403)
	do(admin, "POST", "/settings/worktime", url.Values{"start": {"09:00"}, "end": {"18:00"}, "time_control": {"on"},
		"idle_minutes": {"20"}, "idle_ask": {"on"}}, 303)
	sched, err := srv.Tasks.Schedule(ctx)
	must(err)
	if !sched.TimeControl || !sched.IdleAsk || sched.IdleMinutes != 20 || sched.TimeControlSince == nil {
		t.Fatalf("settings %+v", sched)
	}
	since := *sched.TimeControlSince
	// saved again: still counted from the first switching on
	do(admin, "POST", "/settings/worktime", url.Values{"start": {"09:00"}, "end": {"18:00"}, "time_control": {"on"},
		"idle_minutes": {"20"}, "idle_ask": {"on"}}, 303)
	if sched, _ = srv.Tasks.Schedule(ctx); !sched.TimeControlSince.Equal(since) {
		t.Fatal("switching on was moved")
	}
	// the control was on an hour ago, the last activity was 40 minutes ago
	_, err = pool.Exec(ctx, `UPDATE company_settings SET value = jsonb_set(value::jsonb, '{time_control_since}', to_jsonb($1::timestamptz))
		WHERE key = 'worktime'`, now.Add(-time.Hour))
	must(err)
	_, err = pool.Exec(ctx, `UPDATE workdays SET active_at = $2 WHERE user_id = $1`, anna, now.Add(-40*time.Minute))
	must(err)
	if page := do(anna, "GET", "/worktime", nil, 200); !strings.Contains(page, "timecontrol.js") || !strings.Contains(page, `id="idle-tpl"`) {
		t.Fatal("no script for the running day")
	}
	var out struct {
		Idle struct {
			ID       int64
			From, To string
			Ask      bool
		}
	}
	must(json.Unmarshal([]byte(do(anna, "POST", "/worktime/alive", nil, 200)), &out))
	if out.Idle.ID == 0 || !out.Idle.Ask || out.Idle.From != now.Add(-40*time.Minute).Format("15:04") {
		t.Fatalf("alive: %+v", out)
	}
	if got := do(anna, "POST", "/worktime/alive", nil, 200); got != "{}\n" {
		t.Fatalf("an absence twice: %q", got)
	}
	idle := strconv.FormatInt(out.Idle.ID, 10)
	if page := do(anna, "GET", "/company", nil, 200); !strings.Contains(page, `action="/worktime/idle/`+idle+`"`) {
		t.Fatal("the question is not on top")
	}
	// the manager sees the absence, a stranger — only themself
	if page := do(boss, "GET", "/worktime/timecontrol", nil, 200); !strings.Contains(page, "Анна Сотрудникова") || !strings.Contains(page, "dl over") {
		t.Fatal("the manager does not see the absence")
	}
	if page := do(olga, "GET", "/worktime/timecontrol?all=1", nil, 200); strings.Contains(page, "Анна Сотрудникова") {
		t.Fatal("a stranger sees the absence")
	}
	dayURL := "/worktime/d/" + anna + "/" + now.Format(time.DateOnly)
	if page := do(boss, "GET", dayURL, nil, 200); !strings.Contains(page, `id="idle"`) || strings.Contains(page, `action="/worktime/idle/`) {
		t.Fatal("the manager's view of the day")
	}
	// only the person answers
	do(boss, "POST", "/worktime/idle/"+idle, url.Values{"kind": {"work"}}, 400)
	do(anna, "POST", "/worktime/idle/"+idle, url.Values{"kind": {"work"}, "reason": {"на складе"}}, 204)
	if page := do(anna, "GET", "/company", nil, 200); strings.Contains(page, `action="/worktime/idle/`+idle+`"`) {
		t.Fatal("the question stays after the answer")
	}
	if page := do(boss, "GET", dayURL, nil, 200); !strings.Contains(page, "на складе") {
		t.Fatal("the reason is not on the day")
	}
	// REST
	var list []apiIdleOut
	must(json.Unmarshal([]byte(api(bossKey, "GET", "/api/v1/worktime/timecontrol", "", 200)), &list))
	if len(list) != 1 || list[0].UserID != anna || list[0].Kind != "work" || list[0].Reason != "на складе" {
		t.Fatalf("api list %+v", list)
	}
	api(olgaKey, "GET", "/api/v1/worktime/timecontrol?user_id="+anna, "", 403)
	if got := api(olgaKey, "GET", "/api/v1/worktime/timecontrol", "", 200); got != "[]\n" {
		t.Fatalf("a stranger's list %s", got)
	}
	api(annaKey, "POST", "/api/v1/worktime/timecontrol/"+idle, `{"kind":"personal","reason":"обед"}`, 204)
	api(bossKey, "POST", "/api/v1/worktime/timecontrol/"+idle, `{"kind":"work"}`, 404)
	var set apiTimeControlSettings
	must(json.Unmarshal([]byte(api(annaKey, "GET", "/api/v1/worktime/timecontrol/settings", "", 200)), &set))
	if !set.Active || set.IdleMinutes != 20 || !set.Ask {
		t.Fatalf("settings %+v", set)
	}
	must(json.Unmarshal([]byte(api(adminKey, "PUT", "/api/v1/worktime/timecontrol/settings", `{"active":false}`, 200)), &set))
	if set.Active {
		t.Fatal("not switched off")
	}
	if sched, _ = srv.Tasks.Schedule(ctx); sched.TimeControl || sched.Start != "09:00" {
		t.Fatalf("the schedule after REST %+v", sched)
	}
	api(adminKey, "PUT", "/api/v1/worktime/timecontrol/settings", `{"active":true,"idle_minutes":2}`, 422)
}
