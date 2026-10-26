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

// TestCRMPresetsHTTP: «Пресеты фильтра» of the CRM lists — the built-in «… в работе», «Мои …»,
// «Закрытые …» (for whoever looks); one's own filter saved under a name, per list, the default one opening the list
// unless the address has a filter or «Сбросить»; the star toggles it; the same name replaces a preset.
func TestCRMPresetsHTTP(t *testing.T) {
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
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, nil), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	pipe, won := pipes[0].ID, ""
	for _, st := range pipes[0].Stages {
		if st.Semantic == "success" {
			won = st.ID
		}
	}

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Admin: true}))
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
	var bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Борис', 'bob@t') RETURNING id`).Scan(&bob))
	for _, d := range []crm.Deal{{Title: "Альфа", Responsible: crm.Ref{ID: anna}}, {Title: "Бета", Stage: crm.Stage{ID: won}, Responsible: crm.Ref{ID: anna}},
		{Title: "Гамма", Stage: crm.Stage{ID: won}, Responsible: crm.Ref{ID: bob}}} {
		d.PipelineID = pipe
		_, err := srv.CRM.SaveDeal(ctx, anna, d)
		must(err)
	}
	shows := func(path string, want ...string) {
		t.Helper()
		_, page, _ := do(path, nil)
		for _, title := range []string{"Альфа", "Бета", "Гамма"} {
			if strings.Contains(page, ">"+title+"<") != has(want, title) {
				t.Fatalf("%s: %s shown %v, want %v", path, title, !has(want, title), has(want, title))
			}
		}
	}

	// the built-in presets: «Мои сделки» (whoever looks), «Закрытые сделки» (won or lost)
	if _, page, _ := do("/crm/deals?view=list", nil); !strings.Contains(page, "Сделки в работе") || !strings.Contains(page, "Закрытые сделки") {
		t.Fatal("no built-in presets")
	}
	shows("/crm/deals?view=list&responsible=me", "Альфа", "Бета")
	shows("/crm/deals?view=list&semantic=closed", "Бета", "Гамма")
	if _, csv, _ := do("/crm/export/deals?responsible=me", nil); !strings.Contains(csv, "Альфа") || strings.Contains(csv, "Гамма") {
		t.Fatal("export: «me» not applied")
	}

	// an own preset, the list's default: the list opens with it, «Сбросить» shows all; the leads don't have it
	code, _, loc := do("/crm/presets", url.Values{"scope": {"deal"}, "name": {"Мои закрытые"}, "default": {"1"}, "back": {"/crm/deals?view=list"},
		"query": {"semantic=closed&responsible=me&view=list&q=x&sort=title"}})
	if code != http.StatusSeeOther || loc != "/crm/deals?view=list" {
		t.Fatalf("save preset: %d %s", code, loc)
	}
	var id, query string
	must(pool.QueryRow(ctx, `SELECT id, query FROM crm_filter_presets WHERE user_id = $1 AND scope = 'deal' AND is_default`, anna).Scan(&id, &query))
	if query != "responsible=me&semantic=closed" {
		t.Fatalf("preset query %q", query)
	}
	shows("/crm/deals?view=list", "Бета")
	shows("/crm/deals?view=list&reset=1", "Альфа", "Бета", "Гамма")
	shows("/crm/deals?view=list&semantic=process", "Альфа")
	if _, page, _ := do("/crm/deals?view=list", nil); !strings.Contains(page, `class="chip on"><a href="/crm/deals?pipeline=`) {
		t.Fatal("the default preset's chip is not on")
	}
	if _, page, _ := do("/crm/leads?view=list", nil); strings.Contains(page, "Мои закрытые") || !strings.Contains(page, "Мои лиды") {
		t.Fatal("leads: the deals' preset or no built-in ones")
	}
	// the same name replaces it; bad names are refused
	if code, _, _ := do("/crm/presets", url.Values{"scope": {"deal"}, "name": {"мои закрытые"}, "query": {"semantic=closed"}, "back": {"/crm/deals"}}); code != http.StatusSeeOther {
		t.Fatalf("resave: %d", code)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_filter_presets WHERE user_id = $1`, anna).Scan(&n))
	if n != 1 {
		t.Fatalf("presets after resave: %d", n)
	}
	shows("/crm/deals?view=list", "Альфа", "Бета", "Гамма") // saved without «default»: none now
	if code, _, _ := do("/crm/presets", url.Values{"scope": {"deal"}, "name": {strings.Repeat("я", 61)}, "back": {"/crm/deals"}}); code != http.StatusSeeOther {
		t.Fatalf("long name: %d", code)
	}
	if code, _, _ := do("/crm/presets", url.Values{"scope": {"task"}, "name": {"x"}}); code != http.StatusNotFound {
		t.Fatalf("bad scope: %d", code)
	}
	// the star toggles the default; another's preset is out of reach; delete
	do("/crm/presets/"+id, url.Values{"op": {"default"}, "back": {"/crm/deals"}})
	shows("/crm/deals?view=list", "Бета", "Гамма")
	do("/crm/presets/"+id, url.Values{"op": {"default"}, "back": {"/crm/deals"}})
	shows("/crm/deals?view=list", "Альфа", "Бета", "Гамма")
	must(srv.CRM.SetDefaultPreset(ctx, anna, id))
	if err := srv.CRM.DeletePreset(ctx, bob, id); err == nil {
		t.Fatal("bob deleted anna's preset")
	}
	if code, _, loc := do("/crm/presets/"+id, url.Values{"op": {"delete"}, "back": {"https://evil/"}}); code != http.StatusSeeOther || loc != "/crm/deals" {
		t.Fatalf("delete: %d %s", code, loc)
	}
	shows("/crm/deals?view=list", "Альфа", "Бета", "Гамма")
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
