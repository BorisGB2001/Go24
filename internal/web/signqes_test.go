package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
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
	"go24/internal/docgen"
	"go24/internal/i18n"
	"go24/internal/qes/qestest"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignQESHTTP: «Подпись» with КЭП — sent with «КЭП — все подписанты», the employee and the client
// sign the document's PDF with their certificates (the page has the plug-in's form, the client asks for no code); no
// signature, a signature of another document or by an expired certificate is refused; the good ones are kept with
// what the certificate says, given as .sig, and the signing sheet names the certificates.
func TestSignQESHTTP(t *testing.T) {
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
	bundle, err := i18n.Load("")
	must(err)
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Storages: storages.NewStore(pool, nil, s3), Sign: sign.NewStore(pool),
		CRM: crm.NewStore(pool), I18n: bundle, BaseURL: "https://go24", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, bob} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who string, req *http.Request) (int, []byte, string) {
		t.Helper()
		if who != "" {
			req.AddCookie(as[who])
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, raw, loc
	}
	get := func(who, path string) (int, []byte) {
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		code, body, _ := do(who, req)
		return code, body
	}
	post := func(who, path string, form url.Values) string {
		req, _ := http.NewRequest("POST", hs.URL+path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		_, _, loc := do(who, req)
		return loc
	}

	white := image.NewRGBA(image.Rect(0, 0, 200, 300))
	var pic bytes.Buffer
	must(png.Encode(&pic, white))
	page, err := docgen.ImagePDF(pic.Bytes())
	must(err)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("signer", bob)
	mw.WriteField("client_name", "Олег Клиентов")
	mw.WriteField("client_email", "oleg@t")
	mw.WriteField("qes", "all")
	fw, _ := mw.CreateFormFile("file", "Договор.pdf")
	fw.Write(page)
	mw.Close()
	req, _ := http.NewRequest("POST", hs.URL+"/sign", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	_, _, loc := do(anna, req)
	m := regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("sent: %s", loc)
	}
	id := m[1]
	d, err := srv.Sign.Get(ctx, id)
	must(err)
	if len(d.Parties) != 2 || !d.Parties[0].Qualified() || !d.Parties[1].Qualified() {
		t.Fatalf("the signers: %+v", d.Parties)
	}
	emp, cli := d.Parties[0], d.Parties[1]

	if code, html := get(bob, "/sign/"+id); code != http.StatusOK || !bytes.Contains(html, []byte("data-qes")) ||
		!bytes.Contains(html, []byte("cadesplugin_api.js")) || bytes.Contains(html, []byte("data-sgpad")) {
		t.Fatalf("the employee's page: %d %s", code, html)
	}
	if _, html := get(anna, "/sign/"+id); bytes.Contains(html, []byte("cadesplugin_api.js")) {
		t.Fatal("the sender's page loads the plug-in")
	}
	_, pdf := get(bob, "/sign/"+id+"/file/pdf")
	if !bytes.Equal(pdf, page) {
		t.Fatalf("the PDF to sign: %d bytes", len(pdf))
	}
	now := time.Now()
	b64 := func(sig []byte, err error) string {
		t.Helper()
		must(err)
		return base64.StdEncoding.EncodeToString(sig)
	}
	party := func(pid string) sign.Party {
		d, err := srv.Sign.Get(ctx, id)
		must(err)
		return *d.Party(pid)
	}
	for _, bad := range []string{"", b64(qestest.ECDSA([]byte("another document"), now.Add(-time.Hour), now.Add(time.Hour), now))} {
		if loc := post(bob, "/sign/"+id+"/sign", url.Values{"agree": {"1"}, "cms": {bad}}); !strings.Contains(loc, "msg=") {
			t.Fatalf("a bad signature: %s", loc)
		}
		if p := party(emp.ID); p.Status != "ready" {
			t.Fatalf("signed by a bad signature: %+v", p)
		}
	}
	good := b64(qestest.ECDSA(pdf, now.Add(-time.Hour), now.Add(time.Hour), now))
	post(bob, "/sign/"+id+"/sign", url.Values{"agree": {"1"}, "cms": {good}})
	d, err = srv.Sign.Get(ctx, id)
	must(err)
	if p := d.Parties[0]; p.Status != "signed" || p.QES == nil || p.QES.Owner != "ООО «Ромашка»" || p.QES.SNILS != "12345678901" || p.HasSignature {
		t.Fatalf("the employee: %+v %+v", p, p.QES)
	}
	if code, sig := get(anna, "/sign/"+id+"/qes/"+emp.ID); code != http.StatusOK || base64.StdEncoding.EncodeToString(sig) != good {
		t.Fatalf(".sig: %d", code)
	}
	if code, _ := get(anna, "/sign/"+id+"/qes/"+cli.ID); code != http.StatusNotFound {
		t.Fatalf(".sig of one not signed: %d", code)
	}

	// the client: the plug-in's form, no code asked for
	if code, html := get("", "/sg/"+cli.Token); code != http.StatusOK || !bytes.Contains(html, []byte("data-qes")) || bytes.Contains(html, []byte("/code\"")) {
		t.Fatalf("the client's page: %d %s", code, html)
	}
	expired := b64(qestest.ECDSA(pdf, now.Add(-48*time.Hour), now.Add(-24*time.Hour), now))
	post("", "/sg/"+cli.Token+"/sign", url.Values{"agree": {"1"}, "cms": {expired}})
	if p := party(cli.ID); p.Status != "ready" {
		t.Fatalf("signed by an expired certificate: %+v", p)
	}
	post("", "/sg/"+cli.Token+"/sign", url.Values{"agree": {"1"}, "cms": {b64(qestest.ECDSA(pdf, now.Add(-time.Hour), now.Add(time.Hour), now))}})
	d, err = srv.Sign.Get(ctx, id)
	must(err)
	if d.Status != "done" || d.Parties[1].QES == nil {
		t.Fatalf("the client signed: %s %+v", d.Status, d.Parties[1])
	}

	r := httptest.NewRequest("GET", "/", nil)
	sh := srv.signSheet(r, d)
	if !strings.Contains(sh.Rows[0][3], "КЭП: ООО «Ромашка», Иванов Иван Иванович (ИНН 7701234567, ОГРН 1027700000001, СНИЛС 12345678901)") ||
		!strings.Contains(strings.Join(sh.Lines, "\n"), "CAdES-BES") {
		t.Fatalf("the signing sheet: %q %q", sh.Rows, sh.Lines)
	}
	api := srv.apiSignOf(d, anna)
	if p := api.Parties[1]; p.Method != "qes" || p.QES == nil || p.QESFile != "/api/v1/sign/"+id+"/qes/"+cli.ID {
		t.Fatalf("REST: %+v", p)
	}
}
