package web

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/office"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestOfficeHTTP: «Документы онлайн» — the editor's signed config (editing for the file's uploader, viewing for those
// who only read the drive), the file given to the document server by a signed link only, the callback taking the
// edited file as a new version (and refusing a forged one), «Создать документ», and the proxy to the document server.
func TestOfficeHTTP(t *testing.T) {
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
	var anna, eve, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@go24.test') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@go24.test') RETURNING id`).Scan(&bob))

	// the document server: gives the edited file, and answers the proxy with what it was asked
	edited := []byte("edited document")
	var seen http.Header
	var seenPath string
	ds := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cache/") {
			w.Write(edited)
			return
		}
		seen, seenPath = r.Header.Clone(), r.URL.RequestURI()
		io.WriteString(w, "var DocsAPI = {};")
	}))
	defer ds.Close()

	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	s3ep := os.Getenv("GO24_TEST_S3")
	if s3ep != "" { // saving needs the S3 storage: make test-db starts a temporary one
		s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
	}
	hs := httptest.NewServer(srv.Routes()) // no document server yet: no editor
	company, _ := srv.Drive.Open(ctx, anna, "company")
	doc, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "План.docx", ContentType: officeTypes["docx"],
		UploadedBy: anna, Drive: company.Kind})
	must(err)
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Name: "N" + id[:4]}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{anna: cookie(anna), eve: cookie(eve), bob: cookie(bob)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	var base string
	do := func(who, method, path string, body io.Reader, want int) (string, *http.Response) {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, body)
		if who != "" {
			req.AddCookie(as[who])
		}
		if method == "POST" && who != "" {
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
		return string(raw), res
	}
	base = hs.URL
	if page, _ := do(anna, "GET", "/disk/company", nil, 200); strings.Contains(page, "/new-doc") || strings.Contains(page, "/edit/"+doc) {
		t.Error("the editor is offered without a document server")
	}
	hs.Close()
	srv.Office = office.New(ds.URL, "office-secret", "http://go24.invalid")
	hs = httptest.NewServer(srv.Routes())
	defer hs.Close()
	base = hs.URL

	page, _ := do(anna, "GET", "/disk/company", nil, 200)
	for _, want := range []string{`action="/disk/company/new-doc"`, `href="/disk/company/edit/` + doc + `?folder="`, `value="pptx"`} {
		if !strings.Contains(page, want) {
			t.Errorf("the drive page has no %s", want)
		}
	}
	config := func(who string) map[string]any {
		t.Helper()
		page, _ := do(who, "GET", "/disk/company/edit/"+doc, nil, 200)
		m := regexp.MustCompile(`data-office="([^"]+)"`).FindStringSubmatch(page)
		if m == nil || !strings.Contains(page, "/office/web-apps/apps/api/documents/api.js") {
			t.Fatalf("no editor: %.2000s", page)
		}
		var cfg map[string]any
		must(json.Unmarshal([]byte(html.UnescapeString(m[1])), &cfg))
		var signed map[string]any
		must(srv.Office.Verify(cfg["token"].(string), &signed))
		if signed["document"].(map[string]any)["key"] != cfg["document"].(map[string]any)["key"] {
			t.Error("the token is not of the config")
		}
		return cfg
	}
	cfg := config(anna)
	ec, dc := cfg["editorConfig"].(map[string]any), cfg["document"].(map[string]any)
	if cfg["documentType"] != "word" || ec["mode"] != "edit" || dc["fileType"] != "docx" || dc["title"] != "План.docx" ||
		!strings.HasPrefix(ec["callbackUrl"].(string), "http://go24.invalid/onlyoffice/callback/"+doc+"?") {
		t.Errorf("config = %v", cfg)
	}
	if ec := config(eve)["editorConfig"].(map[string]any); ec["mode"] != "view" { // the company drive: Eve changes her own files only
		t.Errorf("eve's mode = %v", ec["mode"])
	}
	do(eve, "GET", "/disk/my/edit/"+doc, nil, 404) // not a file of her drive

	// the file for the document server: by its signed link only
	fileURL := strings.TrimPrefix(dc["url"].(string), "http://go24.invalid")
	do("", "GET", fileURL, nil, 200)
	do("", "GET", "/onlyoffice/file/"+doc+"/forged", nil, 404)

	// the callback: a forged one is refused, the signed one saves the edited file as a new version
	cbPath := strings.TrimPrefix(ec["callbackUrl"].(string), "http://go24.invalid")
	callback := func(path string, claims map[string]any, secret string, want int) string {
		t.Helper()
		token, _ := (&office.Server{Secret: secret}).Sign(claims)
		claims["token"] = token
		raw, _ := json.Marshal(claims)
		body, _ := do("", "POST", path, bytes.NewReader(raw), want)
		return body
	}
	save := map[string]any{"key": dc["key"], "status": office.StatusSave, "url": "http://192.168.1.5:8080/office/cache/files/out.docx", "users": []string{anna}}
	callback(cbPath, save, "wrong", 403)
	u, _ := url.Parse(cbPath)
	q := u.Query()
	q.Set("u", eve)
	callback(u.Path+"?"+q.Encode(), save, "office-secret", 404) // the stamp is Anna's
	if body := callback(cbPath, map[string]any{"key": dc["key"], "status": 1, "users": []string{anna, "bad"}}, "office-secret", 200); !strings.Contains(body, `"error": 0`) {
		t.Errorf("editing = %s", body)
	}
	editing := func() (n int) { // who has it open now, as the drive's list shows
		must(pool.QueryRow(ctx, `SELECT coalesce(cardinality(editing), 0) FROM files WHERE id = $1`, doc).Scan(&n))
		return n
	}
	if n := editing(); n != 1 {
		t.Errorf("editing = %d", n)
	}
	callback(cbPath, map[string]any{"key": dc["key"], "status": office.StatusClosed}, "office-secret", 200)
	if n := editing(); n != 0 {
		t.Errorf("editing after closing = %d", n)
	}
	if s3ep == "" {
		t.Log("no GO24_TEST_S3: saving and «Создать документ» are not checked")
	} else {
		if body := callback(cbPath, save, "office-secret", 200); !strings.Contains(body, `"error": 0`) {
			t.Fatalf("save = %s", body)
		}
		vs, err := srv.Drive.Versions(ctx, doc)
		must(err)
		if len(vs) != 2 || vs[0].AuthorID != anna || vs[0].Size != int64(len(edited)) {
			t.Fatalf("versions = %+v", vs)
		}
		got, _ := do(anna, "GET", "/files/"+doc, nil, 200)
		if got != string(edited) {
			t.Errorf("the file is %q", got)
		}
		if config(anna)["document"].(map[string]any)["key"] == dc["key"] {
			t.Error("the document's key stays the same after saving")
		}
		do("", "GET", fileURL, nil, 404) // the link was for the former state

		// «Создать документ»: an empty spreadsheet in the folder, opened in the editor
		body, _ := do(bob, "POST", "/disk/my/new-doc", strings.NewReader("kind=xlsx&name=Бюджет"), 200) // the page's script follows it
		var res struct{ Redirect string }
		must(json.Unmarshal([]byte(body), &res))
		loc := res.Redirect
		var id, name string
		must(pool.QueryRow(ctx, `SELECT id::text, name FROM files WHERE uploaded_by = $1`, bob).Scan(&id, &name))
		if name != "Бюджет.xlsx" || loc != "/disk/my/edit/"+id+"?folder=" {
			t.Errorf("new = %s %s", name, loc)
		}
		if xlsx, _ := do(bob, "GET", "/files/"+id, nil, 200); !strings.HasPrefix(xlsx, "PK") {
			t.Error("the new spreadsheet is not an Office file")
		}
		do(bob, "POST", "/disk/my/new-doc", strings.NewReader("kind=xlsx&name=Бюджет.xlsx"), 422)
		do(bob, "POST", "/disk/my/new-doc", strings.NewReader("kind=exe"), 404)
	}

	// a file of «Мой диск» shared by itself: «Доступ» in its menu; it opens from «Общие со мной» in the editor
	mine, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "Смета.docx", ContentType: officeTypes["docx"],
		UploadedBy: anna, Drive: "user", DriveOwner: anna})
	must(err)
	if page, _ := do(anna, "GET", "/disk/my", nil, 200); !strings.Contains(page, `id="sh-`+mine+`"`) || !strings.Contains(page, `data-toggle="sh-`+mine+`"`) {
		t.Error("no «Доступ» for a file of one's drive")
	}
	do(anna, "POST", "/disk/my/share", strings.NewReader("file="+mine+"&whom=user:"+eve+"&access=full"), 404)
	do(anna, "POST", "/disk/my/share", strings.NewReader("file="+mine+"&whom=user:"+eve+"&access=edit"), 200)
	if page, _ := do(eve, "GET", "/disk/shared", nil, 200); !strings.Contains(page, `href="/disk/sf/`+mine+`"`) {
		t.Error("the shared file is not in «Общие со мной»")
	}
	if _, res := do(eve, "GET", "/disk/sf/"+mine, nil, 302); res.Header.Get("Location") != "/disk/sf/"+mine+"/edit/"+mine {
		t.Errorf("the shared file opens at %s", res.Header.Get("Location"))
	}
	do(bob, "GET", "/disk/sf/"+mine, nil, 404)
	do(bob, "GET", "/disk/sf/"+mine+"/edit/"+mine, nil, 404)
	do(eve, "GET", "/disk/sf/"+mine+"/edit/"+doc, nil, 404)                 // not the file shared
	do(eve, "POST", "/disk/sf/"+mine+"/upload", strings.NewReader(""), 404) // nothing is put beside it
	page, _ = do(eve, "GET", "/disk/sf/"+mine+"/edit/"+mine, nil, 200)
	m := regexp.MustCompile(`data-office="([^"]+)"`).FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no editor for the shared file: %.1000s", page)
	}
	var sc map[string]any
	must(json.Unmarshal([]byte(html.UnescapeString(m[1])), &sc))
	sec := sc["editorConfig"].(map[string]any)
	if sec["mode"] != "edit" || sec["customization"].(map[string]any)["goback"].(map[string]any)["url"] != "/disk/shared" {
		t.Errorf("the shared file's editor = %v", sec)
	}
	if s3ep != "" { // eve's save is a version of anna's file
		cb := strings.TrimPrefix(sec["callbackUrl"].(string), "http://go24.invalid")
		key := sc["document"].(map[string]any)["key"]
		if body := callback(cb, map[string]any{"key": key, "status": office.StatusSave, "url": save["url"], "users": []string{eve}}, "office-secret", 200); !strings.Contains(body, `"error": 0`) {
			t.Fatalf("save of the shared file = %s", body)
		}
		if vs, _ := srv.Drive.Versions(ctx, mine); len(vs) != 2 || vs[0].AuthorID != eve {
			t.Errorf("versions of the shared file = %+v", vs)
		}
	}

	// the proxy: signed in only, without go24's cookie, with the address the document server builds its links from
	do("", "GET", "/office/web-apps/apps/api/documents/api.js", nil, 302)
	if body, _ := do(anna, "GET", "/office/web-apps/apps/api/documents/api.js?v=1", nil, 200); body != "var DocsAPI = {};" {
		t.Errorf("proxy = %s", body)
	}
	if seenPath != "/web-apps/apps/api/documents/api.js?v=1" || seen.Get("Cookie") != "" ||
		seen.Get("X-Forwarded-Host") != strings.TrimPrefix(hs.URL, "http://")+"/office" || seen.Get("X-Forwarded-Proto") != "http" {
		t.Errorf("the document server got %s %v", seenPath, seen)
	}
}
