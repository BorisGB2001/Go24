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
	"go24/internal/calls"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestCallRing: the person rung answers once (the caller learns it); the caller cannot answer their own call but
// ends its ringing, and who has not answered missed it; someone not rung does not find the ring. «Звонки» of the
// messenger: the declined and the missed call in the person's history (and in the REST API), the counter beside the
// tab until they look at it.
func TestCallRing(t *testing.T) {
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
	user := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username) VALUES ($1, $1, $1) RETURNING id`, name).Scan(&id))
		return id
	}
	ann, bob, eve, ghost := user("ann"), user("bob"), user("eve"), user("ghost")
	srv := &Server{DB: pool, Rings: &calls.Rings{DB: pool}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	post := func(who, path string) int {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who}))
		req, _ := http.NewRequest("POST", hs.URL+path, nil)
		req.AddCookie(rec.Result().Cookies()[0])
		req.Header.Set("Accept", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		res.Body.Close()
		return res.StatusCode
	}
	ring, err := srv.Rings.Start(ctx, ann, "!g:x", true, []string{bob, eve})
	must(err)
	base := "/call/ring/" + ring.ID + "/"
	if st := post(ghost, base+"accept"); st != http.StatusNotFound {
		t.Errorf("someone not rung answered: %d", st)
	}
	if st := post(ann, base+"accept"); st != http.StatusForbidden {
		t.Errorf("the caller answered their own call: %d", st)
	}
	if st := post(bob, base+"end"); st != http.StatusForbidden {
		t.Errorf("someone rung ended the ringing: %d", st)
	}
	if st := post(bob, base+"decline"); st != http.StatusNoContent {
		t.Fatalf("bob declines: %d", st)
	}
	if st := post(bob, base+"accept"); st < 400 {
		t.Errorf("bob answered twice: %d", st)
	}
	if st := post(ann, base+"end"); st != http.StatusNoContent {
		t.Fatalf("ann ends the ringing: %d", st)
	}
	var answers string
	must(pool.QueryRow(ctx, `SELECT string_agg(u.name || '=' || p.answer, ',' ORDER BY u.name) FROM call_ring_people p JOIN users u ON u.id = p.user_id`).Scan(&answers))
	if answers != "bob=declined,eve=missed" {
		t.Fatalf("the answers: %s", answers)
	}
	if st := post(eve, base+"accept"); st < 400 {
		t.Errorf("eve answered after the end: %d", st)
	}

	// the history of calls
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	must(err)
	srv.I18n, srv.pages, srv.Keys, srv.Tasks = bundle, pages, apikeys.NewStore(pool), tasks.NewStore(pool)
	get := func(who, path string) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who}))
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.AddCookie(rec.Result().Cookies()[0])
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Language", "ru")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if n, _ := srv.Rings.Unseen(ctx, eve); n != 1 {
		t.Fatalf("eve's missed calls not looked at: %d", n)
	}
	tok, err := srv.Keys.Create(ctx, eve, "im", []string{"im:read"}, time.Hour)
	must(err)
	req, _ := http.NewRequest("GET", hs.URL+"/api/v1/im/calls", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	res, err := http.DefaultClient.Do(req)
	must(err)
	var api struct {
		Calls []struct {
			Kind string
			By   struct{ ID, Name string }
		}
		Unseen int
	}
	must(json.NewDecoder(res.Body).Decode(&api))
	res.Body.Close()
	if len(api.Calls) != 1 || api.Calls[0].Kind != "missed" || api.Calls[0].By.Name != "ann" || api.Unseen != 1 {
		t.Fatalf("eve's calls by the API: %+v", api)
	}
	st, body := get(eve, "/im/calls")
	if st != http.StatusOK || !strings.Contains(body, "Пропущенный") || !strings.Contains(body, "ann") {
		t.Fatalf("eve's calls: %d %s", st, body)
	}
	if n, _ := srv.Rings.Unseen(ctx, eve); n != 0 {
		t.Fatal("the counter stays after eve looked at the calls")
	}
	if st, body := get(bob, "/im/calls?missed=1"); st != http.StatusOK || !strings.Contains(body, "Отклонён") {
		t.Fatalf("bob's missed calls: %d %s", st, body)
	}
	if st, body := get(ghost, "/im/calls"); st != http.StatusOK || !strings.Contains(body, "Звонков пока нет") {
		t.Fatalf("ghost's calls: %d %s", st, body)
	}
}
