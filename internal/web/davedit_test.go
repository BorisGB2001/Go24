package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/i18n"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDavEditHTTP: «Редактировать на компьютере» gives the document's page with the links of Microsoft Office and
// LibreOffice to a signed WebDAV address of the one file; the program reads it there, a PUT makes its new version,
// LOCK marks it as being edited and UNLOCK lifts that; nothing else is written there. Who may only read the file
// reads it but saves nothing; a link changed, expired or of a file moved to the trash does not work.
func TestDavEditHTTP(t *testing.T) {
	dbURL, s3ep := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_S3")
	if dbURL == "" || s3ep == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_S3 not set")
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
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	var anna, petr string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('petr', 'Пётр', 'petr@t') RETURNING id`).Scan(&petr))
	bundle, err := i18n.Load("")
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3), I18n: bundle,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	srv.BaseURL = hs.URL

	company, err := srv.Drive.Open(ctx, anna, "company")
	must(err)
	id, err := srv.diskStore(httptest.NewRequest("GET", "/", nil).WithContext(tasks.WithViewer(ctx, anna, false)), auth.Session{UserID: anna},
		company, "", "", "План.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", strings.NewReader("первый"), int64(len("первый")))
	must(err)

	do := func(who, method, path, body string, hdr map[string]string, want int) string {
		t.Helper()
		req, _ := http.NewRequest(method, path, strings.NewReader(body))
		if !strings.HasPrefix(path, "http") {
			req, _ = http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		}
		if who != "" {
			rec := httptest.NewRecorder()
			must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
			req.AddCookie(rec.Result().Cookies()[0])
		}
		req.Header.Set("Accept-Language", "ru-RU")
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	link := func(who string) string {
		t.Helper()
		page := do(who, "GET", "/disk/company/local/"+id, "", nil, 200)
		for _, want := range []string{"Редактировать на компьютере", "ms-word:ofe|u|" + hs.URL + "/dav-edit/", "vnd.libreoffice.command:ofe|u|"} {
			if !strings.Contains(page, want) {
				t.Errorf("the page has no %q: %s", want, regexp.MustCompile(`<a [^>]*data-local-app[^>]*`).FindAllString(page, -1))
			}
		}
		m := regexp.MustCompile(`value="(http[^"]+/dav-edit/[^"]+)"`).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no address on the page: %.2000s", page)
		}
		return strings.ReplaceAll(m[1], "&#43;", "+")
	}
	if list := do(anna, "GET", "/disk/company", "", nil, 200); !strings.Contains(list, "/disk/company/local/"+id) {
		t.Error("the file's menu has no «Редактировать на компьютере»")
	}

	f := link(anna)
	dir := f[:strings.LastIndex(f, "/")+1]
	if got := do("", "GET", f, "", nil, 200); got != "первый" {
		t.Errorf("GET = %q", got)
	}
	if list := do("", "PROPFIND", dir, "", map[string]string{"Depth": "1"}, 207); !strings.Contains(list, url.PathEscape("План.docx")) && !strings.Contains(list, "План.docx") {
		t.Errorf("the folder: %.1000s", list)
	}
	lock := do("", "LOCK", f, `<?xml version="1.0"?><D:lockinfo xmlns:D="DAV:"><D:lockscope><D:exclusive/></D:lockscope><D:locktype><D:write/></D:locktype><D:owner>Word</D:owner></D:lockinfo>`,
		map[string]string{"Timeout": "Second-3600"}, 200)
	token := regexp.MustCompile(`<D:locktoken><D:href>([^<]+)</D:href>`).FindStringSubmatch(lock)
	if token == nil {
		t.Fatalf("LOCK: %.500s", lock)
	}
	var editors int
	must(pool.QueryRow(ctx, `SELECT coalesce(cardinality(editing), 0) FROM files WHERE id::text = $1`, id).Scan(&editors))
	if editors != 1 {
		t.Errorf("editors while locked = %d", editors)
	}
	do("", "PUT", f, "второй вариант", map[string]string{"If": "(<" + token[1] + ">)"}, 201)
	if got := do("", "GET", f, "", nil, 200); got != "второй вариант" {
		t.Errorf("GET after PUT = %q", got)
	}
	if vs, _ := srv.Drive.Versions(ctx, id); len(vs) != 2 {
		t.Errorf("versions = %+v", vs)
	}
	do("", "UNLOCK", f, "", map[string]string{"Lock-Token": "<" + token[1] + ">"}, 204)
	must(pool.QueryRow(ctx, `SELECT coalesce(cardinality(editing), 0) FROM files WHERE id::text = $1`, id).Scan(&editors))
	if editors != 0 {
		t.Errorf("editors after UNLOCK = %d", editors)
	}
	do("", "PUT", dir+url.PathEscape("~$План.docx"), "x", nil, 403) // the one file only
	do("", "DELETE", f, "", nil, 403)
	do("", "MKCOL", dir+"x", "", nil, 403)

	// Пётр: the company's drive he reads; the file is Анна's to change
	pf := link(petr)
	if got := do("", "GET", pf, "", nil, 200); got != "второй вариант" {
		t.Errorf("Пётр's GET = %q", got)
	}
	do("", "PUT", pf, "чужой", nil, 403)

	// a changed link, an expired one, a file in the trash
	tok := strings.Split(strings.TrimPrefix(f, hs.URL+"/dav-edit/"), "/")[0]
	do("", "GET", strings.Replace(f, tok, tok[:len(tok)-1]+"x", 1), "", nil, 403)
	old := srv.davEditToken(anna, "company", id, time.Now().Add(-time.Minute))
	do("", "GET", hs.URL+"/dav-edit/"+old+"/"+url.PathEscape("План.docx"), "", nil, 403)
	must(srv.Drive.DeleteFile(tasks.WithViewer(ctx, anna, false), anna, company, id))
	do("", "GET", f, "", nil, 404)
}
