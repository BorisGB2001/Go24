package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sysinfo"
	"go24/internal/tasks"
)

// TestSystemHTTP: «Состояние системы» — only for administrators; the copies of go24 by their heartbeats (a silent
// worker is a trouble), the database, the services (one down is a trouble), the folders' space, no backups; the same
// by REST.
func TestSystemHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('boss', 'Борис', 'boss@go24.test', true) RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	bundle, err := i18n.Load("")
	must(err)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer down.Close()
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		SysProbes: append(sysinfo.HTTPProbe("matrix", up.URL, "/_matrix/client/versions"), sysinfo.HTTPProbe("office", down.URL, "/healthcheck")...),
		SysDisks:  []SysDisk{{ID: "backup", Path: t.TempDir()}}}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()

	// this copy (web) beats and drops the web copy of the container before it; the worker was last seen ten minutes ago
	_, err = pool.Exec(ctx, `INSERT INTO system_nodes (id, role, host, started_at, seen_at) VALUES ('old/web', 'web', 'old', now() - interval '1 hour', now() - interval '5 minutes')`)
	must(err)
	bctx, stop := context.WithCancel(ctx)
	go sysinfo.NewStore(pool).Beat(bctx, "web", nil)
	defer stop()
	_, err = pool.Exec(ctx, `INSERT INTO system_nodes (id, role, host, version, started_at, seen_at) VALUES ('w/worker', 'worker', 'w', '0.5.3', now() - interval '1 hour', now() - interval '10 minutes')`)
	must(err)
	for i := 0; ; i++ {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM system_nodes WHERE role = 'web'`).Scan(&n))
		if n == 1 {
			break
		}
		if i > 50 {
			t.Fatal("no heartbeat of this copy")
		}
		time.Sleep(20 * time.Millisecond)
	}

	get := func(sess *auth.Session, key, path string, want int) string {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		if sess != nil {
			rec := httptest.NewRecorder()
			must(srv.Sessions.Set(rec, *sess))
			req.AddCookie(rec.Result().Cookies()[0])
		} else {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		req.Header.Set("Accept-Language", "ru")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("GET %s: %d, want %d: %.300s", path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	get(&auth.Session{UserID: anna}, "", "/settings/system", 403)
	page := get(&auth.Session{UserID: boss, Admin: true}, "", "/settings/system", 200)
	var last string // the last migration, whichever it is by now
	must(pool.QueryRow(ctx, `SELECT max(version) FROM schema_migrations`).Scan(&last))
	for _, w := range []string{"Состояние системы", "Фоновые задания (go24-worker) не работают", "Не отвечает служба: ONLYOFFICE",
		"Резервное копирование не настроено", "Matrix (мессенджер)", "HTTP 503", "Не отвечает с", "Страницы и API", "PostgreSQL ",
		last, "Резервные копии", "свободно "} {
		if !strings.Contains(page, w) {
			t.Errorf("page has no %q", w)
		}
	}
	if strings.Contains(page, "Не отвечает служба: Matrix") || strings.Contains(page, "%!") {
		t.Error("wrong troubles on the page")
	}
	if page := get(&auth.Session{UserID: boss, Admin: true}, "", "/settings", 200); !strings.Contains(page, `href="/settings/system"`) {
		t.Error("no «Состояние системы» in «Настройки»")
	}

	annaKey, err := srv.Keys.Create(ctx, anna, "n8n", apikeys.AllScopes, 0)
	must(err)
	get(nil, annaKey, "/api/v1/settings/system", 403)
	bossKey, err := srv.Keys.Create(ctx, boss, "admin", apikeys.AllScopes, 0)
	must(err)
	var rep struct {
		Version  string `json:"version"`
		Database struct {
			Size      int64  `json:"size"`
			Migration string `json:"migration"`
		} `json:"database"`
		Nodes []struct {
			Role  string `json:"role"`
			Alive bool   `json:"alive"`
		} `json:"nodes"`
		Services []struct {
			ID string `json:"id"`
			OK bool   `json:"ok"`
		} `json:"services"`
		Disks []struct {
			ID   string `json:"id"`
			Free int64  `json:"free"`
		} `json:"disks"`
		Problems []struct {
			Code string `json:"code"`
			Arg  string `json:"arg"`
		} `json:"problems"`
	}
	must(json.Unmarshal([]byte(get(nil, bossKey, "/api/v1/settings/system", 200)), &rep))
	if rep.Version == "" || rep.Database.Size <= 0 || rep.Database.Migration == "" || len(rep.Nodes) != 2 ||
		rep.Nodes[0].Role != "web" || !rep.Nodes[0].Alive || rep.Nodes[1].Alive || len(rep.Services) != 2 ||
		!rep.Services[0].OK || rep.Services[1].OK || len(rep.Disks) != 1 || rep.Disks[0].Free <= 0 {
		t.Errorf("report = %+v", rep)
	}
	codes := map[string]string{}
	for _, p := range rep.Problems {
		codes[p.Code] = p.Arg
	}
	if len(codes) != 3 || codes["sys.p.service"] != "sys.svc.office" || codes["sys.p.backup_off"] != "" {
		t.Errorf("problems = %+v", rep.Problems)
	}
	if _, ok := codes["sys.p.worker"]; !ok {
		t.Errorf("no worker trouble: %+v", rep.Problems)
	}
}
