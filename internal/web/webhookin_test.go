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

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestIncomingHookHTTP: «Входящий вебхук» — the address with the key (/rest/<employee>/<key>/<path>) is REST
// /api/v1/<path> with that key, «.json» too; another employee's address, a wrong or revoked key get 401; the keys
// page shows the address of a new key.
func TestIncomingHookHTTP(t *testing.T) {
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
	var anna, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Audit: audit.NewStore(pool),
		BaseURL: "https://go24.example", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	get := func(path string, want int) string {
		t.Helper()
		res, err := http.Get(hs.URL + path)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("GET %s: %d, want %d: %.300s", path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	token, err := srv.Keys.Create(ctx, anna, "n8n", apikeys.AllScopes, 0)
	must(err)

	if body := get("/rest/"+anna+"/"+token+"/me", 200); !strings.Contains(body, anna) {
		t.Errorf("the webhook's /me is not Анна: %.200s", body)
	}
	get("/rest/"+anna+"/"+token+"/me.json", 200)
	get("/rest/"+ivan+"/"+token+"/me", 401) // the key is not Иван's
	get("/rest/"+anna+"/go24_000000000000_"+strings.Repeat("0", 64)+"/me", 401)
	get("/rest/"+anna+"/"+token+"/", 401)
	var keyID string
	must(pool.QueryRow(ctx, `SELECT id FROM api_keys WHERE user_id = $1`, anna).Scan(&keyID))
	must(srv.Keys.Revoke(ctx, anna, keyID))
	get("/rest/"+anna+"/"+token+"/me", 401)

	// the keys page shows the webhook's address of a new key
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	req, _ := http.NewRequest("POST", hs.URL+"/settings/keys", strings.NewReader(url.Values{"name": {"Вебхук"}, "scopes": {"tasks:read"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept-Language", "ru")
	req.AddCookie(rec.Result().Cookies()[0])
	res, err := http.DefaultClient.Do(req)
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(raw), `value="https://go24.example/rest/`+anna+`/go24_`) || !strings.Contains(string(raw), "Входящий вебхук") {
		t.Errorf("no webhook address on the keys page: %d", res.StatusCode)
	}
}
