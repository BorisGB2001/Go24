package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestRestPortalIMDepts (GO24DEV-922): im.notify.get pages the notifications with their authors and counts,
// im.notify.history.search finds by text, day and module; im.department.* give the structure (full name, head,
// employees with the head, colleagues), not to a guest (whose key does not reach REST); im.dialog.users.list and im.dialog.unread want Matrix.
func TestRestPortalIMDepts(t *testing.T) {
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
	var boss, anna string
	var annaNum, bossNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position) VALUES ('boss', 'Борис Главный', 'boss@t', 'Директор')
		RETURNING id, num`).Scan(&boss, &bossNum))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id, num`).Scan(&anna, &annaNum))
	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		Notify: notify.NewStore(pool), I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	full, err := srv.Keys.Create(ctx, anna, "k", []string{"im:read", "im:write"}, time.Hour)
	must(err)
	owner := map[string]int64{full: annaNum} // the webhook's employee by its key
	call := func(key, method, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/rest/"+strconv.FormatInt(owner[key], 10)+"/"+key+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	must2 := func(_ any, err error) { t.Helper(); must(err) }

	must2(pool.Exec(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, 'app', 'Склад закрыт')`, anna))
	must2(pool.Exec(ctx, `INSERT INTO notifications (user_id, actor_id, kind, body) VALUES ($1, $2, 'app_personal', 'Зайди ко мне')`, anna, boss))
	got := call(full, "im.notify.get", `{}`, 200)["result"].(map[string]any)
	list := got["notifications"].([]any)
	if len(list) != 2 || got["total_count"] != float64(2) || got["total_unread_count"] != float64(2) {
		t.Fatalf("im.notify.get: %v", got)
	}
	first, second := list[0].(map[string]any), list[1].(map[string]any)
	if first["notify_type"] != float64(2) || first["author_id"] != float64(bossNum) || first["notify_read"] != "N" ||
		!strings.Contains(first["text"].(string), "Зайди ко мне") || second["notify_type"] != float64(4) {
		t.Errorf("notifications: %v", list)
	}
	if u := got["users"].([]any); len(u) != 1 || u[0].(map[string]any)["id"] != float64(bossNum) {
		t.Errorf("users: %v", got["users"])
	}
	page := call(full, "im.notify.get", `{"LIMIT": 1, "LAST_ID": `+strconv.FormatInt(int64(first["id"].(float64)), 10)+`}`, 200)["result"].(map[string]any)
	if p := page["notifications"].([]any); len(p) != 1 || p[0].(map[string]any)["id"] != second["id"] {
		t.Errorf("the next page: %v", page)
	}
	if f := call(full, "im.notify.history.search", `{"SEARCH_TEXT": "склад", "SEARCH_DATE": "`+time.Now().Format("2006-01-02")+`"}`, 200)["result"].(map[string]any); len(f["notifications"].([]any)) != 1 {
		t.Errorf("search by text and day: %v", f)
	}
	if f := call(full, "im.notify.history.search", `{"SEARCH_DATE": "2001-01-01"}`, 200)["result"].(map[string]any); len(f["notifications"].([]any)) != 0 {
		t.Errorf("search by another day: %v", f)
	}
	if f := call(full, "im.notify.history.search", `{"SEARCH_TYPE": "tasks"}`, 200)["result"].(map[string]any); len(f["notifications"].([]any)) != 0 {
		t.Errorf("search by module: %v", f)
	}
	if e := call(full, "im.notify.history.search", `{"SEARCH_DATE": "вчера"}`, 400); e["error"] != "SEARCH_DATE_ERROR" {
		t.Errorf("bad day: %v", e)
	}

	var root, sales int64
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING num`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) SELECT 'Продажи', id, $1 FROM departments
		WHERE num = $2 RETURNING num`, boss, root).Scan(&sales))
	must2(pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) SELECT $1, id FROM departments WHERE num = $2`, anna, sales))
	salesID := strconv.FormatInt(sales, 10)
	d := call(full, "im.department.get", `{"ID": [`+salesID+`], "USER_DATA": "Y"}`, 200)["result"].([]any)
	if len(d) != 1 || d[0].(map[string]any)["full_name"] != "Продажи / Компания" || d[0].(map[string]any)["manager_user_id"] != float64(bossNum) ||
		d[0].(map[string]any)["manager_user_data"].(map[string]any)["name"] != "Борис Главный" {
		t.Errorf("im.department.get: %v", d)
	}
	emp := call(full, "im.department.employees.get", `{"ID": [`+salesID+`]}`, 200)["result"].(map[string]any)
	if e := emp[salesID].([]any); len(e) != 2 || e[0] != float64(bossNum) || e[1] != float64(annaNum) {
		t.Errorf("employees, the head first: %v", emp)
	}
	if m := call(full, "im.department.managers.get", `{"ID": [`+salesID+`]}`, 200)["result"].(map[string]any); len(m[salesID].([]any)) != 1 {
		t.Errorf("managers: %v", m)
	}
	col := call(full, "im.department.colleagues.list", `{"USER_DATA": "Y"}`, 200)
	if c := col["result"].([]any); len(c) != 1 || c[0].(map[string]any)["id"] != float64(bossNum) || col["total"] != float64(1) {
		t.Errorf("colleagues: %v", col)
	}
	if s := call(full, "im.search.department.list", `{"FIND": "прод"}`, 200)["result"].([]any); len(s) != 1 {
		t.Errorf("department search: %v", s)
	}
	if r := call(full, "im.revision.get", `{}`, 200)["result"].(map[string]any); r["rest"] == nil {
		t.Errorf("im.revision.get: %v", r)
	}

	var guest string
	var guestNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ('g', 'Гость', 'g@t', true)
		RETURNING id, num`).Scan(&guest, &guestNum))
	gkey, err := srv.Keys.Create(ctx, guest, "g", []string{"im:read"}, time.Hour)
	must(err)
	owner[gkey] = guestNum
	if e := call(gkey, "im.department.get", `{"ID": [`+salesID+`]}`, 400); e["error"] != "ERROR_NOT_FOUND" {
		t.Errorf("a guest sees the structure: %v", e)
	}

	for _, m := range []string{"im.dialog.users.list", "im.dialog.unread"} {
		if e := call(full, m, `{}`, 404); e["error"] != "ERROR_METHOD_NOT_FOUND" {
			t.Errorf("%s without Matrix: %v", m, e)
		}
	}
	srv.Matrix = &matrixsync.Service{DB: pool}
	for _, m := range []string{"im.dialog.users.list", "im.dialog.unread"} {
		if e := call(full, m, `{"DIALOG_ID": "chat77"}`, 400); e["error"] != "ERROR_NOT_FOUND" {
			t.Errorf("%s of chat77: %v", m, e)
		}
	}
}
