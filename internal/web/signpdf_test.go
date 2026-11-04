package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/docgen"
	"go24/internal/office"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignPDF: not only Word is sent for signing — a PDF as it is, a picture as a page of PDF, an RTF as
// the PDF the document server makes; another file is refused. A PDF is read as it is, and signed it gets the signing
// sheet as its last page.
func TestSignPDF(t *testing.T) {
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
	var anna, bob string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), Storages: storages.NewStore(pool, nil, s3), Sign: sign.NewStore(pool),
		BaseURL: "https://go24", Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	var pic bytes.Buffer
	must(png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 300, 100))))
	page, err := docgen.ImagePDF(pic.Bytes()) // what the document server «makes» of any file
	must(err)
	var converted []string
	var fake *httptest.Server
	fake = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/out.pdf" {
			w.Write(page)
			return
		}
		var req struct{ URL, Token, Outputtype, Filetype string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if res, err := http.Get(req.URL); err != nil || res.StatusCode != http.StatusOK || req.Outputtype != "pdf" {
			io.WriteString(w, `{"error": -4}`)
			return
		}
		converted = append(converted, req.Filetype)
		io.WriteString(w, `{"endConvert": true, "fileUrl": "`+fake.URL+`/out.pdf"}`)
	}))
	defer fake.Close()
	srv.Office = office.New(fake.URL, "secret", "http://go24")
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	srv.Office.Self = hs.URL
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, bob} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who string, req *http.Request) (int, []byte, string) {
		t.Helper()
		req.AddCookie(as[who])
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		loc, _ := url.QueryUnescape(res.Header.Get("Location"))
		return res.StatusCode, raw, loc
	}
	send := func(name string, data []byte) (sign.Doc, string) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("signer", bob)
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/sign", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		_, _, loc := do(anna, req)
		m := regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc)
		if m == nil {
			return sign.Doc{}, loc
		}
		d, err := srv.Sign.Get(ctx, m[1])
		must(err)
		return d, loc
	}
	name := func(d sign.Doc) string {
		m, err := srv.Storages.FileMeta(ctx, d.FileID)
		must(err)
		return m.Name + " " + m.ContentType
	}
	get := func(who, path string) (int, []byte) {
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		code, body, _ := do(who, req)
		return code, body
	}

	// the form says what may be sent
	if code, body := get(anna, "/sign/new"); code != http.StatusOK || !bytes.Contains(body, []byte(`accept=".docx,.pdf,.jpg,.jpeg,.png,.doc,.odt,.rtf"`)) {
		t.Fatalf("form: %d", code)
	}
	// a PDF: kept and read as it is, the document server not asked
	src, _ := docgen.ImagePDF(pic.Bytes())
	src = append(src, "% the sender's own\n"...)
	d, loc := send("Акт сверки.pdf", src)
	if d.ID == "" || d.Title != "Акт сверки" || d.Hash != sign.Hash(src) || name(d) != "Акт сверки.pdf application/pdf" {
		t.Fatalf("pdf sent: %s %+v", loc, d)
	}
	if code, body := get(bob, "/sign/"+d.ID+"/file/pdf?inline=1"); code != http.StatusOK || !bytes.Equal(body, src) {
		t.Fatalf("pdf read: %d", code)
	}
	if code, body := get(bob, "/sign/"+d.ID+"/file/docx"); code != http.StatusOK || !bytes.Equal(body, src) {
		t.Fatalf("pdf downloaded: %d", code)
	}
	if len(converted) != 0 {
		t.Fatalf("converted: %v", converted)
	}
	// a picture becomes a page of PDF
	if p, loc := send("Скан.png", pic.Bytes()); p.ID == "" || name(p) != "Скан.pdf application/pdf" {
		t.Fatalf("png: %s", loc)
	} else if code, body := get(bob, "/sign/"+p.ID+"/file/pdf"); code != http.StatusOK || !docgen.IsPDF(body) {
		t.Fatalf("png read: %d", code)
	}
	// an RTF: the PDF the document server makes of it
	if p, loc := send("Письмо.rtf", []byte(`{\rtf1 Письмо}`)); p.ID == "" || name(p) != "Письмо.pdf application/pdf" || p.Hash != sign.Hash(page) ||
		strings.Join(converted, ",") != "rtf" {
		t.Fatalf("rtf: %s %v", loc, converted)
	}
	var files int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE name LIKE 'Письмо.%'`).Scan(&files))
	if files != 1 { // the RTF and the PDF made of it are not left behind
		t.Fatalf("files of the rtf: %d", files)
	}
	// several files are one document: the pictures pages of their own, in the order chosen
	sendMany := func(files ...string) (sign.Doc, string) {
		t.Helper()
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		mw.WriteField("signer", bob)
		for i := 0; i < len(files); i += 2 {
			fw, _ := mw.CreateFormFile("file", files[i])
			io.WriteString(fw, files[i+1])
		}
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/sign", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		_, _, loc := do(anna, req)
		m := regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc)
		if m == nil {
			return sign.Doc{}, loc
		}
		d, err := srv.Sign.Get(ctx, m[1])
		must(err)
		return d, loc
	}
	converted = nil
	if p, loc := sendMany("Стр. 1.png", pic.String(), "Стр. 2.png", pic.String(), "Стр. 3.png", pic.String()); p.ID == "" ||
		p.Title != "Стр. 1" || name(p) != "Стр. 1.pdf application/pdf" || len(converted) != 0 {
		t.Fatalf("pages: %s %v", loc, converted)
	} else if code, body := get(bob, "/sign/"+p.ID+"/file/pdf"); code != http.StatusOK || !docgen.IsPDF(body) ||
		bytes.Count(body, []byte("/Type /Page /Parent")) != 3 {
		t.Fatalf("pages read: %d", code)
	}
	if p, loc := sendMany("Стр. 1.png", pic.String(), "notes.txt", "text"); p.ID != "" || !strings.Contains(loc, "/sign/new") {
		t.Fatalf("pages with a text: %s", loc)
	}
	if _, err := exec.LookPath("pdfunite"); err == nil { // a picture, a PDF and an RTF the document server makes a PDF
		if p, loc := sendMany("Скан.png", pic.String(), "Акт.pdf", string(src), "Письмо.rtf", `{\rtf1 Письмо}`); p.ID == "" ||
			name(p) != "Скан.pdf application/pdf" || strings.Join(converted, ",") != "rtf" {
			t.Fatalf("mixed: %s %v", loc, converted)
		}
	}
	converted = nil

	// anything else is refused
	for n, data := range map[string][]byte{"notes.txt": []byte("text"), "fake.pdf": []byte("text"), "fake.png": []byte("text"), "fake.docx": src} {
		if p, loc := send(n, data); p.ID != "" || !strings.Contains(loc, "/sign/new") {
			t.Fatalf("%s sent: %s", n, loc)
		}
	}

	// bob signs the PDF: the signed one is the PDF with the signing sheet after its pages
	drawn := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pic.Bytes())
	req, _ := http.NewRequest("POST", hs.URL+"/sign/"+d.ID+"/sign", strings.NewReader(url.Values{"agree": {"1"}, "signature": {drawn}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	do(bob, req)
	d, err = srv.Sign.Get(ctx, d.ID)
	must(err)
	if d.Status != "done" || converted[len(converted)-1] != "docx" {
		t.Fatalf("signed: %s %v", d.Status, converted)
	}
	if _, err := exec.LookPath("pdfunite"); err != nil {
		if d.ResultFile != "" {
			t.Fatal("joined with no pdfunite")
		}
		t.Log("no pdfunite: the signed PDF is not checked")
		return
	}
	code, body := get(anna, "/sign/"+d.ID+"/file/result")
	if code != http.StatusOK || !docgen.IsPDF(body) || d.ResultHash != sign.Hash(body) || len(body) <= len(src) {
		t.Fatalf("result: %d %d", code, len(body))
	}
	m, _ := srv.Storages.FileMeta(ctx, d.ResultFile)
	if !strings.HasPrefix(m.Name, "Акт сверки — ") || !strings.HasSuffix(m.Name, ".pdf") {
		t.Fatalf("result name: %s", m.Name)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE name LIKE '%.docx'`).Scan(&files))
	if files != 0 { // the sheet made in Word is not left behind
		t.Fatalf("sheet left: %d", files)
	}
}
