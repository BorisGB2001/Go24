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
	"go24/internal/i18n"
	"go24/internal/office"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestIMAttachMenu: the paperclip's menu — the computer always; the drive and new documents not for a guest, the
// documents only with a document server; a poll where polls are; a task and a meeting of the chat in the chat itself.
func TestIMAttachMenu(t *testing.T) {
	bundle, _ := i18n.Load("")
	pages, err := buildPages(bundle)
	if err != nil {
		t.Fatal(err)
	}
	menu := func(kv ...any) string {
		t.Helper()
		data := map[string]any{"Guest": false, "Office": false, "Poll": false, "Room": ""}
		for i := 0; i < len(kv); i += 2 {
			data[kv[i].(string)] = kv[i+1]
		}
		var b strings.Builder
		if err := pages["ru-RU"]["im"].ExecuteTemplate(&b, "imattachmenu", data); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	out := menu("Office", true, "Poll", true, "Room", "!r:go24")
	for _, want := range []string{"data-attach-menu", "data-attach>", "Файл с компьютера", "data-disk-pick data-event", "Файл с Диска",
		`data-im-doc="docx"`, `data-im-doc="xlsx"`, `data-im-doc="pptx"`, "Презентация", "data-poll-new",
		`href="/tasks/new?im_room=%21r%3Ago24"`, `href="/calendar/new?im_room=%21r%3Ago24"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the menu lacks %s:\n%s", want, out)
		}
	}
	if out := menu("Poll", true); strings.Contains(out, "data-im-doc") || strings.Contains(out, "im_room") || !strings.Contains(out, "data-disk-pick") {
		t.Errorf("no document server, a thread: no documents, no task and meeting:\n%s", out)
	}
	if out := menu("Guest", true, "Office", true); strings.Contains(out, "data-disk-pick") || strings.Contains(out, "data-im-doc") ||
		strings.Contains(out, "data-poll-new") || !strings.Contains(out, "data-attach>") {
		t.Errorf("a guest attaches from the computer only (comments of a channel have no poll):\n%s", out)
	}
}

// TestFileOpen: a link to a document of a drive opens it in the editor as each one reaches it — on their own drive,
// the company's, a project's, as a file shared with them; other files open as their card; those who cannot read the
// file get nothing, and guests do not make documents or send files of drives into chats.
func TestFileOpen(t *testing.T) {
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
	var anna, ivan, eve, gost string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ivan', 'Иван', 'ivan@go24.test') RETURNING id`).Scan(&ivan))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ('gost', 'Гость', 'gost@go24.test', true) RETURNING id`).Scan(&gost))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Office: office.New("http://office.invalid", "office-secret", "http://go24.invalid")}
	_, err = ts.CreateProject(ctx, anna, "DOC", "Проект", "")
	must(err)
	add := func(name, drv, owner string) string {
		t.Helper()
		id, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: name, UploadedBy: anna, Drive: drv, DriveOwner: owner})
		must(err)
		return id
	}
	mine, sheet, company, card := add("Документ.docx", "user", anna), add("Таблица.xlsx", "user", anna), add("Устав.docx", "company", ""), add("фото.png", "user", anna)
	var project string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, uploaded_by, drive, drive_owner)
		SELECT 'k/p', 'План.pptx', 3, $1, 'project', id FROM projects WHERE key = 'DOC' RETURNING id`, anna).Scan(&project))
	my, _ := srv.Drive.Open(ctx, anna, "my")
	must(srv.Drive.ShareFileWith(ctx, anna, my, sheet, []string{ivan}, "edit"))

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Name: "N" + id[:4]}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), ivan: cookie(ivan), eve: cookie(eve), gost: cookie(gost)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(form.Encode()))
		req.AddCookie(as[who])
		if method == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("X-Go24-Form", "1")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return res.Header.Get("Location")
	}
	for _, c := range []struct{ who, file, want string }{
		{anna, mine, "/disk/my/edit/" + mine},
		{anna, company, "/disk/company/edit/" + company},
		{ivan, company, "/disk/company/edit/" + company},
		{anna, project, "/disk/p/DOC/edit/" + project},
		{ivan, sheet, "/disk/sf/" + sheet + "/edit/" + sheet},
		{anna, card, "/files/" + card + "/about"},
	} {
		if got := do(c.who, "GET", "/files/"+c.file+"/open", nil, http.StatusFound); got != c.want {
			t.Errorf("%s opens %s at %q, want %q", c.who[:4], c.file, got, c.want)
		}
	}
	do(ivan, "GET", "/files/"+mine+"/open", nil, http.StatusForbidden) // not shared with him
	do(eve, "GET", "/files/"+project+"/open", nil, http.StatusForbidden)

	// not a document's kind; a guest has neither (their link to a collab's document opens as anyone's)
	do(anna, "POST", "/im/doc", url.Values{"room": {"!r:go24"}, "kind": {"exe"}}, http.StatusNotFound)
	do(gost, "POST", "/im/doc", url.Values{"room": {"!r:go24"}, "kind": {"docx"}}, http.StatusNotFound)
	do(gost, "POST", "/im/disk", url.Values{"room": {"!r:go24"}, "file": {company}}, http.StatusNotFound)
	do(gost, "GET", "/files/"+company+"/open", nil, http.StatusForbidden)
}
