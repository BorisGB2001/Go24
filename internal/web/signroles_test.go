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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sign"
	"go24/internal/tasks"
)

// TestSignRoles: «Подпись → Права доступа» — only administrators manage the roles; by default an employee sees and
// manages only their own documents; a head given «свой отдел» sees and manages the documents of their department's
// people (not others'); a role without sending closes «Отправить на подпись»; the right «hr» opens HR documents; the
// default role cannot be deleted.
func TestSignRoles(t *testing.T) {
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
	var anna, bob, eve, admin, sales, file string
	for _, u := range []struct {
		id   *string
		name string
	}{{&anna, "Анна"}, {&bob, "Борис"}, {&eve, "Ева"}, {&admin, "Админ"}} {
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, u.name).Scan(u.id))
	}
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, head_id) VALUES ('Продажи', $1) RETURNING id`, anna).Scan(&sales))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, bob, sales)
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size) VALUES ('k1', 'dog.pdf', 10) RETURNING id`).Scan(&file))
	store := sign.NewStore(pool)
	docOf := func(author, title string) string {
		id, err := store.Create(ctx, sign.Doc{Title: title, FileID: file, AuthorID: author},
			[]sign.Party{{Name: "Клиент", Email: "client@t"}})
		must(err)
		return id
	}
	bobDoc, eveDoc := docOf(bob, "Договор Бориса"), docOf(eve, "Договор Евы")

	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Sign: store, I18n: bundle, pages: pages,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
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
	list := func(who string, want, not []string) {
		t.Helper()
		code, page, _ := do(who, "GET", "/sign", nil)
		if code != http.StatusOK {
			t.Fatalf("%s list: %d", who, code)
		}
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Fatalf("%s list: no %q", who, w)
			}
		}
		for _, n := range not {
			if strings.Contains(page, n) {
				t.Fatalf("%s list: %q", who, n)
			}
		}
	}
	// manages: reminding the signer is saved, not refused
	manages := func(who, doc string, want bool) {
		t.Helper()
		code, _, loc := do(who, "POST", "/sign/"+doc+"/remind", url.Values{"remind": {"2"}})
		saved := code == http.StatusSeeOther && strings.Contains(loc, "msg=")
		if saved != want {
			t.Fatalf("%s manages %s: %v (%d %s)", who, doc, saved, code, loc)
		}
	}

	// by default — one's own documents only
	list(anna, nil, []string{"Договор Бориса", "Договор Евы", `href="/sign/rights"`})
	list(bob, []string{"Договор Бориса", `href="/sign/new"`}, []string{"Договор Евы"})
	manages(anna, bobDoc, false)
	manages(bob, bobDoc, true)
	if code, _, _ := do(anna, "GET", "/sign/rights", nil); code != http.StatusForbidden {
		t.Fatalf("rights to an employee: %d", code)
	}
	list(admin, []string{"Договор Бориса", "Договор Евы", `href="/sign/rights"`}, nil)

	roles, err := store.Roles(ctx)
	must(err)
	if len(roles) != 2 || !roles[0].Default || roles[1].Code != "heads" {
		t.Fatalf("roles: %+v", roles)
	}
	code, page, _ := do(admin, "GET", "/sign/rights", nil)
	if code != http.StatusOK || !strings.Contains(page, "Руководители отделов") || !strings.Contains(page, `name="docs"`) {
		t.Fatalf("rights page: %d", code)
	}
	// the heads' role to Anna: her department's documents
	heads := roles[1].ID
	if code, _, loc := do(admin, "POST", "/sign/rights", url.Values{"id": {heads}, "docs": {"dept"}, "edit": {"dept"}, "send": {"all"},
		"templates": {"own"}, "hr": {"none"}, "members": {anna}}); code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("save heads: %d %s", code, loc)
	}
	list(anna, []string{"Договор Бориса"}, []string{"Договор Евы"})
	manages(anna, bobDoc, true)
	manages(anna, eveDoc, false)
	if code, _, _ := do(anna, "GET", "/sign/"+eveDoc+"/file/pdf", nil); code == http.StatusOK {
		t.Fatalf("anna got eve's document")
	}

	// no sending for everyone else; HR documents to Eve by a role of her own
	must2 := func(code int, _, loc string) {
		t.Helper()
		if code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
			t.Fatalf("save: %d %s", code, loc)
		}
	}
	must2(do(admin, "POST", "/sign/rights", url.Values{"id": {roles[0].ID}, "docs": {"own"}, "edit": {"own"}, "send": {"none"}, "templates": {"own"}, "hr": {"none"}}))
	if code, _, _ := do(bob, "GET", "/sign/new", nil); code != http.StatusForbidden {
		t.Fatalf("bob sends: %d", code)
	}
	if code, _, _ := do(bob, "POST", "/sign", url.Values{"title": {"x"}}); code != http.StatusForbidden {
		t.Fatalf("bob posts: %d", code)
	}
	list(bob, []string{"Договор Бориса"}, []string{`href="/sign/new"`})
	list(eve, nil, []string{`href="/sign/hr"`})
	must2(do(admin, "POST", "/sign/rights", url.Values{"name": {"Кадры"}, "docs": {"own"}, "edit": {"own"}, "send": {"all"},
		"templates": {"all"}, "hr": {"all"}, "members": {eve}}))
	list(eve, []string{`href="/sign/hr"`, `href="/sign/new"`}, nil)

	// the default role stays
	if code, _, loc := do(admin, "POST", "/sign/rights/delete", url.Values{"id": {roles[0].ID}}); code != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Fatalf("default deleted: %d %s", code, loc)
	}
	roles, err = store.Roles(ctx)
	must(err)
	if len(roles) != 3 || roles[2].Name != "Кадры" || len(roles[2].Members) != 1 || roles[2].Members[0].ID != eve {
		t.Fatalf("roles: %+v", roles)
	}
	if code, _, _ := do(admin, "POST", "/sign/rights/delete", url.Values{"id": {roles[2].ID}}); code != http.StatusSeeOther {
		t.Fatalf("delete: %d", code)
	}
	list(eve, nil, []string{`href="/sign/hr"`})
}
