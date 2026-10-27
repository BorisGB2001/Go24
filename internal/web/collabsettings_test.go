package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// navItem is an item or a section of the menu: they are dragged to reorder it, which is no change of a collab.
var navItem = regexp.MustCompile(`<[a-z]+ [^>]*data-mid="[^>]*>`)

// TestCollabSecurityHTTP: the settings, the archive, removing and deleting of a collab hold on the server — the
// pages and the REST API refuse what the collab does not allow, whatever the browser sends; an outsider still
// learns nothing; the company's administrator manages any collab.
func TestCollabSecurityHTTP(t *testing.T) {
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
	newUser := func(name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $1 || '@t', $2) RETURNING id`, name, guest).Scan(&id))
		return id
	}
	owner, mod, dev, client := newUser("Ольга", false), newUser("Модест", false), newUser("Девид", false), newUser("Клиент Гостев", true)
	outsider, admin := newUser("Посторонний", false), newUser("Админ", false)

	ts := tasks.NewStore(pool)
	keys := apikeys.NewStore(pool)
	srv := &Server{
		DB: pool, Tasks: ts, Keys: keys, Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := map[string]*http.Cookie{}
	for _, u := range []string{owner, mod, dev, client, outsider, admin} {
		rec := httptest.NewRecorder()
		sess := auth.Session{UserID: u}
		if u == admin {
			sess.Admin, sess.AdminMode = true, true
		}
		must(srv.Sessions.Set(rec, sess))
		cookie[u] = rec.Result().Cookies()[0]
	}
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who, method, path, ctype string, body io.Reader) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		req.AddCookie(cookie[who])
		res, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		if form == nil {
			return send(who, method, path, "", nil)
		}
		return send(who, method, path, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	}
	upload := func(who, path string) (int, string) {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		fw, _ := mw.CreateFormFile("file", "a.txt")
		_, _ = fw.Write([]byte("hello"))
		_ = mw.Close()
		st, _, loc := send(who, "POST", path, mw.FormDataContentType(), &b)
		return st, loc
	}
	refused := func(st int, loc string) bool { return st >= 400 || strings.Contains(loc, "msg=") }
	token := func(u string) string {
		k, err := keys.Create(ctx, u, "k", []string{"collabs:read", "collabs:write", "projects:read", "projects:write", "tasks:read", "tasks:write"}, time.Hour)
		must(err)
		return k
	}
	api := func(tok, method, path string, body any) (int, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, hs.URL+path, rd)
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}
	settings := func(who string, v url.Values) (int, string) {
		st, _, loc := do(who, "POST", "/p/CL1/collab-settings", v)
		return st, loc
	}
	base := url.Values{"invite_perm": {"managers"}, "guest_invite_perm": {"managers"}, "chat_history": {"on"}, "guests_tasks": {"on"}, "guests_files": {"on"}}

	_, _, loc := do(owner, "POST", "/collabs", url.Values{"name": {"Альфа"}, "members": {dev, client}})
	if loc != "/c/CL1" { // opened with its start tool: the chat, the page without the messenger
		t.Fatalf("create: %s", loc)
	}
	if _, _, loc := do(owner, "GET", "/c/CL1", nil); loc != "/p/CL1" {
		t.Fatalf("the collab opened without the messenger: %s", loc)
	}
	c, err := ts.CollabByKey(ctx, owner, "CL1")
	must(err)
	must(ts.SetMember(tasks.WithViewer(ctx, owner, false), c, mod, "manager"))
	tkey, err := ts.CreateTask(ctx, owner, c, tasks.NewTask{Title: "Макет", ResponsibleID: client})
	must(err)

	// the settings card: for the owner and moderators; the page of a collab task leads to «Коллабы», a guest's chip
	// carries the mark
	if _, body, _ := do(owner, "GET", "/p/CL1", nil); !strings.Contains(body, "/p/CL1/collab-settings") || !strings.Contains(body, `id="log"`) || !strings.Contains(body, "/p/CL1/delete") {
		t.Error("the owner sees no settings, log or delete")
	}
	for _, u := range []string{dev, client} {
		if _, body, _ := do(u, "GET", "/p/CL1", nil); strings.Contains(body, "collab-settings") || strings.Contains(body, `id="log"`) || strings.Contains(body, "/p/CL1/delete") {
			t.Errorf("%s sees the settings, the log or delete", u)
		}
	}
	// the search in «Коллабы»
	if _, body, _ := do(client, "GET", "/collabs?q=%D0%B0%D0%BB%D1%8C%D1%84", nil); !strings.Contains(body, `<b>Альфа</b>`) || !strings.Contains(body, `value="альф"`) {
		t.Error("the search does not find the collab")
	}
	if _, body, _ := do(client, "GET", "/collabs?q=zzz", nil); strings.Contains(body, `<b>Альфа</b>`) || !strings.Contains(body, "Коллабов по такому запросу нет") {
		t.Error("the search finds what it should not")
	}
	_, body, _ := do(owner, "GET", "/t/"+tkey, nil)
	if !strings.Contains(body, `<a href="/collabs">`) {
		t.Error("the collab task's crumbs do not lead to «Коллабы»")
	}
	if !strings.Contains(body, `<span class="upick-name">Клиент Гостев <span class="pill guest">`) {
		t.Error("the guest's chip has no mark")
	}

	// who changes the settings: the owner and moderators; a member is refused, a guest and an outsider find nothing
	if st, _ := settings(dev, base); st != http.StatusForbidden {
		t.Errorf("a member changed the settings: %d", st)
	}
	for _, u := range []string{client, outsider} {
		if st, _ := settings(u, base); st != http.StatusNotFound {
			t.Errorf("%s: settings = %d, want 404", u, st)
		}
	}
	v := url.Values{"invite_perm": {"members"}, "guest_invite_perm": {"owner"}} // history off, guests make no tasks, upload nothing
	if st, loc := settings(mod, v); st != http.StatusSeeOther || strings.Contains(loc, "err") {
		t.Fatalf("a moderator's settings: %d %s", st, loc)
	}
	set, _ := ts.CollabSettings(ctx, c)
	if set != (tasks.CollabSettings{InvitePerm: "members", GuestInvitePerm: "owner", EditPerm: "managers", ChatPerm: "members", StartTool: "chat"}) {
		t.Fatalf("saved: %+v", set)
	}
	// a guest where guests make no tasks and upload nothing is not offered them (GO24DEV-211); a member still is
	offers := map[string]string{"/p/CL1": `/tasks/new?project=CL1"`, "/p/CL1/board": `class="quick"`,
		"/t/" + tkey: `action="/p/CL1/tasks"`, "/tasks/new": `value="CL1"`}
	for pg, mark := range offers {
		if _, body, _ := do(client, "GET", pg, nil); strings.Contains(body, mark) {
			t.Errorf("the guest is offered a new task on %s (%s)", pg, mark)
		}
		if _, body, _ := do(dev, "GET", pg, nil); !strings.Contains(body, mark) {
			t.Errorf("the member is not offered a new task on %s (%s)", pg, mark)
		}
	}
	if _, body, _ := do(client, "GET", "/t/"+tkey, nil); strings.Contains(body, `action="/files?task=`) {
		t.Error("the guest is offered an upload")
	}
	// «все участники» invite: the member adds an employee; a guest is the owner's to invite
	if st, _, loc := do(dev, "POST", "/p/CL1/members", url.Values{"user_id": {outsider}, "role": {"member"}}); st != http.StatusSeeOther || strings.Contains(loc, "msg") {
		t.Errorf("a member could not add an employee: %d %s", st, loc)
	}
	if st, _, _ := do(mod, "POST", "/p/CL1/guests", url.Values{"kind": {"link"}}); st != http.StatusForbidden {
		t.Errorf("a moderator made a guest link: %d", st)
	}
	if st, _, _ := do(owner, "POST", "/p/CL1/guests", url.Values{"kind": {"link"}}); st != http.StatusSeeOther {
		t.Errorf("the owner's guest link: %d", st)
	}

	// guests make no tasks, upload no files — by the pages and the API
	if st, _, loc := do(client, "POST", "/p/CL1/tasks", url.Values{"title": {"От гостя"}}); !refused(st, loc) {
		t.Errorf("a guest made a task: %d %s", st, loc)
	}
	ct := token(client)
	if st, _ := api(ct, "POST", "/api/v1/projects/CL1/tasks", map[string]any{"title": "От гостя"}); st < 400 {
		t.Errorf("a guest made a task over the API: %d", st)
	}
	for _, p := range []string{"/disk/p/CL1/upload", "/files?task=" + tkey} {
		if st, loc := upload(client, p); !refused(st, loc) {
			t.Errorf("a guest uploaded to %s: %d %s", p, st, loc)
		}
	}
	var files int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files`).Scan(&files))
	if files != 0 {
		t.Fatalf("the guest's files were kept: %d", files)
	}
	if _, body, _ := do(client, "GET", "/p/CL1", nil); strings.Contains(body, "/tasks/new?project=CL1") {
		t.Error("a guest who makes no tasks is offered «Создать»")
	}
	if d, err := srv.Drive.Open(ctx, dev, "CL1"); err != nil || !d.CanWrite { // (no storage here to upload into)
		t.Errorf("an employee may not upload: %v", err)
	}

	// the archive: read-only for everyone, hidden from the list; an outsider still finds nothing
	if st, _, _ := do(dev, "POST", "/p/CL1/archive", url.Values{"on": {"1"}}); st != http.StatusForbidden {
		t.Errorf("a member archived the collab: %d", st)
	}
	do(owner, "POST", "/p/CL1/archive", url.Values{"on": {"1"}})
	if _, body, _ := do(dev, "GET", "/collabs", nil); strings.Contains(body, "Альфа") {
		t.Error("an archived collab in the list")
	}
	if _, body, _ := do(dev, "GET", "/collabs?archived=1", nil); !strings.Contains(body, "Альфа") {
		t.Error("the archive does not list the collab")
	}
	if st, body, _ := do(client, "GET", "/t/"+tkey, nil); st != http.StatusOK || !strings.Contains(body, "flash") {
		t.Errorf("the archived collab's task: %d", st)
	}
	// …and offers no changes: no chat field, upload, subtask, stages, dragging, reminders, new meetings (GO24DEV-210)
	for _, pg := range []string{"/p/CL1", "/p/CL1/board", "/t/" + tkey, "/disk/p/CL1", "/calendar?project=CL1"} {
		for _, who := range []string{owner, client, admin} {
			_, body, _ := do(who, "GET", pg, nil)
			body = navItem.ReplaceAllString(body, "") // the menu's own items are dragged to reorder it
			for _, mark := range []string{`/comments"`, `action="/files?task=`, `action="/p/CL1/tasks"`, `/stages"`, `draggable="true"`,
				`/reminders"`, `/calendar/new`, `/tasks/new?project=CL1"`, `/templates/new?from=`, `class="quick"`} {
				if strings.Contains(body, mark) {
					t.Errorf("%s in the archive offers %s", pg, mark)
				}
			}
		}
	}
	for _, w := range []struct{ who, path string }{{client, "/t/" + tkey + "/comments"}, {owner, "/t/" + tkey + "/comments"},
		{admin, "/t/" + tkey + "/comments"}, {owner, "/p/CL1/info"}, {owner, "/p/CL1/tasks"}, {owner, "/disk/p/CL1/folders"},
		{owner, "/p/CL1/members"}, {owner, "/p/CL1/collab-settings"}} {
		st, _, loc := do(w.who, "POST", w.path, url.Values{"body": {"x"}, "name": {"x"}, "title": {"x"}, "user_id": {mod}, "role": {"member"},
			"invite_perm": {"owner"}, "guest_invite_perm": {"owner"}})
		if st != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
			t.Errorf("POST %s in the archive: %d %s", w.path, st, loc)
		}
	}
	if st, _, _ := do(newUserOutside(t, pool, srv, cookie), "POST", "/p/CL1/info", url.Values{"name": {"x"}}); st != http.StatusNotFound {
		t.Errorf("a stranger learns the archived collab exists: %d", st)
	}
	var comments int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM task_comments`).Scan(&comments))
	if comments != 0 {
		t.Fatalf("comments written into the archive: %d", comments)
	}
	if st, _ := api(token(owner), "PATCH", "/api/v1/tasks/"+tkey, map[string]any{"title": "x"}); st < 400 {
		t.Errorf("a task of the archive changed over the API: %d", st)
	}
	// restored, it works again
	do(owner, "POST", "/p/CL1/archive", url.Values{"on": {"0"}})
	if st, _, loc := do(owner, "POST", "/t/"+tkey+"/comments", url.Values{"body": {"снова"}}); st != http.StatusSeeOther || strings.Contains(loc, "msg=") {
		t.Errorf("a comment after restoring: %d %s", st, loc)
	}

	// taking a guest out: at once they find nothing
	if st, _, _ := do(dev, "POST", "/p/CL1/members/"+client+"/delete", nil); st != http.StatusForbidden {
		t.Errorf("a member took out a guest: %d", st)
	}
	if st, _, _ := do(mod, "POST", "/p/CL1/members/"+client+"/delete", nil); st != http.StatusSeeOther {
		t.Errorf("a moderator could not take out a guest: %d", st)
	}
	for _, p := range []string{"/p/CL1", "/t/" + tkey, "/disk/p/CL1"} {
		if st, _, _ := do(client, "GET", p, nil); st != http.StatusNotFound {
			t.Errorf("the removed guest: GET %s = %d", p, st)
		}
	}
	if st, _ := api(ct, "GET", "/api/v1/collabs/CL1", nil); st != http.StatusNotFound {
		t.Errorf("the removed guest over the API: %d", st)
	}

	// the API: settings, log, archive
	ot := token(owner)
	st, raw := api(ot, "GET", "/api/v1/collabs/CL1", nil)
	if st != http.StatusOK || !strings.Contains(string(raw), `"invite_perm": "members"`) {
		t.Fatalf("the collab over the API: %d %s", st, raw)
	}
	if st, _ := api(token(dev), "PATCH", "/api/v1/collabs/CL1/settings", map[string]any{"guests_tasks": true}); st != http.StatusForbidden {
		t.Errorf("a member's settings over the API: %d", st)
	}
	if st, raw := api(ot, "PATCH", "/api/v1/collabs/CL1/settings", map[string]any{"guests_tasks": true}); st != http.StatusOK ||
		!strings.Contains(string(raw), `"guests_tasks": true`) || !strings.Contains(string(raw), `"invite_perm": "members"`) {
		t.Errorf("the owner's settings over the API: %d %s", st, raw)
	}
	// «Автоудаление сообщений»: one of the messenger's lifetimes, in the form and over the API
	if st, raw := api(ot, "PATCH", "/api/v1/collabs/CL1/settings", map[string]any{"msg_ttl": 3600}); st != http.StatusOK ||
		!strings.Contains(string(raw), `"msg_ttl": 3600`) || !strings.Contains(string(raw), `"guests_tasks": true`) {
		t.Errorf("auto-delete over the API: %d %s", st, raw)
	}
	if st, _ := api(ot, "PATCH", "/api/v1/collabs/CL1/settings", map[string]any{"msg_ttl": 5}); st != http.StatusUnprocessableEntity {
		t.Errorf("a lifetime not offered over the API: %d", st)
	}
	if _, body, _ := do(owner, "GET", "/p/CL1", nil); !strings.Contains(body, `name="msg_ttl"`) || !strings.Contains(body, `value="3600" selected`) {
		t.Error("the settings card has no auto-delete")
	}
	// «Стартовый инструмент»: what the collab opens with; the feed is not a guest's, a tool not offered is refused
	if st, raw := api(ot, "PATCH", "/api/v1/collabs/CL1/settings", map[string]any{"start_tool": "drive"}); st != http.StatusOK ||
		!strings.Contains(string(raw), `"start_tool": "drive"`) || !strings.Contains(string(raw), `"msg_ttl": 3600`) {
		t.Errorf("the start tool over the API: %d %s", st, raw)
	}
	if st, _ := api(ot, "PATCH", "/api/v1/collabs/CL1/settings", map[string]any{"start_tool": "wiki"}); st != http.StatusUnprocessableEntity {
		t.Errorf("a start tool not offered: %d", st)
	}
	must(ts.SetMember(tasks.WithViewer(ctx, owner, false), c, client, "member")) // the guest back in it
	stranger := newUser("Чужой", false) // not in the collab: it does not open
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: stranger}))
	cookie[stranger] = rec.Result().Cookies()[0]
	if st, _, loc := do(stranger, "GET", "/c/CL1", nil); st != http.StatusNotFound {
		t.Errorf("a stranger opens the collab: %d %s", st, loc)
	}
	if _, _, loc := do(client, "GET", "/c/CL1", nil); loc != "/disk/p/CL1" {
		t.Errorf("a guest opens the collab: %s", loc)
	}
	if _, body, _ := do(owner, "GET", "/p/CL1", nil); !strings.Contains(body, `name="start_tool"`) || !strings.Contains(body, `value="drive" selected`) {
		t.Error("the settings card has no start tool")
	}
	if st, _ := settings(owner, url.Values{"invite_perm": {"members"}, "guest_invite_perm": {"managers"}, "start_tool": {"feed"}}); st != http.StatusSeeOther {
		t.Errorf("the start tool in the form: %d", st)
	}
	if _, _, loc := do(owner, "GET", "/c/CL1", nil); loc != "/feed?project="+c.ID {
		t.Errorf("an employee opens the collab with the feed: %s", loc)
	}
	if _, _, loc := do(client, "GET", "/c/CL1", nil); loc != "/p/CL1" {
		t.Errorf("a guest has no feed: %s", loc)
	}
	if _, body, _ := do(owner, "GET", "/p/CL1", nil); !strings.Contains(body, "стартовый инструмент — лента") {
		t.Error("the log has no start tool")
	}
	st, raw = api(ot, "GET", "/api/v1/collabs/CL1/log", nil)
	if st != http.StatusOK || !strings.Contains(string(raw), `"action": "member_removed"`) || !strings.Contains(string(raw), `"action": "archived"`) {
		t.Errorf("the log over the API: %d %s", st, raw)
	}
	if st, _ := api(token(dev), "GET", "/api/v1/collabs/CL1/log", nil); st != http.StatusForbidden {
		t.Errorf("a member reads the log over the API: %d", st)
	}
	if st, _ := api(ot, "POST", "/api/v1/collabs/CL1/archive", map[string]any{"archived": true}); st != http.StatusOK {
		t.Errorf("archive over the API: %d", st)
	}
	if st, raw := api(ot, "GET", "/api/v1/collabs?archived=1", nil); st != http.StatusOK || !strings.Contains(string(raw), `"CL1"`) {
		t.Errorf("the archive over the API: %d %s", st, raw)
	}
	if st, _ := api(ot, "POST", "/api/v1/collabs/CL1/archive", map[string]any{"archived": false}); st != http.StatusOK {
		t.Errorf("restore over the API: %d", st)
	}

	// the API: the name and description, the avatar, the invitations of guests
	if st, _ := api(token(dev), "PATCH", "/api/v1/collabs/CL1", map[string]any{"name": "Чужое"}); st != http.StatusForbidden {
		t.Errorf("a member renamed the collab over the API: %d", st)
	}
	if st, raw := api(ot, "PATCH", "/api/v1/collabs/CL1", map[string]any{"description": "Про альфу"}); st != http.StatusOK ||
		!strings.Contains(string(raw), `"name": "Альфа"`) || !strings.Contains(string(raw), `"description": "Про альфу"`) {
		t.Errorf("the description over the API: %d %s", st, raw)
	}
	if st, _ := api(ot, "PATCH", "/api/v1/collabs/CL1", map[string]any{"name": " "}); st != http.StatusUnprocessableEntity {
		t.Errorf("an empty name over the API: %d", st)
	}
	if st, _ := api(ot, "PUT", "/api/v1/collabs/CL1/photo", "not a picture"); st != http.StatusUnprocessableEntity {
		t.Errorf("a photo that is not a picture: %d", st)
	}
	if st, _ := api(token(dev), "DELETE", "/api/v1/collabs/CL1/photo", nil); st != http.StatusForbidden {
		t.Errorf("a member took the avatar off: %d", st)
	}
	if st, _ := api(token(dev), "POST", "/api/v1/collabs/CL1/invites", map[string]any{}); st != http.StatusForbidden {
		t.Errorf("a member invited a guest over the API: %d", st)
	}
	var inv struct {
		ID, Kind, URL, State, Email string
	}
	st, raw = api(ot, "POST", "/api/v1/collabs/CL1/invites", map[string]any{"kind": "email", "contact": "Гость <G@Example.org>", "days": 3})
	if st != http.StatusCreated || json.Unmarshal(raw, &inv) != nil || inv.Kind != "email" || inv.Email != "g@example.org" ||
		inv.State != "active" || !strings.Contains(inv.URL, "/join/") {
		t.Fatalf("an invitation by e-mail over the API: %d %s", st, raw)
	}
	if st, _ := api(ot, "POST", "/api/v1/collabs/CL1/invites", map[string]any{"kind": "phone", "contact": "12"}); st != http.StatusUnprocessableEntity {
		t.Errorf("a wrong phone: %d", st)
	}
	if st, raw := api(ot, "POST", "/api/v1/collabs/CL1/invites", map[string]any{}); st != http.StatusCreated || !strings.Contains(string(raw), `"kind": "link"`) {
		t.Errorf("a link over the API: %d %s", st, raw)
	}
	if st, _ := api(ot, "DELETE", "/api/v1/collabs/CL1/invites/"+inv.ID, nil); st != http.StatusNoContent {
		t.Errorf("revoke over the API: %d", st)
	}
	if st, _ := api(ot, "DELETE", "/api/v1/collabs/CL1/invites/"+inv.ID, nil); st != http.StatusNotFound {
		t.Errorf("revoked twice: %d", st)
	}
	st, raw = api(ot, "GET", "/api/v1/collabs/CL1/invites", nil)
	if st != http.StatusOK || !strings.Contains(string(raw), `"state": "revoked"`) || !strings.Contains(string(raw), `"state": "active"`) {
		t.Errorf("the invitations over the API: %d %s", st, raw)
	}
	if st, _ := api(token(dev), "GET", "/api/v1/collabs/CL1/invites", nil); st != http.StatusForbidden {
		t.Errorf("a member reads the invitations: %d", st)
	}
	// «Пригласить повторно»: a personal invitation works a week more; a link or a revoked one — not found
	var ph struct {
		ID      string
		Expires string `json:"expires_at"`
	}
	st, raw = api(ot, "POST", "/api/v1/collabs/CL1/invites", map[string]any{"kind": "phone", "contact": "+7 999 123-45-67", "days": 1})
	if st != http.StatusCreated || json.Unmarshal(raw, &ph) != nil {
		t.Fatalf("an invitation by phone: %d %s", st, raw)
	}
	var again struct {
		Expires string `json:"expires_at"`
	}
	st, raw = api(ot, "POST", "/api/v1/collabs/CL1/invites/"+ph.ID+"/resend", nil)
	if st != http.StatusOK || json.Unmarshal(raw, &again) != nil || again.Expires <= ph.Expires {
		t.Errorf("resend: %d %s (was %s)", st, raw, ph.Expires)
	}
	if st, _ := api(ot, "POST", "/api/v1/collabs/CL1/invites/"+inv.ID+"/resend", nil); st != http.StatusNotFound {
		t.Errorf("a revoked invitation resent: %d", st)
	}
	if st, _, loc := do(owner, "POST", "/p/CL1/guests/"+ph.ID+"/resend", nil); st != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Errorf("resend on the page: %d %s", st, loc)
	}
	// several guests at once: the wrong contacts are named, the rest are invited
	var before, after int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM collab_invites`).Scan(&before))
	st, _, loc = do(owner, "POST", "/p/CL1/guests", url.Values{"kind": {"email"}, "days": {"7"},
		"contact": {"a1@example.org, не-адрес\nБорис <b2@example.org>; a1@example.org"}})
	must(pool.QueryRow(ctx, `SELECT count(*) FROM collab_invites`).Scan(&after))
	if msg, _ := url.QueryUnescape(loc); st != http.StatusSeeOther || after-before != 2 || !strings.Contains(msg, "не-адрес") {
		t.Errorf("several guests: %d, %d made, %s", st, after-before, msg)
	}
	var bname string
	must(pool.QueryRow(ctx, `SELECT name FROM collab_invites WHERE email = 'b2@example.org'`).Scan(&bname))
	if bname != "Борис" {
		t.Errorf("the name from the address: %q", bname)
	}
	if st, _, loc := do(owner, "POST", "/p/CL1/guests", url.Values{"kind": {"phone"}, "contact": {"12"}}); st != http.StatusSeeOther || !strings.Contains(loc, "msg=") {
		t.Errorf("one wrong phone: %d %s", st, loc)
	}

	// the company's administrator: sees and manages a collab they are not in
	if _, body, _ := do(admin, "GET", "/collabs", nil); !strings.Contains(body, "Альфа") {
		t.Error("the administrator does not see the collab")
	}
	if st, body, _ := do(admin, "GET", "/p/CL1", nil); st != http.StatusOK || !strings.Contains(body, "collab-settings") || !strings.Contains(body, "/p/CL1/delete") {
		t.Errorf("the administrator's collab page: %d", st)
	}
	if st, loc := settings(admin, base); st != http.StatusSeeOther || strings.Contains(loc, "err") {
		t.Errorf("the administrator's settings: %d %s", st, loc)
	}

	// deleting: the owner or the administrator, with the name
	if st, _, _ := do(mod, "POST", "/p/CL1/delete", url.Values{"confirm": {"Альфа"}}); st != http.StatusForbidden {
		t.Errorf("a moderator deleted the collab: %d", st)
	}
	if st, _, loc := do(owner, "POST", "/p/CL1/delete", url.Values{"confirm": {"Бета"}}); st != http.StatusSeeOther || !strings.Contains(loc, "/p/CL1?msg=") {
		t.Errorf("deleted with a wrong name: %d %s", st, loc)
	}
	if st, _, loc := do(owner, "POST", "/p/CL1/delete", url.Values{"confirm": {"Альфа"}}); st != http.StatusSeeOther || !strings.HasPrefix(loc, "/collabs") {
		t.Fatalf("delete: %d %s", st, loc)
	}
	for _, u := range []string{owner, dev, admin} {
		if st, _, _ := do(u, "GET", "/p/CL1", nil); st != http.StatusNotFound {
			t.Errorf("the deleted collab for %s: %d", u, st)
		}
	}
	// the administrator deletes over the page a collab of someone else
	_, err = ts.CreateCollab(ctx, owner, "Гамма", "", nil)
	must(err)
	if st, _, loc := do(admin, "POST", "/p/CL2/delete", url.Values{"confirm": {"Гамма"}}); st != http.StatusSeeOther || !strings.HasPrefix(loc, "/collabs") {
		t.Errorf("the administrator's delete: %d %s", st, loc)
	}
}

// newUserOutside signs in one more employee, outside every collab.
func newUserOutside(t *testing.T, pool *pgxpool.Pool, srv *Server, cookie map[string]*http.Cookie) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(), `INSERT INTO users (oidc_sub, name, email) VALUES ('stranger', 'Чужак', 'stranger@t') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if err := srv.Sessions.Set(rec, auth.Session{UserID: id}); err != nil {
		t.Fatal(err)
	}
	cookie[id] = rec.Result().Cookies()[0]
	return id
}
