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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestEventDiscussionHTTP: «Файлы» and «Комментарии» of a private meeting's page — the organizer attaches a file of
// the drive, the invited do not; the invited comment with a file of their drive and read the meeting's files; someone
// who sees only «Занят» neither comments nor downloads them.
func TestEventDiscussionHTTP(t *testing.T) {
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
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@go24.test') RETURNING id`, name).Scan(&id))
		return id
	}
	boss, anna, olga := user("Босс"), user("Анна"), user("Ольга")
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Calendar: calendar.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	start := time.Now().Truncate(time.Hour).AddDate(0, 0, 1)
	ev, err := srv.Calendar.Save(ctx, boss, calendar.Event{Title: "Планёрка", Start: start, End: start.Add(time.Hour), Private: true}, []string{anna})
	must(err)
	driveFile := func(owner, name string) string {
		id, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: name, ContentType: "application/pdf", // empty: no stored bytes to copy
			UploadedBy: owner, Drive: "user", DriveOwner: owner})
		must(err)
		return id
	}
	plan, notes := driveFile(boss, "план.pdf"), driveFile(anna, "заметки.pdf")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss), anna: cookie(anna), olga: cookie(olga)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s %s: %d, want %d: %.300s", who, method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	base := "/calendar/" + ev

	// the invitation not answered yet is the «Календарь» counter of the menu; the answer takes it away
	counter := `data-counter="calendar" title="Приглашения" >1</a>`
	if page := do(anna, "GET", "/my", nil, http.StatusOK); !strings.Contains(page, counter) {
		t.Fatalf("no invitations counter in the menu: %q", counter)
	}
	if page := do(boss, "GET", "/my", nil, http.StatusOK); !strings.Contains(page, `data-counter="calendar" title="Приглашения" hidden>0</a>`) {
		t.Fatal("the organizer has an invitations counter")
	}
	must(srv.Calendar.Respond(ctx, anna, ev, "accepted"))
	if page := do(anna, "GET", "/my", nil, http.StatusOK); strings.Contains(page, counter) {
		t.Fatal("the answered invitation is still counted")
	}

	do(boss, "POST", base+"/files", url.Values{"file": {plan}}, http.StatusOK)
	do(anna, "POST", base+"/files", url.Values{"file": {notes}}, http.StatusForbidden) // the organizer's files
	do(anna, "POST", base+"/comments", url.Values{"body": {"Возьму <b>отчёт</b>"}, "drive_file": {notes}}, http.StatusOK)
	do(anna, "POST", base+"/comments", url.Values{"drive_file": {plan}}, http.StatusForbidden) // not her file
	do(olga, "POST", base+"/comments", url.Values{"body": {"А я?"}}, http.StatusNotFound)

	d, err := srv.Calendar.Discussion(ctx, ev)
	must(err)
	if len(d.Files) != 1 || d.Files[0].Name != "план.pdf" || len(d.Comments) != 1 || len(d.Comments[0].Files) != 1 {
		t.Fatalf("discussion %+v", d)
	}
	page := do(anna, "GET", base, nil, http.StatusOK)
	for _, want := range []string{`id="comments"`, "Возьму &lt;b&gt;отчёт&lt;/b&gt;", "заметки.pdf", `href="/files/` + d.Files[0].ID + `"`} {
		if !strings.Contains(page, want) {
			t.Fatalf("the page has no %q", want)
		}
	}
	if strings.Contains(page, `action="`+base+`/files"`) {
		t.Fatal("the invited may attach files to the meeting")
	}
	if page = do(boss, "GET", base, nil, http.StatusOK); !strings.Contains(page, `action="`+base+`/files"`) {
		t.Fatal("the organizer has no form to attach files")
	}
	do(anna, "GET", "/files/"+d.Files[0].ID, nil, http.StatusOK)
	do(anna, "GET", "/files/"+d.Comments[0].Files[0].ID, nil, http.StatusOK)
	do(olga, "GET", "/files/"+d.Files[0].ID, nil, http.StatusForbidden)
	do(olga, "GET", base, nil, http.StatusNotFound)

	do(anna, "POST", base+"/files/"+d.Files[0].ID+"/remove", nil, http.StatusNotFound)
	do(boss, "POST", base+"/files/"+d.Files[0].ID+"/remove", nil, http.StatusOK)
	do(anna, "POST", base+"/comments", url.Values{"delete": {d.Comments[0].ID}}, http.StatusOK)
	if d, err = srv.Calendar.Discussion(ctx, ev); err != nil || len(d.Files)+len(d.Comments) != 0 {
		t.Fatalf("after removing: %+v, %v", d, err)
	}
}
