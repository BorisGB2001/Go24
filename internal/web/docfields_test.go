package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDocTemplateFieldsHTTP: «Поля» of a template (GO24DEV-1007) — a required field left empty makes no document
// (and takes no number) but opens «Изменить значения» with its title; the value by default fills an empty field; the
// paragraph of an empty field to hide goes; only those who change the templates set the fields; REST and
// crm.documentgenerator.template.getfields give the settings.
func TestDocTemplateFieldsHTTP(t *testing.T) {
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
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, groups) VALUES ('anna', 'Анна', 'anna@t', '{admins}') RETURNING id`).Scan(&anna))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@t') RETURNING id`).Scan(&eve))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Calendar: calendar.NewStore(pool),
		Storages: storages.NewStore(pool, nil, s3), Keys: apikeys.NewStore(pool), AdminGroups: []string{"admins"},
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Продажи", Perms: crm.Perms{"deal": {"read": "all", "write": "all"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, eve, true))
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", Amount: 1500, PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)

	var tpl bytes.Buffer
	zw := zip.NewWriter(&tpl)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:body><w:p><w:t>Договор № {DocumentNumber}: {Title}</w:t></w:p><w:p><w:t>ИНН {INN}</w:t></w:p>`+
		`<w:p><w:t>Доставка: {Delivery}</w:t></w:p><w:p><w:t>Примечание: {Note}</w:t></w:p><w:sectPr/></w:body></w:document>`)
	zw.Close()
	key := "go24/doc-templates/t/dog.docx"
	must(srv.Storages.Put(ctx, anna, false, "", key, bytes.NewReader(tpl.Bytes()), int64(tpl.Len()), docxType))
	file, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: key, Name: "dog.docx", ContentType: docxType, Size: int64(tpl.Len()), UploadedBy: anna})
	must(err)
	tplID, err := srv.CRM.NewDocTemplate(ctx, anna, "Договор", "deal", file)
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	as := map[string]*http.Cookie{}
	for _, u := range []string{anna, eve} {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: u, Admin: u == anna, AdminMode: u == anna}))
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
		req.AddCookie(as[who])
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(raw)
	}
	fields := "/crm/documents/" + tplID + "/fields"
	if code, _, _ := do(eve, "GET", fields, nil); code != http.StatusForbidden {
		t.Fatalf("an employee opened the fields: %d", code)
	}
	code, _, page := do(anna, "GET", fields, nil)
	if code != http.StatusOK || !strings.Contains(page, "{INN}") || !strings.Contains(page, "{Note}") || strings.Contains(page, "{DocumentNumber}") {
		t.Fatalf("fields page %d: %s", code, page)
	}
	if code, _, _ := do(eve, "POST", fields, url.Values{"f.0.name": {"INN"}, "f.0.required": {"1"}}); code != http.StatusForbidden {
		t.Fatalf("an employee set the fields: %d", code)
	}
	if code, _, _ := do(anna, "POST", fields, url.Values{"f.0.name": {"INN"}, "f.0.title": {"ИНН клиента"}, "f.0.required": {"1"},
		"f.1.name": {"Delivery"}, "f.1.value": {"Самовывоз"}, "f.2.name": {"Note"}, "f.2.hide_row": {"1"}, "f.3.name": {"Title"}}); code != http.StatusSeeOther {
		t.Fatalf("save fields: %d", code)
	}
	set, err := srv.CRM.DocTemplateFields(ctx, tplID)
	must(err)
	if len(set) != 3 || !set["INN"].Required || set["INN"].Title != "ИНН клиента" || set["Delivery"].Value != "Самовывоз" || !set["Note"].HideRow {
		t.Fatalf("saved: %+v", set)
	}

	// a required field empty: no document, no number taken, «Изменить значения» opens
	card := "/crm/deals/" + deal
	code, loc, _ := do(eve, "POST", card+"/document", url.Values{"template": {tplID}})
	if code != http.StatusSeeOther || !strings.HasPrefix(loc, card+"/document?") || !strings.Contains(loc, url.QueryEscape("ИНН клиента")) {
		t.Fatalf("required empty: %d %s", code, loc)
	}
	var docs, counter int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_documents`).Scan(&docs))
	must(pool.QueryRow(ctx, `SELECT coalesce(max(last), 0) FROM doc_numerators`).Scan(&counter))
	if docs != 0 || counter != 0 {
		t.Fatalf("made %d documents, number %d", docs, counter)
	}
	_, _, form := do(eve, "GET", card+"/document?template="+tplID, nil)
	if !strings.Contains(form, "ИНН клиента") || !strings.Contains(form, `name="v.INN" value="" required`) || !strings.Contains(form, `value="Самовывоз"`) {
		t.Fatalf("the form: %s", form)
	}
	if code, loc, _ := do(eve, "POST", card+"/document", url.Values{"template": {tplID}, "v.INN": {"7701234567"}}); code != http.StatusSeeOther || !strings.Contains(loc, "#documents") {
		t.Fatalf("make: %d %s", code, loc)
	}
	list, err := srv.CRM.Documents(ctx, "deal", deal)
	must(err)
	if len(list) != 1 {
		t.Fatalf("documents: %+v", list)
	}
	m, err := srv.Storages.FileMeta(ctx, list[0].FileID)
	must(err)
	obj, err := srv.Storages.Open(ctx, m.StorageID, m.Key)
	must(err)
	raw, _ := io.ReadAll(obj)
	obj.Close()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	must(err)
	rc, err := zr.Open("word/document.xml")
	must(err)
	body, _ := io.ReadAll(rc)
	if text := string(body); !strings.Contains(text, "ИНН 7701234567") || !strings.Contains(text, "Доставка: Самовывоз") ||
		strings.Contains(text, "Примечание") || !strings.Contains(text, "Поставка") {
		t.Fatalf("the document: %s", text)
	}

	// the step of a business process makes it with the required field empty
	if _, err := srv.BPDocument(ctx, anna, "deal", deal, tplID, nil); err != nil {
		t.Fatalf("business process: %v", err)
	}

	// REST
	k, err := srv.Keys.Create(ctx, anna, "k", []string{"documents:read", "documents:write", "crm:read"}, time.Hour)
	must(err)
	ek, err := srv.Keys.Create(ctx, eve, "k", []string{"documents:read", "documents:write", "crm:read"}, time.Hour)
	must(err)
	call := func(method, path, token string, in any, want int, out any) {
		t.Helper()
		var body io.Reader
		if in != nil {
			raw, _ := json.Marshal(in)
			body = bytes.NewReader(raw)
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %s", method, path, res.StatusCode, want, raw)
		}
		if out != nil {
			must(json.Unmarshal(raw, out))
		}
	}
	var got []crm.DocField
	call("GET", "/api/v1/document-templates/"+tplID+"/fields", ek, nil, http.StatusOK, &got)
	if len(got) != 4 || got[0].Name != "Title" || got[1].Name != "INN" || !got[1].Required || got[2].Value != "Самовывоз" {
		t.Fatalf("REST fields: %+v", got)
	}
	call("PUT", "/api/v1/document-templates/"+tplID+"/fields", ek, []crm.DocField{}, http.StatusForbidden, nil)
	call("PUT", "/api/v1/document-templates/"+tplID+"/fields", k, []crm.DocField{{Name: "Note", Required: true}}, http.StatusOK, &got)
	if len(got) != 4 || got[1].Required || !got[3].Required || got[2].Value != "" {
		t.Fatalf("REST set: %+v", got)
	}
	call("POST", "/api/v1/documents", ek, map[string]any{"template": tplID, "entity": "deals", "entity_id": deal}, http.StatusUnprocessableEntity, nil)
	set, err = srv.CRM.DocTemplateFields(ctx, tplID)
	must(err)
	out := portalDocFieldsOut([]string{"Note", "Title"}, map[string]string{"Title": "Поставка"}, set)
	if f := out["Note"].(map[string]any); f["required"] != "Y" || f["hideRow"] != "N" || out["Title"].(map[string]any)["value"] != "Поставка" {
		t.Fatalf("getfields: %+v", out)
	}
}
