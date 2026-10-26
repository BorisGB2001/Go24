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
	"strconv"
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

// TestCRMTimelineHTTP: in a card's history a line is pinned (it shows in «Закреплённые» too) and unpinned; the author
// edits and deletes a comment, another person sees no such buttons and cannot; the same over REST.
func TestCRMTimelineHTTP(t *testing.T) {
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
	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка", Responsible: crm.Ref{ID: anna}})
	must(err)
	must(srv.CRM.Comment(ctx, anna, "lead", lead, "Позвонить"))
	ev, err := srv.CRM.Timeline(ctx, "lead", lead)
	must(err)
	var comment string // the comment's id as text
	for _, e := range ev {
		if e.Kind == "comment" {
			comment = evID(e.ID)
		}
	}

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
	line := card + "/timeline/" + comment + "/"
	body := func() (string, bool, bool) {
		t.Helper()
		var b string
		var pinned, edited bool
		must(pool.QueryRow(ctx, `SELECT body, pinned_at IS NOT NULL, edited_at IS NOT NULL FROM crm_timeline WHERE id::text = $1`, comment).Scan(&b, &pinned, &edited))
		return b, pinned, edited
	}

	if code, _ := do(anna, "POST", line+"pin", url.Values{}); code != http.StatusSeeOther {
		t.Fatalf("pin: %d", code)
	}
	_, page := do(anna, "GET", card, nil)
	if !strings.Contains(page, `class="crmpinned"`) || !strings.Contains(page, line+"unpin") || !strings.Contains(page, line+"edit") {
		t.Fatalf("card: no pinned block or buttons")
	}
	do(anna, "POST", line+"edit", url.Values{"body": {"Позвонить завтра"}})
	if b, pinned, edited := body(); b != "Позвонить завтра" || !pinned || !edited {
		t.Fatalf("edited %q %v %v", b, pinned, edited)
	}
	_, page = do(eve, "GET", card, nil)
	if strings.Contains(page, line+"edit") || strings.Contains(page, line+"delete") {
		t.Fatal("another person has the comment's buttons")
	}
	do(eve, "POST", line+"edit", url.Values{"body": {"Чужое"}})
	do(eve, "POST", line+"delete", url.Values{})
	if b, _, _ := body(); b != "Позвонить завтра" {
		t.Fatalf("changed by another: %q", b)
	}
	do(anna, "POST", line+"unpin", url.Values{})
	if _, pinned, _ := body(); pinned {
		t.Fatal("not unpinned")
	}

	token, err := srv.Keys.Create(ctx, anna, "t", apikeys.AllScopes, time.Hour)
	must(err)
	api := func(method, path string, in any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if in != nil {
			b, _ := json.Marshal(in)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+"/api/v1/crm/leads/"+lead+path, rd)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}
	if code, raw := api("PATCH", "/timeline/"+comment, map[string]any{"body": "По REST", "pinned": true}); code != http.StatusNoContent {
		t.Fatalf("patch: %d %s", code, raw)
	}
	code, raw := api("GET", "/timeline", nil)
	var list []struct {
		ID     int64  `json:"id"`
		Kind   string `json:"kind"`
		Body   string `json:"body"`
		Pinned bool   `json:"pinned"`
		Edited bool   `json:"edited"`
	}
	must(json.Unmarshal(raw, &list))
	found := false
	for _, e := range list {
		if evID(e.ID) == comment {
			found = e.Body == "По REST" && e.Pinned && e.Edited
		}
	}
	if code != http.StatusOK || !found {
		t.Fatalf("timeline: %d %s", code, raw)
	}
	if code, raw := api("DELETE", "/timeline/"+comment, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", code, raw)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE kind = 'comment'`).Scan(&n))
	if n != 0 {
		t.Fatalf("%d comments left", n)
	}
}

func evID(id int64) string { return strconv.FormatInt(id, 10) }
