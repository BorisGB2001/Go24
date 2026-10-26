package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/calls"
	"go24/internal/db"
	"go24/internal/tasks"
)

// TestCallGuests: one of a task's call makes a guest link with a password and the lobby (someone outside the task
// cannot); a guest gives a wrong password and is refused, the right one puts them in the lobby; the people of the
// call see them waiting and let them in once; the guest's page then joins the call as «Имя (гость)»; the link revoked
// puts the guest out of the LiveKit room and lets nobody in; a link without the lobby lets the guest in at once.
func TestCallGuests(t *testing.T) {
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
	newUser := func(name string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, name).Scan(&id))
		return id
	}
	anna, outsider := newUser("Анна"), newUser("Посторонний")
	ts := tasks.NewStore(pool)
	_, err = ts.CreateProject(ctx, anna, "CG", "Проект", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, anna, "CG")
	must(err)
	key, err := ts.CreateTask(ctx, anna, p, tasks.NewTask{Title: "Созвон с клиентом", ResponsibleID: anna})
	must(err)

	var mu sync.Mutex
	var removed []string
	lk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if strings.HasSuffix(r.URL.Path, "/RemoveParticipant") {
			mu.Lock()
			removed = append(removed, in["room"]+" "+in["identity"])
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer lk.Close()
	prov := calls.New("wss://lk", "key", "secret")
	prov.APIURL = lk.URL
	srv := &Server{DB: pool, Tasks: ts, Calls: prov, CallGuests: &calls.Guests{DB: pool}, BaseURL: "http://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := func(c *http.Client) *http.Client {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}
	member := noRedirect(&http.Client{})
	do := func(c *http.Client, who, method, path string, form url.Values) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Accept-Language", "ru")
		if who != "" {
			rec := httptest.NewRecorder()
			must(srv.Sessions.Set(rec, auth.Session{UserID: who, Name: who}))
			req.AddCookie(rec.Result().Cookies()[0])
		}
		res, err := c.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	q := "?task=" + key + "&video=1"
	invite := func(form url.Values) string {
		t.Helper()
		st, body := do(member, anna, "POST", "/call/guests"+q, form)
		if st != http.StatusCreated {
			t.Fatalf("make a guest link: %d %s", st, body)
		}
		var l struct{ ID, URL string }
		must(json.Unmarshal([]byte(body), &l))
		if !strings.HasPrefix(l.URL, "http://go24/call/g/") {
			t.Fatalf("guest link = %s", l.URL)
		}
		return strings.TrimPrefix(l.URL, "http://go24")
	}

	if st, _ := do(member, outsider, "POST", "/call/guests"+q, url.Values{"ttl": {"24"}}); st != http.StatusForbidden && st != http.StatusNotFound {
		t.Fatalf("someone outside the task made a guest link: %d", st)
	}
	link := invite(url.Values{"ttl": {"1"}, "password": {"тайна"}, "lobby": {"1"}})

	jar, _ := cookiejar.New(nil)
	guest := noRedirect(&http.Client{Jar: jar})
	if st, body := do(guest, "", "GET", link, nil); st != http.StatusOK || !strings.Contains(body, `name="name"`) || !strings.Contains(body, `name="password"`) || !strings.Contains(body, "Созвон с клиентом") {
		t.Fatalf("the guest page asks no name and password: %d %s", st, body)
	}
	if _, body := do(guest, "", "POST", link, url.Values{"name": {"Иван Петров"}, "password": {"не та"}}); !strings.Contains(body, "Неверный пароль") {
		t.Fatalf("a wrong password let the guest on: %s", body)
	}
	if st, _ := do(guest, "", "POST", link, url.Values{"name": {"Иван Петров"}, "password": {"тайна"}}); st != http.StatusSeeOther {
		t.Fatalf("the right password: %d", st)
	}
	if _, body := do(guest, "", "GET", link, nil); !strings.Contains(body, "data-guest-wait") {
		t.Fatalf("the guest is not in the lobby: %s", body)
	}
	if _, body := do(guest, "", "GET", link+"/state", nil); !strings.Contains(body, `"waiting"`) {
		t.Fatalf("state = %s", body)
	}

	_, body := do(member, anna, "GET", "/call/guests"+q, nil)
	var list struct {
		Links   []struct{ ID string }
		Waiting []struct{ ID, Name string }
	}
	must(json.Unmarshal([]byte(body), &list))
	if len(list.Links) != 1 || len(list.Waiting) != 1 || list.Waiting[0].Name != "Иван Петров" {
		t.Fatalf("the people of the call see %s", body)
	}
	if st, _ := do(member, outsider, "GET", "/call/guests"+q, nil); st == http.StatusOK {
		t.Fatal("someone outside the task sees the lobby")
	}
	gid := list.Waiting[0].ID
	if st, _ := do(member, anna, "POST", "/call/lobby/"+gid+"/admit"+q, url.Values{}); st != http.StatusNoContent {
		t.Fatalf("admit: %d", st)
	}
	if st, _ := do(member, anna, "POST", "/call/lobby/"+gid+"/deny"+q, url.Values{}); st == http.StatusNoContent {
		t.Fatal("the guest let in was declined after")
	}

	_, page := do(guest, "", "GET", link, nil)
	m := regexp.MustCompile(`data-token="([^"]+)"`).FindStringSubmatch(page)
	if m == nil || strings.Contains(page, "data-guests") || strings.Contains(page, "data-copy=") {
		t.Fatalf("the guest's call page: %s", page)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(strings.Split(m[1], ".")[1])
	var claims struct {
		Sub, Name string
		Video     struct{ Room string }
	}
	must(json.Unmarshal(raw, &claims))
	if claims.Sub != calls.GuestIdentity(gid) || claims.Name != "Иван Петров (гость)" || claims.Video.Room != "task-"+key {
		t.Fatalf("the guest's token: %+v", claims)
	}

	if st, _ := do(member, anna, "POST", "/call/guests/"+list.Links[0].ID+"/revoke"+q, url.Values{}); st != http.StatusNoContent {
		t.Fatalf("revoke: %d", st)
	}
	if len(removed) != 1 || removed[0] != "task-"+key+" "+calls.GuestIdentity(gid) {
		t.Fatalf("revoked, LiveKit was asked to remove %v", removed)
	}
	if st, body := do(guest, "", "GET", link, nil); st != http.StatusNotFound || !strings.Contains(body, "не действует") {
		t.Fatalf("a revoked link still opens: %d", st)
	}

	open := invite(url.Values{"ttl": {"168"}})
	other := noRedirect(&http.Client{Jar: func() http.CookieJar { j, _ := cookiejar.New(nil); return j }()})
	do(other, "", "POST", open, url.Values{"name": {"Ольга"}})
	if _, page := do(other, "", "GET", open, nil); !strings.Contains(page, `data-token="`) {
		t.Fatal("a link without the lobby did not let the guest in at once")
	}
	if _, err := pool.Exec(ctx, `UPDATE call_guest_links SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if st, _ := do(other, "", "GET", open, nil); st != http.StatusNotFound {
		t.Fatalf("a link out of time still opens: %d", st)
	}
}
