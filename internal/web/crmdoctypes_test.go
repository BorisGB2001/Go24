package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDocTemplatesOfSmartProcess: a template of a smart process is offered on its cards only and makes their documents;
// one of deals is not offered there nor makes them; the document robot of the smart process's stage takes its own
// templates; REST lists them by type_id (GO24DEV-847).
func TestDocTemplatesOfSmartProcess(t *testing.T) {
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
	var anna string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('anna', 'Анна', 'anna@t') RETURNING id`).Scan(&anna))
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Storages: storages.NewStore(pool, nil, s3),
		Keys: apikeys.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: anna}})
	must(err)
	typ, err := srv.CRM.AddType(ctx, "Договоры", "")
	must(err)
	tp, err := srv.CRM.PipelinesOf(ctx, typ)
	must(err)
	smart, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Аренда склада", PipelineID: tp[0].ID, TypeID: typ, Responsible: crm.Ref{ID: anna}})
	must(err)

	var tpl bytes.Buffer
	zw := zip.NewWriter(&tpl)
	f, _ := zw.Create("word/document.xml")
	io.WriteString(f, `<w:document><w:t>№ {DocumentNumber}: {Title}</w:t></w:document>`)
	zw.Close()
	add := func(name, kind string) (string, error) {
		return srv.addTemplate(ctx, anna, name, kind, "t.docx", tpl.Bytes())
	}
	ofDeals, err := add("Счёт сделки", "deal")
	must(err)
	ofType, err := add("Договор аренды", crm.TypeKind(typ))
	must(err)
	var ve *i18n.Error
	if _, err := add("Чужой", crm.TypeKind("00000000-0000-0000-0000-000000000000")); !errors.As(err, &ve) {
		t.Fatalf("a template of no smart process: %v", err)
	}
	if l, _ := srv.CRM.DocTemplates(ctx, "deal"); len(l) != 1 || l[0].ID != ofDeals {
		t.Fatalf("the deals' templates: %+v", l)
	}
	if l, _ := srv.CRM.DocTemplates(ctx, crm.TypeKind(typ)); len(l) != 1 || l[0].ID != ofType || l[0].Entity != "deal" || l[0].TypeID != typ {
		t.Fatalf("the smart process's templates: %+v", l)
	}

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: anna, Admin: true, AdminMode: true}))
	cookie := rec.Result().Cookies()[0]
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, path string, form url.Values) (int, string) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(raw)
	}

	// the cards offer the templates of their own kind
	if _, page := do("GET", "/crm/deals/"+smart, nil); !strings.Contains(page, "Договор аренды") || strings.Contains(page, "Счёт сделки") {
		t.Fatal("the smart process's card offers other templates than its own")
	}
	if _, page := do("GET", "/crm/deals/"+deal, nil); strings.Contains(page, "Договор аренды") || !strings.Contains(page, "Счёт сделки") {
		t.Fatal("the deal offers the smart process's template")
	}
	// «Генератор документов»: the template is listed with its smart process, which is offered for a new one
	if _, page := do("GET", "/crm/documents", nil); !strings.Contains(page, "<td>Договоры</td>") || !strings.Contains(page, `value="type:`+typ+`"`) {
		t.Fatal("the templates' page does not name the smart process")
	}

	// documents: by the card's own template only
	do("POST", "/crm/deals/"+smart+"/document", url.Values{"template": {ofDeals}})
	if docs, _ := srv.CRM.Documents(ctx, "deal", smart); len(docs) != 0 {
		t.Fatal("a deals' template made a document of the smart process's card")
	}
	do("POST", "/crm/deals/"+deal+"/document", url.Values{"template": {ofType}})
	if docs, _ := srv.CRM.Documents(ctx, "deal", deal); len(docs) != 0 {
		t.Fatal("the smart process's template made a document of a deal")
	}
	if code, _ := do("POST", "/crm/deals/"+smart+"/document", url.Values{"template": {ofType}}); code != http.StatusSeeOther {
		t.Fatalf("document of the smart process: %d", code)
	}
	docs, _ := srv.CRM.Documents(ctx, "deal", smart)
	if len(docs) != 1 || docs[0].TemplateName != "Договор аренды" {
		t.Fatalf("the smart process's documents: %+v", docs)
	}
	if code, _ := do("POST", "/crm/deals/"+smart+"/documents/"+docs[0].ID, url.Values{"v.Title": {"Аренда офиса"}}); code != http.StatusSeeOther {
		t.Fatalf("made again: %d", code)
	}

	// the robot «Создать документ» of the smart process's stage
	stage := tp[0].Stages[0].ID
	if err := srv.CRM.AddRobot(ctx, anna, stage, "document", crm.RobotParams{Doc: ofDeals}); !errors.As(err, &ve) || ve.Key != "err.robot_doc" {
		t.Fatalf("the robot took a deals' template: %v", err)
	}
	must(srv.CRM.AddRobot(ctx, anna, stage, "document", crm.RobotParams{Doc: ofType}))
	if err := srv.CRM.AddRobot(ctx, anna, pipes[0].Stages[0].ID, "document", crm.RobotParams{Doc: ofType}); !errors.As(err, &ve) {
		t.Fatalf("a deals' robot took the smart process's template: %v", err)
	}

	// REST: ?entity=deals — of deals; &type_id= — of the smart process; a new one with type_id
	key, err := srv.Keys.Create(ctx, anna, "k", []string{"documents:read", "documents:write"}, time.Hour)
	must(err)
	list := func(q string) []apiDocTemplate {
		t.Helper()
		req, _ := http.NewRequest("GET", hs.URL+"/api/v1/document-templates"+q, nil)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		var out []apiDocTemplate
		must(json.NewDecoder(res.Body).Decode(&out))
		return out
	}
	if l := list("?entity=deals"); len(l) != 1 || l[0].ID != ofDeals {
		t.Fatalf("REST, deals: %+v", l)
	}
	if l := list("?entity=deals&type_id=" + typ); len(l) != 1 || l[0].ID != ofType || l[0].TypeID != typ || l[0].Entity != "deals" {
		t.Fatalf("REST, the smart process: %+v", l)
	}

	// deleting a smart process deletes its templates
	other, err := srv.CRM.AddType(ctx, "Заявки", "")
	must(err)
	ofOther, err := add("Заявление", crm.TypeKind(other))
	must(err)
	must(srv.CRM.DeleteType(ctx, other))
	if _, err := srv.CRM.DocTemplate(ctx, ofOther); !errors.Is(err, crm.ErrNotFound) {
		t.Fatalf("the template outlived its smart process: %v", err)
	}
}
