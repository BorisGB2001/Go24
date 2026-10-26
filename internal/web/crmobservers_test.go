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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMObserversHTTP: the card's form makes a person with "own" rights an observer: they open the card, find it
// with the filter «Наблюдатель», comment it; the form shows the field; REST puts the observers and shows them.
func TestCRMObserversHTTP(t *testing.T) {
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
	if code, _ := do(eve, "GET", card, nil); code == http.StatusOK {
		t.Fatal("another's lead opened without being an observer")
	}
	if code, _ := do(anna, "POST", card, url.Values{"title": {"Заявка"}, "responsible": {anna}, "with_observers": {"1"},
		"observers": {eve}}); code != http.StatusSeeOther {
		t.Fatalf("save: %d", code)
	}
	if code, page := do(eve, "GET", card, nil); code != http.StatusOK || !strings.Contains(page, "Заявка") {
		t.Fatalf("the observer: %d", code)
	}
	_, page := do(anna, "GET", card+"?edit=1", nil)
	if !strings.Contains(page, `name="with_observers"`) {
		t.Fatal("no observers in the form")
	}
	_, page = do(eve, "GET", "/crm/leads?view=list&observer="+eve, nil)
	if !strings.Contains(page, card) {
		t.Fatal("the filter by observer misses the lead")
	}
	if code, _ := do(eve, "POST", card+"/comment", url.Values{"body": {"Вижу"}}); code != http.StatusSeeOther {
		t.Fatalf("the observer's comment: %d", code)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'crm_comment'`, anna).Scan(&n))
	if n != 1 {
		t.Fatalf("the responsible got %d comment notifications", n)
	}

	token, err := srv.Keys.Create(ctx, anna, "t", apikeys.AllScopes, time.Hour)
	must(err)
	b, _ := json.Marshal(map[string]any{"observer_ids": []string{anna}})
	req, _ := http.NewRequest("PUT", hs.URL+"/api/v1/crm/leads/"+lead+"/observers", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	must(err)
	defer res.Body.Close()
	var out struct {
		Observers []struct{ ID, Name string } `json:"observers"`
	}
	must(json.NewDecoder(res.Body).Decode(&out))
	if res.StatusCode != http.StatusOK || len(out.Observers) != 1 || out.Observers[0].Name != "Анна" {
		t.Fatalf("REST observers: %d %+v", res.StatusCode, out)
	}
	if code, _ := do(eve, "GET", card, nil); code == http.StatusOK {
		t.Fatal("a former observer opens the lead")
	}
}
