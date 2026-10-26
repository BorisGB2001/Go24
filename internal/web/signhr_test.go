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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignHR: «Подпись для сотрудников» — an HR document of a template «Сотрудник» goes to a department (its
// sub-departments too, not guests nor the dismissed), each employee their own copy with their fields; the
// representative signs all the copies at once, then each employee theirs («Мои документы»); the sender reminds and
// recalls the unsigned; a Word file of one's own goes too; only administrators and HR officers send.
func TestSignHR(t *testing.T) {
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
	user := func(sub, name, more string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position, active, guest) VALUES ($1, $2, $1 || '@t', $3,
			$4 <> 'gone', $4 = 'guest') RETURNING id`, sub, name, "Бухгалтер", more).Scan(&id))
		return id
	}
	anna, bob, eve, kim, gus, dan := user("anna", "Анна", ""), user("bob", "Боб", ""), user("eve", "Ева", ""),
		user("kim", "Ким", ""), user("gus", "Гость", "guest"), user("dan", "Дан", "gone")
	var root, sales, sub string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&root))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Продажи', $1) RETURNING id`, root).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Опт', $1) RETURNING id`, sales).Scan(&sub))
	for u, d := range map[string]string{eve: sales, kim: sub, gus: sales, dan: sales} {
		_, err := pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, u, d)
		must(err)
	}
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Storages: storages.NewStore(pool, nil, s3),
		Sign: sign.NewStore(pool), BaseURL: "https://go24", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	docx := func(text string) []byte {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		f, _ := zw.Create("word/document.xml")
		io.WriteString(f, `<w:document><w:body><w:p><w:r><w:t>`+text+`</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`)
		zw.Close()
		return b.Bytes()
	}
	text := func(data []byte) string {
		t.Helper()
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		must(err)
		rc, err := zr.Open("word/document.xml")
		must(err)
		x, _ := io.ReadAll(rc)
		return string(x)
	}
	tpl, err := srv.addTemplate(ctx, anna, "Приказ об отпуске", crm.TemplateEmployee, "prikaz.docx",
		docx("Приказ № {DocumentNumber}: {Name}, {Position}, {Department}"))
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, bob, eve, kim} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who string, req *http.Request) (int, string, string) {
		t.Helper()
		req.AddCookie(as[who])
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
	hr := func(who string, fields url.Values, file []byte) (int, string) {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for k, vs := range fields {
			for _, v := range vs {
				mw.WriteField(k, v)
			}
		}
		if file != nil {
			fw, _ := mw.CreateFormFile("file", "Ознакомление.docx")
			fw.Write(file)
		}
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/sign/hr", &b)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		code, _, loc := send(who, req)
		return code, loc
	}
	notes := func(user, url string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'sign_request' AND url LIKE $2`, user, url).Scan(&n))
		return n
	}
	batchID := regexp.MustCompile(`^/sign/batch/([0-9a-f-]{36})\?msg=`)

	// who sends
	if code, _, _ := do(bob, "GET", "/sign/hr", nil); code != http.StatusForbidden {
		t.Fatalf("bob's form: %d", code)
	}
	if code, _ := hr(bob, url.Values{"template": {tpl}, "to": {eve}}, nil); code != http.StatusForbidden {
		t.Fatalf("bob sends: %d", code)
	}
	if code, body, _ := do(anna, "GET", "/sign/hr", nil); code != http.StatusOK || !strings.Contains(body, "Приказ об отпуске") {
		t.Fatalf("anna's form: %d", code)
	}
	if _, loc := hr(anna, url.Values{"template": {tpl}}, nil); !strings.HasPrefix(loc, "/sign/hr?") {
		t.Fatalf("nobody chosen: %s", loc)
	}

	// to the department, the representative first
	_, loc := hr(anna, url.Values{"template": {tpl}, "to": {"dept:" + sales}, "signer": {bob}}, nil)
	m := batchID.FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("send: %s", loc)
	}
	b, docs, err := srv.Sign.Batch(ctx, m[1])
	must(err)
	if b.Title != "Приказ об отпуске" || b.Total != 2 || b.Ready != 2 || len(docs) != 2 || docs[0].Employee().UserID != eve ||
		docs[1].Employee().UserID != kim || docs[0].Parties[0].UserID != bob {
		t.Fatalf("batch: %+v %+v", b, docs)
	}
	if notes(bob, "/sign/batch/"+b.ID) != 1 || notes(eve, "/sign/%") != 0 {
		t.Fatalf("notified: bob %d, eve %d", notes(bob, "/sign/batch/"+b.ID), notes(eve, "/sign/%"))
	}
	// the reminders by themselves: two days later, not on a Saturday; the representative — once for the sending
	_, err = pool.Exec(ctx, `UPDATE sign_parties SET ready_at = now() - interval '2 days' WHERE status = 'ready'`)
	must(err)
	day := func(wd time.Weekday) time.Time {
		d := time.Now().AddDate(0, 0, 1)
		for d.Weekday() != wd {
			d = d.AddDate(0, 0, 1)
		}
		return time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, time.Local)
	}
	must(srv.SignDue(ctx, day(time.Saturday)))
	must(srv.SignDue(ctx, day(time.Monday).Add(-2*time.Hour)))
	if notes(bob, "/sign/batch/"+b.ID) != 1 {
		t.Fatal("reminded on a Saturday or at 8")
	}
	must(srv.SignDue(ctx, day(time.Monday)))
	if n := notes(bob, "/sign/batch/"+b.ID); n != 2 || notes(bob, "/sign/"+docs[0].ID) != 0 {
		t.Fatalf("bob reminded: %d", n)
	}
	if d, _ := srv.Sign.Get(ctx, docs[1].ID); d.RemindDays != 1 || d.Current().Reminders != 1 {
		t.Fatalf("kim's copy: %+v", d)
	}
	m0, err := srv.Storages.FileMeta(ctx, docs[0].FileID)
	must(err)
	raw, err := srv.readFile(httptest.NewRequest("GET", "/", nil), m0, signMax)
	must(err)
	if x := text(raw); !strings.Contains(x, "Приказ № 1: Ева, Бухгалтер, Продажи") {
		t.Fatalf("eve's copy: %s", x)
	}
	if code, _, _ := do(eve, "GET", "/sign/batch/"+b.ID, nil); code != http.StatusNotFound {
		t.Fatalf("eve sees the batch: %d", code)
	}
	if code, body, _ := do(bob, "GET", "/sign/batch/"+b.ID, nil); code != http.StatusOK || !strings.Contains(body, `action="/sign/batch/`+b.ID+`/sign"`) {
		t.Fatalf("bob's batch: %d", code)
	}
	if code, body, _ := do(anna, "GET", "/sign?f=hr", nil); code != http.StatusOK || !strings.Contains(body, "/sign/batch/"+b.ID) {
		t.Fatalf("anna's sendings: %d", code)
	}

	// the representative signs all; then the employees
	do(bob, "POST", "/sign/batch/"+b.ID+"/sign", nil) // not confirmed reading
	if b, _, _ = srv.Sign.Batch(ctx, b.ID); b.Ready != 2 {
		t.Fatalf("signed with no confirmation: %+v", b)
	}
	if code, _, _ := do(eve, "POST", "/sign/batch/"+b.ID+"/sign", url.Values{"agree": {"1"}}); code != http.StatusNotFound {
		t.Fatalf("eve signs the batch: %d", code)
	}
	do(bob, "POST", "/sign/batch/"+b.ID+"/sign", url.Values{"agree": {"1"}})
	if b, docs, _ = srv.Sign.Batch(ctx, b.ID); b.Ready != 0 || b.Signing != 2 || docs[0].Current().UserID != eve {
		t.Fatalf("bob signed all: %+v", b)
	}
	if notes(eve, "/sign/"+docs[0].ID) != 1 || notes(kim, "/sign/"+docs[1].ID) != 1 {
		t.Fatal("employees not notified")
	}
	if code, body, _ := do(eve, "GET", "/sign?f=mine", nil); code != http.StatusOK || !strings.Contains(body, "/sign/"+docs[0].ID) || strings.Contains(body, "/sign/"+docs[1].ID) {
		t.Fatalf("eve's documents: %d", code)
	}
	do(eve, "POST", "/sign/"+docs[0].ID+"/sign", url.Values{"agree": {"1"}})
	d, err := srv.Sign.Get(ctx, docs[0].ID)
	must(err)
	if d.Status != "done" || d.ResultFile == "" {
		t.Fatalf("eve signed: %+v", d)
	}
	mr, err := srv.Storages.FileMeta(ctx, d.ResultFile)
	must(err)
	raw, err = srv.readFile(httptest.NewRequest("GET", "/", nil), mr, signMax)
	must(err)
	if x := text(raw); !strings.Contains(x, "Ева") || !strings.Contains(x, "Боб") || !strings.Contains(x, ">Сотрудник<") {
		t.Fatalf("signing sheet: %s", x)
	}

	// reminded, recalled
	if code, _, _ := do(bob, "POST", "/sign/batch/"+b.ID+"/remind", nil); code != http.StatusNotFound {
		t.Fatalf("bob reminds: %d", code)
	}
	do(anna, "POST", "/sign/batch/"+b.ID+"/remind", nil)
	if notes(kim, "/sign/"+docs[1].ID) != 2 || notes(eve, "/sign/"+docs[0].ID) != 1 {
		t.Fatal("reminded")
	}
	do(anna, "POST", "/sign/batch/"+b.ID+"/cancel", nil)
	if b, _, _ = srv.Sign.Batch(ctx, b.ID); b.Signed != 1 || b.Cancelled != 1 || b.Signing != 0 {
		t.Fatalf("recalled: %+v", b)
	}

	// a Word file of one's own to an employee, no representative
	_, loc = hr(anna, url.Values{"to": {kim}}, docx("Ознакомлен: {Name}"))
	if m = batchID.FindStringSubmatch(loc); m == nil {
		t.Fatalf("own file: %s", loc)
	}
	b, docs, err = srv.Sign.Batch(ctx, m[1])
	must(err)
	if b.Title != "Ознакомление" || b.Total != 1 || len(docs[0].Parties) != 1 || docs[0].Current().UserID != kim || notes(kim, "/sign/"+docs[0].ID) != 1 {
		t.Fatalf("own file batch: %+v %+v", b, docs)
	}
}
