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
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMCopyHTTP: «Копировать» on the card makes a copy and opens it for editing; a user with "own" rights who sees
// another's lead as an observer gets the copy as its responsible; one who cannot read the card cannot copy it; REST
// gives the copy.
func TestCRMCopyHTTP(t *testing.T) {
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Менеджер", Perms: crm.Perms{"lead": {"read": "own", "write": "own"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, eve, true))
	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка", Responsible: crm.Ref{ID: anna}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
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
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	card := "/crm/leads/" + lead
	if _, page := do(anna, "GET", card, nil); !strings.Contains(page, card+"/copy") {
		t.Fatal("no «Копировать» on the card")
	}
	if code, _ := do(eve, "POST", card+"/copy", url.Values{}); code == http.StatusSeeOther {
		t.Fatal("copied a lead she cannot read")
	}
	must(srv.CRM.SetObservers(ctx, anna, "lead", lead, []string{eve}))
	req, _ := http.NewRequest("POST", hs.URL+card+"/copy", strings.NewReader(""))
	req.AddCookie(as[eve])
	res, err := client.Do(req)
	must(err)
	res.Body.Close()
	loc := res.Header.Get("Location")
	if res.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/crm/leads/") || !strings.Contains(loc, "edit=1") || strings.HasPrefix(loc, card) {
		t.Fatalf("copy: %d %s", res.StatusCode, loc)
	}
	cp, _, _ := strings.Cut(strings.TrimPrefix(loc, "/crm/leads/"), "?")
	l, err := srv.CRM.Lead(ctx, cp)
	must(err)
	if l.Title != "Заявка" || l.Responsible.ID != eve {
		t.Fatalf("the copy: %+v", l)
	}

	token, err := srv.Keys.Create(ctx, anna, "t", apikeys.AllScopes, time.Hour)
	must(err)
	req, _ = http.NewRequest("POST", hs.URL+"/api/v1/crm/leads/"+lead+"/copy", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err = http.DefaultClient.Do(req)
	must(err)
	defer res.Body.Close()
	var out struct{ ID, Title string }
	must(json.NewDecoder(res.Body).Decode(&out))
	if res.StatusCode != http.StatusCreated || out.ID == "" || out.ID == lead || out.ID == cp || out.Title != "Заявка" {
		t.Fatalf("REST copy: %d %+v", res.StatusCode, out)
	}
	if r, _ := srv.CRM.Responsible(ctx, "lead", out.ID); r != anna {
		t.Fatalf("REST copy's responsible: %s", r)
	}
}
