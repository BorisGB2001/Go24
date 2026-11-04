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
	"slices"
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

// TestDeptChatsHTTP: «Чаты и каналы» of a department — the head makes the department's chat with its people, others
// may not; the people who come and go follow the structure, one who left the chat is not brought back, one added by
// hand stays; sub-departments on demand; an administrator ties their own chat and unties it; the REST lists, makes and
// unties.
func TestDeptChatsHTTP(t *testing.T) {
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
	boss, anna, eve, kim, olga, adm, zed := user("Борис"), user("Анна"), user("Ева"), user("Ким"), user("Ольга"), user("Админ"), user("Зед")
	_, err = pool.Exec(ctx, `UPDATE users SET admin = true WHERE id = $1`, adm)
	must(err)
	var root, dept, group string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id, head_id) VALUES ('Продажи', $1, $2) RETURNING id`, root, boss).Scan(&dept))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Опт', $1) RETURNING id`, dept).Scan(&group))
	member := func(u, d string) {
		_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, u, d)
		must(err)
	}
	member(anna, dept)
	member(eve, dept)
	member(kim, group)
	member(olga, root)

	chats := &fakeChats{owner: map[string]string{}, members: map[string][]string{}, names: map[string]string{}, topics: map[string]string{}}
	keys := apikeys.NewStore(pool)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Company: company.NewStore(pool), Keys: keys, Chats: chats, BaseURL: "https://go24.test",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{boss, anna, olga, adm} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == adm}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string, string) {
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
		return res.StatusCode, string(raw), res.Header.Get("Location")
	}
	in := func(room string, want ...string) {
		t.Helper()
		got := slices.Clone(chats.members[room])
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("chat %s: %v, want %v", room, got, want)
		}
	}
	page := "/company/departments/" + dept + "/chats"

	// one who is neither an administrator nor the department's head or deputy may not
	if st, _, loc := do(olga, "POST", page, url.Values{"op": {"create"}}); strings.HasPrefix(loc, "/im") || st == http.StatusOK {
		t.Fatalf("an outsider made the chat: %d %s", st, loc)
	}
	if len(chats.owner) != 0 {
		t.Fatal("an outsider made a chat")
	}
	// the head makes it: the department's name, its people with him
	st, _, loc := do(boss, "POST", page, url.Values{"op": {"create"}})
	if st != http.StatusSeeOther || !strings.HasPrefix(loc, "/im?room=") {
		t.Fatalf("create: %d %s", st, loc)
	}
	room, _ := url.QueryUnescape(strings.TrimPrefix(loc, "/im?room="))
	if chats.owner[room] != boss || chats.names[room] != "Продажи" {
		t.Fatalf("chat %s: owner %s, name %q", room, chats.owner[room], chats.names[room])
	}
	in(room, boss, anna, eve)
	if st, body, _ := do(boss, "GET", page, nil); st != http.StatusOK || !strings.Contains(body, `value="unlink"`) {
		t.Fatalf("chats page: %d", st)
	}
	if st, body, _ := do(anna, "GET", page, nil); st != http.StatusOK || strings.Contains(body, `value="unlink"`) || !strings.Contains(body, "Продажи") {
		t.Fatalf("chats page of an employee: %d", st)
	}
	if st, body, _ := do(anna, "GET", "/company/structure", nil); st != http.StatusOK || !strings.Contains(body, "dchats") {
		t.Fatalf("structure: %d", st)
	}

	// the structure changes: Anna moves out, Zed comes in; Eve leaves the chat herself, Olga is added by hand
	_, err = pool.Exec(ctx, `DELETE FROM user_departments WHERE user_id = $1`, anna)
	must(err)
	member(zed, dept)
	must(chats.RemoveFromGroup(ctx, boss, room, eve))
	must(chats.AddToGroup(ctx, boss, room, []string{olga}))
	srv.syncDeptChats(ctx)
	in(room, boss, zed, olga)
	// once more: nothing changes (Eve, who left the chat herself, is not brought back)
	srv.syncDeptChats(ctx)
	in(room, boss, zed, olga)

	// with sub-departments: Kim of «Опт» comes in, and goes out again without them
	if st, _, _ := do(boss, "POST", page, url.Values{"op": {"sub"}, "room": {room}, "sub": {"1"}}); st != http.StatusSeeOther {
		t.Fatalf("sub: %d", st)
	}
	in(room, boss, zed, olga, kim)
	do(boss, "POST", page, url.Values{"op": {"sub"}, "room": {room}, "sub": {"0"}})
	in(room, boss, zed, olga)

	// an administrator ties his own group chat: the department's people come into it; tied twice — refused
	own, err := chats.CreateGroup(ctx, adm, "Общий", []string{olga}, false)
	must(err)
	if st, _, loc := do(adm, "POST", page, url.Values{"op": {"link"}, "room": {own}}); st != http.StatusSeeOther || loc != page {
		t.Fatalf("link: %d %s", st, loc)
	}
	in(own, adm, olga, boss, eve, zed)
	if list, _ := srv.Company.DeptChats(ctx, dept); len(list) != 2 || list[1].Name != "Общий" {
		t.Fatalf("department chats: %+v", list)
	}
	do(adm, "POST", page, url.Values{"op": {"link"}, "room": {own}})
	// a chat the person is not in may not be tied
	do(olga, "POST", page, url.Values{"op": {"link"}, "room": {room}})
	if list, _ := srv.Company.DeptChats(ctx, dept); len(list) != 2 {
		t.Fatalf("department chats after the refusals: %d", len(list))
	}
	if st, _, _ := do(adm, "POST", page, url.Values{"op": {"unlink"}, "room": {own}}); st != http.StatusSeeOther {
		t.Fatalf("unlink: %d", st)
	}
	in(own, adm, olga, boss, eve, zed) // the chat and its people stay

	// REST
	key := func(uid string) string {
		k, err := keys.Create(ctx, uid, "k", []string{"company:read", "company:write"}, time.Hour)
		must(err)
		return k
	}
	bossKey, olgaKey := key(boss), key(olga)
	api := func(k, method, path, body string, want int) []byte {
		t.Helper()
		var rd io.Reader
		if body != "" {
			rd = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+k)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, b)
		}
		return b
	}
	var list []apiDeptChat
	must(json.Unmarshal(api(olgaKey, "GET", "/api/v1/departments/"+dept+"/chats", "", http.StatusOK), &list))
	if len(list) != 1 || list[0].Room != room || list[0].URL == "" {
		t.Fatalf("REST chats: %+v", list)
	}
	api(olgaKey, "POST", "/api/v1/departments/"+dept+"/chats", `{"name":"Канал"}`, http.StatusForbidden)
	var made struct{ Room string }
	must(json.Unmarshal(api(bossKey, "POST", "/api/v1/departments/"+dept+"/chats", `{"name":"Новости продаж","channel":true,"sub":true}`, http.StatusCreated), &made))
	in(made.Room, boss, eve, zed, kim)
	api(bossKey, "PATCH", "/api/v1/departments/"+dept+"/chats?room="+url.QueryEscape(made.Room), `{"sub":false}`, http.StatusNoContent)
	in(made.Room, boss, eve, zed)
	api(bossKey, "DELETE", "/api/v1/departments/"+dept+"/chats?room="+url.QueryEscape(made.Room), "", http.StatusNoContent)
	api(bossKey, "DELETE", "/api/v1/departments/"+dept+"/chats?room="+url.QueryEscape(made.Room), "", http.StatusNotFound)
}
