package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sign"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestRestPortalDocGen: crm.documentgenerator.* — an administrator adds a numerator and a template of deals by base64,
// gives it to one employee; he sees its fields with the deal's values, makes a document with his value, lists, gets,
// remakes it (the number stays), downloads it by downloadUrl, switches its public link; one who does not see the deal
// or the template does not; a non-administrator may not change templates and numerators.
func TestRestPortalDocGen(t *testing.T) {
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
	user := func(name string, admin bool) (string, int64) {
		var id string
		var n int64
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, admin) VALUES ($1, $1, $1 || '@t', $2) RETURNING id, num`,
			name, admin).Scan(&id, &n))
		return id, n
	}
	anna, annaNum := user("anna", true)
	bob, bobNum := user("bob", false)
	eve, eveNum := user("eve", false)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: crm.NewStore(pool), Storages: storages.NewStore(pool, nil, s3),
		Keys: apikeys.NewStore(pool), Sign: sign.NewStore(pool), I18n: bundle, BaseURL: "https://go24",
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer bundle.SetFilter(nil)
	role, err := srv.CRM.SaveRole(ctx, crm.Role{Name: "Свои", Perms: crm.Perms{"deal": {"read": "own", "write": "own"}}})
	must(err)
	must(srv.CRM.SetMember(ctx, role, eve, true))
	pipes, err := srv.CRM.Pipelines(ctx)
	must(err)
	deal, err := srv.CRM.SaveDeal(ctx, anna, crm.Deal{Title: "Поставка", Amount: 1500, PipelineID: pipes[0].ID, Responsible: crm.Ref{ID: bob}})
	must(err)
	var dealNum int64
	must(pool.QueryRow(ctx, `SELECT num FROM crm_deals WHERE id = $1`, deal).Scan(&dealNum))
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	num := func(n int64) string { return strconv.FormatInt(n, 10) }
	hook := func(uid string, n int64) string {
		token, err := srv.Keys.Create(ctx, uid, "k", apikeys.AllScopes, 0)
		must(err)
		return "/rest/" + num(n) + "/" + token + "/"
	}
	annaHook, bobHook, eveHook := hook(anna, annaNum), hook(bob, bobNum), hook(eve, eveNum)
	call := func(hook, method string, in any, want int) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(in)
		res, err := http.Post(hs.URL+hook+method+".json", "application/json", bytes.NewReader(raw))
		must(err)
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s: %d, want %d: %.500s", method, res.StatusCode, want, body)
		}
		var out map[string]any
		must(json.Unmarshal(body, &out))
		if r, ok := out["result"].(map[string]any); ok {
			return r
		}
		return out
	}
	str := func(v any) string { return portalStr(v) }
	docx := func(text string) string {
		var b bytes.Buffer
		zw := zip.NewWriter(&b)
		f, _ := zw.Create("word/document.xml")
		io.WriteString(f, `<w:document><w:body><w:p><w:r><w:t>`+text+`</w:t></w:r></w:p><w:sectPr/></w:body></w:document>`)
		zw.Close()
		return base64.StdEncoding.EncodeToString(b.Bytes())
	}

	// numerators: administrators'
	numIn := map[string]any{"fields": map[string]any{"name": "Договоры", "template": "Д-{NUMBER}",
		"settings": map[string]any{portalNumeratorGen: map[string]any{"start": 7, "periodicBy": "year"}}}}
	call(bobHook, "crm.documentgenerator.numerator.add", numIn, http.StatusForbidden)
	n := call(annaHook, "crm.documentgenerator.numerator.add", numIn, http.StatusOK)["numerator"].(map[string]any)
	gen := n["settings"].(map[string]any)[portalNumeratorGen].(map[string]any)
	if n["template"] != "Д-{NUMBER}" || str(gen["start"]) != "7" || gen["periodicBy"] != "year" {
		t.Fatalf("numerator: %v", n)
	}

	// a template of deals, given to bob
	tplIn := map[string]any{"fields": map[string]any{"name": "Договор", "file": docx("Договор № {DocumentNumber}: {Title}, срок {Term}"),
		"entityTypeId": []string{"2"}, "numeratorId": n["id"], "users": []string{"U" + num(bobNum)}}}
	call(bobHook, "crm.documentgenerator.template.add", tplIn, http.StatusForbidden)
	tpl := call(annaHook, "crm.documentgenerator.template.add", tplIn, http.StatusOK)["template"].(map[string]any)
	if tpl["name"] != "Договор" || str(tpl["numeratorId"]) != str(n["id"]) || str(tpl["entityTypeId"].([]any)[0]) != "2" ||
		str(tpl["users"].([]any)[0]) != "U"+num(bobNum) {
		t.Fatalf("template: %v", tpl)
	}
	list := func(hook string, filter map[string]any) []any {
		return call(hook, "crm.documentgenerator.template.list", map[string]any{"filter": filter}, http.StatusOK)["templates"].([]any)
	}
	if l := list(bobHook, nil); len(l) != 1 || str(l[0].(map[string]any)["id"]) != str(tpl["id"]) {
		t.Fatalf("bob's templates: %v", l)
	}
	if l := list(bobHook, map[string]any{"entityTypeId": 1}); len(l) != 0 {
		t.Fatalf("templates of leads: %v", l)
	}
	if l := list(eveHook, nil); len(l) != 0 {
		t.Fatalf("eve's templates: %v", l)
	}
	call(eveHook, "crm.documentgenerator.template.get", map[string]any{"id": tpl["id"]}, http.StatusBadRequest)
	fields := call(bobHook, "crm.documentgenerator.template.getfields", map[string]any{"id": tpl["id"], "entityTypeId": 2,
		"entityId": dealNum, "values": map[string]any{"Term": "10 дней"}}, http.StatusOK)["templateFields"].(map[string]any)
	if fields["Title"].(map[string]any)["value"] != "Поставка" || fields["Term"].(map[string]any)["value"] != "10 дней" || len(fields) != 3 {
		t.Fatalf("template fields: %v", fields)
	}

	// a document of the deal
	mk := map[string]any{"templateId": tpl["id"], "entityTypeId": 2, "entityId": dealNum, "values": map[string]any{"Term": "10 дней"}}
	call(eveHook, "crm.documentgenerator.document.add", mk, http.StatusForbidden)
	doc := call(bobHook, "crm.documentgenerator.document.add", mk, http.StatusOK)["document"].(map[string]any)
	if doc["number"] != "Д-7" || str(doc["entityId"]) != num(dealNum) || str(doc["entityTypeId"]) != "2" ||
		str(doc["templateId"]) != str(tpl["id"]) || doc["values"].(map[string]any)["Term"] != "10 дней" || doc["stampsEnabled"] != true {
		t.Fatalf("document: %v", doc)
	}
	text := func(link string) string {
		t.Helper()
		res, err := http.Get(strings.Replace(link, "https://go24", hs.URL, 1))
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		must(err)
		f, err := zr.Open("word/document.xml")
		must(err)
		b, _ := io.ReadAll(f)
		return string(b)
	}
	if got := text(str(doc["downloadUrl"])); !strings.Contains(got, "Договор № Д-7: Поставка, срок 10 дней") {
		t.Fatalf("docx: %s", got)
	}
	docs := call(bobHook, "crm.documentgenerator.document.list", map[string]any{"filter": map[string]any{"entityTypeId": 2,
		"entityId": dealNum}}, http.StatusOK)["documents"].([]any)
	if len(docs) != 1 || str(docs[0].(map[string]any)["id"]) != str(doc["id"]) {
		t.Fatalf("documents: %v", docs)
	}
	call(eveHook, "crm.documentgenerator.document.list", map[string]any{"filter": map[string]any{"entityTypeId": 2, "entityId": dealNum}}, http.StatusForbidden)
	call(eveHook, "crm.documentgenerator.document.get", map[string]any{"id": doc["id"]}, http.StatusForbidden)
	doc = call(bobHook, "crm.documentgenerator.document.update", map[string]any{"id": doc["id"], "values": map[string]any{"Term": "5 дней"},
		"stampsEnabled": "N"}, http.StatusOK)["document"].(map[string]any)
	if doc["number"] != "Д-7" || doc["stampsEnabled"] != false || !strings.Contains(text(str(doc["downloadUrl"])), "срок 5 дней") {
		t.Fatalf("remade: %v", doc)
	}
	df := call(bobHook, "crm.documentgenerator.document.getfields", map[string]any{"id": doc["id"]}, http.StatusOK)["documentFields"].(map[string]any)
	if df["Term"].(map[string]any)["value"] != "5 дней" || df["DocumentNumber"].(map[string]any)["value"] != "Д-7" {
		t.Fatalf("document fields: %v", df)
	}
	pub := call(bobHook, "crm.documentgenerator.document.enablepublicurl", map[string]any{"id": doc["id"], "status": 1}, http.StatusOK)
	if !strings.HasPrefix(str(pub["publicUrl"]), "https://go24/d/") {
		t.Fatalf("public: %v", pub)
	}
	if pub = call(bobHook, "crm.documentgenerator.document.enablepublicurl", map[string]any{"id": doc["id"], "status": 0}, http.StatusOK); pub["publicUrl"] != nil {
		t.Fatalf("public off: %v", pub)
	}

	// changes of the template: the name, everybody may use it
	tpl = call(annaHook, "crm.documentgenerator.template.update", map[string]any{"id": tpl["id"], "fields": map[string]any{"name": "Договор поставки",
		"users": []string{"UA"}}}, http.StatusOK)["template"].(map[string]any)
	if tpl["name"] != "Договор поставки" || str(tpl["users"].([]any)[0]) != "UA" {
		t.Fatalf("template changed: %v", tpl)
	}
	if l := list(eveHook, map[string]any{"%name": "поставки"}); len(l) != 1 {
		t.Fatalf("eve's templates now: %v", l)
	}
	// ACTIVE, SORT and WITH_STAMPS: an inactive template is not offered to eve, a document by it is not made
	tpl = call(annaHook, "crm.documentgenerator.template.update", map[string]any{"id": tpl["id"], "fields": map[string]any{"active": "N",
		"sort": 100, "withStamps": "N"}}, http.StatusOK)["template"].(map[string]any)
	if tpl["active"] != "N" || tpl["withStamps"] != "N" || tpl["sort"] != float64(100) || tpl["updatedBy"] == nil {
		t.Fatalf("template settings: %v", tpl)
	}
	if l := list(eveHook, map[string]any{"%name": "поставки"}); len(l) != 0 {
		t.Fatalf("eve sees an inactive template: %v", l)
	}
	if l := list(annaHook, map[string]any{"active": "N"}); len(l) != 1 {
		t.Fatalf("inactive templates: %v", l)
	}
	call(bobHook, "crm.documentgenerator.document.add", map[string]any{"templateId": tpl["id"], "entityTypeId": 2, "entityId": doc["entityId"]},
		http.StatusBadRequest)
	tpl = call(annaHook, "crm.documentgenerator.template.update", map[string]any{"id": tpl["id"], "fields": map[string]any{"active": "Y"}},
		http.StatusOK)["template"].(map[string]any)
	plain := call(bobHook, "crm.documentgenerator.document.add", map[string]any{"templateId": tpl["id"], "entityTypeId": 2,
		"entityId": doc["entityId"]}, http.StatusOK)["document"].(map[string]any)
	if tpl["active"] != "Y" || tpl["withStamps"] != "N" || plain["stampsEnabled"] != false {
		t.Fatalf("by the template's withStamps: %v, %v", tpl, plain)
	}
	call(bobHook, "crm.documentgenerator.document.delete", map[string]any{"id": plain["id"]}, http.StatusOK)
	call(annaHook, "crm.documentgenerator.numerator.delete", map[string]any{"id": n["id"]}, http.StatusBadRequest) // in use
	call(bobHook, "crm.documentgenerator.document.delete", map[string]any{"id": doc["id"]}, http.StatusOK)
	call(annaHook, "crm.documentgenerator.template.delete", map[string]any{"id": tpl["id"]}, http.StatusOK)
	if l := list(annaHook, nil); len(l) != 0 {
		t.Fatalf("templates after delete: %v", l)
	}
}
