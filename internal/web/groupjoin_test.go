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

// TestGroupJoin: open, closed and secret groups (GO24DEV-774) — an open group is in «Все группы» and joined at once,
// a closed one on a request its owner accepts or rejects (both told), a secret one is seen by members only; a guest
// joins none; a closed group made open lets those who asked in.
func TestGroupJoin(t *testing.T) {
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
	boss, anna, vera, guest := newUser("Борис"), newUser("Анна"), newUser("Вера"), newUser("Гость")
	_, err = pool.Exec(ctx, `UPDATE users SET guest = true WHERE id = $1`, guest)
	must(err)
	ts := tasks.NewStore(pool)

	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool), CRM: crm.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	role := func(who, key string) string {
		t.Helper()
		var r string
		_ = pool.QueryRow(ctx, `SELECT m.role FROM project_members m JOIN projects p ON p.id = m.project_id
			WHERE p.key = $1 AND m.user_id = $2`, key, who).Scan(&r)
		return r
	}
	notes := func(who, kind string) int {
		t.Helper()
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = $2`, who, kind).Scan(&n))
		return n
	}

	for key, privacy := range map[string]string{"OG": "open", "CG": "closed", "SG": ""} {
		if st, _ := do(boss, "POST", "/projects", url.Values{"key": {key}, "name": {"Группа " + key}, "privacy": {privacy}}); st != http.StatusSeeOther {
			t.Fatalf("create %s: %d", key, st)
		}
	}
	var privacy string
	must(pool.QueryRow(ctx, `SELECT string_agg(key || '=' || privacy, ',' ORDER BY key) FROM projects`).Scan(&privacy))
	if privacy != "CG=closed,OG=open,SG=secret" {
		t.Fatalf("privacy: %s", privacy)
	}

	// «Все группы»: the open and the closed group, not the secret one
	st, body := do(anna, "GET", "/projects?tab=all", nil)
	if st != http.StatusOK || !strings.Contains(body, `href="/p/OG"`) || !strings.Contains(body, `href="/p/CG"`) || strings.Contains(body, `/p/SG`) ||
		!strings.Contains(body, `action="/p/OG/join"`) {
		t.Fatalf("all groups: %d %s", st, body)
	}
	if _, body := do(anna, "GET", "/projects?tab=all&q=CG", nil); strings.Contains(body, `/p/OG"`) || !strings.Contains(body, `/p/CG"`) {
		t.Fatalf("search: %s", body)
	}
	if st, _ := do(anna, "GET", "/p/SG", nil); st != http.StatusForbidden {
		t.Fatalf("secret group seen: %d", st)
	}
	if st, _ := do(anna, "POST", "/p/SG/join", nil); st != http.StatusForbidden || role(anna, "SG") != "" {
		t.Fatalf("secret group joined: %d", st)
	}
	if _, body := do(guest, "GET", "/projects?tab=all", nil); strings.Contains(body, `/p/OG"`) {
		t.Fatal("a guest sees the groups")
	}
	if st, _ := do(guest, "POST", "/p/OG/join", nil); st == http.StatusSeeOther || role(guest, "OG") != "" {
		t.Fatalf("a guest joined: %d", st)
	}

	// open: the card with «Вступить», then a member at once
	if st, body := do(anna, "GET", "/p/OG", nil); st != http.StatusOK || !strings.Contains(body, `action="/p/OG/join"`) || strings.Contains(body, `name="message"`) {
		t.Fatalf("open group card: %d %s", st, body)
	}
	if st, _ := do(anna, "POST", "/p/OG/join", nil); st != http.StatusSeeOther || role(anna, "OG") != "member" {
		t.Fatalf("join open: %d %q", st, role(anna, "OG"))
	}
	if st, body := do(anna, "GET", "/p/OG", nil); st != http.StatusOK || !strings.Contains(body, `action="/p/OG/leave"`) {
		t.Fatalf("member's page: %d", st)
	}
	if st, _ := do(anna, "POST", "/p/OG/leave", nil); st != http.StatusSeeOther || role(anna, "OG") != "" {
		t.Fatalf("leave: %d", st)
	}

	// closed: a request with a message, the owner told; taken back — the notification goes
	if _, body := do(anna, "GET", "/p/CG", nil); !strings.Contains(body, `name="message"`) {
		t.Fatalf("closed group card: %s", body)
	}
	do(vera, "POST", "/p/CG/join", url.Values{"message": {"Возьмите меня"}})
	if role(vera, "CG") != "" || notes(boss, "group_join_request") != 1 {
		t.Fatalf("request: role %q, notes %d", role(vera, "CG"), notes(boss, "group_join_request"))
	}
	if _, body := do(vera, "GET", "/p/CG", nil); !strings.Contains(body, `action="/p/CG/join/cancel"`) {
		t.Fatalf("requested card: %s", body)
	}
	do(vera, "POST", "/p/CG/join/cancel", nil)
	if notes(boss, "group_join_request") != 0 {
		t.Fatal("the request's notification stays")
	}
	do(anna, "POST", "/p/CG/join", url.Values{"message": {"Хочу помогать"}})
	if _, body := do(boss, "GET", "/p/CG", nil); !strings.Contains(body, `action="/p/CG/requests/`+anna+`"`) || !strings.Contains(body, "Хочу помогать") {
		t.Fatalf("requests on the page: %s", body)
	}
	if st, _ := do(vera, "POST", "/p/CG/requests/"+anna, url.Values{"accept": {"1"}}); st != http.StatusForbidden || role(anna, "CG") != "" {
		t.Fatalf("an outsider accepts: %d", st)
	}
	do(boss, "POST", "/p/CG/requests/"+anna, url.Values{"accept": {"0"}})
	if role(anna, "CG") != "" || notes(anna, "group_join_rejected") != 1 {
		t.Fatal("rejected")
	}
	do(anna, "POST", "/p/CG/join", nil)
	do(boss, "POST", "/p/CG/requests/"+anna, url.Values{"accept": {"1"}})
	if role(anna, "CG") != "member" || notes(anna, "group_join_accepted") != 1 {
		t.Fatalf("accepted: %q", role(anna, "CG"))
	}

	// the closed group made open: those who asked are in; made secret — not listed
	do(vera, "POST", "/p/CG/join", nil)
	if st, _ := do(boss, "POST", "/p/CG/settings", url.Values{"privacy": {"open"}}); st != http.StatusSeeOther || role(vera, "CG") != "member" {
		t.Fatalf("made open: %d %q", st, role(vera, "CG"))
	}
	if st, _ := do(anna, "POST", "/p/CG/settings", url.Values{"privacy": {"secret"}}); st != http.StatusForbidden {
		t.Fatalf("a member changes the type: %d", st)
	}
	do(boss, "POST", "/p/OG/settings", url.Values{"privacy": {"secret"}})
	if _, body := do(vera, "GET", "/projects?tab=all", nil); strings.Contains(body, `/p/OG"`) || !strings.Contains(body, `/p/CG"`) {
		t.Fatalf("after secret: %s", body)
	}

	// REST: «Все группы», the type, «Вступить», a request and its answer
	key := func(who string) string {
		k, err := keys.Create(ctx, who, "k", apikeys.AllScopes, time.Hour)
		must(err)
		return k
	}
	bossKey, annaKey := key(boss), key(anna)
	call := func(method, path, token string, body any) (int, string) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if st, body := call("POST", "/api/v1/projects", bossKey, map[string]any{"key": "RG", "name": "Через REST", "privacy": "closed"}); st != http.StatusCreated ||
		!strings.Contains(body, `"privacy": "closed"`) {
		t.Fatalf("create: %d %s", st, body)
	}
	if st, body := call("GET", "/api/v1/groups?q=RG", annaKey, nil); st != http.StatusOK || !strings.Contains(body, `"key": "RG"`) || !strings.Contains(body, `"requested": false`) {
		t.Fatalf("groups: %d %s", st, body)
	}
	if st, body := call("POST", "/api/v1/projects/RG/join", annaKey, map[string]any{"message": "Привет"}); st != http.StatusOK || !strings.Contains(body, `"requested": true`) {
		t.Fatalf("join: %d %s", st, body)
	}
	if st, body := call("GET", "/api/v1/projects/RG/join-requests", bossKey, nil); st != http.StatusOK || !strings.Contains(body, "Привет") || !strings.Contains(body, anna) {
		t.Fatalf("requests: %d %s", st, body)
	}
	if st, _ := call("GET", "/api/v1/projects/RG/join-requests", annaKey, nil); st != http.StatusForbidden {
		t.Fatalf("an outsider reads requests: %d", st)
	}
	if st, body := call("POST", "/api/v1/projects/RG/join-requests/"+anna+"/accept", bossKey, nil); st != http.StatusNoContent || role(anna, "RG") != "member" {
		t.Fatalf("accept: %d %s", st, body)
	}
	if st, _ := call("POST", "/api/v1/projects/RG/join", annaKey, nil); st != http.StatusUnprocessableEntity {
		t.Fatalf("a member joins again: %d", st)
	}
	if st, body := call("PATCH", "/api/v1/projects/RG", bossKey, map[string]any{"privacy": "open"}); st != http.StatusOK || !strings.Contains(body, `"privacy": "open"`) {
		t.Fatalf("patch: %d %s", st, body)
	}
	if st, _ := call("PATCH", "/api/v1/projects/RG", annaKey, map[string]any{"privacy": "secret"}); st != http.StatusForbidden {
		t.Fatalf("a member patches: %d", st)
	}
}
