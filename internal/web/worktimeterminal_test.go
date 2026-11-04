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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestTerminalHTTP: «Терминал учёта времени» — the settings page is for those who change the settings of working
// time; an employee sets their PIN; the terminal page, with nobody signed in, shows the PIN pad, then the person by
// the PIN, starts the day there (the day remembers the terminal) and refuses a wrong PIN or an old link; the REST
// lists, adds, changes and deletes terminals.
func TestTerminalHTTP(t *testing.T) {
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, adm := user("Анна"), user("Админ")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, adm)
	must(err)

	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Keys: keys, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, adm} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == adm}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	status := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if c := as[who]; c != nil {
			req.AddCookie(c)
		}
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw) + res.Header.Get("Location")
	}

	status(anna, "GET", "/settings/worktime/terminals", nil, http.StatusForbidden)
	status(anna, "POST", "/settings/worktime/terminals", url.Values{"op": {"add"}, "name": {"Вход"}}, http.StatusForbidden)
	status(adm, "POST", "/settings/worktime/terminals", url.Values{"op": {"add"}, "name": {"Вход"}}, http.StatusSeeOther)
	list, err := srv.Company.Terminals(ctx)
	must(err)
	if len(list) != 1 {
		t.Fatalf("terminals %v", list)
	}
	term := list[0]
	if body := status(adm, "GET", "/settings/worktime/terminals", nil, http.StatusOK); !strings.Contains(body, "https://go24.test/worktime/terminal/"+term.Token) {
		t.Error("the settings page misses the terminal link")
	}

	// the PIN: the worktime page offers it once a terminal works; the administrator sets someone's PIN
	if body := status(anna, "GET", "/worktime", nil, http.StatusOK); !strings.Contains(body, `action="/worktime/pin"`) {
		t.Error("the worktime page offers no PIN")
	}
	if loc := status(anna, "POST", "/worktime/pin", url.Values{"pin": {"12"}}, http.StatusSeeOther); !strings.Contains(loc, "msg=") {
		t.Error("a short PIN is taken")
	}
	status(anna, "POST", "/worktime/pin", url.Values{"pin": {"4321"}}, http.StatusSeeOther)
	status(adm, "POST", "/settings/worktime/terminals", url.Values{"op": {"pin"}, "user": {adm}, "pin": {"4321"}}, http.StatusSeeOther)
	if ok, _ := srv.Company.HasWorkPIN(ctx, adm); ok {
		t.Error("a taken PIN is set")
	}
	status(adm, "POST", "/settings/worktime/terminals", url.Values{"op": {"pin"}, "user": {"nobody"}, "pin": {"5555"}}, http.StatusNotFound)

	// the terminal, nobody signed in
	path := "/worktime/terminal/" + term.Token
	if body := status("", "GET", path, nil, http.StatusOK); !strings.Contains(body, "wterm-pad") {
		t.Error("no PIN pad")
	}
	if body := status("", "POST", path, url.Values{"pin": {"0000"}}, http.StatusOK); !strings.Contains(body, "Неверный PIN") &&
		!strings.Contains(body, "Wrong PIN") {
		t.Error("a wrong PIN is not told")
	}
	if body := status("", "POST", path, url.Values{"pin": {"4321"}}, http.StatusOK); !strings.Contains(body, "Анна") ||
		!strings.Contains(body, `value="start"`) {
		t.Error("the PIN does not show the person with «start»")
	}
	status("", "POST", path, url.Values{"pin": {"4321"}, "action": {"start"}}, http.StatusOK)
	d, err := srv.Company.Today(ctx, anna, time.Now())
	must(err)
	if d == nil || d.Start.Terminal != "Вход" {
		t.Fatalf("the day %+v", d)
	}
	if body := status("", "POST", path, url.Values{"pin": {"4321"}}, http.StatusOK); !strings.Contains(body, `value="finish"`) {
		t.Error("a started day offers no «finish»")
	}
	if body := status(anna, "GET", "/worktime/d/"+anna+"/"+time.Now().Format(time.DateOnly), nil, http.StatusOK); !strings.Contains(body, "Вход") {
		t.Error("the day page does not show the terminal")
	}
	status(adm, "POST", "/settings/worktime/terminals", url.Values{"op": {"link"}, "id": {strconv.FormatInt(term.ID, 10)}}, http.StatusSeeOther)
	if body := status("", "GET", path, nil, http.StatusOK); strings.Contains(body, "wterm-pad") {
		t.Error("the old link still works")
	}

	// REST
	k, err := keys.Create(ctx, adm, "k", []string{"company:read", "company:write"}, time.Hour)
	must(err)
	annaKey, err := keys.Create(ctx, anna, "k", []string{"company:read", "company:write"}, time.Hour)
	must(err)
	api := func(key, method, path, body string, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+key)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, res.StatusCode, raw, want)
		}
		return raw
	}
	api(annaKey, "GET", "/api/v1/worktime/terminals", "", http.StatusForbidden)
	var out apiTerminalOut
	must(json.Unmarshal(api(k, "POST", "/api/v1/worktime/terminals", `{"name":"Склад"}`, http.StatusOK), &out))
	if out.Name != "Склад" || !out.Active || !strings.HasPrefix(out.Link, "https://go24.test/worktime/terminal/") {
		t.Fatalf("added %+v", out)
	}
	link := out.Link
	must(json.Unmarshal(api(k, "PUT", "/api/v1/worktime/terminals/"+strconv.FormatInt(out.ID, 10), `{"active":false,"new_link":true}`, http.StatusOK), &out))
	if out.Active || out.Name != "Склад" || out.Link == link {
		t.Fatalf("changed %+v", out)
	}
	var all []apiTerminalOut
	must(json.Unmarshal(api(k, "GET", "/api/v1/worktime/terminals", "", http.StatusOK), &all))
	if len(all) != 2 {
		t.Fatalf("list %+v", all)
	}
	api(k, "DELETE", "/api/v1/worktime/terminals/"+strconv.FormatInt(out.ID, 10), "", http.StatusNoContent)
	api(k, "GET", "/api/v1/worktime/terminals/"+strconv.FormatInt(out.ID, 10), "", http.StatusNotFound)
}
