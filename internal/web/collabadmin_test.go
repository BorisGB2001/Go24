package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/authentik"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCollabAdminAPI: a guest is made an employee by an administrator only — out of the guests' group of Authentik
// first, so the next sign-in keeps them an employee; over the REST API an administrator's key in admin mode
// (X-Go24-Admin: 1) sees and runs any collab, anybody else's header is refused.
func TestCollabAdminAPI(t *testing.T) {
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
	newUser := func(name string, guest bool, groups ...string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, username, guest, groups) VALUES ($1, $1, $1 || '@t', $1, $2, $3)
			RETURNING id`, name, guest, append([]string{}, groups...)).Scan(&id))
		return id
	}
	owner, client := newUser("owner", false), newUser("client", true, "go24-guests")
	admin, plain := newUser("admin", false, "go24-admins"), newUser("plain", false)

	// Authentik: the guests' group, the guest in it; go24 takes them out
	var mu sync.Mutex
	var removed []string
	ak := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/api/v3/core/users/" && r.URL.Query().Get("username") == "client":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"pk": 7, "username": "client"}}})
		case r.URL.Path == "/api/v3/core/groups/" && r.URL.Query().Get("name") == "go24-guests":
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []map[string]any{{"pk": "g-uuid", "name": "go24-guests"}}})
		case r.Method == "POST" && r.URL.Path == "/api/v3/core/groups/g-uuid/remove_user/":
			var b map[string]int
			_ = json.NewDecoder(r.Body).Decode(&b)
			removed = append(removed, "g-uuid:"+string(rune('0'+b["pk"])))
			w.WriteHeader(http.StatusNoContent)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
		}
	}))
	defer ak.Close()

	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Authentik: authentik.New(ak.URL, "tok"),
		GuestGroups: []string{"go24-guests"}, AdminGroups: []string{"go24-admins"},
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	_, err = ts.CreateCollab(tasks.WithViewer(ctx, owner, false), owner, "Альфа", "", []string{client})
	must(err)

	token := func(u string) string {
		k, err := keys.Create(ctx, u, "k", []string{"collabs:read", "collabs:write"}, time.Hour)
		must(err)
		return k
	}
	api := func(tok, method, path string, adminMode bool) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, bytes.NewReader(nil))
		req.Header.Set("Authorization", "Bearer "+tok)
		if adminMode {
			req.Header.Set("X-Go24-Admin", "1")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	at, pt := token(admin), token(plain)

	// REST admin mode: the administrator's key sees every collab with the header, its own only without it
	if _, body := api(at, "GET", "/api/v1/collabs", false); strings.Contains(body, `"CL1"`) {
		t.Error("an administrator's key sees someone else's collab without admin mode")
	}
	if st, body := api(at, "GET", "/api/v1/collabs", true); st != http.StatusOK || !strings.Contains(body, `"CL1"`) {
		t.Errorf("admin mode over the API: %d %s", st, body)
	}
	if st, _ := api(at, "GET", "/api/v1/collabs/CL1/log", true); st != http.StatusOK {
		t.Errorf("the log in admin mode: %d", st)
	}
	if st, _ := api(pt, "GET", "/api/v1/collabs", true); st != http.StatusForbidden {
		t.Errorf("an employee's key in admin mode: %d", st)
	}
	if st, _ := api(pt, "GET", "/api/v1/collabs/CL1", false); st != http.StatusNotFound {
		t.Errorf("an employee's key finds someone else's collab: %d", st)
	}

	// a guest made an employee: never by a plain employee, by the administrator over the page or the API
	if st, _ := api(pt, "POST", "/api/v1/users/"+client+"/employee", false); st != http.StatusForbidden {
		t.Errorf("an employee made a guest an employee: %d", st)
	}
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: plain}))
	req, _ := http.NewRequest("POST", hs.URL+"/u/"+client+"/employee", nil)
	req.AddCookie(rec.Result().Cookies()[0])
	res, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	must(err)
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("the page let an employee make a guest an employee: %d", res.StatusCode)
	}
	if st, body := api(at, "POST", "/api/v1/users/"+client+"/employee", false); st != http.StatusNoContent {
		t.Fatalf("the administrator's API: %d %s", st, body)
	}
	var guest bool
	must(pool.QueryRow(ctx, `SELECT guest FROM users WHERE id = $1`, client).Scan(&guest))
	if guest || len(removed) != 1 || removed[0] != "g-uuid:7" {
		t.Fatalf("guest = %v, removed from Authentik groups: %v", guest, removed)
	}
	if st, _ := api(at, "POST", "/api/v1/users/"+client+"/employee", false); st < 400 {
		t.Errorf("an employee made an employee again: %d", st)
	}
	// still a member of the collab, now with the company's rights: sees the collab and the company's people
	if st, _ := api(token(client), "GET", "/api/v1/collabs/CL1", false); st != http.StatusOK {
		t.Errorf("the new employee lost the collab: %d", st)
	}
}
