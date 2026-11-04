package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestRestPortalGroups (GO24DEV-940): sonet_group.create makes a closed group (OPENED — open, VISIBLE N — secret,
// PROJECT — a project) with its keywords and subject, sonet_group.get finds it by FILTER with «%», «!», «>=» and
// ORDER; update renames, archives and changes INITIATE_PERMS; user.add, user.delete (not the owner), user.invite (the
// bell asks), user.request (an open group — at once, a closed one — a request), user.get and user.groups by the
// roles A / E / K; feature.access as the rights; socialnetwork.api.workgroup.get and list; setowner; delete; a guest
// creates none, a stranger sees no secret group.
func TestRestPortalGroups(t *testing.T) {
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
	type person struct {
		id  string
		num int64
		key string
	}
	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), Notify: notify.NewStore(pool),
		I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	people := map[string]*person{}
	for _, n := range []string{"anna", "boris", "vera", "guest"} {
		p := &person{}
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@t', $1 = 'guest') RETURNING id, num`, n).
			Scan(&p.id, &p.num))
		p.key, err = srv.Keys.Create(ctx, p.id, "k", []string{"projects:read", "projects:write"}, time.Hour)
		must(err)
		people[n] = p
	}
	anna, boris, vera := people["anna"], people["boris"], people["vera"]
	num := func(p *person) string { return strconv.FormatInt(p.num, 10) }
	call := func(who *person, method, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/rest/"+num(who)+"/"+who.key+"/"+method, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, raw)
		}
		var out map[string]any
		must(json.Unmarshal(raw, &out))
		return out
	}
	list := func(v any) string {
		var out []string
		for _, x := range v.([]any) {
			out = append(out, portalStr(x))
		}
		return strings.Join(out, ",")
	}
	var subject string
	must(pool.QueryRow(ctx, `INSERT INTO group_subjects (name) VALUES ('Отделы') RETURNING num::text`).Scan(&subject))

	// creating: closed by default, open with OPENED, a project with keywords and a subject
	closed := portalStr(call(anna, "sonet_group.create", `{"NAME": "Закрытая", "DESCRIPTION": "о ней"}`, 200)["result"])
	open := portalStr(call(anna, "sonet_group.create", `{"NAME": "Открытая", "OPENED": "Y", "PROJECT": "Y", "KEYWORDS": "склад, учёт",
		"SUBJECT_ID": `+subject+`, "PROJECT_DATE_START": "2026-11-01", "INITIATE_PERMS": "K"}`, 200)["result"])
	secret := portalStr(call(anna, "sonet_group.create", `{"NAME": "Тайная", "VISIBLE": "N"}`, 200)["result"])
	call(people["guest"], "sonet_group.create", `{"NAME": "Гостевая"}`, 400) // a guest's key reaches only their collabs
	call(anna, "sonet_group.create", `{"NAME": ""}`, 400)
	row := func(who *person, id string) map[string]any {
		t.Helper()
		list := call(who, "sonet_group.get", `{"FILTER": {"ID": `+id+`}}`, 200)["result"].([]any)
		if len(list) != 1 {
			return nil
		}
		return list[0].(map[string]any)
	}
	g := row(anna, open)
	if g["NAME"] != "Открытая" || g["OPENED"] != "Y" || g["VISIBLE"] != "Y" || g["PROJECT"] != "Y" || g["KEYWORDS"] != "склад,учёт" ||
		g["SUBJECT_ID"] != subject || g["SUBJECT_NAME"] != "Отделы" || g["OWNER_ID"] != num(anna) || g["INITIATE_PERMS"] != "K" ||
		g["PROJECT_DATE_START"] != "2026-11-01" || g["NUMBER_OF_MEMBERS"] != "1" {
		t.Fatalf("the open project: %v", g)
	}
	if g := row(anna, closed); g["OPENED"] != "N" || g["VISIBLE"] != "Y" || g["PROJECT"] != "N" || g["DESCRIPTION"] != "о ней" {
		t.Fatalf("the closed group: %v", g)
	}
	if g := row(anna, secret); g["VISIBLE"] != "N" {
		t.Fatalf("the secret group: %v", g)
	}
	if row(boris, secret) != nil || row(boris, closed) == nil {
		t.Fatal("a stranger sees the closed group, not the secret one")
	}

	// the list: newest first by default, FILTER with «%», «!», «>=», ORDER
	ids := func(r map[string]any) []string {
		var out []string
		for _, x := range r["result"].([]any) {
			out = append(out, x.(map[string]any)["ID"].(string))
		}
		return out
	}
	if got := ids(call(anna, "sonet_group.get", `{}`, 200)); strings.Join(got, ",") != secret+","+open+","+closed {
		t.Fatalf("by default: %v", got)
	}
	if got := ids(call(anna, "sonet_group.get", `{"FILTER": {"%NAME": "тая"}, "ORDER": {"ID": "ASC"}}`, 200)); strings.Join(got, ",") != closed+","+open {
		t.Fatalf("%%NAME: %v", got)
	}
	if got := ids(call(anna, "sonet_group.get", `{"FILTER": {"!ID": `+open+`, ">=ID": `+closed+`, "VISIBLE": "Y"}}`, 200)); strings.Join(got, ",") != closed {
		t.Fatalf("!ID, >=ID: %v", got)
	}

	// changing
	call(anna, "sonet_group.update", `{"GROUP_ID": `+closed+`, "NAME": "Закрытая 2", "INITIATE_PERMS": "A"}`, 200)
	if g := row(anna, closed); g["NAME"] != "Закрытая 2" || g["INITIATE_PERMS"] != "A" || g["DESCRIPTION"] != "о ней" {
		t.Fatalf("updated: %v", g)
	}
	call(boris, "sonet_group.update", `{"GROUP_ID": `+closed+`, "NAME": "Чужая"}`, 403)

	// members: added at once, the owner is not taken out, invited — the bell asks
	if r := call(anna, "sonet_group.user.add", `{"GROUP_ID": `+closed+`, "USER_ID": [`+num(boris)+`, 999999]}`, 200); list(r["result"]) != num(boris) {
		t.Fatalf("added: %v", r)
	}
	call(boris, "sonet_group.user.add", `{"GROUP_ID": `+closed+`, "USER_ID": `+num(vera)+`}`, 403) // a member only
	users := call(anna, "sonet_group.user.get", `{"ID": `+closed+`}`, 200)["result"].([]any)
	if len(users) != 2 || users[0].(map[string]any)["ROLE"] != "A" || users[1].(map[string]any)["USER_ID"] != num(boris) || users[1].(map[string]any)["ROLE"] != "K" {
		t.Fatalf("the members: %v", users)
	}
	if r := call(anna, "sonet_group.user.delete", `{"GROUP_ID": `+closed+`, "USER_ID": [`+num(anna)+`, `+num(boris)+`]}`, 200); list(r["result"]) != num(boris) {
		t.Fatalf("taken out: %v", r)
	}
	if r := call(anna, "sonet_group.user.invite", `{"GROUP_ID": `+closed+`, "USER_ID": `+num(vera)+`, "MESSAGE": "к нам"}`, 200); list(r["result"]) != num(vera) {
		t.Fatalf("invited: %v", r)
	}
	var asked int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'project_invite'`, vera.id).Scan(&asked))
	if asked != 1 {
		t.Fatalf("the invitation in the bell: %d", asked)
	}
	full := call(anna, "socialnetwork.api.workgroup.get", `{"params": {"groupId": `+closed+`}}`, 200)["result"].(map[string]any)
	if full["ID"] != float64(portalInt(closed)) || list(full["INVITED_MEMBERS"]) != num(vera) || list(full["MEMBERS"]) != num(anna) ||
		full["TYPE"] != "group" || full["NAME"] != "Закрытая 2" {
		t.Fatalf("workgroup.get: %v", full)
	}

	// asking to join: an open group — at once, a closed one — a request; a secret one is not found
	call(boris, "sonet_group.user.request", `{"GROUP_ID": `+open+`}`, 200)
	call(boris, "sonet_group.user.request", `{"GROUP_ID": `+closed+`, "MESSAGE": "возьмите"}`, 200)
	call(boris, "sonet_group.user.request", `{"GROUP_ID": `+secret+`}`, 403)
	var requested int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM project_join_requests WHERE user_id = $1 AND message = 'возьмите'`, boris.id).Scan(&requested))
	if requested != 1 {
		t.Fatalf("the request: %d", requested)
	}
	mine := call(boris, "sonet_group.user.groups", `{}`, 200)["result"].([]any)
	if len(mine) != 1 || mine[0].(map[string]any)["GROUP_ID"] != open || mine[0].(map[string]any)["ROLE"] != "K" {
		t.Fatalf("his groups: %v", mine)
	}

	// features: a member works, the owner moderates; an outsider views an open group only
	feature := func(who *person, group, f, op string) any {
		return call(who, "sonet_group.feature.access", `{"GROUP_ID": `+group+`, "FEATURE": "`+f+`", "OPERATION": "`+op+`"}`, 200)["result"]
	}
	if feature(boris, open, "blog", "write_post") != true || feature(boris, open, "tasks", "delete_tasks") != false ||
		feature(anna, open, "tasks", "delete_tasks") != true || feature(vera, open, "calendar", "view") != true ||
		feature(vera, closed, "files", "view") != false || feature(anna, open, "photo", "view") != false {
		t.Fatal("feature.access")
	}
	call(anna, "sonet_group.feature.access", `{"GROUP_ID": `+open+`, "FEATURE": "wiki", "OPERATION": "view"}`, 400)

	// the list of socialnetwork.api
	wl := call(anna, "socialnetwork.api.workgroup.list", `{"filter": {"PROJECT": "Y"}, "select": ["NAME", "OPENED"]}`, 200)
	groups := wl["result"].(map[string]any)["workgroups"].([]any)
	if wl["total"] != float64(1) || len(groups) != 1 || groups[0].(map[string]any)["name"] != "Открытая" || groups[0].(map[string]any)["opened"] != "Y" ||
		groups[0].(map[string]any)["additionalData"].(map[string]any)["role"] != "A" {
		t.Fatalf("workgroup.list: %v", wl)
	}

	// the owner: handed to a member; archiving; deleting
	call(anna, "sonet_group.setowner", `{"GROUP_ID": `+open+`, "USER_ID": `+num(boris)+`}`, 200)
	if g := row(anna, open); g["OWNER_ID"] != num(boris) {
		t.Fatalf("the new owner: %v", g)
	}
	call(anna, "sonet_group.update", `{"GROUP_ID": `+closed+`, "CLOSED": "Y"}`, 200)
	if g := row(anna, closed); g["CLOSED"] != "Y" {
		t.Fatalf("archived: %v", g)
	}
	call(boris, "sonet_group.delete", `{"GROUP_ID": `+secret+`}`, 403)
	if r := call(anna, "sonet_group.delete", `{"GROUP_ID": `+secret+`}`, 200); r["result"] != true || row(anna, secret) != nil {
		t.Fatalf("deleted: %v", r)
	}
	call(anna, "sonet_group.get", `{"FILTER": {"ID": `+secret+`}}`, 200)
	call(anna, "sonet_group.setowner", `{"GROUP_ID": 0, "USER_ID": 1}`, 400)
}

// TestPortalGroupParams: the group's type of VISIBLE and OPENED (an open group is seen by all), IMAGE as [name, base64]
// or a data: URL; the filter's «!», «%», «!%», «>», «>=», «<», «<=» on rows, numbers compared as numbers.
func TestPortalGroupParams(t *testing.T) {
	y, n := true, false
	for _, c := range []struct {
		now             string
		visible, opened *bool
		want            string
	}{{"secret", nil, &y, "open"}, {"secret", &y, nil, "closed"}, {"open", nil, &n, "closed"}, {"open", &n, nil, "secret"},
		{"closed", &y, &y, "open"}, {"closed", nil, nil, "closed"}, {"open", &n, &y, "secret"}} {
		if got := portalGroupPrivacy(c.now, c.visible, c.opened); got != c.want {
			t.Errorf("%s %v %v: %s, want %s", c.now, c.visible, c.opened, got, c.want)
		}
	}
	for _, in := range []map[string]any{{"IMAGE": []any{"a.png", "iVBORw0K"}}, {"image": "data:image/png;base64,iVBORw0K"}} {
		if raw, ok, err := portalGroupImage(in); err != nil || !ok || string(raw[1:4]) != "PNG" {
			t.Errorf("%v: %q %v %v", in, raw, ok, err)
		}
	}
	if _, ok, _ := portalGroupImage(map[string]any{}); ok {
		t.Error("no IMAGE")
	}
	if _, _, err := portalGroupImage(map[string]any{"IMAGE": []any{"a.png", "не base64"}}); err == nil {
		t.Error("a bad IMAGE")
	}
	row := map[string]any{"ID": "12", "NAME": "Склад Москва", "DATE_CREATE": "2026-10-29T10:00:00+01:00"}
	for f, want := range map[string]bool{
		`{"ID": 12}`: true, `{"!ID": 12}`: false, `{"!=ID": 9}`: true, `{">ID": 9}`: true, `{">ID": 100}`: false,
		`{">=ID": 12}`: true, `{"<ID": 12}`: false, `{"<=ID": "12"}`: true, `{"%NAME": "москва"}`: true,
		`{"!%NAME": "москва"}`: false, `{"=NAME": "склад москва"}`: true, `{">=DATE_CREATE": "2026-10-29"}`: true,
		`{"<DATE_CREATE": "2026-10-29"}`: false, `{"ID": [1, 12]}`: true, `{"NOPE": 1}`: true,
	} {
		var filter map[string]any
		if err := json.Unmarshal([]byte(f), &filter); err != nil {
			t.Fatal(err)
		}
		if got := portalRowMatch(row, filter); got != want {
			t.Errorf("%s: %v, want %v", f, got, want)
		}
	}
}
