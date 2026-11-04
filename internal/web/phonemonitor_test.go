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
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestPhoneMonitor: «Текущие звонки» — an employee without the right «monitor» has no page; a head of department
// (the built-in role «Руководители отделов» has «свой отдел») sees and joins the calls of their department only, not
// their own or others'; joining rings the head's inner number and puts them on ChanSpy of the employee's channel in
// the mode; an ended call is not joined; an administrator sees everyone's; the same by REST. A call ringing for a
// colleague is picked up: the head's phone rings and goes to go24-pickup with the colleague's number.
func TestPhoneMonitor(t *testing.T) {
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
	var anna, bob, eve, admin, sales string
	for _, u := range []struct {
		id   *string
		name string
	}{{&anna, "Анна"}, {&bob, "Борис"}, {&eve, "Ева"}, {&admin, "Админ"}} {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, u.name).Scan(u.id))
	}
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Продажи', $1) RETURNING id`, anna).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, bob, sales)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO telephony_role_members (role_id, user_id) SELECT id, $1 FROM telephony_roles WHERE code = 'heads'`, anna)
	must(err)
	store := telephony.NewStore(pool, "secret")
	for u, n := range map[string]string{anna: "101", bob: "102", eve: "103"} {
		must(store.SetNumber(ctx, u, n))
	}
	must(store.SetEnabled(ctx, admin, true))

	var mu sync.Mutex
	var spied []url.Values
	var spyVars []map[string]string
	ast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/ari/channels":
			now := time.Now().Add(-90 * time.Second).Format("2006-01-02T15:04:05.000-0700")
			io.WriteString(w, `[
			 {"name": "PJSIP/w102-00000001", "state": "Up", "caller": {"number": "79001112233"}, "creationtime": "`+now+`", "dialplan": {"app_name": "AppDial"}},
			 {"name": "PJSIP/103-00000002", "state": "Up", "caller": {"number": "103"}, "connected": {"number": "74950001122"}, "creationtime": "`+now+`", "dialplan": {"app_name": "Dial"}},
			 {"name": "PJSIP/101-00000003", "state": "Up", "caller": {"number": "101"}, "connected": {"number": "74950009999"}, "creationtime": "`+now+`", "dialplan": {"app_name": "Dial"}},
			 {"name": "PJSIP/102-00000004", "state": "Ringing", "caller": {"number": "74957770000"}, "creationtime": "`+now+`", "dialplan": {"app_name": "AppDial"}}]`)
		case r.Method == "POST" && r.URL.Path == "/ari/channels":
			var body struct {
				Variables map[string]string `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			spied, spyVars = append(spied, r.URL.Query()), append(spyVars, body.Variables)
			mu.Unlock()
			io.WriteString(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ast.Close()

	keys := apikeys.NewStore(pool)
	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: keys, I18n: bundle, pages: pages,
		Telephony: &telephony.PBX{Store: store, Key: "ast-key", Dir: t.TempDir(), ARI: &telephony.ARI{URL: ast.URL, User: "go24", Password: "x"}},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}

	// no right — no page, no link
	if code, _, _ := do(bob, "GET", "/telephony/active", nil); code != http.StatusForbidden {
		t.Fatalf("bob opens the monitor: %d", code)
	}
	if _, page, _ := do(bob, "GET", "/telephony/calls", nil); strings.Contains(page, "/telephony/active") {
		t.Error("bob sees the link to the monitor")
	}
	// the head: the department's call with the buttons, their own without, others' not at all
	_, page, _ := do(anna, "GET", "/telephony/active", nil)
	if !strings.Contains(page, "79001112233") || !strings.Contains(page, `value="PJSIP/w102-00000001"`) || !strings.Contains(page, "1:3") {
		t.Errorf("anna does not see bob's call: %s", page[strings.Index(page, "telactive"):])
	}
	if strings.Contains(page, "74950001122") || strings.Contains(page, `value="PJSIP/101-00000003"`) || !strings.Contains(page, "74950009999") {
		t.Errorf("anna sees eve's call or may join her own")
	}
	if _, page, _ := do(anna, "GET", "/telephony/calls", nil); !strings.Contains(page, "/telephony/active") {
		t.Error("anna has no link to the monitor")
	}
	code, _, loc := do(anna, "POST", "/telephony/active/spy", url.Values{"channel": {"PJSIP/w102-00000001"}, "mode": {"whisper"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/telephony/active?msg=") || len(spied) != 1 {
		t.Fatalf("anna joins: %d %s %v", code, loc, spied)
	}
	if q := spied[0]; q.Get("endpoint") != "Local/101@go24-call/n" || q.Get("context") != "go24-spy" ||
		spyVars[0]["GO24_SPY"] != "PJSIP/w102-00000001" || spyVars[0]["GO24_SPYMODE"] != "whisper" {
		t.Errorf("spy: %v %v", q, spyVars[0])
	}
	for _, ch := range []string{"PJSIP/103-00000002", "PJSIP/101-00000003", "PJSIP/w102-00000009"} {
		if code, _, loc := do(anna, "POST", "/telephony/active/spy", url.Values{"channel": {ch}}); code == http.StatusSeeOther && !strings.Contains(loc, "msg=") {
			t.Errorf("anna joins %s", ch)
		}
	}
	if len(spied) != 1 {
		t.Errorf("rang: %d", len(spied))
	}
	// the administrator sees everyone's (but has no inner number to listen with)
	_, page, _ = do(admin, "GET", "/telephony/active", nil)
	if !strings.Contains(page, "74950001122") || !strings.Contains(page, "79001112233") || strings.Contains(page, `name="mode"`) {
		t.Errorf("admin's monitor: %s", page)
	}

	// REST
	k, err := keys.Create(ctx, anna, "k", []string{"telephony:read", "telephony:write"}, time.Hour)
	must(err)
	api := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if code, body := api("GET", "/api/v1/telephony/active", ""); code != 200 || !strings.Contains(body, `"channel": "PJSIP/w102-00000001"`) ||
		!strings.Contains(body, `"direction": "in"`) || strings.Contains(body, "+74950001122") {
		t.Errorf("api active: %d %s", code, body)
	}
	if code, body := api("POST", "/api/v1/telephony/active/spy", `{"channel": "PJSIP/w102-00000001", "mode": "barge"}`); code != 200 || len(spied) != 2 || spyVars[1]["GO24_SPYMODE"] != "barge" {
		t.Errorf("api spy: %d %s", code, body)
	}
	if code, _ := api("POST", "/api/v1/telephony/active/spy", `{"channel": "PJSIP/103-00000002"}`); code == 200 {
		t.Error("api spy on eve")
	}

	// «Перехватить» a call ringing for bob: anna's phone rings, the one who answers takes it (go24-pickup, **102)
	_, page, _ = do(anna, "GET", "/telephony/active", nil)
	if !strings.Contains(page, `action="/telephony/active/pick"`) || !strings.Contains(page, `value="PJSIP/102-00000004"`) {
		t.Errorf("anna cannot pick bob's ringing call up")
	}
	code, _, loc = do(anna, "POST", "/telephony/active/pick", url.Values{"channel": {"PJSIP/102-00000004"}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, "/telephony/active?msg=") || len(spied) != 3 {
		t.Fatalf("anna picks up: %d %s %d", code, loc, len(spied))
	}
	if q := spied[2]; q.Get("endpoint") != "Local/101@go24-call/n" || q.Get("context") != "go24-pickup" || q.Get("extension") != "102" ||
		spyVars[2]["GO24_PICKER"] != "101" || spyVars[2]["GO24_PICKOK"] != "1" {
		t.Errorf("pickup: %v %v", q, spyVars[2])
	}
	if code, _, loc := do(anna, "POST", "/telephony/active/pick", url.Values{"channel": {"PJSIP/w102-00000001"}}); code == http.StatusSeeOther && !strings.Contains(loc, "msg=") || len(spied) != 3 {
		t.Errorf("anna picks up a call bob talks in")
	}
	if code, body := api("POST", "/api/v1/telephony/active/pick", `{"channel": "PJSIP/102-00000004"}`); code != 200 || len(spied) != 4 {
		t.Errorf("api pick: %d %s", code, body)
	}
	// turned off: no button, no picking up
	must(store.SetPicking(ctx, admin, false))
	if _, page, _ = do(anna, "GET", "/telephony/active", nil); strings.Contains(page, `action="/telephony/active/pick"`) {
		t.Error("the button while picking up is off")
	}
	if code, _ := api("POST", "/api/v1/telephony/active/pick", `{"channel": "PJSIP/102-00000004"}`); code == 200 || len(spied) != 4 {
		t.Error("api pick while off")
	}
}
