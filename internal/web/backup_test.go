package web

import (
	"context"
	"encoding/json"
	"errors"
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
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/backup"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestBackupHTTP: «Резервное копирование» — administrators only; the settings are saved (the password is never
// shown back), «Сделать сейчас» queues one copy (a second is refused while it waits), REST shows the same and
// queues too; both are written in «Журнал событий».
func TestBackupHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('boss', 'Борис', 'boss@go24.test', '{go24-admins}') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Audit: audit.NewStore(pool),
		Backup: backup.NewStore(pool, strings.Repeat("s", 32)), BackupHostDir: "/srv/go24-backups", AdminGroups: []string{"go24-admins"},
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Admin: true}), anna: cookie(auth.Session{UserID: anna})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var last *http.Response
	send := func(who, method, path, ctype, body string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		if c := as[who]; c != nil {
			req.AddCookie(c)
		} else {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("Accept-Language", "ru")
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		last = res
		return string(raw)
	}
	post := func(who, path string, v url.Values) string {
		t.Helper()
		send(who, "POST", path, "application/x-www-form-urlencoded", v.Encode(), 303)
		u, err := url.Parse(last.Header.Get("Location"))
		must(err)
		return u.Query().Get("msg")
	}

	send(anna, "GET", "/settings/backup", "", "", 403)
	send(anna, "POST", "/settings/backup/run", "", "", 403)
	page := send(boss, "GET", "/settings/backup", "", "", 200)
	for _, w := range []string{"Резервное копирование", "/srv/go24-backups", "Копий ещё не было", `value="go24" checked`, `value="config" checked`, "Пароль не задан"} {
		if !strings.Contains(page, w) {
			t.Errorf("page has no %q", w)
		}
	}
	if page := send(boss, "GET", "/settings", "", "", 200); !strings.Contains(page, `href="/settings/backup"`) {
		t.Error("no «Резервное копирование» in «Настройки»")
	}
	if m := post(boss, "/settings/backup", url.Values{"dir": {"../etc"}, "parts": {"go24"}}); !strings.HasPrefix(m, "Папка для копий должна быть") {
		t.Errorf("bad dir: %q", m)
	}
	if m := post(boss, "/settings/backup", url.Values{"dir": {"ночные"}, "parts": {"go24", "synapse", "config"}, "password": {"пароль копий"}}); m != "Сохранено" {
		t.Errorf("saved: %q", m)
	}
	if m := post(boss, "/settings/backup", url.Values{"parts": {"go24"}, "keep": {"много"}}); m != "Сколько копий хранить — число от 0 до 1000" {
		t.Errorf("bad keep: %q", m)
	}
	page = send(boss, "GET", "/settings/backup", "", "", 200)
	if !strings.Contains(page, "только кнопкой «Сделать сейчас»") || !strings.Contains(page, `name="keep" min="0" max="1000" value="7"`) {
		t.Errorf("default schedule:\n%s", page)
	}
	post(boss, "/settings/backup", url.Values{"dir": {"ночные"}, "parts": {"go24", "synapse", "config"}, "schedule": {"weekly"}, "weekday": {"3"}, "at": {"02:15"}, "keep": {"4"}})
	page = send(boss, "GET", "/settings/backup", "", "", 200)
	if !strings.Contains(page, "Следующая копия по расписанию:") || !strings.Contains(page, `<option value="weekly" selected>`) ||
		!strings.Contains(page, `<option value="3" selected>`) || !strings.Contains(page, `value="02:15"`) || !strings.Contains(page, `value="4"`) {
		t.Errorf("schedule page:\n%s", page)
	}
	if !strings.Contains(page, `value="synapse" checked`) || strings.Contains(page, `value="authentik" checked`) || !strings.Contains(page, "Пароль задан") ||
		!strings.Contains(page, `value="ночные"`) || strings.Contains(page, "пароль копий") {
		t.Errorf("saved page:\n%s", page)
	}

	// an outside place: kept with its password sealed, «Проверить подключение» through rclone-rcd
	if m := post(boss, "/settings/backup/check", nil); m != "Внешнее место не задано" {
		t.Errorf("check without a place: %q", m)
	}
	if m := post(boss, "/settings/backup", url.Values{"parts": {"go24"}, "t_type": {"sftp"}}); !strings.HasPrefix(m, "Заполните данные внешнего места") {
		t.Errorf("sftp without a server: %q", m)
	}
	post(boss, "/settings/backup", url.Values{"dir": {"ночные"}, "parts": {"go24", "synapse", "config"}, "schedule": {"weekly"}, "weekday": {"3"}, "at": {"02:15"}, "keep": {"4"},
		"t_type": {"sftp"}, "t_host": {"nas.local"}, "t_user": {"bk"}, "t_password": {"sftp-pass"}, "t_path": {"/go24/"}})
	page = send(boss, "GET", "/settings/backup", "", "", 200)
	if !strings.Contains(page, `<option value="sftp" selected>`) || !strings.Contains(page, `value="nas.local"`) || !strings.Contains(page, `value="go24"`) ||
		strings.Contains(page, "sftp-pass") || !strings.Contains(page, "Проверить подключение") {
		t.Errorf("target page:\n%s", page)
	}
	stub := &rcStub{}
	srv.BackupRC = stub
	if m := post(boss, "/settings/backup/check", nil); m != "Подключение работает: папка для копий на месте" ||
		strings.Join(stub.calls, "|") != "create sftp nas.local bk sftp-pass|mkdir go24-backup:go24" {
		t.Errorf("check: %q, %q", m, stub.calls)
	}
	stub.fail = true
	if m := post(boss, "/settings/backup/check", nil); m != "Не удалось подключиться: connection refused" {
		t.Errorf("check failing: %q", m)
	}

	if m := post(boss, "/settings/backup/run", nil); !strings.HasPrefix(m, "Копия поставлена в очередь") {
		t.Errorf("run: %q", m)
	}
	if m := post(boss, "/settings/backup/run", nil); m != "Копия уже делается или ждёт в очереди" {
		t.Errorf("second run: %q", m)
	}
	page = send(boss, "GET", "/settings/backup", "", "", 200)
	if !strings.Contains(page, "В очереди") || !strings.Contains(page, "location.reload") {
		t.Errorf("queued page:\n%s", page)
	}

	tokBoss, err := srv.Keys.Create(ctx, boss, "backup", []string{"settings:read", "settings:write"}, 0)
	must(err)
	tokAnna, err := srv.Keys.Create(ctx, anna, "backup", []string{"settings:read", "settings:write"}, 0)
	must(err)
	send(tokAnna, "GET", "/api/v1/settings/backup", "", "", 403)
	var got struct {
		Dir      string
		Parts    []string
		Password bool
		Folder   string
		Schedule string
		Keep     int
		Next     *time.Time
		Runs     []backup.Run
	}
	raw := send(tokBoss, "GET", "/api/v1/settings/backup", "", "", 200)
	must(json.Unmarshal([]byte(raw), &got))
	if got.Dir != "ночные" || len(got.Parts) != 3 || !got.Password || got.Folder != "/srv/go24-backups" || len(got.Runs) != 1 ||
		got.Schedule != "weekly" || got.Keep != 4 || got.Next == nil || got.Next.Weekday() != time.Wednesday ||
		got.Runs[0].Status != "queued" || got.Runs[0].By != "Борис" || strings.Contains(raw, "secret") {
		t.Errorf("api = %s", raw)
	}
	send(tokBoss, "POST", "/api/v1/settings/backup/run", "", "", 422) // one is waiting
	_, err = pool.Exec(ctx, `UPDATE backup_runs SET status = 'done'`)
	must(err)
	send(tokBoss, "POST", "/api/v1/settings/backup/run", "", "", 202)

	// restoring: from a copy kept, its name typed again, with the password for the secrets of the configuration
	var copyID int64
	_, err = pool.Exec(ctx, `UPDATE backup_runs SET status = 'done' WHERE status <> 'done'`)
	must(err)
	must(pool.QueryRow(ctx, `INSERT INTO backup_runs (status, name, parts) VALUES ('done', 'ночные/go24-20261026-010000', '{go24,media,config,secrets}')
		RETURNING id`).Scan(&copyID))
	restore := "/settings/backup/" + strconv.FormatInt(copyID, 10) + "/restore"
	if page := send(boss, "GET", "/settings/backup", "", "", 200); !strings.Contains(page, `href="`+restore+`"`) {
		t.Error("no «Восстановить…» in the journal")
	}
	send(anna, "GET", restore, "", "", 403)
	send(anna, "POST", restore, "", "", 403)
	page = send(boss, "GET", restore, "", "", 200)
	for _, w := range []string{"Восстановление из копии", "go24-20261026-010000", `value="go24" checked`, `value="config" checked`, `name="password"`,
		`name="safety" value="1" checked`, "/srv/go24-backups/ночные/go24-20261026-010000/media/"} {
		if !strings.Contains(page, w) {
			t.Errorf("restore page has no %q", w)
		}
	}
	if strings.Contains(page, `value="media"`) || strings.Contains(page, `value="authentik"`) {
		t.Error("parts the copy has not or cannot be restored offered")
	}
	if m := post(boss, restore, url.Values{"parts": {"go24"}, "confirm": {"go24-20261026"}}); m != "Имя копии введено неверно — восстановление не запущено" {
		t.Errorf("wrong confirm: %q", m)
	}
	if m := post(boss, restore, url.Values{"parts": {"config"}, "confirm": {"go24-20261026-010000"}}); !strings.HasPrefix(m, "Введите пароль копии") {
		t.Errorf("no password: %q", m)
	}
	send(boss, "POST", "/settings/backup/999999/restore", "application/x-www-form-urlencoded", "confirm=x", 404)
	if m := post(boss, restore, url.Values{"parts": {"go24", "config", "media"}, "password": {"пароль копий"}, "safety": {"1"},
		"confirm": {" go24-20261026-010000 "}}); !strings.HasPrefix(m, "Восстановление поставлено в очередь") {
		t.Errorf("restore: %q", m)
	}
	var kind, name, secret string
	var parts []string
	var safety bool
	must(pool.QueryRow(ctx, `SELECT kind, name, parts, secret, safety FROM backup_runs ORDER BY id DESC LIMIT 1`).Scan(&kind, &name, &parts, &secret, &safety))
	if kind != "restore" || name != "ночные/go24-20261026-010000" || strings.Join(parts, ",") != "go24,config" || !safety || secret == "" || strings.Contains(secret, "пароль") {
		t.Errorf("restore queued: %s %s %v %v %q", kind, name, parts, safety, secret)
	}
	if m := post(boss, restore, url.Values{"parts": {"go24"}, "confirm": {"go24-20261026-010000"}}); m != "Копия уже делается или ждёт в очереди" {
		t.Errorf("second restore: %q", m)
	}
	if page := send(boss, "GET", "/settings/backup", "", "", 200); !strings.Contains(page, "Восстановление") || strings.Contains(page, `href="`+restore+`"`) {
		t.Error("the journal shows no restoring, or offers another while it is queued")
	}

	list, err := srv.Audit.List(ctx, audit.Filter{})
	must(err)
	kinds := map[string]int{}
	for _, e := range list {
		kinds[e.Kind]++
	}
	if kinds["backup_saved"] != 3 || kinds["backup_started"] != 2 || kinds["backup_restore"] != 1 {
		t.Errorf("events = %v", kinds)
	}
}

// rcStub is rclone-rcd of the check.
type rcStub struct {
	calls []string
	fail  bool
}

func (r *rcStub) CreateRemote(_ context.Context, _, typ string, p map[string]string) error {
	r.calls = append(r.calls, "create "+typ+" "+p["host"]+" "+p["user"]+" "+p["pass"])
	if r.fail {
		return errors.New("connection refused")
	}
	return nil
}
func (r *rcStub) DeleteRemote(context.Context, string) error { return nil }
func (r *rcStub) Mkdir(_ context.Context, remote, dir string) error {
	r.calls = append(r.calls, "mkdir "+remote+":"+dir)
	return nil
}
func (r *rcStub) CopyDir(context.Context, string, string) error { return nil }
func (r *rcStub) Purge(context.Context, string, string) error   { return nil }
