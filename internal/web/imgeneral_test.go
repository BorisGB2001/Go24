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
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/tasks"
)

// TestGeneralChatSettings: «Общий чат» — an administrator chooses who writes in it and the welcome of the new, an
// employee may not; the REST reads the settings and lets an administrator only change them; the messenger shows the
// chat as the general one, with its settings for an administrator, and read-only to one who may not write.
func TestGeneralChatSettings(t *testing.T) {
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
	user := func(name string, admin bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, username, admin) VALUES ($1, $1, $1, $2) RETURNING id`, name, admin).Scan(&id))
		return id
	}
	adm, ann := user("adm", true), user("ann", false)
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	must(err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := &Server{DB: pool, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), I18n: bundle, pages: pages,
		Keys: apikeys.NewStore(pool), Tasks: tasks.NewStore(pool), Log: log, BaseURL: "https://go24.test",
		Matrix: matrixsync.New(&matrixsync.Service{DB: pool, Log: log})}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()

	cl := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who string, admin bool, method string, v url.Values) (int, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who, Admin: admin}))
		req, _ := http.NewRequest(method, hs.URL+"/settings/general-chat", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := cl.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	if st, body := do(adm, true, "GET", nil); st != http.StatusOK || !strings.Contains(body, `name="who" value="all" checked`) ||
		!strings.Contains(body, `name="hello" value="1" checked`) {
		t.Fatalf("the settings page: %d", st)
	}
	if st, _ := do(ann, false, "POST", url.Values{"who": {"all"}}); st != http.StatusForbidden {
		t.Fatalf("an employee changed the settings: %d", st)
	}
	if st, _ := do(adm, true, "POST", url.Values{"who": {"admins"}}); st != http.StatusSeeOther {
		t.Fatalf("the form: %d", st)
	}
	if g, _ := srv.Matrix.GeneralSettings(ctx); g != (matrixsync.General{Who: "admins", NoHello: true}) {
		t.Fatalf("the settings saved: %+v", g)
	}
	if !srv.Matrix.GeneralClosed(ctx, ann) || srv.Matrix.GeneralClosed(ctx, adm) {
		t.Fatal("who writes in the general chat")
	}

	api := func(who, method, body string) (int, apiGeneral) {
		t.Helper()
		tok, err := srv.Keys.Create(ctx, who, "im", []string{"im:read", "im:write"}, time.Hour)
		must(err)
		req, _ := http.NewRequest(method, hs.URL+"/api/v1/im/general", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var out apiGeneral
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}
	if st, g := api(ann, "GET", ""); st != http.StatusOK || g.Who != "admins" || g.Hello || g.Room != "" {
		t.Fatalf("GET: %d %+v", st, g)
	}
	if st, _ := api(ann, "PATCH", `{"who": "all"}`); st != http.StatusForbidden {
		t.Fatalf("an employee's PATCH: %d", st)
	}
	if st, g := api(adm, "PATCH", `{"hello": true}`); st != http.StatusOK || g.Who != "admins" || !g.Hello {
		t.Fatalf("PATCH of one field: %d %+v", st, g)
	}
	if st, _ := api(adm, "PATCH", `{"who": "some"}`); st == http.StatusOK {
		t.Fatal("PATCH of a rule that is not a choice")
	}

	r := matrixsync.Room{ID: "!g:x", Name: "Общий чат", Kind: "general"}
	render := func(admin bool, data map[string]any) string {
		var b strings.Builder
		data["Rooms"], data["Room"], data["Current"], data["Tab"] = []matrixsync.Room{r}, r.ID, r, "all"
		must(pages["ru-RU"]["im"].ExecuteTemplate(&b, "content", page{User: &auth.Session{Admin: admin}, Data: data}))
		return b.String()
	}
	out := render(true, map[string]any{})
	for _, want := range []string{`class="im-ico k-general"`, `title="Чат всех сотрудников компании`, `href="/settings/general-chat"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the administrator's page lacks %q", want)
		}
	}
	out = render(false, map[string]any{"ReadOnly": true, "ReadOnlyKey": "im.general_readonly"})
	if strings.Contains(out, `href="/settings/general-chat"`) || !strings.Contains(out, "В общем чате пишут только администраторы") {
		t.Error("the employee's page: the settings link or no read-only line")
	}
}
