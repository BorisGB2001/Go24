package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/docgen"
	"go24/internal/office"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignBlocksHTTP: the sender puts blocks on the pages of a PDF sent for signing — the editor shows
// the pages, the blocks are kept (of a signer by number; the others see no editor); signed, the PDF has the signer's
// signature and the date drawn in their places, the signing sheet after; signed — the blocks no longer change.
func TestSignBlocksHTTP(t *testing.T) {
	dbURL, s3ep := os.Getenv("GO24_TEST_DATABASE_URL"), os.Getenv("GO24_TEST_S3")
	if dbURL == "" || s3ep == "" {
		t.Skip("GO24_TEST_DATABASE_URL or GO24_TEST_S3 not set")
	}
	for _, tool := range []string{"pdfinfo", "pdftoppm", "pdfunite", "qpdf"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip("no " + tool)
		}
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
		CRM: crm.NewStore(pool), BaseURL: "https://go24", Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	white := image.NewRGBA(image.Rect(0, 0, 200, 300))
	for i := range white.Pix {
		white.Pix[i] = 255
	}
	var pic bytes.Buffer
	must(png.Encode(&pic, white))
	page, err := docgen.ImagePDF(pic.Bytes()) // the white A4 page: the document and what the document server makes
	must(err)
	fake := httptest.NewServer(nil)
	fake.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/out.pdf" {
			w.Write(page)
			return
		}
		io.WriteString(w, `{"endConvert": true, "fileUrl": "`+fake.URL+`/out.pdf"}`)
	})
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

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("signer", bob)
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

	// the editor: the sender's, with the pages and the signers
	if code, b := get(anna, "/sign/"+id); code != http.StatusOK || !bytes.Contains(b, []byte(`href="/sign/`+id+`/blocks/edit"`)) {
		t.Fatalf("doc page: %d", code)
	}
	if code, b := get(bob, "/sign/"+id); code != http.StatusOK || bytes.Contains(b, []byte(`/blocks/edit"`)) {
		t.Fatalf("the signer is offered the editor: %d", code)
	}
	code, b := get(anna, "/sign/"+id+"/blocks/edit")
	if code != http.StatusOK || !bytes.Contains(b, []byte(`data-page="1"`)) || bytes.Contains(b, []byte(`data-page="2"`)) ||
		!bytes.Contains(b, []byte(`<option value="1">1. Боб</option>`)) || !bytes.Contains(b, []byte(`data-kind="stamp" aria-pressed="false" disabled`)) {
		t.Fatalf("editor: %d %s", code, b)
	}
	if code, _ := get(bob, "/sign/"+id+"/blocks/edit"); code == http.StatusOK {
		t.Fatal("the editor opened to the signer")
	}
	code, b = get(bob, "/sign/"+id+"/page/1")
	if code != http.StatusOK || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatalf("page picture: %d", code)
	}
	if code, _ := get(anna, "/sign/"+id+"/page/2"); code != http.StatusNotFound {
		t.Fatalf("no page 2: %d", code)
	}

	// kept: the signature at the top left quarter, the date at the bottom right; a wrong signer — refused
	if loc := post(anna, "/sign/"+id+"/blocks", url.Values{"blocks": {`[{"signer": 2, "kind": "name", "page": 1, "x": 0.1, "y": 0.1, "w": 0.1, "h": 0.1}]`}}); !strings.Contains(loc, "/blocks/edit?") {
		t.Fatalf("a wrong signer: %s", loc)
	}
	blocks := `[{"signer": 1, "kind": "signature", "page": 1, "x": 0.05, "y": 0.05, "w": 0.4, "h": 0.4},
		{"signer": 1, "kind": "date", "page": 1, "x": 0.55, "y": 0.85, "w": 0.4, "h": 0.1},
		{"signer": 1, "kind": "field", "page": 1, "x": 0.05, "y": 0.6, "w": 0.4, "h": 0.1, "text": "ИНН", "required": true}]`
	if loc := post(anna, "/sign/"+id+"/blocks", url.Values{"blocks": {blocks}}); loc != "/sign/"+id+"?msg=Блоки сохранены: 3" {
		t.Fatalf("saved: %s", loc)
	}
	if code, b := get(anna, "/sign/"+id+"/blocks/edit"); code != http.StatusOK || !bytes.Contains(b, []byte(`&#34;kind&#34;:&#34;date&#34;`)) {
		t.Fatalf("editor with the blocks: %s", b)
	}

	// bob signs with a black signature
	black := image.NewNRGBA(image.Rect(0, 0, 40, 40))
	for y := 0; y < 40; y++ {
		for x := 0; x < 40; x++ {
			black.Set(x, y, color.NRGBA{0, 0, 0, 255})
		}
	}
	var sig bytes.Buffer
	must(png.Encode(&sig, black))
	// the field to fill in is on bob's form; left empty, he cannot sign
	bs, err := srv.Sign.Blocks(ctx, id)
	must(err)
	field := bs[1].ID
	if code, b := get(bob, "/sign/"+id); code != http.StatusOK || !bytes.Contains(b, []byte(`name="field_`+field+`" value="" maxlength="200" required`)) {
		t.Fatalf("bob's fields: %d %s", code, b)
	}
	signed := url.Values{"agree": {"1"}, "signature": {"data:image/png;base64," + base64.StdEncoding.EncodeToString(sig.Bytes())}}
	if loc := post(bob, "/sign/"+id+"/sign", signed); !strings.Contains(loc, "Заполните обязательные поля") {
		t.Fatalf("signed with the field empty: %s", loc)
	}
	signed.Set("field_"+field, "7701234567")
	post(bob, "/sign/"+id+"/sign", signed)
	d, err := srv.Sign.Get(ctx, id)
	must(err)
	if d.Status != "done" || d.ResultFile == "" {
		t.Fatalf("signed: %+v", d)
	}
	code, res := get(anna, "/sign/"+id+"/file/result")
	if code != http.StatusOK || d.ResultHash != sign.Hash(res) {
		t.Fatalf("result: %d", code)
	}
	pages, err := docgen.PDFPages(ctx, res)
	if err != nil || len(pages) != 2 {
		t.Fatalf("result pages: %v %v", pages, err)
	}
	shot, err := docgen.PDFPagePNG(ctx, res, 1, 200)
	must(err)
	img, err := png.Decode(bytes.NewReader(shot))
	must(err)
	h := img.Bounds().Dy()
	dark := func(x0, y0, x1, y1 int) int {
		n := 0
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if r, _, _, _ := img.At(x, y).RGBA(); r < 0x8000 {
					n++
				}
			}
		}
		return n
	}
	if dark(10, h*60/100, 90, h*70/100) < 20 {
		t.Fatalf("the field's value on the page: %d", dark(10, h*60/100, 90, h*70/100))
	}
	if dark(20, h/10, 80, h/4) < 1000 || dark(110, h*85/100, 200, h*95/100) < 20 || dark(110, h/10, 200, h/2) > 0 {
		t.Fatalf("blocks on the page: signature %d, date %d, elsewhere %d", dark(20, h/10, 80, h/4), dark(110, h*85/100, 200, h*95/100), dark(110, h/10, 200, h/2))
	}

	// signed: no editor, no change; REST gives the blocks
	if code, b := get(anna, "/sign/"+id); code != http.StatusOK || bytes.Contains(b, []byte(`/blocks/edit"`)) || !bytes.Contains(b, []byte("Блоков на страницах: 3")) ||
		!bytes.Contains(b, []byte("<div>ИНН: 7701234567</div>")) {
		t.Fatalf("signed doc page: %d", code)
	}
	if loc := post(anna, "/sign/"+id+"/blocks", url.Values{"blocks": {"[]"}}); !strings.Contains(loc, "/blocks/edit?") {
		t.Fatalf("changed after signing: %s", loc)
	}
	bs, err = srv.Sign.Blocks(ctx, id)
	must(err)
	raw, _ := json.Marshal(signBlocksJSON(d, bs))
	if !strings.Contains(string(raw), `"signer":1,"kind":"signature","page":1`) || !strings.Contains(string(raw), `"text":"ИНН","required":true,"value":"7701234567"`) || len(bs) != 3 {
		t.Fatalf("blocks: %s", raw)
	}
}
