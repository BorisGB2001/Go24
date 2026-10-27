package web

import (
	"archive/zip"
	"bytes"
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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignSelf: «Документы от сотрудников» — an HR officer opens a template «Сотрудник» to employees with its
// representative; an employee makes an application by it (their profile and the fields typed in), signs it first, then
// the representative; «Кому доступен» of the template holds; guests and closed templates make nothing.
func TestSignSelf(t *testing.T) {
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
	user := func(sub, name string, guest bool) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, position, active, guest) VALUES ($1, $2, $1 || '@t',
			'Бухгалтер', true, $3) RETURNING id`, sub, name, guest).Scan(&id))
		return id
	}
	anna, bob, eve, gus := user("anna", "Анна", false), user("bob", "Боб", false), user("eve", "Ева", false), user("gus", "Гость", true)
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
	vacation, err := srv.addTemplate(ctx, anna, "Заявление на отпуск", crm.TemplateEmployee, "z.docx",
		docx("№ {DocumentNumber}. {Name}, {Position}: прошу отпуск с {StartDate} на {Days} дней. {MyCompany.Title}"))
	must(err)
	closed, err := srv.addTemplate(ctx, anna, "Служебная записка", crm.TemplateEmployee, "s.docx", docx("{Name}: {Text}"))
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, bob, eve, gus} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
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
		req.AddCookie(as[who])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, string(raw), loc
	}
	notes := func(user, url string) int {
		var n int
		must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'sign_request' AND url = $2`, user, url).Scan(&n))
		return n
	}
	made := regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`)

	// nothing open yet; only HR officers and administrators open templates
	if _, body, _ := do(eve, "GET", "/sign", nil); strings.Contains(body, `href="/sign/my"`) {
		t.Fatal("the button with no templates")
	}
	if code, _, _ := do(bob, "POST", "/sign/hr/self/"+vacation, url.Values{"signer": {bob}}); code != http.StatusForbidden {
		t.Fatalf("bob opens: %d", code)
	}
	if _, _, loc := do(anna, "POST", "/sign/hr/self/"+vacation, url.Values{"signer": {gus}}); !strings.Contains(loc, "#self") {
		t.Fatalf("a guest representative: %s", loc)
	}
	do(anna, "POST", "/sign/hr/self/"+vacation, url.Values{"signer": {anna}})
	do(anna, "POST", "/sign/hr/self/"+closed, url.Values{"signer": {anna}})
	must(srv.CRM.SetDocTemplateAccess(ctx, closed, []string{bob}))
	if list, err := srv.Sign.SelfTemplates(ctx); err != nil || len(list) != 2 || list[0].Signer != "Анна" {
		t.Fatalf("self templates: %+v %v", list, err)
	}
	if code, body, _ := do(anna, "GET", "/sign/hr", nil); code != http.StatusOK || !strings.Contains(body, `action="/sign/hr/self/`+vacation+`"`) {
		t.Fatalf("anna's HR page: %d", code)
	}

	// the employee sees the template open to them, its fields to fill
	if _, body, _ := do(eve, "GET", "/sign", nil); !strings.Contains(body, `href="/sign/my"`) {
		t.Fatal("no «Создать документ»")
	}
	if _, body, _ := do(eve, "GET", "/sign/my", nil); !strings.Contains(body, "Заявление на отпуск") || strings.Contains(body, "Служебная записка") {
		t.Fatal("eve's templates")
	}
	if code, body, _ := do(eve, "GET", "/sign/my?tpl="+vacation, nil); code != http.StatusOK || !strings.Contains(body, `type="date" id="sf-StartDate" name="v.StartDate"`) ||
		!strings.Contains(body, `name="v.Days"`) || strings.Contains(body, `name="v.Name"`) || strings.Contains(body, `v.MyCompany`) {
		t.Fatalf("eve's form: %d", code)
	}
	if code, _, _ := do(eve, "GET", "/sign/my?tpl="+closed, nil); code == http.StatusOK {
		t.Fatal("eve opens a closed template")
	}
	if _, _, loc := do(eve, "POST", "/sign/my", url.Values{"template": {closed}, "v.Text": {"x"}}); made.MatchString(loc) {
		t.Fatal("eve makes by a closed template")
	}
	if _, _, loc := do(gus, "POST", "/sign/my", url.Values{"template": {vacation}}); made.MatchString(loc) {
		t.Fatal("a guest makes a document")
	}
	if _, _, loc := do(eve, "POST", "/sign/my", url.Values{"template": {vacation}, "v.Days": {strings.Repeat("я", 2001)}}); made.MatchString(loc) {
		t.Fatal("a too long field")
	}

	// made and signed: the employee first, then the representative
	_, _, loc := do(eve, "POST", "/sign/my", url.Values{"template": {vacation}, "v.StartDate": {"2026-11-02"}, "v.Days": {"14"}})
	m := made.FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("made: %s", loc)
	}
	d, err := srv.Sign.Get(ctx, m[1])
	must(err)
	if d.Title != "Заявление на отпуск — Ева" || d.AuthorID != eve || len(d.Parties) != 2 || d.Employee().UserID != eve ||
		d.Current().UserID != eve || d.Parties[1].UserID != anna || notes(eve, "/sign/"+d.ID) != 0 {
		t.Fatalf("the document: %+v", d)
	}
	meta, err := srv.Storages.FileMeta(ctx, d.FileID)
	must(err)
	raw, err := srv.readFile(httptest.NewRequest("GET", "/", nil), meta, signMax)
	must(err)
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	must(err)
	rc, err := zr.Open("word/document.xml")
	must(err)
	x, _ := io.ReadAll(rc)
	if !strings.Contains(string(x), "№ 1. Ева, Бухгалтер: прошу отпуск с 02.11.2026 на 14 дней.") {
		t.Fatalf("the text: %s", x)
	}
	if code, _, _ := do(anna, "POST", "/sign/"+d.ID+"/sign", url.Values{"agree": {"1"}}); code != http.StatusSeeOther {
		t.Fatalf("anna before eve: %d", code)
	}
	if d, _ = srv.Sign.Get(ctx, d.ID); d.Current().UserID != eve {
		t.Fatal("anna signed before eve")
	}
	do(eve, "POST", "/sign/"+d.ID+"/sign", url.Values{"agree": {"1"}})
	if d, _ = srv.Sign.Get(ctx, d.ID); d.Status != "signing" || d.Current().UserID != anna || notes(anna, "/sign/"+d.ID) != 1 {
		t.Fatalf("eve signed: %+v", d)
	}
	do(anna, "POST", "/sign/"+d.ID+"/sign", url.Values{"agree": {"1"}})
	if d, _ = srv.Sign.Get(ctx, d.ID); d.Status != "done" {
		t.Fatalf("anna signed: %+v", d)
	}
	if _, body, _ := do(eve, "GET", "/sign?f=mine", nil); !strings.Contains(body, "/sign/"+d.ID) {
		t.Fatal("not in eve's documents")
	}

	// closed again
	do(anna, "POST", "/sign/hr/self/"+vacation, url.Values{"signer": {""}})
	if _, body, _ := do(eve, "GET", "/sign", nil); strings.Contains(body, `href="/sign/my"`) {
		t.Fatal("the button after closing")
	}
}
