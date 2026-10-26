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
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMRecycleHTTP: «Корзина» of the CRM — a deleted deal is in the bin page, «Восстановить» brings it back to
// its card with «Восстановлено из корзины», «Удалить навсегда» forgets it; another person does not see it; REST lists
// and restores.
func TestCRMRecycleHTTP(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	var bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe := pipes[0].ID

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(path string, form url.Values, header ...string) (int, string, string) {
		t.Helper()
		method, body := "GET", io.Reader(nil)
		if form != nil {
			method, body = "POST", strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		for i := 0; i+1 < len(header); i += 2 {
			req.Header.Set(header[i], header[i+1])
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Альфа", PipelineID: pipe, Responsible: crm.Ref{ID: anna}})
	must(err)
	if code, _, loc := do("/crm/deals/"+deal+"/delete", url.Values{}); code != http.StatusSeeOther || loc != "/crm/deals" {
		t.Fatalf("delete: %d %s", code, loc)
	}
	code, page, _ := do("/crm/recycle", nil)
	if code != http.StatusOK || !strings.Contains(page, "Альфа") || !strings.Contains(page, "Анна") {
		t.Fatalf("the bin page: %d", code)
	}
	items, err := srv.CRM.Recycle(ctx, anna, "", "")
	must(err)
	if bobs, _ := srv.CRM.Recycle(ctx, bob, "", ""); len(items) != 1 || len(bobs) != 0 {
		t.Fatalf("bins: %+v %+v", items, bobs)
	}
	if code, _, loc := do("/crm/recycle/"+items[0].ID, url.Values{"op": {"restore"}}); code != http.StatusSeeOther || loc != "/crm/deals/"+deal {
		t.Fatalf("restore: %d %s", code, loc)
	}
	if code, page, _ := do("/crm/deals/"+deal, nil); code != http.StatusOK || !strings.Contains(page, "Восстановлено из корзины") {
		t.Fatalf("the restored card: %d", code)
	}
	do("/crm/deals/"+deal+"/delete", url.Values{})
	items, _ = srv.CRM.Recycle(ctx, anna, "deal", "Аль")
	if code, _, _ := do("/crm/recycle/"+items[0].ID, url.Values{"op": {"delete"}}); code != http.StatusSeeOther {
		t.Fatalf("delete for good: %d", code)
	}
	if code, page, _ := do("/crm/recycle", nil); code != http.StatusOK || strings.Contains(page, "Альфа") || !strings.Contains(page, "Корзина пуста") {
		t.Fatalf("the emptied bin: %d", code)
	}

	// REST
	tok, err := srv.Keys.Create(ctx, anna, "crm", []string{"crm:read", "crm:write"}, 0)
	must(err)
	lead, err := srv.CRM.SaveLead(ctx, anna, crm.Lead{Title: "Заявка", Responsible: crm.Ref{ID: anna}})
	must(err)
	must(srv.CRM.DeleteLead(ctx, anna, lead))
	api := func(method, path string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	code, out := api("GET", "/api/v1/crm/recycle")
	if code != http.StatusOK || !strings.Contains(out, `"entity": "lead"`) || !strings.Contains(out, `"title": "Заявка"`) {
		t.Fatalf("GET recycle: %d %s", code, out)
	}
	items, _ = srv.CRM.Recycle(ctx, anna, "", "")
	if code, out := api("POST", "/api/v1/crm/recycle/"+items[0].ID+"/restore"); code != http.StatusOK || !strings.Contains(out, lead) {
		t.Fatalf("restore: %d %s", code, out)
	}
	if _, err := srv.CRM.Lead(ctx, lead); err != nil {
		t.Fatal(err)
	}
	if code, _ := api("DELETE", "/api/v1/crm/recycle/"+items[0].ID); code != http.StatusNotFound {
		t.Fatalf("a restored card deleted from the bin: %d", code)
	}
}
