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
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestStorageCloudHTTP: «Хранилища» offer a cloud service once its application is set; «Подключить» sends the user
// to sign in there with the form signed in the state; coming back with a state changed, of another user or none
// connects nothing.
func TestStorageCloudHTTP(t *testing.T) {
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
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	bundle, err := i18n.Load("")
	must(err)
	st := storages.NewStore(pool, storage.NewRC("http://127.0.0.1:1", "go24", "x"), nil)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Storages: st, I18n: bundle, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept-Language", "ru-RU")
		req.Header.Set("Origin", hs.URL)
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res, string(raw)
	}

	if _, page := do(anna, "GET", "/settings/storages", nil); strings.Contains(page, `value="dropbox"`) {
		t.Error("Dropbox is offered without its application")
	}
	st.Apps = map[string]storages.App{"dropbox": {ClientID: "B", Secret: "BS"}}
	if _, page := do(anna, "GET", "/settings/storages", nil); !strings.Contains(page, `<option value="dropbox">Dropbox</option>`) {
		t.Error("Dropbox is not offered")
	}
	res, _ := do(anna, "POST", "/settings/storages", url.Values{"type": {"dropbox"}, "name": {"Мой Dropbox"}, "root": {"go24"}})
	to, _ := url.Parse(res.Header.Get("Location"))
	q := to.Query()
	if res.StatusCode != http.StatusSeeOther || to.Host != "www.dropbox.com" || q.Get("client_id") != "B" ||
		q.Get("redirect_uri") != "https://go24.test/settings/storages/oauth" || q.Get("token_access_type") != "offline" {
		t.Fatalf("«Подключить» leads to %d %s", res.StatusCode, to)
	}
	state := q.Get("state")
	if got, ok := srv.storageStateFrom(state, anna); !ok || got.Name != "Мой Dropbox" || got.Root != "go24" || got.Type != "dropbox" {
		t.Fatalf("the state: %+v %v", got, ok)
	}
	if _, ok := srv.storageStateFrom(state, petr); ok {
		t.Error("another user's state is taken")
	}
	// a user may not make a shared storage; nameless — nothing
	if res, _ := do(anna, "POST", "/settings/storages", url.Values{"type": {"dropbox"}, "name": {"X"}, "shared": {"on"}}); strings.Contains(res.Header.Get("Location"), "dropbox.com") {
		t.Error("a shared storage by a user")
	}
	if res, _ := do(anna, "POST", "/settings/storages", url.Values{"type": {"dropbox"}}); strings.Contains(res.Header.Get("Location"), "dropbox.com") {
		t.Error("a storage without a name")
	}
	refused := url.QueryEscape("Не удалось войти")
	for _, back := range []url.Values{{"code": {"c"}, "state": {state[:len(state)-1] + "x"}}, {"code": {"c"}}, {"state": {state}}} {
		if res, _ := do(anna, "GET", "/settings/storages/oauth?"+back.Encode(), nil); !strings.Contains(res.Header.Get("Location"), refused) {
			t.Errorf("back with %v: %s", back, res.Header.Get("Location"))
		}
	}
	if res, _ := do(petr, "GET", "/settings/storages/oauth?"+url.Values{"code": {"c"}, "state": {state}}.Encode(), nil); !strings.Contains(res.Header.Get("Location"), refused) {
		t.Errorf("another user's state: %s", res.Header.Get("Location"))
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM storages`).Scan(&n))
	if n != 0 {
		t.Errorf("%d storages connected", n)
	}
}
