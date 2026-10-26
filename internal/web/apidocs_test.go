package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

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

// TestDocsAPI: the document generator and «Подпись» through REST API v1, as documentgenerator.* and sign.* —
// an administrator adds a template, gives it to one person and numbers it; a document of a deal is made by it with
// a value given, read, made again, its public link switched on, sent for signing and signed by the employee, recalled;
// who may not see the card or the template does not; a key without the scope may not.
func TestDocsAPI(t *testing.T) {
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
	user := func(name string, groups ...string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ($1, $1, $1 || '@t', $2) RETURNING id`,
			name, append([]string{}, groups...)).Scan(&id))
		return id
	}
	anna, bob, eve := user("anna", "admins"), user("bob"), user("eve")
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, s3), Keys: apikeys.NewStore(pool), Sign: sign.NewStore(pool), BaseURL: "https://go24",
		AdminGroups: []string{"admins"}, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Свои", Perms: crm.Perms{"deal": {"read": "own", "write": "own"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, eve, true))
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", Amount: 1500, PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: bob}})
	must(err)
	key := func(uid string, scopes ...string) string {
		k, err := srv.Keys.Create(ctx, uid, "k", scopes, time.Hour)
		must(err)
		return k
	}
	annaKey, bobKey, eveKey := key(anna, "documents:read", "documents:write"), key(bob, "documents:read", "documents:write"),
		key(eve, "documents:read", "documents:write")
	bobRead, bobCRM := key(bob, "documents:read"), key(bob, "crm:read")

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	do := func(method, path, token, ctype string, body io.Reader, want int) []byte {
		t.Helper()
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		return raw
	}
	call := func(method, path, token string, in any, want int, out any) {
		t.Helper()
		var body io.Reader
		if in != nil {
			raw, _ := json.Marshal(in)
			body = bytes.NewReader(raw)
		}
		raw := do(method, path, token, "application/json", body, want)
		if out != nil {
			must(json.Unmarshal(raw, out))
		}
	}
	docx := func(text string) []byte {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		f, _ := zw.Create("word/document.xml")
		io.WriteString(f, `<w:document><w:body><w:p><w:r><w:t>`+text+`</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`)
		zw.Close()
		return b.Bytes()
	}
	upload := func(path, token string, fields map[string]string, data []byte, want int, out any) {
		t.Helper()
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for k, v := range fields {
			mw.WriteField(k, v)
		}
		if data != nil {
			fw, _ := mw.CreateFormFile("file", "dog.docx")
			fw.Write(data)
		}
		mw.Close()
		raw := do("POST", path, token, mw.FormDataContentType(), &b, want)
		if out != nil {
			must(json.Unmarshal(raw, out))
		}
	}

	// the templates: an administrator's
	tplData := docx("Договор № {DocumentNumber}: {Title}, {Amount} {Currency}, срок {Term}")
	upload("/api/v1/document-templates", bobKey, map[string]string{"name": "Договор", "entity": "deals"}, tplData, http.StatusForbidden, nil)
	upload("/api/v1/document-templates", annaKey, map[string]string{"name": "Договор", "entity": "deals"}, []byte("not a docx"), http.StatusUnprocessableEntity, nil)
	var tpl apiDocTemplate
	upload("/api/v1/document-templates", annaKey, map[string]string{"name": "Договор", "entity": "deals"}, tplData, http.StatusCreated, &tpl)
	if tpl.ID == "" || tpl.Entity != "deals" || tpl.Name != "Договор" {
		t.Fatalf("template: %+v", tpl)
	}
	var builtin apiDocTemplate
	upload("/api/v1/document-templates", annaKey, map[string]string{"builtin": "invoice"}, nil, http.StatusCreated, &builtin)
	call("PATCH", "/api/v1/document-templates/"+tpl.ID, annaKey, map[string]any{"numerator": "Д-{NUMBER}", "access": []string{bob}}, http.StatusOK, &tpl)
	if tpl.Numerator != "Д-{NUMBER}" {
		t.Fatalf("numerator: %+v", tpl)
	}
	var one apiDocTemplate
	call("GET", "/api/v1/document-templates/"+tpl.ID, bobKey, nil, http.StatusOK, &one)
	if strings.Join(one.Fields, ",") != "DocumentNumber,Title,Amount,Currency,Term" || len(one.Access) != 1 || one.Access[0].ID != bob {
		t.Fatalf("template of bob: %+v", one)
	}
	call("GET", "/api/v1/document-templates/"+tpl.ID, eveKey, nil, http.StatusNotFound, nil) // not given to her
	var list []apiDocTemplate
	call("GET", "/api/v1/document-templates?entity=deals", eveKey, nil, http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != builtin.ID {
		t.Fatalf("eve's templates: %+v", list)
	}
	call("GET", "/api/v1/document-templates", annaKey, nil, http.StatusOK, &list)
	if len(list) != 2 {
		t.Fatalf("all templates: %+v", list)
	}
	call("GET", "/api/v1/document-templates", bobCRM, nil, http.StatusForbidden, nil)

	// a document of the deal: bob is its responsible
	mk := map[string]any{"entity": "deals", "entity_id": deal, "template": tpl.ID, "values": map[string]string{"Term": "10 дней"}}
	call("POST", "/api/v1/documents", eveKey, mk, http.StatusForbidden, nil) // she does not see the deal
	call("POST", "/api/v1/documents", bobRead, mk, http.StatusForbidden, nil)
	var doc apiDocument
	call("POST", "/api/v1/documents", bobKey, mk, http.StatusCreated, &doc)
	if doc.Number != "Д-1" || doc.Values["Term"] != "10 дней" || doc.Values["Title"] != "Поставка" || doc.Entity != "deals" {
		t.Fatalf("made: %+v", doc)
	}
	text := func(raw []byte) string {
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		must(err)
		f, err := zr.Open("word/document.xml")
		must(err)
		b, _ := io.ReadAll(f)
		return string(b)
	}
	if got := text(do("GET", "/api/v1/documents/"+doc.ID+"/file", bobRead, "", nil, http.StatusOK)); !strings.Contains(got, "Договор № Д-1: Поставка") ||
		!strings.Contains(got, "срок 10 дней") {
		t.Fatalf("docx: %s", got)
	}
	var docs []apiDocument
	call("GET", "/api/v1/documents?entity=deals&entity_id="+deal, bobRead, nil, http.StatusOK, &docs)
	if len(docs) != 1 || docs[0].ID != doc.ID || docs[0].Values != nil {
		t.Fatalf("card's documents: %+v", docs)
	}
	call("GET", "/api/v1/documents/"+doc.ID, eveKey, nil, http.StatusForbidden, nil)
	call("PATCH", "/api/v1/documents/"+doc.ID, bobKey, map[string]any{"values": map[string]string{"Term": "5 дней"}}, http.StatusOK, &doc)
	if doc.Number != "Д-1" || doc.Values["Term"] != "5 дней" {
		t.Fatalf("remade: %+v", doc)
	}
	if got := text(do("GET", "/api/v1/documents/"+doc.ID+"/file", bobRead, "", nil, http.StatusOK)); !strings.Contains(got, "срок 5 дней") {
		t.Fatalf("remade docx: %s", got)
	}
	call("POST", "/api/v1/documents/"+doc.ID+"/public", bobKey, map[string]bool{"on": true}, http.StatusOK, &doc)
	if !strings.HasPrefix(doc.PublicURL, "https://go24/d/") {
		t.Fatalf("public: %+v", doc)
	}

	// «Подпись»: the deal's document to anna, recalled; a Word file to bob, signed by him
	var sd apiSignDoc
	call("POST", "/api/v1/sign", bobKey, map[string]string{"document": doc.ID, "signer": anna}, http.StatusCreated, &sd)
	if sd.Status != "signing" || sd.Title != "Договор № Д-1" || sd.DocumentID != doc.ID || len(sd.Parties) != 1 || sd.Parties[0].Status != "ready" {
		t.Fatalf("sent: %+v", sd)
	}
	call("GET", "/api/v1/documents/"+doc.ID, bobRead, nil, http.StatusOK, &doc)
	if doc.Sign == nil || doc.Sign.ID != sd.ID {
		t.Fatalf("document's signing: %+v", doc)
	}
	call("GET", "/api/v1/sign/"+sd.ID, eveKey, nil, http.StatusNotFound, nil)
	call("POST", "/api/v1/sign/"+sd.ID+"/sign", bobKey, map[string]bool{"agree": true}, http.StatusUnprocessableEntity, nil) // not his turn
	var mine []apiSignDoc
	call("GET", "/api/v1/sign?filter=me", annaKey, nil, http.StatusOK, &mine)
	if len(mine) != 1 || mine[0].ID != sd.ID {
		t.Fatalf("waiting for anna: %+v", mine)
	}
	call("POST", "/api/v1/sign/"+sd.ID+"/cancel", annaKey, nil, http.StatusNotFound, nil) // the sender recalls
	call("POST", "/api/v1/sign/"+sd.ID+"/cancel", bobKey, nil, http.StatusOK, &sd)
	if sd.Status != "cancelled" {
		t.Fatalf("cancelled: %+v", sd)
	}
	sd = apiSignDoc{}
	upload("/api/v1/sign", annaKey, map[string]string{"signer": bob}, docx("Акт"), http.StatusCreated, &sd)
	if sd.Title != "dog" || sd.Entity != "" {
		t.Fatalf("file sent: %+v", sd)
	}
	call("POST", "/api/v1/sign/"+sd.ID+"/sign", bobKey, map[string]bool{}, http.StatusUnprocessableEntity, nil) // has not confirmed reading
	call("POST", "/api/v1/sign/"+sd.ID+"/sign", bobKey, map[string]bool{"agree": true}, http.StatusOK, &sd)
	if sd.Status != "done" || sd.Parties[0].Status != "signed" || sd.Files["result"] == "" {
		t.Fatalf("signed: %+v", sd)
	}
	if raw := do("GET", "/api/v1/sign/"+sd.ID+"/file/docx", bobRead, "", nil, http.StatusOK); !strings.Contains(text(raw), "Акт") {
		t.Fatal("signed docx")
	}

	call("DELETE", "/api/v1/documents/"+doc.ID, bobRead, nil, http.StatusForbidden, nil)
	call("DELETE", "/api/v1/documents/"+doc.ID, bobKey, nil, http.StatusNoContent, nil)
	call("DELETE", "/api/v1/document-templates/"+tpl.ID, bobKey, nil, http.StatusForbidden, nil)
	call("DELETE", "/api/v1/document-templates/"+tpl.ID, annaKey, nil, http.StatusNoContent, nil)
	call("GET", "/api/v1/document-templates/"+tpl.ID, annaKey, nil, http.StatusNotFound, nil)
}
