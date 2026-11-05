package web

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/audit"
	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sysinfo"
	"go24/internal/tasks"
	"go24/internal/update"
)

// TestUpdateHTTP: «Обновление» — only for administrators; «Проверить» reads the latest release, «Обновить» fetches
// the signed archive and leaves the job for go24-updater (a foreign signature — refused), the job's status is shown
// live; a development build has no buttons; the end of a job is told to the administrators once.
func TestUpdateHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ('boss', 'Борис', 'boss@go24.test', true) RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))

	// a release 0.5.8 on a GitHub of the test, signed by the key of the test
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	var tgz bytes.Buffer
	gz := gzip.NewWriter(&tgz)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "VERSION", Mode: 0o644, Size: 6, Typeflag: tar.TypeReg})
	tw.Write([]byte("0.5.8\n"))
	tw.Close()
	gz.Close()
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, tgz.Bytes()))
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/" + update.Repo + "/releases/latest":
			fmt.Fprintf(w, `{"tag_name":"v0.5.8","body":"- новое в 0.5.8","html_url":"https://github.com/r","published_at":"2026-11-04T10:00:00Z",
				"assets":[{"name":"go24-0.5.8.tar.gz","browser_download_url":"%[1]s/a"},{"name":"go24-0.5.8.tar.gz.sig","browser_download_url":"%[1]s/s"}]}`, gh.URL)
		case "/a":
			w.Write(tgz.Bytes())
		case "/s":
			io.WriteString(w, sig)
		}
	}))
	defer gh.Close()

	old := sysinfo.Version
	sysinfo.Version = "0.5.7"
	defer func() { sysinfo.Version = old }()
	dir := t.TempDir()
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Keys: apikeys.NewStore(pool), I18n: bundle, Audit: audit.NewStore(pool),
		Update:   &update.Client{Dir: dir, Current: "0.5.7", Key: base64.StdEncoding.EncodeToString(pub), API: gh.URL},
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(sess auth.Session) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, sess))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(auth.Session{UserID: boss, Name: "Борис", Admin: true}), anna: cookie(auth.Session{UserID: anna})}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who, method, path string, want int) (string, *http.Response) {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, nil)
		if c := as[who]; c != nil {
			req.AddCookie(c)
		} else {
			req.Header.Set("Authorization", "Bearer "+who)
		}
		req.Header.Set("Accept-Language", "ru")
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res
	}
	post := func(path string) (msg, errText string) {
		t.Helper()
		_, res := send(boss, "POST", path, 303)
		u, err := url.Parse(res.Header.Get("Location"))
		must(err)
		return u.Query().Get("msg"), u.Query().Get("err")
	}

	send(anna, "GET", "/settings/update", 403)
	send(anna, "POST", "/settings/update/install", 403)
	send(anna, "GET", "/settings/update/status", 403)
	page, _ := send(boss, "GET", "/settings/update", 200)
	for _, w := range []string{"Обновление", "0.5.7", "Выпуски ещё не проверялись", "Проверить"} {
		if !strings.Contains(page, w) {
			t.Errorf("page has no %q", w)
		}
	}
	if page, _ := send(boss, "GET", "/settings", 200); !strings.Contains(page, `href="/settings/update"`) {
		t.Error("no «Обновление» in «Настройки»")
	}
	if _, e := post("/settings/update/install"); !strings.Contains(e, "Нет выпуска") {
		t.Errorf("install before a check: %q", e)
	}
	if m, e := post("/settings/update/check"); m != "Выпуски проверены" || e != "" {
		t.Fatalf("check: %q %q", m, e)
	}
	page, _ = send(boss, "GET", "/settings/update", 200)
	for _, w := range []string{"Доступна новая версия", "Обновить до 0.5.8", "- новое в 0.5.8", "https://github.com/r"} {
		if !strings.Contains(page, w) {
			t.Errorf("page has no %q", w)
		}
	}

	// a foreign signature: refused, nothing asked
	good := sig
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	sig = base64.StdEncoding.EncodeToString(ed25519.Sign(other, tgz.Bytes()))
	if _, e := post("/settings/update/install"); !strings.Contains(e, "Подпись выпуска неверна") {
		t.Errorf("foreign signature: %q", e)
	}
	if _, err := os.Stat(filepath.Join(dir, update.RequestFile)); err == nil {
		t.Fatal("a job asked with a foreign signature")
	}
	sig = good
	if m, e := post("/settings/update/install"); e != "" || !strings.Contains(m, "Обновление началось") {
		t.Fatalf("install: %q %q", m, e)
	}
	var req update.Request
	b, err := os.ReadFile(filepath.Join(dir, update.RequestFile))
	must(err)
	must(json.Unmarshal(b, &req))
	if req.Kind != "update" || req.Version != "0.5.8" || req.By != "Борис" {
		t.Errorf("request %+v", req)
	}
	if _, e := post("/settings/update/install"); !strings.Contains(e, "уже идёт") {
		t.Errorf("second install: %q", e)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM audit_log WHERE kind = 'update_started' AND target = '0.5.8'`).Scan(&n))
	if n != 1 {
		t.Errorf("audit: %d", n)
	}

	// go24-updater took it and works: the status, live; then it is over and told to the administrators once
	os.Remove(filepath.Join(dir, update.RequestFile))
	st := update.Status{ID: "j1", Kind: "update", State: update.Installing, From: "0.5.7", To: "0.5.8", Step: "backup",
		Log: []update.LogLine{{At: time.Now(), Text: "step: backup"}}, StartedAt: time.Now()}
	b, _ = json.Marshal(st)
	must(os.WriteFile(filepath.Join(dir, update.StatusFile), b, 0o644))
	raw, _ := send(boss, "GET", "/settings/update/status", 200)
	var live struct {
		Busy   bool          `json:"busy"`
		Newer  bool          `json:"newer"`
		Status update.Status `json:"status"`
	}
	must(json.Unmarshal([]byte(raw), &live))
	if !live.Busy || !live.Newer || live.Status.Step != "backup" {
		t.Errorf("live %+v", live)
	}
	page, _ = send(boss, "GET", "/settings/update", 200)
	if !strings.Contains(page, "копия базы и файлов") || !strings.Contains(page, "step: backup") || !strings.Contains(page, "disabled") {
		t.Error("the job under way is not on the page")
	}
	st.State, st.Step = update.RolledBack, "rollback"
	st.Error = "go24 did not come up"
	b, _ = json.Marshal(st)
	must(os.WriteFile(filepath.Join(dir, update.StatusFile), b, 0o644))
	actx, stop := context.WithCancel(ctx)
	go srv.AnnounceUpdates(actx)
	for i := 0; ; i++ {
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind = 'update_rolled_back' AND user_id = $1 AND body LIKE '0.5.8 · go24 did not%'`, boss).Scan(&n))
		if n == 1 {
			break
		}
		if i > 100 {
			t.Fatal("the administrators are not told")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE kind LIKE 'update_%' AND user_id = $1`, anna).Scan(&n))
	if n != 0 {
		t.Error("an employee is told of the update")
	}
	if _, ok, _ := srv.Update.Announce(); ok {
		t.Error("told twice")
	}

	// REST: administrators only
	annaKey, err := srv.Keys.Create(ctx, anna, "n8n", apikeys.AllScopes, 0)
	must(err)
	send(annaKey, "GET", "/api/v1/settings/update", 403)
	bossKey, err := srv.Keys.Create(ctx, boss, "admin", apikeys.AllScopes, 0)
	must(err)
	raw, _ = send(bossKey, "GET", "/api/v1/settings/update", 200)
	var rest struct {
		Version string        `json:"version"`
		Status  update.Status `json:"status"`
	}
	must(json.Unmarshal([]byte(raw), &rest))
	if rest.Version != "0.5.7" || rest.Status.State != update.RolledBack {
		t.Errorf("REST: %.300s", raw)
	}

	// a development build: no buttons, the actions refused
	srv.Update.Off = true
	page, _ = send(boss, "GET", "/settings/update", 200)
	if !strings.Contains(page, "сборка для разработки") || strings.Contains(page, "Обновить до") {
		t.Error("a development build shows the update button")
	}
	if _, e := post("/settings/update/check"); !strings.Contains(e, "через git") {
		t.Errorf("dev check: %q", e)
	}
}
