package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
	"go24/internal/telephony"
)

// TestTelRoles: «Права доступа» of telephony — only administrators manage the roles; by default an employee sees and
// hears their own calls, a head with «свой отдел» — their department's too (not others', even asked by name), a role
// without the log closes it, «Настройки телефонии» opens the settings to a non-administrator; Asterisk learns whether
// a number is CRM's only with its key.
func TestTelRoles(t *testing.T) {
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
	store := telephony.NewStore(pool, "secret")
	for u, n := range map[string]string{anna: "101", bob: "102", eve: "103"} {
		must(store.SetNumber(ctx, u, n))
	}
	must(store.SetEnabled(ctx, admin, true))
	records := t.TempDir()
	for i, c := range []struct{ from, user string }{{"102", bob}, {"103", eve}} {
		rec := c.from + ".wav"
		must(os.WriteFile(filepath.Join(records, rec), []byte("RIFF"), 0o644))
		_, err = pool.Exec(ctx, `INSERT INTO telephony_calls (uniqueid, from_number, to_number, from_user, started_at, status, record, line)
			VALUES ($1, $2, $3, $4, now(), 'answered', $5, '+74950000000')`, "u"+c.from, c.from, "8900000000"+string(rune('1'+i)), c.user, rec)
		must(err)
	}
	callOf := func(number string) string {
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM telephony_calls WHERE from_number = $1`, number).Scan(&id))
		return id
	}

	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	ast := httptest.NewServer(http.NotFoundHandler()) // Asterisk is down
	defer ast.Close()
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), I18n: bundle, pages: pages,
		Telephony: &telephony.PBX{Store: store, Records: records, Key: "ast-key", Dir: t.TempDir(), ARI: &telephony.ARI{URL: ast.URL, User: "go24", Password: "x"}},
		Sessions:  auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
		if who != "" {
			rec := httptest.NewRecorder()
			must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who, Admin: who == admin, AdminMode: who == admin}))
			req.AddCookie(rec.Result().Cookies()[0])
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	sees := func(who, path string, want, not []string) {
		t.Helper()
		code, page := do(who, "GET", path, nil)
		if code != http.StatusOK {
			t.Fatalf("%s %s: %d", who, path, code)
		}
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Errorf("%s %s: no %q", who, path, w)
			}
		}
		for _, n := range not {
			if strings.Contains(page, n) {
				t.Errorf("%s %s: %q is seen", who, path, n)
			}
		}
	}

	// by default — one's own calls only
	sees(anna, "/telephony/calls", nil, []string{"89000000001", "89000000002"})
	sees(bob, "/telephony/calls", []string{"89000000001"}, []string{"89000000002"})
	if code, _ := do(anna, "GET", "/telephony/calls/"+callOf("102")+"/record", nil); code != http.StatusForbidden {
		t.Fatalf("anna hears bob's call by default: %d", code)
	}
	// the roles are the administrators'
	if code, _ := do(anna, "GET", "/telephony/rights", nil); code != http.StatusForbidden {
		t.Fatalf("anna opens the rights: %d", code)
	}
	if code, _ := do(anna, "POST", "/telephony/rights/new", url.Values{"name": {"Я"}, "calls": {"all"}, "members": {anna}}); code != http.StatusForbidden {
		t.Fatalf("anna makes a role: %d", code)
	}
	roles, err := store.Roles(ctx)
	must(err)
	if code, _ := do(admin, "POST", "/telephony/rights/"+roles[1].ID, url.Values{"calls": {"dept"}, "records": {"dept"}, "outgoing": {"all"},
		"settings": {"none"}, "members": {anna}}); code != http.StatusSeeOther {
		t.Fatalf("the heads' role: %d", code)
	}
	if code, _ := do(admin, "POST", "/telephony/rights/new", url.Values{"name": {"Без журнала"}, "calls": {"none"}, "records": {"none"},
		"outgoing": {"crm"}, "settings": {"all"}, "members": {eve}}); code != http.StatusSeeOther {
		t.Fatalf("a new role: %d", code)
	}
	sees(admin, "/telephony/rights", []string{"Руководители отделов", "Без журнала", "Анна", "Только клиентам CRM"}, nil)

	// the head: the department's calls and records, not eve's even asked by name
	sees(anna, "/telephony/calls", []string{"89000000001", `name="user"`}, []string{"89000000002"})
	sees(anna, "/telephony/calls?user="+eve, []string{"89000000001"}, []string{"89000000002"})
	sees(anna, "/telephony/stats", []string{"Борис"}, []string{"Ева"})
	if code, _ := do(anna, "GET", "/telephony/calls/"+callOf("102")+"/record", nil); code != http.StatusOK {
		t.Fatalf("anna does not hear bob's call: %d", code)
	}
	if code, _ := do(anna, "GET", "/telephony/calls/"+callOf("103")+"/record", nil); code != http.StatusForbidden {
		t.Fatalf("anna hears eve's call: %d", code)
	}
	// eve: no log, no records of her own, but the settings
	if code, _ := do(eve, "GET", "/telephony/calls", nil); code == http.StatusOK {
		t.Fatalf("eve opens the log")
	}
	if code, _ := do(eve, "GET", "/telephony/calls/"+callOf("103")+"/record", nil); code != http.StatusForbidden {
		t.Fatalf("eve hears her call: %d", code)
	}
	if code, _ := do(eve, "POST", "/telephony/blacklist", url.Values{"number": {"84951112233"}}); code != http.StatusSeeOther {
		t.Fatalf("eve with the settings right adds to the black list: %d", code)
	}
	if code, _ := do(bob, "POST", "/telephony/blacklist", url.Values{"number": {"84951112244"}}); code != http.StatusForbidden {
		t.Fatalf("bob adds to the black list: %d", code)
	}
	sees(eve, "/my", []string{`href="/settings/telephony"`}, nil)
	sees(bob, "/my", nil, []string{`href="/settings/telephony"`})
	// the dialplan knows eve's limit
	limits, err := store.OutLimits(ctx, []string{anna, bob, eve})
	must(err)
	if len(limits) != 1 || limits[eve] != "crm" {
		t.Fatalf("the limits: %v", limits)
	}
	// Asterisk asks with its key; without CRM no number is a client's
	if code, _ := do("", "POST", "/telephony/outok", url.Values{"key": {"bad"}, "to": {"89000000001"}}); code != http.StatusForbidden {
		t.Fatalf("outok without the key: %d", code)
	}
	if code, body := do("", "POST", "/telephony/outok", url.Values{"key": {"ast-key"}, "to": {"89000000001"}}); code != http.StatusOK || body != "" {
		t.Fatalf("outok: %d %q", code, body)
	}
	// the built-in role is not deleted
	if code, _ := do(admin, "POST", "/telephony/rights/"+roles[0].ID+"/delete", nil); code != http.StatusSeeOther {
		t.Fatalf("delete the default role: %d", code)
	}
	if roles, _ = store.Roles(ctx); len(roles) != 3 {
		t.Fatalf("the default role is gone: %+v", roles)
	}
}
