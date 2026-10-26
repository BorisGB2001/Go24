package web

import (
	"archive/zip"
	"bytes"
	"context"
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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignCollab (GO24DEV-691): an employee of a collab sends a document for signing from it — the form offers its
// guests as the client; a guest and a stranger do not send from it; its employees see the document; signed, it lies
// on the collab's drive.
func TestSignCollab(t *testing.T) {
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
	newUser := func(name, email string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ($1, $1, $2, $3) RETURNING id`, name, email, guest).Scan(&id))
		return id
	}
	anna, bob, eve := newUser("Анна", "anna@t", false), newUser("Боб", "bob@t", false), newUser("Ева", "eve@t", false)
	gus := newUser("Гость Иванов", "gus@client.test", true)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3), Sign: sign.NewStore(pool),
		BaseURL: "https://go24", Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	key, err := ts.CreateCollab(ctx, anna, "Стройка", "", []string{bob, gus})
	must(err)
	c, err := ts.CollabByKey(ctx, anna, key)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, bob, eve, gus} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who string, req *http.Request) (int, string, string) {
		t.Helper()
		if who != "" {
			req.AddCookie(as[who])
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}
	do := func(who, method, path string, form url.Values) (int, string, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return send(who, req)
	}

	// the form: the collab's only guest is the client already; a guest and a stranger get nothing
	code, body, _ := do(anna, "GET", "/sign/new?collab="+key, nil)
	if code != http.StatusOK || !strings.Contains(body, `name="collab" value="`+key+`"`) || !strings.Contains(body, `value="gus@client.test"`) ||
		!strings.Contains(body, "Стройка") {
		t.Fatalf("form: %d", code)
	}
	for who, u := range map[string]string{"guest": gus, "stranger": eve} {
		if code, _, _ := do(u, "GET", "/sign/new?collab="+key, nil); code != http.StatusNotFound {
			t.Errorf("%s's form: %d", who, code)
		}
	}

	var src bytes.Buffer
	zw := zip.NewWriter(&src)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:body><w:p><w:r><w:t>Договор подряда</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`)
	zw.Close()
	upload := func(who string, fields ...string) (int, string) {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for i := 0; i+1 < len(fields); i += 2 {
			mw.WriteField(fields[i], fields[i+1])
		}
		fw, _ := mw.CreateFormFile("file", "Договор.docx")
		fw.Write(src.Bytes())
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/sign", &b)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		code, _, loc := send(who, req)
		return code, loc
	}
	if code, _ := upload(eve, "collab", key, "signer", eve, "client_name", "Гость Иванов", "client_email", "gus@client.test"); code != http.StatusNotFound {
		t.Fatalf("a stranger sends from the collab: %d", code)
	}
	code, loc := upload(anna, "collab", key, "signer", anna, "client_name", "Гость Иванов", "client_email", "gus@client.test")
	m := regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc)
	if code != http.StatusSeeOther || m == nil {
		t.Fatalf("send: %d %s", code, loc)
	}
	id := m[1]
	d, err := srv.Sign.Get(ctx, id)
	must(err)
	if d.ProjectID != c.ID || len(d.Parties) != 2 {
		t.Fatalf("kept: %+v", d)
	}

	// the collab's employees see it (its chat links to it), with the way back to the collab; the stranger and the guest do not
	if code, body, _ := do(bob, "GET", "/sign/"+id, nil); code != http.StatusOK || !strings.Contains(body, `href="/p/`+key+`"`) {
		t.Fatalf("bob's page: %d", code)
	}
	for who, u := range map[string]string{"guest": gus, "stranger": eve} {
		if code, _, _ := do(u, "GET", "/sign/"+id, nil); code != http.StatusNotFound {
			t.Errorf("%s sees it: %d", who, code)
		}
	}

	// signed by both: the signed document is on the collab's drive
	do(anna, "POST", "/sign/"+id+"/sign", url.Values{"agree": {"1"}})
	d, err = srv.Sign.Get(ctx, id)
	must(err)
	must(srv.Sign.SetCode(ctx, d.Parties[1].ID, "424242"))
	do("", "POST", "/sg/"+d.Parties[1].Token+"/sign", url.Values{"code": {"424242"}, "agree": {"1"}})
	if d, _ = srv.Sign.Get(ctx, id); d.Status != "done" {
		t.Fatalf("not done: %s", d.Status)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE drive = 'project' AND drive_owner = $1 AND name LIKE 'Договор — %'`, c.ID).Scan(&n))
	if n != 1 {
		t.Fatalf("on the collab's drive: %d", n)
	}
	if code, body, _ := do(gus, "GET", "/disk/p/"+key, nil); code != http.StatusOK || !strings.Contains(body, "Договор — ") {
		t.Fatalf("the guest's drive of the collab: %d", code)
	}
}
