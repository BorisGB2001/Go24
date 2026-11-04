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
	"go24/internal/calendar"
	"go24/internal/company"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/matrixsync"
	"go24/internal/notify"
	"go24/internal/tasks"
)

// TestRestPortalIMChats (GO24DEV-909): im.notify.read reads the notification with the older ones (ONLY_CURRENT — it
// alone), im.notify.read.all — all; im.notify.confirm answers an invitation to a project with Y / N as the bell's
// buttons do (the key needs projects:write too) and a meeting's with «tentative»; the chat methods want Matrix and
// CHAT_ID.
func TestRestPortalIMChats(t *testing.T) {
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
	var boss, anna string
	var annaNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Борис', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id, num`).Scan(&anna, &annaNum))
	bundle, err := i18n.Load("")
	must(err)
	defer bundle.SetFilter(nil)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Keys: apikeys.NewStore(pool), Company: company.NewStore(pool), Calendar: calendar.NewStore(pool),
		Notify: notify.NewStore(pool), I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	full, err := srv.Keys.Create(ctx, anna, "k", []string{"im:read", "im:write", "projects:write", "calendar:write"}, time.Hour)
	must(err)
	imOnly, err := srv.Keys.Create(ctx, anna, "k", []string{"im:read", "im:write"}, time.Hour)
	must(err)
	call := func(key, method, body string, want int) map[string]any {
		t.Helper()
		req, _ := http.NewRequest("POST", hs.URL+"/rest/"+strconv.FormatInt(annaNum, 10)+"/"+key+"/"+method, strings.NewReader(body))
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
	unread := func() int {
		n, err := srv.Notify.Unread(ctx, anna)
		must(err)
		return n
	}
	var ids []string
	for _, body := range []string{"один", "два", "три"} {
		var id int64
		must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, 'app', $2) RETURNING id`, anna, body).Scan(&id))
		ids = append(ids, strconv.FormatInt(id, 10))
	}

	// reading: it alone, then it with the older ones, then all
	if r := call(full, "im.notify.read", `{"ID": `+ids[1]+`, "ONLY_CURRENT": "Y"}`, 200); r["result"] != true || unread() != 2 {
		t.Fatalf("only current: %v, unread %d", r, unread())
	}
	call(full, "im.notify.read", `{"ID": `+ids[1]+`}`, 200)
	if unread() != 1 {
		t.Fatalf("with the older ones: unread %d", unread())
	}
	if e := call(full, "im.notify.read", `{}`, 400); e["error"] != "ID_EMPTY" {
		t.Errorf("no ID: %v", e)
	}
	call(full, "im.notify.read.all", `{}`, 200)
	if unread() != 0 {
		t.Fatalf("all: unread %d", unread())
	}

	// confirming: an invitation to a project, Y — she is in it; the key without projects:write may not
	_, err = ts.CreateProject(ctx, boss, "CG", "Группа", "")
	must(err)
	p, err := ts.ProjectByKey(ctx, boss, "CG")
	must(err)
	must(ts.InviteMember(ctx, p, boss, anna, "member"))
	var inv string
	must(pool.QueryRow(ctx, `SELECT id::text FROM notifications WHERE user_id = $1 AND kind = 'project_invite'`, anna).Scan(&inv))
	role := func() string {
		var r string
		_ = pool.QueryRow(ctx, `SELECT role FROM project_members WHERE project_id = $1 AND user_id = $2`, p.ID, anna).Scan(&r)
		return r
	}
	call(imOnly, "im.notify.confirm", `{"ID": `+inv+`, "NOTIFY_VALUE": "Y"}`, 401)
	if role() != "" {
		t.Fatal("confirmed without projects:write")
	}
	if r := call(full, "im.notify.confirm", `{"ID": `+inv+`, "NOTIFY_VALUE": "Y"}`, 200); r["result"] != true || role() != "member" {
		t.Fatalf("confirmed: %v, role %q", r, role())
	}
	call(full, "im.notify.confirm", `{"ID": `+inv+`, "NOTIFY_VALUE": "N"}`, 400) // answered already
	if e := call(full, "im.notify.confirm", `{"NOTIFY_VALUE": "Y"}`, 400); e["error"] != "ID_EMPTY" {
		t.Errorf("no ID: %v", e)
	}
	call(full, "im.notify.confirm", `{"ID": 999999, "NOTIFY_VALUE": "Y"}`, 400)

	// a meeting: «tentative», an answer of go24's own
	var ev, nEv string
	start := time.Now().Add(24 * time.Hour)
	must(pool.QueryRow(ctx, `INSERT INTO events (owner_id, title, starts_at, ends_at) VALUES ($1, 'Планёрка', $2, $3) RETURNING id`,
		boss, start, start.Add(time.Hour)).Scan(&ev))
	_, err = pool.Exec(ctx, `INSERT INTO event_attendees (event_id, user_id, status) VALUES ($1, $2, 'invited')`, ev, anna)
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO notifications (user_id, actor_id, kind, event_id, body) VALUES ($1, $2, 'event', $3, 'Планёрка')
		RETURNING id::text`, anna, boss, ev).Scan(&nEv))
	call(full, "im.notify.confirm", `{"ID": "`+nEv+`", "NOTIFY_VALUE": "tentative"}`, 200)
	var status string
	must(pool.QueryRow(ctx, `SELECT status FROM event_attendees WHERE event_id = $1 AND user_id = $2`, ev, anna).Scan(&status))
	if status != "tentative" {
		t.Fatalf("meeting: %q", status)
	}

	// the chats: without Matrix the methods are not there; with it, CHAT_ID is needed and must be known
	if e := call(full, "im.chat.add", `{"TITLE": "x", "USERS": [1]}`, 404); e["error"] != "ERROR_METHOD_NOT_FOUND" {
		t.Errorf("no Matrix: %v", e)
	}
	srv.Matrix = &matrixsync.Service{DB: pool}
	for _, m := range []string{"im.chat.user.add", "im.chat.user.delete", "im.chat.leave", "im.chat.updateTitle", "im.chat.setOwner", "im.chat.updateAvatar"} {
		if e := call(full, m, `{}`, 400); e["error"] != "CHAT_ID_EMPTY" {
			t.Errorf("%s without CHAT_ID: %v", m, e)
		}
	}
	if e := call(full, "im.chat.leave", `{"CHAT_ID": 77}`, 400); e["error"] != "ERROR_NOT_FOUND" {
		t.Errorf("chat 77: %v", e)
	}
	if e := call(imOnly, "im.chat.updateTitle", `{"CHAT_ID": "chat77"}`, 400); e["error"] != "ERROR_NOT_FOUND" {
		t.Errorf("chat77 by its dialog: %v", e)
	}
}
