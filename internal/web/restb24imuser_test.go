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

// TestRestPortalIMUsers (GO24DEV-915): im.user.get and im.user.list.get give employees in the messenger's fields
// (status, absence, departments), im.user.status.set/get keep the status of the card, im.search.user.list finds by
// name or position (FIND of three characters at least), im.counters.get counts the unread notifications; the chat
// marks want Matrix and a known chat.
func TestRestPortalIMUsers(t *testing.T) {
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
	var annaNum, bossNum int64
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position) VALUES ('boss', 'Борис Главный', 'boss@t', 'Директор')
		RETURNING id, num`).Scan(&boss, &bossNum))
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
	full, err := srv.Keys.Create(ctx, anna, "k", []string{"im:read", "im:write"}, time.Hour)
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
	bossID, annaID := strconv.FormatInt(bossNum, 10), strconv.FormatInt(annaNum, 10)
	must2 := func(_ any, err error) { t.Helper(); must(err) }
	must2(pool.Exec(ctx, `UPDATE users SET presence = 'dnd', seen_at = now() WHERE id = $1`, boss))
	must2(pool.Exec(ctx, `INSERT INTO absences (user_id, kind, starts_on, ends_on, created_by)
		VALUES ($1, 'vacation', current_date - 1, current_date + 3, $1)`, boss))
	var dept int64
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Отдел') RETURNING num`).Scan(&dept))
	must2(pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) SELECT $1, id FROM departments WHERE num = $2`, boss, dept))

	me := call(full, "im.user.get", `{}`, 200)["result"].(map[string]any)
	if me["id"] != float64(annaNum) || me["status"] != "online" || me["absent"] != false {
		t.Errorf("im.user.get of the caller: %v", me)
	}
	b := call(full, "im.user.get", `{"ID": `+bossID+`}`, 200)["result"].(map[string]any)
	if b["status"] != "dnd" || b["work_position"] != "Директор" || b["absent"] == false || b["last_activity_date"] == false {
		t.Errorf("im.user.get of the boss: %v", b)
	}
	if d, _ := b["departments"].([]any); len(d) != 1 || d[0] != float64(dept) {
		t.Errorf("departments: %v", b["departments"])
	}
	list := call(full, "im.user.list.get", `{"ID": [`+bossID+`, `+annaID+`, 99999]}`, 200)["result"].(map[string]any)
	if list[bossID] == nil || list[annaID] == nil || list["99999"] != nil || len(list) != 3 {
		t.Errorf("im.user.list.get: %v", list)
	}

	if call(full, "im.user.status.get", `{}`, 200)["result"] != "online" {
		t.Error("status: not online")
	}
	call(full, "im.user.status.set", `{"STATUS": "away"}`, 200)
	if call(full, "im.user.status.get", `{}`, 200)["result"] != "away" || srv.myPresence(ctx, anna) != "away" {
		t.Error("status: not away")
	}
	call(full, "im.user.status.set", `{"STATUS": "online"}`, 200)
	if srv.myPresence(ctx, anna) != "" {
		t.Error("status: not back online")
	}
	if e := call(full, "im.user.status.set", `{"STATUS": "busy"}`, 400); e["error"] != "STATUS_ERROR" {
		t.Errorf("bad status: %v", e)
	}

	found := call(full, "im.search.user.list", `{"FIND": "директ"}`, 200)["result"].(map[string]any)
	if len(found) != 1 || found[bossID] == nil {
		t.Errorf("search by position: %v", found)
	}
	if e := call(full, "im.search.user.list", `{"FIND": "Бо"}`, 400); e["error"] != "FIND_SHORT" {
		t.Errorf("short FIND: %v", e)
	}

	must2(pool.Exec(ctx, `INSERT INTO notifications (user_id, kind, body) VALUES ($1, 'app', 'a'), ($1, 'app', 'b')`, anna))
	cnt := call(full, "im.counters.get", `{}`, 200)["result"].(map[string]any)
	if ty := cnt["TYPE"].(map[string]any); ty["NOTIFY"] != float64(2) || ty["ALL"] != float64(2) || ty["CHAT"] != float64(0) {
		t.Errorf("im.counters.get: %v", cnt)
	}

	for _, m := range []string{"im.chat.mute", "im.recent.pin", "im.message.like", "im.dialog.writing", "im.search.chat.list"} {
		if e := call(full, m, `{}`, 404); e["error"] != "ERROR_METHOD_NOT_FOUND" {
			t.Errorf("%s without Matrix: %v", m, e)
		}
	}
	srv.Matrix = &matrixsync.Service{DB: pool}
	if e := call(full, "im.chat.mute", `{}`, 400); e["error"] != "CHAT_ID_EMPTY" {
		t.Errorf("im.chat.mute without CHAT_ID: %v", e)
	}
	for _, m := range []string{"im.recent.pin", "im.recent.unread", "im.recent.hide", "im.dialog.writing"} {
		if e := call(full, m, `{}`, 400); e["error"] != "DIALOG_ID_EMPTY" {
			t.Errorf("%s without DIALOG_ID: %v", m, e)
		}
		if e := call(full, m, `{"DIALOG_ID": "chat77"}`, 400); e["error"] != "ERROR_NOT_FOUND" {
			t.Errorf("%s of chat77: %v", m, e)
		}
	}
	if e := call(full, "im.message.like", `{"MESSAGE_ID": 77}`, 400); e["error"] != "ERROR_NOT_FOUND" {
		t.Errorf("like of message 77: %v", e)
	}
}
