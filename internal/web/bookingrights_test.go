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
	"go24/internal/booking"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestBookingRightsHTTP: «Права доступа» of «Онлайн-запись» — by default everyone works with it and administrators
// manage it; chosen ones (an employee, a department with its sub-departments) work with it, the others lose the menu
// item, its pages and REST; a chosen manager changes resources, types and links like an administrator.
func TestBookingRightsHTTP(t *testing.T) {
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
	var boss, ann, bob, eve, top, sales string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Босс', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@go24.test') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@go24.test') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&top))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Салон') RETURNING id`, top).Scan(&sales))
	var east string
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Салон Восток') RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, bob, east)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Booking: booking.NewStore(pool), Keys: keys, I18n: bundle,
		AdminGroups: []string{"go24-admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(s auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, s))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), ann: cookie(auth.Session{UserID: ann}),
		bob: cookie(auth.Session{UserID: bob}), eve: cookie(auth.Session{UserID: eve})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(as[who])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	menuHas := func(who string) bool {
		t.Helper()
		code, body := do(who, "GET", "/my", nil)
		if code != 200 {
			t.Fatalf("GET /my: %d", code)
		}
		return strings.Contains(body, `href="/booking"`)
	}
	types, err := srv.Booking.Types(ctx)
	must(err)
	addType := url.Values{"name": {"Кресло"}}

	// By default: everyone works with it, only administrators manage it and set the rights.
	if !menuHas(ann) {
		t.Fatal("by default everyone sees «Онлайн-запись»")
	}
	if code, _ := do(ann, "GET", "/booking", nil); code != 200 {
		t.Fatalf("by default the page is open: %d", code)
	}
	if code, _ := do(ann, "POST", "/booking/types", addType); code != 403 {
		t.Fatalf("an employee adds a type: %d", code)
	}
	if code, _ := do(ann, "GET", "/booking/rights", nil); code != 403 {
		t.Fatalf("an employee opens the rights: %d", code)
	}
	if code, body := do(boss, "GET", "/booking", nil); code != 200 || !strings.Contains(body, `href="/booking/rights"`) {
		t.Fatalf("an administrator has «Права доступа»: %d", code)
	}

	// Anna and the department «Салон» (Bob is in its sub-department) work with it; Eve manages it.
	if code, _ := do(boss, "POST", "/booking/rights", url.Values{"use_who": {"chosen"}, "use": {ann, "dept:" + sales}, "manage": {eve}}); code != 303 {
		t.Fatalf("save rights: %d", code)
	}
	r, err := srv.Booking.GetRights(ctx)
	must(err)
	if len(r.Use.Users) != 1 || len(r.Use.Depts) != 1 || len(r.Manage.Users) != 1 || r.Manage.Users[0] != eve {
		t.Fatalf("rights: %+v", r)
	}
	if code, body := do(boss, "GET", "/booking/rights", nil); code != 200 || !strings.Contains(body, "Салон") || !strings.Contains(body, "Ева") {
		t.Fatalf("the rights page shows the chosen: %d", code)
	}
	for who, want := range map[string]bool{ann: true, bob: true, eve: true, boss: true} {
		if menuHas(who) != want {
			t.Fatalf("menu of %s: want %v", who, want)
		}
	}
	// Somebody outside of them: no menu item, no pages.
	var zoe string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('zoe', 'Зоя', 'zoe@go24.test') RETURNING id`).Scan(&zoe))
	as[zoe] = cookie(auth.Session{UserID: zoe})
	if menuHas(zoe) {
		t.Fatal("an employee not chosen sees «Онлайн-запись»")
	}
	for _, p := range []string{"/booking", "/booking/list", "/booking/stats", "/booking/links", "/booking/resources"} {
		if code, _ := do(zoe, "GET", p, nil); code != 403 {
			t.Fatalf("GET %s by an employee not chosen: %d", p, code)
		}
	}
	if code, _ := do(zoe, "POST", "/booking", url.Values{"resource": {"x"}}); code != 403 {
		t.Fatalf("a booking by an employee not chosen: %d", code)
	}
	// Bob works with it, yet doesn't manage; Eve manages: a type, a resource, a link.
	if code, _ := do(bob, "GET", "/booking", nil); code != 200 {
		t.Fatalf("Bob through his department: %d", code)
	}
	if code, _ := do(bob, "POST", "/booking/types", addType); code != 403 {
		t.Fatalf("Bob adds a type: %d", code)
	}
	if code, _ := do(eve, "POST", "/booking/types", addType); code != 303 {
		t.Fatalf("Eve adds a type: %d", code)
	}
	if code, body := do(eve, "GET", "/booking/resources", nil); code != 200 || !strings.Contains(body, `href="/booking/resources?edit=new"`) {
		t.Fatalf("Eve sees «Добавить ресурс»: %d", code)
	}
	if code, body := do(bob, "GET", "/booking/resources", nil); code != 200 || strings.Contains(body, `href="/booking/resources?edit=new"`) {
		t.Fatalf("Bob doesn't see «Добавить ресурс»: %d", code)
	}
	if code, _ := do(eve, "POST", "/booking/resources", url.Values{"type": {types[0].ID}, "name": {"Мастер"}, "slot": {"30"},
		"day": {"1", "2", "3", "4", "5"}, "from": {"09:00"}, "to": {"18:00"}}); code != 303 {
		t.Fatalf("Eve adds a resource: %d", code)
	}
	if code, _ := do(eve, "POST", "/booking/links", url.Values{"name": {"Запись"}, "days_ahead": {"14"}, "active": {"1"}}); code != 303 {
		t.Fatalf("Eve adds a link: %d", code)
	}
	if code, _ := do(eve, "GET", "/booking/rights", nil); code != 403 {
		t.Fatalf("a manager sets the rights: %d", code)
	}

	// REST: the key of Zoe is refused, Bob reads and doesn't change types, Eve changes them.
	api := func(who, method, path, body string) int {
		t.Helper()
		k, err := keys.Create(ctx, who, "k", []string{"booking:read", "booking:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+k)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		res.Body.Close()
		return res.StatusCode
	}
	if code := api(zoe, "GET", "/api/v1/booking/resources", ""); code != 403 {
		t.Fatalf("REST by an employee not chosen: %d", code)
	}
	if code := api(bob, "GET", "/api/v1/booking/resources", ""); code != 200 {
		t.Fatalf("REST by Bob: %d", code)
	}
	if code := api(bob, "POST", "/api/v1/booking/types", `{"name":"Стол"}`); code != 403 {
		t.Fatalf("REST type by Bob: %d", code)
	}
	if code := api(eve, "POST", "/api/v1/booking/types", `{"name":"Стол"}`); code != 201 {
		t.Fatalf("REST type by Eve: %d", code)
	}

	// «Все сотрудники» again: Zoe works with it, the managers stay.
	if code, _ := do(boss, "POST", "/booking/rights", url.Values{"use_who": {""}, "use": {ann}, "manage": {eve}}); code != 303 {
		t.Fatalf("save rights: %d", code)
	}
	if !menuHas(zoe) {
		t.Fatal("everyone works with «Онлайн-запись» again")
	}
	if r, _ := srv.Booking.GetRights(ctx); len(r.Use.Users) != 0 || len(r.Manage.Users) != 1 {
		t.Fatalf("rights: %+v", r)
	}
}
