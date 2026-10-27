package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"html"
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

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/calendar"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/inwords"
	"go24/internal/office"
	"go24/internal/payqr"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestCRMDocumentsHTTP: a document made from a template is kept at the card with the template's number
// (Д-{YEAR}/{NUMBER}); «Изменить значения» lists the template's fields with the card's values, the document is made
// with the changed ones and made again keeping its number; who only reads the card downloads it but may not delete it.
func TestCRMDocumentsHTTP(t *testing.T) {
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
	var anna, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, s3), Keys: apikeys.NewStore(pool),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Наблюдатель", Perms: crm.Perms{"deal": {"read": "all"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, eve, true))
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", Amount: 1500, PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)

	var tpl bytes.Buffer
	zw := zip.NewWriter(&tpl)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:t>Договор № {DocumentNumber} от {DocumentDate}: {Title}, {Amount} {Currency}</w:t></w:document>`)
	zw.Close()
	key := "go24/doc-templates/t/dog.docx"
	must(srv.Storages.Put(ctx, anna, false, "", key, bytes.NewReader(tpl.Bytes()), int64(tpl.Len()), docxType))
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: "dog.docx", ContentType: docxType, Size: int64(tpl.Len()), UploadedBy: anna})
	must(err)
	must(srv.CRM.AddDocTemplate(ctx, anna, "Договор", "deal", file))
	tpls, _ := srv.CRM.DocTemplates(ctx, "deal")

	// the document server: converts what go24 gives it (through the signed link) into a «PDF»
	converted := 0
	var edited []byte // the template edited in the editor
	var fake *httptest.Server
	fake = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/edited.docx" {
			w.Write(edited)
			return
		}
		if r.URL.Path == "/out.pdf" {
			io.WriteString(w, "%PDF-1.4 fake")
			return
		}
		var req struct{ URL, Token, Outputtype string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		var claims map[string]any
		if srv.Office.Verify(req.Token, &claims) != nil || claims["url"] != req.URL || req.Outputtype != "pdf" {
			io.WriteString(w, `{"error": -8}`)
			return
		}
		res, err := http.Get(req.URL)
		if err != nil || res.StatusCode != http.StatusOK {
			io.WriteString(w, `{"error": -4}`)
			return
		}
		res.Body.Close()
		converted++
		io.WriteString(w, `{"endConvert": true, "fileUrl": "`+fake.URL+`/out.pdf"}`)
	}))
	defer fake.Close()
	srv.Office = office.New(fake.URL, "secret", "http://go24")
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	srv.Office.Self = hs.URL
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna, AdminMode: u == anna}))
		as[u] = rec.Result().Cookies()[0]
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	text := func(doc string) string {
		t.Helper()
		zr, err := zip.NewReader(strings.NewReader(doc), int64(len(doc)))
		must(err)
		rc, err := zr.Open("word/document.xml")
		must(err)
		b, _ := io.ReadAll(rc)
		return string(b)
	}
	card := "/crm/deals/" + deal

	if code, _ := do(anna, "POST", "/crm/documents/"+tpls[0].ID+"/numerator", url.Values{"numerator": {"Д-{NUMBER}"}}); code != http.StatusSeeOther {
		t.Fatalf("numerator: %d", code)
	}
	if code, _ := do(eve, "POST", "/crm/documents/"+tpls[0].ID+"/numerator", url.Values{"numerator": {"X"}}); code != http.StatusForbidden {
		t.Fatalf("a non-administrator changed the numerator: %d", code)
	}
	// «Нумераторы»: a numerator of its own, chosen for the template (its numbers are the same «Д-1», «Д-2»…)
	if code, _ := do(anna, "POST", "/crm/documents/numerators", url.Values{"name": {"Общий"}, "format": {"Д-{NUMBER}"},
		"start": {"1"}, "step": {"0"}, "pad": {"0"}, "reset": {"never"}}); code != http.StatusSeeOther {
		t.Fatalf("numerator with a zero step: %d", code)
	}
	if code, _ := do(eve, "POST", "/crm/documents/numerators", url.Values{"name": {"Её"}, "format": {"{NUMBER}"},
		"start": {"1"}, "step": {"1"}, "pad": {"0"}, "reset": {"never"}}); code != http.StatusForbidden {
		t.Fatalf("a non-administrator added a numerator: %d", code)
	}
	if code, _ := do(anna, "POST", "/crm/documents/numerators", url.Values{"name": {"Общий"}, "format": {"Д-{NUMBER}"},
		"start": {"1"}, "step": {"1"}, "pad": {"0"}, "reset": {"year"}}); code != http.StatusSeeOther {
		t.Fatalf("numerator: %d", code)
	}
	var numID, oldNum string
	must(pool.QueryRow(ctx, `SELECT id FROM doc_numerators WHERE name = 'Общий'`).Scan(&numID))
	must(pool.QueryRow(ctx, `SELECT numerator_id FROM crm_doc_templates WHERE id = $1`, tpls[0].ID).Scan(&oldNum))
	if code, _ := do(anna, "POST", "/crm/documents/"+tpls[0].ID+"/numerator", url.Values{"numerator_id": {numID}}); code != http.StatusSeeOther {
		t.Fatalf("choose numerator: %d", code)
	}
	if _, page := do(anna, "GET", "/crm/documents", nil); !strings.Contains(page, `value="`+numID+`" selected`) || !strings.Contains(page, `id="numerators"`) {
		t.Fatal("the template's numerator is not chosen on the page")
	}
	if code, _ := do(anna, "POST", "/crm/documents/numerators/delete", url.Values{"id": {numID}}); code != http.StatusSeeOther {
		t.Fatalf("delete numerator in use: %d", code)
	}
	if code, _ := do(anna, "POST", "/crm/documents/numerators/delete", url.Values{"id": {oldNum}}); code != http.StatusSeeOther {
		t.Fatalf("delete numerator: %d", code)
	}
	var nums int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM doc_numerators WHERE id IN ($1, $2)`, numID, oldNum).Scan(&nums))
	if nums != 1 {
		t.Fatalf("numerators left: %d", nums)
	}
	// «Кому доступен»: only the department «Продажи» (and those under it) — Ева, in no department, does not see the template
	var company, sales, store string
	if pool.QueryRow(ctx, `SELECT id FROM departments WHERE parent_id IS NULL`).Scan(&company) != nil {
		must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Компания') RETURNING id`).Scan(&company))
	}
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Продажи') RETURNING id`, company).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (parent_id, name) VALUES ($1, 'Склад') RETURNING id`, sales).Scan(&store))
	if code, _ := do(eve, "POST", "/crm/documents/"+tpls[0].ID+"/access", url.Values{"to": {"dept:" + sales}}); code != http.StatusForbidden {
		t.Fatalf("a non-administrator chose who may use the template: %d", code)
	}
	if code, _ := do(anna, "POST", "/crm/documents/"+tpls[0].ID+"/access", url.Values{"to": {"dept:" + sales, anna}}); code != http.StatusSeeOther {
		t.Fatalf("access: %d", code)
	}
	if _, page := do(anna, "GET", "/crm/documents", nil); !strings.Contains(page, `value="dept:`+sales+`"`) || !strings.Contains(page, "Продажи") {
		t.Fatalf("the chosen department on the templates page: %s", page)
	}
	if _, page := do(eve, "GET", card, nil); strings.Contains(page, "template="+tpls[0].ID) {
		t.Fatal("the template is offered to whom it is not available")
	}
	if code, _ := do(eve, "GET", card+"/document?template="+tpls[0].ID, nil); code == http.StatusOK {
		t.Fatal("the values form of a template not available")
	}
	do(eve, "POST", card+"/document", url.Values{"template": {tpls[0].ID}})
	if docs, _ := srv.CRM.Documents(ctx, "deal", deal); len(docs) != 0 {
		t.Fatal("a document by a template not available")
	}
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, eve, store)
	must(err)
	if _, page := do(eve, "GET", card, nil); !strings.Contains(page, "template="+tpls[0].ID) {
		t.Fatal("the template of a department above one's own is not offered")
	}
	if code, _ := do(anna, "POST", "/crm/documents/"+tpls[0].ID+"/access", url.Values{}); code != http.StatusSeeOther {
		t.Fatalf("access to everybody: %d", code)
	}
	if access, _ := srv.CRM.DocTemplateAccess(ctx); len(access) != 0 {
		t.Fatalf("access left: %v", access)
	}
	// «Изменить значения»: the template's fields with the card's values, no number yet
	_, page := do(anna, "GET", card+"/document?template="+tpls[0].ID, nil)
	if !strings.Contains(page, `name="v.Title" value="Поставка"`) || !strings.Contains(page, `name="v.Amount"`) || strings.Contains(page, "v.DocumentNumber") ||
		!strings.Contains(page, `name="v.DocumentDate" value="`+time.Now().Format("02.01.2006")+`"`) { // today, not blank
		t.Fatalf("values form: %s", page)
	}
	code, _ := do(anna, "POST", card+"/document", url.Values{"template": {tpls[0].ID}, "v.Title": {"Поставка труб"}})
	if code != http.StatusSeeOther {
		t.Fatalf("make: %d", code)
	}
	do(anna, "POST", card+"/document", url.Values{"template": {tpls[0].ID}})
	docs, _ := srv.CRM.Documents(ctx, "deal", deal)
	if len(docs) != 2 || docs[1].Number != "Д-1" || docs[0].Number != "Д-2" {
		t.Fatalf("documents = %+v", docs)
	}
	first := docs[1]
	_, cardPage := do(eve, "GET", card, nil)
	if !strings.Contains(cardPage, `id="documents"`) || !strings.Contains(cardPage, "Договор № Д-1") || strings.Contains(cardPage, "/documents/"+first.ID+"/delete") {
		t.Fatalf("the card's documents for who reads it: %s", cardPage)
	}
	code, got := do(eve, "GET", card+"/documents/"+first.ID, nil)
	if code != http.StatusOK || !strings.Contains(text(got), "Договор № Д-1 от") || !strings.Contains(text(got), "Поставка труб, 1 500") {
		t.Fatalf("download %d: %s", code, got)
	}
	// made again with other values: the number stays
	_, page = do(anna, "GET", card+"/document?doc="+first.ID, nil)
	if !strings.Contains(page, `value="Поставка труб"`) || !strings.Contains(page, `value="Д-1"`) {
		t.Fatalf("values of the document: %s", page)
	}
	if code, _ := do(eve, "POST", card+"/documents/"+first.ID, url.Values{"v.Title": {"Чужое"}}); code == http.StatusSeeOther {
		var title string
		_ = pool.QueryRow(ctx, `SELECT values->>'Title' FROM crm_documents WHERE id = $1`, first.ID).Scan(&title)
		if title == "Чужое" {
			t.Fatal("who only reads the card made the document again")
		}
	}
	do(anna, "POST", card+"/documents/"+first.ID, url.Values{"v.Title": {"Поставка арматуры"}})
	_, got = do(anna, "GET", card+"/documents/"+first.ID, nil)
	if !strings.Contains(text(got), "Договор № Д-1 от") || !strings.Contains(text(got), "Поставка арматуры") {
		t.Fatalf("remade: %s", text(got))
	}
	// PDF: made by the document server once, kept until the document is remade
	for i := 0; i < 2; i++ {
		if code, got := do(eve, "GET", card+"/documents/"+first.ID+"/pdf", nil); code != http.StatusOK || !strings.HasPrefix(got, "%PDF") || converted != 1 {
			t.Fatalf("pdf %d (converted %d): %s", code, converted, got)
		}
	}
	if code, _ := http.Get(hs.URL + "/onlyoffice/convert/" + first.FileID + "/forged"); code.StatusCode != http.StatusNotFound {
		t.Fatalf("the file for the document server without its stamp: %d", code.StatusCode)
	}
	do(anna, "POST", card+"/documents/"+first.ID, url.Values{"v.Title": {"Поставка арматуры"}})
	if do(anna, "GET", card+"/documents/"+first.ID+"/pdf", nil); converted != 2 {
		t.Fatalf("the PDF of the remade document is the old one: converted %d", converted)
	}
	// «Отправить»: the document (PDF or DOCX) attached to the letter; another card's is not
	req := httptest.NewRequest("POST", "/", nil)
	req.Form = url.Values{"doc": {first.ID + ":pdf", first.ID + ":docx"}}
	files, err := srv.documentAttachments(req, "deal", deal)
	if err != nil || len(files) != 2 || !strings.HasSuffix(files[0].Name, ".pdf") || files[0].ContentType != "application/pdf" ||
		!strings.Contains(text(string(files[1].Data)), "Поставка арматуры") {
		t.Fatalf("attachments = %v %+v", err, files)
	}
	// «Публичная ссылка»: who may change the card switches it on; anyone with it sees the document, until it is off
	do(eve, "POST", card+"/documents/"+first.ID+"/public", url.Values{"on": {"1"}})
	if d, _ := srv.CRM.Document(ctx, "deal", deal, first.ID); d.PublicToken != "" {
		t.Fatal("who only reads the card made a public link")
	}
	do(anna, "POST", card+"/documents/"+first.ID+"/public", url.Values{"on": {"1"}})
	pub, _ := srv.CRM.Document(ctx, "deal", deal, first.ID)
	if _, page := do(anna, "GET", card, nil); pub.PublicToken == "" || !strings.Contains(page, "/d/"+pub.PublicToken) {
		t.Fatalf("the link on the card: %q", pub.PublicToken)
	}
	get := func(path string) (int, string) {
		res, err := http.Get(hs.URL + path)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}
	if code, page := get("/d/" + pub.PublicToken); code != http.StatusOK || !strings.Contains(page, "Договор № Д-1") || !strings.Contains(page, "/d/"+pub.PublicToken+"/pdf") {
		t.Fatalf("public page %d: %s", code, page)
	}
	if code, got := get("/d/" + pub.PublicToken + "/docx"); code != http.StatusOK || !strings.Contains(text(got), "Поставка арматуры") {
		t.Fatalf("public docx %d", code)
	}
	if code, got := get("/d/" + pub.PublicToken + "/pdf?inline=1"); code != http.StatusOK || !strings.HasPrefix(got, "%PDF") {
		t.Fatalf("public pdf %d", code)
	}
	do(anna, "POST", card+"/documents/"+first.ID+"/public", url.Values{})
	if code, _ := get("/d/" + pub.PublicToken); code != http.StatusNotFound {
		t.Fatalf("the link switched off still works: %d", code)
	}
	// another card's document is not found through this one
	other, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Другая", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	if code, _ := do(anna, "GET", "/crm/deals/"+other+"/documents/"+first.ID, nil); code == http.StatusOK {
		t.Fatal("a document downloaded through another card")
	}
	do(eve, "POST", card+"/documents/"+first.ID+"/delete", url.Values{})
	if docs, _ := srv.CRM.Documents(ctx, "deal", deal); len(docs) != 2 {
		t.Fatal("who only reads the card deleted a document")
	}
	do(anna, "POST", card+"/documents/"+first.ID+"/delete", url.Values{})
	if docs, _ := srv.CRM.Documents(ctx, "deal", deal); len(docs) != 1 {
		t.Fatal("the document is not deleted")
	}

	// «Счёт»: a row of the table for each product of the deal, totals, the requisites of «Моя компания» and the client
	seller, err := srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "Ромашка", Phone: "+7 495 000"})
	must(err)
	buyer, err := srv.CRM.SaveCompany(ctx, anna, crm.Company{Title: "Лютик", Address: "Москва"})
	must(err)
	_, err = srv.CRM.SaveRequisite(ctx, crm.Requisite{Entity: "company", EntityID: seller, Preset: "org", Name: "ООО «Ромашка»", INN: "7701234567", Bank: "Банк"})
	must(err)
	_, err = srv.CRM.SaveRequisite(ctx, crm.Requisite{Entity: "company", EntityID: buyer, Preset: "org", Name: "ООО «Лютик»", INN: "7707654321"})
	must(err)
	if code, _ := do(eve, "POST", "/crm/documents/my-company", url.Values{"company": {seller}}); code != http.StatusForbidden {
		t.Fatalf("a non-administrator chose my company: %d", code)
	}
	if code, _ := do(anna, "POST", "/crm/documents/my-company", url.Values{"company": {seller}}); code != http.StatusSeeOther {
		t.Fatalf("my company: %d", code)
	}
	if _, page := do(anna, "GET", "/crm/documents", nil); !strings.Contains(page, `value="`+seller+`" selected`) || !strings.Contains(page, `data-copy="{Product.Name}"`) || !strings.Contains(page, `data-copy="{MyCompany.INN}"`) {
		t.Fatalf("templates page: %s", page)
	}
	d, err := srv.CRM.Deal(ctx, other)
	must(err)
	d.Company = crm.Ref{ID: buyer}
	_, err = srv.CRM.SaveDeal(ctx, anna, d)
	must(err)
	vat := 20.0
	_, err = srv.CRM.SetProductRows(ctx, anna, "deal", other, []crm.ProductRow{{Name: "Стол", Price: 100, Quantity: 2, VATRate: &vat, VATIncluded: true},
		{Name: "Стул", Unit: "шт.", Price: 50, Quantity: 1}})
	must(err)
	tpl.Reset()
	zw = zip.NewWriter(&tpl)
	f, _ = zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:p><w:t>{MyCompany.FullName} ИНН {MyCompany.INN} {MyCompany.Bank} → {Client.FullName} ИНН {Client.INN}, {Client.Address}</w:t></w:p>`+
		`<w:tbl><w:tr><w:tc><w:p><w:t>{Product.Number}|{Product.Name}|{Product.Quantity} {Product.Unit}|{Product.Price}|{Product.TaxRate}|{Product.Sum}</w:t></w:p></w:tc></w:tr></w:tbl>`+
		`<w:p><w:t>Итого {Total}, НДС {TaxTotal}, товаров {ProductsCount}</w:t></w:p></w:document>`)
	zw.Close()
	key = "go24/doc-templates/t/inv.docx"
	must(srv.Storages.Put(ctx, anna, false, "", key, bytes.NewReader(tpl.Bytes()), int64(tpl.Len()), docxType))
	file, err = srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: "inv.docx", ContentType: docxType, Size: int64(tpl.Len()), UploadedBy: anna})
	must(err)
	must(srv.CRM.AddDocTemplate(ctx, anna, "Счёт", "deal", file))
	tpls, _ = srv.CRM.DocTemplates(ctx, "deal")
	var inv string
	for _, x := range tpls {
		if x.Name == "Счёт" {
			inv = x.ID
		}
	}
	if _, page := do(anna, "GET", "/crm/deals/"+other+"/document?template="+inv, nil); strings.Contains(page, "v.Product.") || !strings.Contains(page, `name="v.Client.INN" value="7707654321"`) {
		t.Fatalf("values form of the invoice: %s", page)
	}
	do(anna, "POST", "/crm/deals/"+other+"/document", url.Values{"template": {inv}})
	docs, _ = srv.CRM.Documents(ctx, "deal", other)
	if len(docs) != 1 {
		t.Fatalf("invoice documents = %+v", docs)
	}
	_, got = do(anna, "GET", "/crm/deals/"+other+"/documents/"+docs[0].ID, nil)
	for _, want := range []string{"ООО «Ромашка» ИНН 7701234567 Банк → ООО «Лютик» ИНН 7707654321, Москва", "1|Стол|2 шт.|100|20%|200<",
		"2|Стул|1 шт.|50||50<", "Итого 250, НДС 33,33, товаров 2"} {
		if !strings.Contains(text(got), want) {
			t.Errorf("no %q in %s", want, text(got))
		}
	}

	// the template edited in ONLYOFFICE: the editor for administrators, the file for the document server by the
	// signed link, and what is saved becomes the template
	if code, _ := do(eve, "GET", "/crm/documents/"+inv+"/edit", nil); code != http.StatusForbidden {
		t.Fatalf("a non-administrator opened the template's editor: %d", code)
	}
	code, page = do(anna, "GET", "/crm/documents/"+inv+"/edit", nil)
	var cfg struct {
		Document     struct{ URL string }
		EditorConfig struct{ CallbackURL string }
	}
	if attr := regexp.MustCompile(`data-office="([^"]*)"`).FindStringSubmatch(page); code != http.StatusOK || attr == nil ||
		json.Unmarshal([]byte(html.UnescapeString(attr[1])), &cfg) != nil {
		t.Fatalf("editor %d: %s", code, page)
	}
	m, cb := strings.TrimPrefix(cfg.Document.URL, hs.URL), strings.TrimPrefix(cfg.EditorConfig.CallbackURL, hs.URL)
	if !strings.HasPrefix(m, "/onlyoffice/tpl/") || !strings.HasPrefix(cb, "/onlyoffice/tplsave/") {
		t.Fatalf("editor links: %+v", cfg)
	}
	res, err := http.Get(hs.URL + m)
	must(err)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(text(string(b)), "{Product.Name}") {
		t.Fatalf("template for the document server %d", res.StatusCode)
	}
	if res, _ := http.Get(hs.URL + m + "x"); res.StatusCode != http.StatusNotFound {
		t.Fatal("a template given by a wrong link")
	}
	tpl.Reset()
	zw = zip.NewWriter(&tpl)
	f, _ = zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:p><w:t>Счёт № {DocumentNumber} для {Client.FullName}</w:t></w:p></w:document>`)
	zw.Close()
	edited = tpl.Bytes()
	token, err := srv.Office.Sign(map[string]any{"status": office.StatusSave, "url": fake.URL + "/edited.docx", "key": "k"})
	must(err)
	res, err = http.Post(hs.URL+cb, "application/json", strings.NewReader(`{"token": "`+token+`"}`))
	must(err)
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	var saved struct{ Error int }
	if json.Unmarshal(b, &saved) != nil || saved.Error != 0 {
		t.Fatalf("save: %s", b)
	}
	if res, _ := http.Post(hs.URL+strings.Replace(cb, "s=", "s=x", 1), "application/json", strings.NewReader(`{"token": "`+token+`"}`)); res.StatusCode != http.StatusNotFound {
		t.Fatal("a template saved by a wrong link")
	}
	do(anna, "POST", "/crm/deals/"+other+"/document", url.Values{"template": {inv}})
	docs, _ = srv.CRM.Documents(ctx, "deal", other)
	if _, got := do(anna, "GET", "/crm/deals/"+other+"/documents/"+docs[0].ID, nil); len(docs) != 2 || !strings.Contains(text(got), "Счёт № 2 для ООО «Лютик»") {
		t.Fatalf("made by the edited template: %s", text(got))
	}

	// «Готовые шаблоны»: the invoice, the contract and the act of go24 make a document of the deal with no field left
	if code, _ := do(eve, "POST", "/crm/documents/builtin", url.Values{"kind": {"invoice"}}); code != http.StatusForbidden {
		t.Fatalf("a non-administrator added a template: %d", code)
	}
	var dealTemplates int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_doc_templates WHERE entity = 'deal'`).Scan(&dealTemplates))
	for _, kind := range docBuiltins {
		if code, _ := do(anna, "POST", "/crm/documents/builtin", url.Values{"kind": {kind}}); code != http.StatusSeeOther {
			t.Fatalf("builtin %s: %d", kind, code)
		}
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM crm_doc_templates WHERE entity = 'deal' ORDER BY created_at DESC LIMIT 1`).Scan(&id))
		do(anna, "POST", "/crm/deals/"+other+"/document", url.Values{"template": {id}})
		docs, _ = srv.CRM.Documents(ctx, "deal", other)
		_, got := do(anna, "GET", "/crm/deals/"+other+"/documents/"+docs[0].ID, nil)
		if got := text(got); !strings.Contains(got, "ООО «Ромашка»") || !strings.Contains(got, "ООО «Лютик»") || !strings.Contains(got, "Стол") ||
			!strings.Contains(got, "7707654321") || strings.Contains(got, "{") {
			t.Fatalf("made by the builtin %s: %s", kind, got)
		}
	}
	// the invoice's QR code of the payment, when «Моя компания» has the bank details
	_, err = pool.Exec(ctx, `UPDATE crm_requisites SET account = '40702810900000000001', bik = '044525225',
		corr_account = '30101810400000000225', kpp = '770101001' WHERE entity_id = $1`, seller)
	must(err)
	var invoice string
	must(pool.QueryRow(ctx, `SELECT id FROM crm_doc_templates WHERE entity = 'deal' ORDER BY created_at LIMIT 1 OFFSET $1`,
		dealTemplates).Scan(&invoice))
	do(anna, "POST", "/crm/deals/"+other+"/document", url.Values{"template": {invoice}})
	docs, _ = srv.CRM.Documents(ctx, "deal", other)
	_, got = do(anna, "GET", "/crm/deals/"+other+"/documents/"+docs[0].ID, nil)
	v := docs[0].Values
	want, err := payqr.PNG(payqr.Text(payqr.Payee{Name: "ООО «Ромашка»", Account: "40702810900000000001", Bank: "Банк",
		BIK: "044525225", CorrAccount: "30101810400000000225", INN: "7701234567", KPP: "770101001"},
		payqr.Kopecks(v["Total"]), "Оплата по счёту № "+v["DocumentNumber"]+" от "+v["DocumentDate"]))
	must(err)
	zr, err := zip.NewReader(bytes.NewReader([]byte(got)), int64(len(got)))
	must(err)
	var qr []byte
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "word/media/") {
			rc, _ := f.Open()
			qr, _ = io.ReadAll(rc)
			rc.Close()
		}
	}
	if x := text(got); docs[0].TemplateName != "Счёт на оплату" || strings.Count(x, "<w:drawing>") != 1 || !bytes.Equal(qr, want) || payqr.Kopecks(v["Total"]) == 0 {
		t.Fatalf("the invoice's QR code: %s %v", docs[0].TemplateName, v)
	}
	if w := inwords.Amount("ru-RU", payqr.Kopecks(v["Total"]), v["Currency"]); v["TotalWords"] != w || !strings.Contains(text(got), w) ||
		!strings.Contains(w, "рубл") {
		t.Fatalf("the total in words: %q, want %q", v["TotalWords"], w)
	}

	// «Печать и подписи» of «Моя компания»: uploaded by administrators, the builtin act gets the stamp and the
	// director's facsimile as pictures, the accountant's — not uploaded — leaves nothing
	upload := func(who, kind string, data []byte) int {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		mw.WriteField("kind", kind)
		fw, _ := mw.CreateFormFile("file", "pic.png")
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/crm/documents/image", &b)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(as[who])
		res, err := client.Do(req)
		must(err)
		res.Body.Close()
		return res.StatusCode
	}
	var pic bytes.Buffer
	must(png.Encode(&pic, image.NewRGBA(image.Rect(0, 0, 40, 40))))
	if code := upload(eve, "Stamp", pic.Bytes()); code != http.StatusForbidden {
		t.Fatalf("a non-administrator uploaded the stamp: %d", code)
	}
	upload(anna, "Stamp", []byte("not a picture"))
	upload(anna, "Nope", pic.Bytes())
	if imgs, _ := srv.CRM.DocImages(ctx); len(imgs) != 0 {
		t.Fatalf("kept a wrong picture: %v", imgs)
	}
	upload(anna, "Stamp", pic.Bytes())
	upload(anna, "Stamp", pic.Bytes()) // the second one takes the place of the first
	upload(anna, "DirectorSignature", pic.Bytes())
	imgs, _ := srv.CRM.DocImages(ctx)
	var pics int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE content_type = 'image/png'`).Scan(&pics))
	if len(imgs) != 2 || pics != 2 {
		t.Fatalf("pictures: %v, files %d", imgs, pics)
	}
	if _, page := do(anna, "GET", "/crm/documents", nil); !strings.Contains(page, `src="/files/`+imgs["Stamp"]+`?inline=1"`) {
		t.Fatal("the stamp is not shown")
	}
	do(anna, "POST", "/crm/deals/"+other+"/document", url.Values{"template": {func() string {
		var id string
		must(pool.QueryRow(ctx, `SELECT id FROM crm_doc_templates WHERE entity = 'deal' ORDER BY created_at DESC LIMIT 1`).Scan(&id)) // the act, the last builtin
		return id
	}()}})
	docs, _ = srv.CRM.Documents(ctx, "deal", other)
	_, got = do(anna, "GET", "/crm/deals/"+other+"/documents/"+docs[0].ID, nil)
	if x := text(got); strings.Count(x, "<w:drawing>") != 2 || strings.Contains(x, "{") {
		t.Fatalf("the act with the stamp: %s", x)
	}
	do(anna, "POST", "/crm/documents/image", url.Values{"kind": {"Stamp"}, "remove": {"1"}})
	if imgs, _ := srv.CRM.DocImages(ctx); len(imgs) != 1 || imgs["Stamp"] != "" {
		t.Fatalf("removed: %v", imgs)
	}

	// a print form of an invoice: a template of «Счёт или предложение», the document made on the invoice's page
	tpl.Reset()
	zw = zip.NewWriter(&tpl)
	f, _ = zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:p><w:t>{Kind} № {Number} от {IssueDate} до {DueDate}: {Client.FullName}, {Card}</w:t></w:p>`+
		`<w:tbl><w:tr><w:tc><w:p><w:t>{Product.Name}|{Product.Sum}</w:t></w:p></w:tc></w:tr></w:tbl><w:p><w:t>{Total} ({TotalWords})</w:t></w:p></w:document>`)
	zw.Close()
	key = "go24/doc-templates/t/pf.docx"
	must(srv.Storages.Put(ctx, anna, false, "", key, bytes.NewReader(tpl.Bytes()), int64(tpl.Len()), docxType))
	file, err = srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: "pf.docx", ContentType: docxType, Size: int64(tpl.Len()), UploadedBy: anna})
	must(err)
	must(srv.CRM.AddDocTemplate(ctx, anna, "Печатная форма", "invoice", file))
	pf, _ := srv.CRM.DocTemplates(ctx, "invoice")
	invID, err := srv.CRM.MakeInvoice(ctx, anna, "invoice", other, 5)
	must(err)
	ip := "/crm/invoices/" + invID
	if _, page := do(anna, "GET", ip, nil); len(pf) != 1 || !strings.Contains(page, `action="`+ip+`/document"`) {
		t.Fatalf("the invoice's page: %d templates", len(pf))
	}
	if _, page := do(anna, "GET", ip+"/document?template="+pf[0].ID, nil); !strings.Contains(page, `name="v.Kind" value="Счёт"`) {
		t.Fatal("the invoice's values form")
	}
	if code, _ := do(anna, "POST", ip+"/document", url.Values{"template": {pf[0].ID}}); code != http.StatusSeeOther {
		t.Fatalf("made on the invoice: %d", code)
	}
	idocs, _ := srv.CRM.Documents(ctx, "invoice", invID)
	if len(idocs) != 1 {
		t.Fatalf("invoice documents: %+v", idocs)
	}
	_, got = do(anna, "GET", ip+"/documents/"+idocs[0].ID, nil)
	iv, _ := srv.CRM.Invoice(ctx, invID)
	if x := text(got); !strings.Contains(x, "Счёт № "+iv.Code()) || !strings.Contains(x, "ООО «Лютик»") || !strings.Contains(x, "Стол|") ||
		!strings.Contains(x, "Стул|") || !strings.Contains(x, "рубл") || strings.Contains(x, "{") {
		t.Fatalf("the print form: %s", x)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_timeline WHERE entity = 'deal' AND entity_id = $1 AND kind = 'document' AND body LIKE 'Печатная форма%'`, other).Scan(&notes))
	if _, page := do(anna, "GET", ip, nil); notes != 1 || !strings.Contains(page, ip+"/documents/"+idocs[0].ID) {
		t.Fatalf("the invoice's documents: %d notes", notes)
	}
	// sent for signing from the invoice's page
	srv.Sign = sign.NewStore(pool)
	if _, page := do(anna, "GET", "/sign/new?kind=invoices&id="+invID+"&doc="+idocs[0].ID, nil); !strings.Contains(page, ip+"/documents/"+idocs[0].ID) {
		t.Fatal("the signing form of the invoice's document")
	}
	if code, _ := do(anna, "POST", "/sign", url.Values{"kind": {"invoices"}, "id": {invID}, "doc": {idocs[0].ID}, "signer": {anna},
		"title": {"Счёт на подпись"}}); code != http.StatusSeeOther {
		t.Fatalf("sent for signing: %d", code)
	}
	if signs, _ := srv.Sign.OfCard(ctx, "invoice", invID); signs[idocs[0].ID].Title != "Счёт на подпись" {
		t.Fatalf("the invoice's signing: %+v", signs)
	} else if _, page := do(anna, "GET", "/sign/"+signs[idocs[0].ID].ID, nil); !strings.Contains(page, `href="`+ip+`#documents"`) {
		t.Fatal("the signing's link to the invoice")
	}
	if _, page := do(anna, "GET", ip, nil); !strings.Contains(page, `class="pill sg-signing"`) {
		t.Fatal("the signing on the invoice's page")
	}
	do(anna, "POST", ip+"/documents/"+idocs[0].ID+"/delete", nil)
	if idocs, _ = srv.CRM.Documents(ctx, "invoice", invID); len(idocs) != 0 {
		t.Fatal("deleted")
	}

	before, _ := srv.CRM.DocTemplates(ctx, "")
	do(anna, "POST", "/crm/documents/builtin", url.Values{"kind": {"x"}})
	if after, _ := srv.CRM.DocTemplates(ctx, ""); len(after) != len(before) {
		t.Fatal("an unknown builtin template")
	}

	// «Создание документа» of a business process: from the employee chosen, the values given over the card's
	bpDoc, err := srv.BPDocument(ctx, eve, "deal", deal, tpls[0].ID, map[string]string{"Title": "По процессу"})
	must(err)
	made, err := srv.CRM.Document(ctx, "deal", deal, bpDoc)
	must(err)
	if made.AuthorID != eve || made.TemplateID != tpls[0].ID || made.Values["Title"] != "По процессу" || made.Values["Amount"] == "" || made.FileID == "" {
		t.Fatalf("the process's document: %+v", made)
	}
	if _, err := srv.BPDocument(ctx, eve, "none", deal, tpls[0].ID, nil); err == nil {
		t.Fatal("a document of no card")
	}

	// the robot «Создать документ»: the deal entering the stage gets a document of its responsible
	srv.CRM.MakeDocument = func(ctx context.Context, userID, entity, entityID, tplID string) (string, error) {
		return srv.BPDocument(ctx, userID, entity, entityID, tplID, nil)
	}
	st := pipes[0].Stages
	must(srv.CRM.AddRobot(ctx, anna, st[2].ID, "document", crm.RobotParams{Doc: tpls[0].ID}))
	had, _ := srv.CRM.Documents(ctx, "deal", deal)
	_, err = srv.CRM.MoveDeal(ctx, anna, deal, st[2].ID)
	must(err)
	now, _ := srv.CRM.Documents(ctx, "deal", deal)
	var said string
	must(pool.QueryRow(ctx, `SELECT body FROM crm_timeline WHERE entity_id = $1 AND kind = 'robot' ORDER BY id DESC LIMIT 1`, deal).Scan(&said))
	if len(now) != len(had)+1 || now[0].AuthorID != anna || said != "document:Договор № "+now[0].Number {
		t.Fatalf("the document robot: %d → %d, %q", len(had), len(now), said)
	}
	if _, page := do(anna, "GET", "/crm/robots", nil); !strings.Contains(page, `<option value="`+tpls[0].ID+`">Договор</option>`) || !strings.Contains(page, "📄 Договор") {
		t.Fatal("the robots' page: the document robot")
	}
}
