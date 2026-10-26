package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskCardSharingHTTP: «Общий доступ» of the file's card — the owner of «Мой диск», the folders that open the
// file and its own shares and link; the owner opens and closes the file and makes its link right there and comes
// back to the card, one it is shared with only sees it; on the company's drive — every employee, or the rights of
// the nearest folder with its own.
func TestDiskCardSharingHTTP(t *testing.T) {
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
	var boss, eve, ivan string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	my, _ := srv.Drive.Open(ctx, eve, "my")
	work, err := srv.Drive.CreateFolder(ctx, eve, my, "", "Проекты")
	must(err)
	must(srv.Drive.SetShare(ctx, eve, my, work, "", "user", ivan, "edit"))
	plan, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "план.txt", ContentType: "text/plain",
		UploadedBy: eve, Drive: "user", DriveOwner: eve, FolderID: work})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin, AdminMode: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), eve: cookie(eve, false), ivan: cookie(ivan, false)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) (string, string) {
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
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header.Get("Location")
	}
	has := func(page string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(page, w) {
				t.Fatalf("no %q on the card", w)
			}
		}
	}
	about := "/files/" + plan + "/about"
	back := about + "#sharing"

	card, _ := do(eve, "GET", about, nil, 200)
	has(card, `id="sharing"`, "Владелец — Ева", "Через папку «Проекты»", "Иван", `action="/disk/my/share"`, `action="/disk/my/link"`)
	// the owner opens the file to Босс from the card and comes back to it
	_, loc := do(eve, "POST", "/disk/my/share", url.Values{"file": {plan}, "whom": {"user:" + boss}, "access": {"read"}, "back": {back}}, 303)
	if loc != back {
		t.Fatalf("back to %q, want the card", loc)
	}
	_, loc = do(eve, "POST", "/disk/my/link", url.Values{"file": {plan}, "password": {"тайна"}, "back": {back}}, 303)
	if loc != back {
		t.Fatalf("the link: back to %q", loc)
	}
	card, _ = do(eve, "GET", about, nil, 200)
	has(card, "Файл открыт", "Босс", "с паролем", "/s/")
	shares, err := srv.Drive.Shares(ctx, []string{plan})
	must(err)
	if len(shares[plan]) != 1 {
		t.Fatalf("the file's shares: %+v", shares)
	}
	// one it is shared with sees who else has it, but changes nothing
	card, _ = do(ivan, "GET", about, nil, 200)
	has(card, "Общий доступ", "Владелец — Ева", "Босс")
	if strings.Contains(card, `/share"`) || strings.Contains(card, `name="off"`) {
		t.Fatal("one it is shared with may not change who has it")
	}
	// a back elsewhere is not followed
	_, loc = do(eve, "POST", "/disk/my/share", url.Values{"off": {shares[plan][0].ID}, "back": {"//evil.example/files"}}, 303)
	if loc != "/disk/my" {
		t.Fatalf("a foreign back: %q", loc)
	}
	card, _ = do(eve, "GET", about, nil, 200)
	if strings.Contains(card, "Файл открыт") {
		t.Fatal("the share closed is still on the card")
	}

	// the company's drive: every employee, then the rights of the folder
	actx := tasks.WithAdmin(ctx)
	co, _ := srv.Drive.Open(actx, boss, "company")
	money, err := srv.Drive.CreateFolder(actx, boss, co, "", "Отчёты")
	must(err)
	sum, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "итоги.txt", ContentType: "text/plain",
		UploadedBy: boss, Drive: "company", FolderID: money})
	must(err)
	card, _ = do(ivan, "GET", "/files/"+sum+"/about", nil, 200)
	has(card, "Все сотрудники компании")
	if strings.Contains(card, `/share"`) {
		t.Fatal("no shares on the company's drive")
	}
	must(srv.Drive.SetRight(actx, boss, co, money, "all", "", "read"))
	card, _ = do(boss, "GET", "/files/"+sum+"/about", nil, 200)
	has(card, "Права папки «Отчёты»", "Все сотрудники диска", `action="/disk/company/link"`)
	if strings.Contains(card, "Все сотрудники компании") {
		t.Fatal("the folder's rights decide, not «every employee»")
	}
}
