package web

import (
	"bytes"
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
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/notify"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestGroupSubjects: «Тематики групп» and «Ключевые слова» (GO24DEV-811) — the administrator keeps the subjects, a
// group picks one with its keywords at creation and in «О проекте»; «Все группы» is filtered by the subject, the
// keyword and searched by keywords; a deleted subject leaves its groups without one; REST does the same.
func TestGroupSubjects(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna := newUser("Борис"), newUser("Анна")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, boss)
	must(err)
	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who string, admin bool, method, path string, form url.Values) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Admin: admin, AdminMode: admin}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	subject := func(name string) string {
		t.Helper()
		var id string
		_ = pool.QueryRow(ctx, `SELECT id FROM group_subjects WHERE name = $1`, name).Scan(&id)
		return id
	}

	// the subjects: only the administrator (in admin mode) keeps them
	if st, _ := do(anna, false, "GET", "/projects/subjects", nil); st != http.StatusForbidden {
		t.Fatalf("an employee opens the subjects: %d", st)
	}
	if st, _ := do(anna, false, "POST", "/projects/subjects", url.Values{"name": {"Хакеры"}}); st != http.StatusForbidden || subject("Хакеры") != "" {
		t.Fatalf("an employee adds a subject: %d", st)
	}
	for _, n := range []string{"Маркетинг", "Разработка", "Удалить"} {
		if st, _ := do(boss, true, "POST", "/projects/subjects", url.Values{"name": {n}, "sort": {"10"}}); st != http.StatusSeeOther {
			t.Fatalf("add %s: %d", n, st)
		}
	}
	do(boss, true, "POST", "/projects/subjects", url.Values{"name": {"маркетинг"}}) // back to the form with a message
	if subject("маркетинг") != "" {
		t.Fatal("a subject twice")
	}
	mkt, dev, del := subject("Маркетинг"), subject("Разработка"), subject("Удалить")
	if st, body := do(boss, true, "GET", "/projects/subjects", nil); st != http.StatusOK || !strings.Contains(body, `value="Разработка"`) {
		t.Fatalf("subjects page: %d %s", st, body)
	}
	if st, _ := do(boss, true, "POST", "/projects/subjects", url.Values{"id": {dev}, "name": {"Разработка ПО"}, "sort": {"5"}}); st != http.StatusSeeOther || subject("Разработка ПО") != dev {
		t.Fatalf("rename: %d", st)
	}

	// a group with a subject and keywords from the creation form; «О проекте» changes them
	if st, _ := do(anna, false, "POST", "/projects", url.Values{"key": {"MK"}, "name": {"Выставки"}, "privacy": {"open"},
		"subject": {mkt}, "keywords": {"выставки, #реклама, Выставки,  офлайн  ивенты"}}); st != http.StatusSeeOther {
		t.Fatalf("create: %d", st)
	}
	var kw []string
	var sid string
	must(pool.QueryRow(ctx, `SELECT subject_id::text, keywords FROM projects WHERE key = 'MK'`).Scan(&sid, &kw))
	if sid != mkt || strings.Join(kw, "|") != "выставки|реклама|офлайн ивенты" {
		t.Fatalf("created: %s %q", sid, kw)
	}
	if st, _ := do(anna, false, "POST", "/projects", url.Values{"key": {"DV"}, "name": {"Бэкенд"}, "privacy": {"closed"}, "subject": {del}}); st != http.StatusSeeOther {
		t.Fatalf("create DV: %d", st)
	}
	// the projects list: its columns and sorting by a column (GO24DEV-844)
	if st, body := do(anna, false, "GET", "/projects?sort=name&desc=1", nil); st != http.StatusOK || !strings.Contains(body, `class="eff-pct sm`) ||
		!strings.Contains(body, `class="faces"`) || strings.Index(body, " Выставки</a>") < 0 || strings.Index(body, " Выставки</a>") > strings.Index(body, " Бэкенд</a>") {
		t.Fatalf("projects list by name desc: %d", st)
	}
	do(anna, false, "POST", "/projects", url.Values{"key": {"BAD"}, "name": {"Плохая"}, "subject": {"00000000-0000-0000-0000-000000000000"}})
	var bad int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM projects WHERE subject_id IS NOT NULL AND key = 'BAD'`).Scan(&bad))
	if bad != 0 {
		t.Fatal("an unknown subject taken")
	}
	if st, _ := do(anna, false, "POST", "/p/DV/info", url.Values{"name": {"Бэкенд"}, "subject": {dev}, "keywords": {"go, postgres"}}); st != http.StatusSeeOther {
		t.Fatalf("info: %d", st)
	}
	if st, body := do(anna, false, "GET", "/p/DV", nil); st != http.StatusOK || !strings.Contains(body, "Разработка ПО") || !strings.Contains(body, `value="go, postgres"`) ||
		!strings.Contains(body, `href="/projects?tab=all&kw=postgres"`) {
		t.Fatalf("project page: %d %s", st, body)
	}

	// «Все группы»: by the subject, by a keyword, the search finds keywords
	all := func(query string) string {
		t.Helper()
		st, body := do(boss, false, "GET", "/projects?tab=all"+query, nil)
		if st != http.StatusOK {
			t.Fatalf("all groups %s: %d", query, st)
		}
		return body
	}
	if b := all("&subject=" + mkt); !strings.Contains(b, `href="/p/MK"`) || strings.Contains(b, `href="/p/DV"`) {
		t.Fatalf("by subject: %s", b)
	}
	if b := all("&kw=" + url.QueryEscape("Реклама")); !strings.Contains(b, `href="/p/MK"`) || strings.Contains(b, `href="/p/DV"`) || !strings.Contains(b, "#Реклама") {
		t.Fatalf("by keyword: %s", b)
	}
	if b := all("&q=postgr"); strings.Contains(b, `href="/p/MK"`) || !strings.Contains(b, `href="/p/DV"`) {
		t.Fatalf("search by keyword: %s", b)
	}
	if b := all(""); !strings.Contains(b, "Маркетинг") || strings.Contains(b, `href="/projects/subjects"`) {
		t.Fatalf("all: %s", b)
	}
	if _, b := do(boss, true, "GET", "/projects?tab=all", nil); !strings.Contains(b, `href="/projects/subjects"`) {
		t.Fatal("no link to the subjects for the administrator")
	}
	if st, body := do(boss, false, "GET", "/p/MK", nil); st != http.StatusOK || !strings.Contains(body, "#выставки") {
		t.Fatalf("outsider card: %d %s", st, body)
	}

	// a deleted subject: its groups stay, without one
	must(pool.QueryRow(ctx, `SELECT coalesce(subject_id::text, '') FROM projects WHERE key = 'DV'`).Scan(&sid))
	if sid != dev {
		t.Fatalf("DV subject: %s", sid)
	}
	if st, _ := do(boss, true, "POST", "/p/DV/info", url.Values{"name": {"Бэкенд"}, "subject": {del}, "keywords": {""}}); st != http.StatusSeeOther {
		t.Fatalf("admin info: %d", st)
	}
	if st, _ := do(boss, true, "POST", "/projects/subjects", url.Values{"op": {"delete"}, "id": {del}}); st != http.StatusSeeOther || subject("Удалить") != "" {
		t.Fatalf("delete: %d", st)
	}
	must(pool.QueryRow(ctx, `SELECT coalesce(subject_id::text, ''), keywords FROM projects WHERE key = 'DV'`).Scan(&sid, &kw))
	if sid != "" || len(kw) != 0 {
		t.Fatalf("after delete: %q %q", sid, kw)
	}

	// REST
	key := func(who string) string {
		k, err := keys.Create(ctx, who, "k", apikeys.AllScopes, time.Hour)
		must(err)
		return k
	}
	bossKey, annaKey := key(boss), key(anna)
	call := func(method, path, token string, admin bool, body any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		if admin {
			req.Header.Set("X-Go24-Admin", "1")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if st, body := call("GET", "/api/v1/groups/subjects", annaKey, false, nil); st != http.StatusOK || !strings.Contains(body, `"name": "Маркетинг"`) || !strings.Contains(body, `"groups": 1`) {
		t.Fatalf("subjects: %d %s", st, body)
	}
	if st, _ := call("POST", "/api/v1/groups/subjects", annaKey, false, map[string]any{"name": "Своя"}); st != http.StatusForbidden {
		t.Fatalf("an employee adds by REST: %d", st)
	}
	st, body := call("POST", "/api/v1/groups/subjects", bossKey, true, map[string]any{"name": "Продажи", "sort": 1})
	if st != http.StatusCreated || subject("Продажи") == "" {
		t.Fatalf("add by REST: %d %s", st, body)
	}
	sales := subject("Продажи")
	if st, body := call("PATCH", "/api/v1/projects/DV", annaKey, false, map[string]any{"subject_id": sales, "keywords": []string{"crm", "CRM", "b2b"}}); st != http.StatusOK ||
		!strings.Contains(body, `"subject": "Продажи"`) || !strings.Contains(body, `"keywords": [`) || strings.Count(body, "crm") != 1 {
		t.Fatalf("patch: %d %s", st, body)
	}
	if st, body := call("POST", "/api/v1/projects", annaKey, false, map[string]any{"key": "RS", "name": "Через REST", "privacy": "open", "subject_id": mkt,
		"keywords": []string{"rest"}}); st != http.StatusCreated || !strings.Contains(body, `"subject": "Маркетинг"`) || !strings.Contains(body, `"rest"`) {
		t.Fatalf("create by REST: %d %s", st, body)
	}
	if st, body := call("GET", "/api/v1/groups?keyword=b2b", annaKey, false, nil); st != http.StatusOK || !strings.Contains(body, `"key": "DV"`) || strings.Contains(body, `"key": "MK"`) {
		t.Fatalf("groups by keyword: %d %s", st, body)
	}
	if st, body := call("GET", "/api/v1/groups?subject_id="+mkt, annaKey, false, nil); st != http.StatusOK || strings.Contains(body, `"key": "DV"`) || !strings.Contains(body, `"key": "MK"`) {
		t.Fatalf("groups by subject: %d %s", st, body)
	}
	if st, _ := call("PATCH", "/api/v1/groups/subjects/"+sales, bossKey, true, map[string]any{"name": "Продажи B2B"}); st != http.StatusOK || subject("Продажи B2B") != sales {
		t.Fatalf("rename by REST: %d", st)
	}
	if st, _ := call("DELETE", "/api/v1/groups/subjects/"+sales, bossKey, true, nil); st != http.StatusNoContent || subject("Продажи B2B") != "" {
		t.Fatalf("delete by REST: %d", st)
	}
	if st, _ := call("DELETE", "/api/v1/groups/subjects/nope", bossKey, true, nil); st != http.StatusNotFound {
		t.Fatalf("delete unknown: %d", st)
	}
}
