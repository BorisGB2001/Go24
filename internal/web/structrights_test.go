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

// TestStructRightsHTTP: «Права доступа» of the company structure — by default only administrators change it; the
// rights an administrator gives let heads and deputies create, change and move people in their own departments
// (with the sub-departments) and nowhere else, and nothing they were not given; the profile's departments and the
// REST follow the same rights; only administrators set them.
func TestStructRightsHTTP(t *testing.T) {
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
	boss, dep, anna, kim, olga, adm := user("Борис"), user("Денис"), user("Анна"), user("Ким"), user("Ольга"), user("Админ")
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
	opt := dept("Опт", sales, "")
	it := dept("ИТ", root, "")
	_, err = pool.Exec(ctx, `INSERT INTO department_deputies (department_id, user_id) VALUES ($1, $2)`, sales, dep)
	must(err)
	member := func(u, d string) {
		_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, u, d)
		must(err)
	}
	member(anna, sales)
	member(kim, opt)
	member(olga, it)
	in := func(u, d string) bool {
		var ok bool
		must(pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM user_departments WHERE user_id = $1 AND department_id = $2)`, u, d).Scan(&ok))
		return ok
	}

	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Keys: keys, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{boss, dep, anna, adm} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == adm}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, asJSON bool) (int, string) {
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
		if asJSON {
			req.Header.Set("Accept", "application/json")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	moveUser := func(who, u, from, to string) int {
		st, _ := do(who, "POST", "/company/departments", url.Values{"op": {"move_user"}, "user": {u}, "from": {from}, "to": {to}}, true)
		return st
	}
	add := func(who, parent, name string) int {
		st, _ := do(who, "POST", "/company/departments", url.Values{"op": {"add"}, "parent": {parent}, "name": {name}}, false)
		return st
	}

	// by default: only administrators change the structure
	if st := add(boss, sales, "Розница"); st != http.StatusForbidden {
		t.Fatalf("a head added a department by default: %d", st)
	}
	if st := moveUser(boss, kim, opt, sales); st == http.StatusNoContent {
		t.Fatal("a head moved a person by default")
	}
	if _, body := do(boss, "GET", "/company/structure", nil, false); strings.Contains(body, "data-struct-edit") || strings.Contains(body, "data-can-staff") {
		t.Fatal("the structure lets a head drag by default")
	}
	// only administrators see and set the rights
	if st, _ := do(boss, "GET", "/company/structure/rights", nil, false); st != http.StatusForbidden {
		t.Fatalf("rights page for a head: %d", st)
	}
	if st, _ := do(boss, "POST", "/company/structure/rights", url.Values{"head.staff": {"all"}}, false); st != http.StatusForbidden {
		t.Fatalf("a head set the rights: %d", st)
	}
	st, body := do(adm, "GET", "/company/structure/rights", nil, false)
	if st != http.StatusOK || !strings.Contains(body, `name="head.chats"`) {
		t.Fatalf("rights page: %d", st)
	}
	st, _ = do(adm, "POST", "/company/structure/rights", url.Values{"head.create": {"own"}, "head.edit": {"own"}, "head.staff": {"own"},
		"head.chats": {"own"}, "deputy.staff": {"own"}, "employee.delete": {"bogus"}}, false)
	if st != http.StatusSeeOther {
		t.Fatalf("save rights: %d", st)
	}
	rights, err := srv.Company.StructRights(ctx)
	must(err)
	if rights.Scope("head", "create") != "own" || rights.Scope("deputy", "staff") != "own" || rights.Scope("employee", "delete") != "" ||
		rights.Scope("deputy", "chats") != "" {
		t.Fatalf("rights: %v", rights)
	}

	// the head: in his department and under it — yes, elsewhere — no
	if st := add(boss, opt, "Опт-Юг"); st != http.StatusSeeOther {
		t.Fatalf("a head adding under his sub-department: %d", st)
	}
	if st := add(boss, it, "Поддержка"); st != http.StatusForbidden {
		t.Fatalf("a head added a department elsewhere: %d", st)
	}
	if st := moveUser(boss, kim, opt, sales); st != http.StatusNoContent || !in(kim, sales) || in(kim, opt) {
		t.Fatalf("a head moving a person within his departments: %d", st)
	}
	if st := moveUser(boss, olga, it, sales); st == http.StatusNoContent || !in(olga, it) {
		t.Fatalf("a head took a person from another department: %d", st)
	}
	upd := url.Values{"op": {"update"}, "id": {opt}, "name": {"Оптовые продажи"}, "parent": {sales}, "head": {kim}}
	if st, _ := do(boss, "POST", "/company/departments", upd, false); st != http.StatusSeeOther {
		t.Fatalf("a head renaming his sub-department: %d", st)
	}
	upd.Set("parent", it)
	if st, _ := do(boss, "POST", "/company/departments", upd, false); st != http.StatusForbidden {
		t.Fatalf("a head moved his department under another one: %d", st)
	}
	if st, _ := do(boss, "POST", "/company/departments", url.Values{"op": {"move"}, "id": {it}, "parent": {sales}}, true); st == http.StatusNoContent {
		t.Fatal("a head moved another department under his")
	}
	var southID string
	must(pool.QueryRow(ctx, `SELECT id FROM departments WHERE name = 'Опт-Юг'`).Scan(&southID))
	if st, _ := do(boss, "POST", "/company/departments", url.Values{"op": {"delete"}, "id": {southID}}, false); st != http.StatusForbidden {
		t.Fatalf("a head deleted a department without the right: %d", st)
	}
	_, body = do(boss, "GET", "/company/structure", nil, false)
	if !strings.Contains(body, "data-struct-edit") || !strings.Contains(body, `data-dept="`+sales+`" data-can-edit data-can-staff`) ||
		strings.Contains(body, `data-dept="`+it+`" data-can`) || strings.Contains(body, "/company/structure/rights") {
		t.Fatal("the structure page does not follow the head's rights")
	}
	// the deputy: people only; an employee: nothing
	if st := moveUser(dep, anna, sales, opt); st != http.StatusNoContent || !in(anna, opt) {
		t.Fatalf("a deputy moving a person: %d", st)
	}
	if st := add(dep, sales, "Склад"); st != http.StatusForbidden {
		t.Fatalf("a deputy added a department: %d", st)
	}
	if st := moveUser(anna, kim, sales, opt); st == http.StatusNoContent {
		t.Fatal("an employee moved a person")
	}
	// the department's chats: the head over his sub-department now, the deputy no longer
	for who, want := range map[string]bool{boss: true, dep: false} {
		if ok, err := srv.Company.CanManageDeptChats(ctx, who, false, opt); err != nil || ok != want {
			t.Fatalf("chats of %s: %v %v", who, ok, err)
		}
	}
	// the profile's departments: only into and out of his own ones
	if st, _ := do(boss, "POST", "/u/"+kim+"/departments", url.Values{"dept": {sales, it}}, false); st != http.StatusForbidden || in(kim, it) {
		t.Fatalf("a head put a person into another department: %d", st)
	}
	if st, _ := do(boss, "POST", "/u/"+kim+"/departments", url.Values{"dept": {opt}}, false); st != http.StatusSeeOther || !in(kim, opt) || in(kim, sales) {
		t.Fatalf("a head setting departments: %d", st)
	}
	if st, _ := do(boss, "POST", "/u/"+olga+"/departments", url.Values{"dept": {it, sales}}, false); st != http.StatusForbidden {
		t.Fatalf("a head changed the departments of another department's person: %d", st)
	}

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
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, b)
		}
		return b
	}
	api(bossKey, "POST", "/api/v1/departments", `{"parent_id":"`+sales+`","name":"Онлайн"}`, http.StatusCreated)
	api(bossKey, "POST", "/api/v1/departments", `{"parent_id":"`+it+`","name":"Онлайн"}`, http.StatusForbidden)
	api(bossKey, "PATCH", "/api/v1/departments/"+it, `{"name":"ИТ-отдел"}`, http.StatusForbidden)
	api(bossKey, "PATCH", "/api/v1/departments/"+opt, `{"parent_id":"`+it+`"}`, http.StatusForbidden)
	api(bossKey, "PATCH", "/api/v1/departments/"+opt, `{"name":"Опт"}`, http.StatusNoContent)
	api(bossKey, "DELETE", "/api/v1/departments/"+southID, "", http.StatusForbidden)
	var got company.StructRights
	must(json.Unmarshal(api(bossKey, "GET", "/api/v1/departments/rights", "", http.StatusOK), &got))
	if got.Scope("head", "staff") != "own" {
		t.Fatalf("REST rights: %v", got)
	}
	api(bossKey, "PUT", "/api/v1/departments/rights", `{"head":{"delete":"all"}}`, http.StatusForbidden)
	api(admKey, "PUT", "/api/v1/departments/rights", `{"head":{"delete":"anywhere"}}`, http.StatusUnprocessableEntity)
	must(json.Unmarshal(api(admKey, "PUT", "/api/v1/departments/rights", `{"head":{"delete":"all"}}`, http.StatusOK), &got))
	if got.Scope("head", "delete") != "all" || got.Scope("head", "staff") != "" {
		t.Fatalf("REST rights after PUT: %v", got)
	}
	api(bossKey, "DELETE", "/api/v1/departments/"+southID, "", http.StatusNoContent)
	// a guest may nothing whatever the employees' rights
	api(admKey, "PUT", "/api/v1/departments/rights", `{"employee":{"staff":"all"}}`, http.StatusOK)
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, olga)
	must(err)
	if ok, err := srv.Company.StructCan(ctx, olga, false, "staff", it); err != nil || ok {
		t.Fatalf("a guest: %v %v", ok, err)
	}
	if ok, err := srv.Company.StructCan(ctx, anna, false, "staff", it); err != nil || !ok {
		t.Fatalf("an employee with «all»: %v %v", ok, err)
	}
}

func nn(id string) any {
	if id == "" {
		return nil
	}
	return id
}
