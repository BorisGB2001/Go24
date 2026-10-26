package web

import (
	"archive/zip"
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

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSignHTTP: a document of a CRM card is sent for signing to an employee and the client; the employee signs in
// go24 (having confirmed reading it), the client by the link with the code; the signed document has the signing
// sheet; who is not of the document does not see it; a Word file uploaded is sent too.
func TestSignHTTP(t *testing.T) {
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
	var anna, bob, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('bob', 'Боб', 'bob@t') RETURNING id`).Scan(&bob))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, s3), Keys: apikeys.NewStore(pool), Sign: sign.NewStore(pool), BaseURL: "https://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Наблюдатель", Perms: crm.Perms{"deal": {"read": "all"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, eve, true))
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", Amount: 1500, PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)

	var src bytes.Buffer
	zw := zip.NewWriter(&src)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:body><w:p><w:r><w:t>Договор поставки</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`)
	zw.Close()
	key := "go24/crm-documents/t/dog.docx"
	must(srv.Storages.Put(ctx, anna, false, "", key, bytes.NewReader(src.Bytes()), int64(src.Len()), docxType))
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: "dog.docx", ContentType: docxType, Size: int64(src.Len()), UploadedBy: anna})
	must(err)
	docID, err := srv.CRM.AddDocument(ctx, crm.Document{TemplateName: "Договор", Entity: "deal", EntityID: deal, Number: "7", FileID: file,
		Values: map[string]string{"Client.Director": "Иван Петров", "Client.Email": "ivan@client.test"}, AuthorID: anna})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, bob, eve} {
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

	// the form, filled from the card's document
	code, body, _ := do(anna, "GET", "/sign/new?kind=deals&id="+deal+"&doc="+docID, nil)
	if code != http.StatusOK || !strings.Contains(body, "Договор № 7") || !strings.Contains(body, "Иван Петров") || !strings.Contains(body, "ivan@client.test") {
		t.Fatalf("new form: %d", code)
	}
	if code, _, _ := do(eve, "GET", "/sign/new?kind=deals&id="+deal+"&doc="+docID, nil); code != http.StatusForbidden && code != http.StatusNotFound {
		t.Fatalf("eve may not change the card: %d", code)
	}
	code, _, loc := do(anna, "POST", "/sign", url.Values{"kind": {"deals"}, "id": {deal}, "doc": {docID}, "title": {"Договор № 7"},
		"signer": {bob}, "client_name": {"Иван Петров"}, "client_email": {"ivan@client.test"}})
	m := regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc)
	if code != http.StatusSeeOther || m == nil {
		t.Fatalf("send: %d %s", code, loc)
	}
	id := m[1]
	d, err := srv.Sign.Get(ctx, id)
	must(err)
	if d.FileID == file || d.Hash != sign.Hash(src.Bytes()) || d.CRMDocID != docID || len(d.Parties) != 2 {
		t.Fatalf("kept: %+v", d)
	}
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'sign_request' AND url = $2`, bob, "/sign/"+id).Scan(&n))
	if n != 1 {
		t.Fatalf("bob notified: %d", n)
	}
	if code, body, _ := do(anna, "GET", "/crm/deals/"+deal, nil); code != http.StatusOK || !strings.Contains(body, `href="/sign/`+id+`"`) {
		t.Fatalf("card: %d", code)
	}

	// who sees it; the employee signs
	if code, _, _ := do(eve, "GET", "/sign/"+id, nil); code != http.StatusNotFound {
		t.Fatalf("eve sees: %d", code)
	}
	if code, body, _ := do(bob, "GET", "/sign/"+id, nil); code != http.StatusOK || !strings.Contains(body, `action="/sign/`+id+`/sign"`) {
		t.Fatalf("bob's page: %d", code)
	}
	if code, body, _ := do(anna, "GET", "/sign/"+id, nil); code != http.StatusOK || strings.Contains(body, `action="/sign/`+id+`/sign"`) ||
		!strings.Contains(body, "sgev-viewed") || strings.Contains(body, "01.01.0001") {
		t.Fatalf("anna's page: %d", code)
	}
	if code, body, _ := do(bob, "GET", "/sign?f=me", nil); code != http.StatusOK || !strings.Contains(body, "/sign/"+id) {
		t.Fatalf("bob's list: %d", code)
	}
	if code, body, _ := do(eve, "GET", "/sign", nil); code != http.StatusOK || strings.Contains(body, "/sign/"+id) {
		t.Fatalf("eve's list: %d", code)
	}
	// how often the signer is reminded: the sender changes it, the signer cannot
	if code, body, _ := do(anna, "GET", "/sign/"+id, nil); code != http.StatusOK || !strings.Contains(body, `action="/sign/`+id+`/remind"`) {
		t.Fatalf("anna's reminders: %d", code)
	}
	if code, body, _ := do(bob, "GET", "/sign/"+id, nil); code != http.StatusOK || strings.Contains(body, `/remind"`) {
		t.Fatalf("bob's reminders: %d", code)
	}
	if code, _, _ := do(bob, "POST", "/sign/"+id+"/remind", url.Values{"remind": {"0"}}); code != http.StatusNotFound {
		t.Fatalf("bob changes reminders: %d", code)
	}
	if d, _ := srv.Sign.Get(ctx, id); d.RemindDays != 1 {
		t.Fatalf("reminders by default: %d", d.RemindDays)
	}
	do(anna, "POST", "/sign/"+id+"/remind", url.Values{"remind": {"3"}})
	if d, _ := srv.Sign.Get(ctx, id); d.RemindDays != 3 {
		t.Fatalf("reminders changed: %d", d.RemindDays)
	}
	if _, _, loc := do(anna, "POST", "/sign/"+id+"/sign", url.Values{"agree": {"1"}}); !strings.Contains(loc, "msg=") {
		t.Fatalf("anna signs for bob: %s", loc)
	}
	do(bob, "POST", "/sign/"+id+"/sign", nil) // not confirmed reading
	if d, _ = srv.Sign.Get(ctx, id); d.Parties[0].Status != "ready" {
		t.Fatalf("signed with no confirmation: %+v", d.Parties[0])
	}
	// «Ваша подпись»: a picture that is not one is refused; bob signs with a drawn one and keeps it
	if code, body, _ := do(bob, "GET", "/sign/"+id, nil); code != http.StatusOK || !strings.Contains(body, "data-sgpad") || strings.Contains(body, "my-signature/forget") {
		t.Fatalf("bob's pad: %d", code)
	}
	var pic bytes.Buffer
	must(png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 300, 100))))
	drawn := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pic.Bytes())
	do(bob, "POST", "/sign/"+id+"/sign", url.Values{"agree": {"1"}, "signature": {"data:image/png;base64,AAAA"}})
	if d, _ = srv.Sign.Get(ctx, id); d.Parties[0].Status != "ready" {
		t.Fatalf("signed with a bad signature: %+v", d.Parties[0])
	}
	do(bob, "POST", "/sign/"+id+"/sign", url.Values{"agree": {"1"}, "signature": {drawn}, "keep": {"1"}})
	if d, _ = srv.Sign.Get(ctx, id); d.Parties[0].Status != "signed" || d.Parties[1].Status != "ready" || d.Parties[0].IP == "" ||
		!d.Parties[0].HasSignature || d.Parties[1].HasSignature {
		t.Fatalf("bob signed: %+v", d.Parties)
	}
	if kept, err := srv.Sign.MySignature(ctx, bob); err != nil || kept == nil {
		t.Fatalf("bob's signature kept: %v", err)
	}
	if code, body, _ := do(anna, "GET", "/sign/"+id+"/signature/"+d.Parties[0].ID, nil); code != http.StatusOK || !strings.HasPrefix(body, "\x89PNG") {
		t.Fatalf("bob's signature: %d", code)
	}
	if code, _, _ := do(eve, "GET", "/sign/"+id+"/signature/"+d.Parties[0].ID, nil); code != http.StatusNotFound {
		t.Fatalf("eve sees the signature: %d", code)
	}
	if code, body, _ := do(anna, "GET", "/sign/"+id, nil); code != http.StatusOK || !strings.Contains(body, `class="sgsig"`) {
		t.Fatalf("the signature on the page: %d", code)
	}

	// the client: the page by the link, a wrong code, the right one
	tok := d.Parties[1].Token
	if code, body, _ := do("", "GET", "/sg/"+tok, nil); code != http.StatusOK || !strings.Contains(body, "Договор № 7") || !strings.Contains(body, "i***@client.test") {
		t.Fatalf("client's page: %d", code)
	}
	if code, _, _ := do("", "GET", "/sg/nope", nil); code != http.StatusNotFound {
		t.Fatalf("a wrong link: %d", code)
	}
	if code, _, loc := do("", "POST", "/sg/"+tok+"/code", nil); code != http.StatusSeeOther || strings.Contains(loc, "code=1") {
		t.Fatalf("code with no mail: %d %s", code, loc) // go24 with no mail: the code is not sent
	}
	must(srv.Sign.SetCode(ctx, d.Parties[1].ID, "424242"))
	if _, _, loc := do("", "POST", "/sg/"+tok+"/sign", url.Values{"code": {"000000"}, "agree": {"1"}}); !strings.Contains(loc, "code=1") {
		t.Fatalf("wrong code: %s", loc)
	}
	if code, body, _ := do("", "GET", "/sg/"+tok+"?code=1", nil); code != http.StatusOK || !strings.Contains(body, "data-sgpad") || !strings.Contains(body, `data-name="Иван Петров"`) {
		t.Fatalf("client's pad: %d", code)
	}
	if _, _, loc := do("", "POST", "/sg/"+tok+"/sign", url.Values{"code": {"424242"}, "agree": {"1"}, "signature": {"nope"}}); !strings.Contains(loc, "code=1") {
		t.Fatalf("a bad signature: %s", loc)
	}
	if _, _, loc := do("", "POST", "/sg/"+tok+"/sign", url.Values{"code": {"424242"}, "agree": {"1"}, "signature": {drawn}}); strings.Contains(loc, "code=1") {
		t.Fatalf("right code: %s", loc)
	}
	d, err = srv.Sign.Get(ctx, id)
	must(err)
	if d.Status != "done" || d.ResultFile == "" {
		t.Fatalf("done: %+v", d)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'sign_done'`, anna).Scan(&n))
	if n != 1 {
		t.Fatalf("anna notified: %d", n)
	}
	// the history: the employee told, opened it, signed; the client told, opened the link, asked for a code, signed
	evs, err := srv.Sign.Events(ctx, id)
	must(err)
	kinds := []string{}
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
	}
	if got := strings.Join(kinds, ","); got != "created,sent,viewed,signed,sent,viewed,code,signed,done" {
		t.Fatalf("history: %s", got)
	}
	if code, body, _ := do(anna, "GET", "/sign/"+id, nil); code != http.StatusOK || !strings.Contains(body, "sgev-viewed") ||
		!strings.Contains(body, "sgev-done") || strings.Contains(body, "01.01.0001") {
		t.Fatalf("anna's history: %d", code)
	}
	code, result, _ := do(anna, "GET", "/sign/"+id+"/file/result", nil)
	if code != http.StatusOK {
		t.Fatalf("result: %d", code)
	}
	zr, err := zip.NewReader(strings.NewReader(result), int64(len(result)))
	must(err)
	rc, err := zr.Open("word/document.xml")
	must(err)
	x, _ := io.ReadAll(rc)
	for _, want := range []string{"Договор поставки", d.Hash, "Боб", "Иван Петров", "ivan@client.test", `r:embed="rIdGo24Sig1"`, `r:embed="rIdGo24Sig2"`} {
		if !strings.Contains(string(x), want) {
			t.Fatalf("signing sheet has no %q: %s", want, x)
		}
	}
	if _, err := zr.Open("word/media/rIdGo24Sig2.png"); err != nil {
		t.Fatalf("the client's signature: %v", err)
	}
	if code, _, _ := do("", "GET", "/sg/"+tok+"/file/result", nil); code != http.StatusOK {
		t.Fatalf("client downloads: %d", code)
	}
	if code, _, _ := do(eve, "GET", "/sign/"+id+"/file/result", nil); code != http.StatusNotFound {
		t.Fatalf("eve downloads: %d", code)
	}

	// a Word file uploaded, signed by the employee alone; then recalled one
	upload := func(name string, data []byte, fields ...string) (int, string) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		if fields == nil {
			fields = []string{"signer", anna}
		}
		for i := 0; i+1 < len(fields); i += 2 {
			mw.WriteField(fields[i], fields[i+1])
		}
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/sign", &b)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		code, _, loc := send(anna, req)
		return code, loc
	}
	if _, loc := upload("x.txt", []byte("text")); !strings.HasPrefix(loc, "/sign/new?msg=") {
		t.Fatalf("not a docx: %s", loc)
	}
	_, loc = upload("Акт сверки.docx", src.Bytes())
	m = regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc)
	if m == nil {
		t.Fatalf("upload: %s", loc)
	}
	d2, _ := srv.Sign.Get(ctx, m[1])
	if d2.Title != "Акт сверки" || len(d2.Parties) != 1 || d2.Entity != "" {
		t.Fatalf("uploaded: %+v", d2)
	}
	if code, _, _ := do(bob, "POST", "/sign/"+d2.ID+"/cancel", nil); code != http.StatusNotFound {
		t.Fatalf("bob recalls anna's: %d", code)
	}
	do(anna, "POST", "/sign/"+d2.ID+"/cancel", nil)
	if d2, _ = srv.Sign.Get(ctx, d2.ID); d2.Status != "cancelled" {
		t.Fatalf("recalled: %+v", d2)
	}

	if code, body, _ := do(anna, "GET", "/sign/new", nil); code != http.StatusOK || !strings.Contains(body, `data-add-row="sg-client"`) {
		t.Fatalf("the form with no card's document: %d", code)
	}
	// «Сохранить как шаблон»: the document, the signers and the reminders; sent by the template with a client only
	_, loc = upload("Договор оферты.docx", src.Bytes(), "signer", anna, "signer", bob, "remind", "3", "save_template", "1")
	tpls, err := srv.Sign.Templates(ctx)
	must(err)
	if !strings.HasPrefix(loc, "/sign/") || len(tpls) != 1 || tpls[0].Title != "Договор оферты" || len(tpls[0].Signers) != 2 ||
		tpls[0].Signers[1] != bob || tpls[0].RemindDays != 3 || tpls[0].FileName != "Договор оферты.docx" {
		t.Fatalf("template: %s %+v", loc, tpls)
	}
	if code, body, _ := do(bob, "GET", "/sign?f=templates", nil); code != http.StatusOK || !strings.Contains(body, "/sign/new?tpl="+tpls[0].ID) ||
		strings.Contains(body, "/sign/templates/"+tpls[0].ID+"/delete") {
		t.Fatalf("bob's templates: %d", code)
	}
	if code, body, _ := do(bob, "GET", "/sign/new?tpl="+tpls[0].ID, nil); code != http.StatusOK || !strings.Contains(body, `name="tpl" value="`+tpls[0].ID+`"`) ||
		!strings.Contains(body, `<option value="3" selected>`) || strings.Contains(body, `name="save_template"`) {
		t.Fatalf("the form by the template: %d", code)
	}
	if code, _, loc := do(bob, "POST", "/sign", url.Values{"tpl": {tpls[0].ID}, "signer": {bob}, "remind": {"3"},
		"client_name": {"Ольга"}, "client_email": {"olga@t"}}); code != http.StatusSeeOther || !strings.HasPrefix(loc, "/sign/") {
		t.Fatalf("sent by the template: %d %s", code, loc)
	} else {
		d, _ := srv.Sign.Get(ctx, regexp.MustCompile(`^/sign/([0-9a-f-]{36})`).FindStringSubmatch(loc)[1])
		if d.Title != "Договор оферты" || d.Hash != sign.Hash(src.Bytes()) || len(d.Parties) != 2 || d.Parties[1].Email != "olga@t" || d.RemindDays != 3 {
			t.Fatalf("by the template: %+v", d)
		}
		// bob's kept signature: offered on the page, signed with when none is given; then forgotten
		if code, body, _ := do(bob, "GET", "/sign/"+d.ID, nil); code != http.StatusOK || !strings.Contains(body, `value="data:image/png;base64,`) ||
			!strings.Contains(body, "my-signature/forget") {
			t.Fatalf("bob's kept signature: %d", code)
		}
		do(bob, "POST", "/sign/"+d.ID+"/sign", url.Values{"agree": {"1"}})
		if d, _ = srv.Sign.Get(ctx, d.ID); d.Parties[0].Status != "signed" || !d.Parties[0].HasSignature {
			t.Fatalf("signed with the kept signature: %+v", d.Parties[0])
		}
		if _, _, loc := do(bob, "POST", "/sign/my-signature/forget", url.Values{"back": {"//evil"}}); loc != "/sign" {
			t.Fatalf("forget: %s", loc)
		}
		if kept, _ := srv.Sign.MySignature(ctx, bob); kept != nil {
			t.Fatal("the signature is not forgotten")
		}
	}
	if code, _, _ := do(bob, "POST", "/sign/templates/"+tpls[0].ID+"/delete", nil); code != http.StatusNotFound {
		t.Fatalf("bob deletes anna's template: %d", code)
	}
	do(anna, "POST", "/sign/templates/"+tpls[0].ID+"/delete", nil)
	if tpls, _ = srv.Sign.Templates(ctx); len(tpls) != 0 {
		t.Fatal("deleted")
	}

	// several signers of each side, in turn: the employees, then the clients; an empty row of a client left out
	if _, loc := upload("Договор.docx", src.Bytes(), "client_name", "Иван", "client_email", "ivan@t"); !strings.HasPrefix(loc, "/sign/new?msg=") {
		t.Fatalf("no employee: %s", loc)
	}
	_, loc = upload("Договор.docx", src.Bytes(), "signer", anna, "signer", bob, "signer", anna,
		"client_name", "Иван", "client_email", "ivan@t", "client_name", "", "client_email", "", "client_name", "Пётр", "client_email", "petr@t")
	if m = regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc); m == nil {
		t.Fatalf("several: %s", loc)
	}
	d3, _ := srv.Sign.Get(ctx, m[1])
	if len(d3.Parties) != 4 || d3.Parties[0].UserID != anna || d3.Parties[1].UserID != bob || d3.Parties[2].Email != "ivan@t" ||
		d3.Parties[3].Name != "Пётр" || d3.Current().UserID != anna {
		t.Fatalf("several: %+v", d3.Parties)
	}
	do(anna, "POST", "/sign/"+d3.ID+"/sign", url.Values{"agree": {"1"}})
	do(bob, "POST", "/sign/"+d3.ID+"/sign", url.Values{"agree": {"1"}})
	if d3, _ = srv.Sign.Get(ctx, d3.ID); d3.Current().Email != "ivan@t" || d3.Parties[3].Status != "wait" {
		t.Fatalf("the clients' turn: %+v", d3.Parties)
	}

	// a reviewer approves before the signers, with no signature; an observer sees the document and is told how it ended
	_, loc = upload("Акт.docx", src.Bytes(), "reviewer", eve, "signer", anna, "observer", bob, "save_template", "1")
	if m = regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc); m == nil {
		t.Fatalf("with a reviewer: %s", loc)
	}
	d4, _ := srv.Sign.Get(ctx, m[1])
	if len(d4.Parties) != 2 || !d4.Parties[0].Reviewer() || d4.Parties[0].UserID != eve || len(d4.Observers) != 1 || d4.Observers[0].UserID != bob {
		t.Fatalf("with a reviewer: %+v", d4)
	}
	if tpls, _ = srv.Sign.Templates(ctx); len(tpls) != 1 || len(tpls[0].Reviewers) != 1 || tpls[0].Reviewers[0] != eve || len(tpls[0].Observers) != 1 {
		t.Fatalf("the template with a reviewer: %+v", tpls)
	}
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'sign_review' AND url = $2`, eve, "/sign/"+d4.ID).Scan(&n))
	if n != 1 {
		t.Fatalf("eve asked to approve: %d", n)
	}
	if code, body, _ := do(eve, "GET", "/sign/"+d4.ID, nil); code != http.StatusOK || strings.Contains(body, "data-sgpad") || !strings.Contains(body, `name="refuse"`) {
		t.Fatalf("eve's page: %d", code)
	}
	if code, body, _ := do(bob, "GET", "/sign/"+d4.ID, nil); code != http.StatusOK || strings.Contains(body, `action="/sign/`+d4.ID+`/sign"`) ||
		!strings.Contains(body, `data-user="`+bob+`"`) {
		t.Fatalf("the observer's page: %d", code)
	}
	if code, body, _ := do(bob, "GET", "/sign", nil); code != http.StatusOK || !strings.Contains(body, "/sign/"+d4.ID) {
		t.Fatalf("the observer's list: %d", code)
	}
	do(eve, "POST", "/sign/"+d4.ID+"/sign", url.Values{"agree": {"1"}})
	if d4, _ = srv.Sign.Get(ctx, d4.ID); d4.Parties[0].Status != "signed" || d4.Parties[0].HasSignature || d4.Current().UserID != anna {
		t.Fatalf("approved: %+v", d4.Parties)
	}
	do(anna, "POST", "/sign/"+d4.ID+"/cancel", nil)
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'sign_cancelled' AND url = $2`, bob, "/sign/"+d4.ID).Scan(&n))
	if n != 1 {
		t.Fatalf("the observer told of the recall: %d", n)
	}
	code, _, loc = do(anna, "POST", "/sign", url.Values{"tpl": {tpls[0].ID}, "signer": {anna}, "reviewer": {eve}, "observer": {bob}})
	if m = regexp.MustCompile(`^/sign/([0-9a-f-]{36})\?msg=`).FindStringSubmatch(loc); m == nil {
		t.Fatalf("by the template with a reviewer: %d %s", code, loc)
	}
	do(eve, "POST", "/sign/"+m[1]+"/sign", url.Values{"refuse": {"1"}, "reason": {"не та цена"}})
	d5, _ := srv.Sign.Get(ctx, m[1])
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id IN ($1, $2) AND kind = 'sign_refused' AND url = $3`, anna, bob, "/sign/"+d5.ID).Scan(&n))
	if d5.Status != "refused" || d5.Parties[0].Reason != "не та цена" || n != 2 {
		t.Fatalf("rejected: %d %+v", n, d5)
	}
}
