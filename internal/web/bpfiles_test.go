package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/apikeys"
	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/feed"
	"go24/internal/i18n"
	"go24/internal/notify"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestBPFilesHTTP: a «Файл» field of a process — the start form takes the file (a copy goes to
// «Загруженные файлы»), the process's page links it, the approver downloads it and an outsider does not; the answer to
// a request attaches its file; bizproc.workflow.start takes [name, base64].
func TestBPFilesHTTP(t *testing.T) {
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
	newUser := func(name string) (string, int64) {
		var id string
		var n int64
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id, num`, name).Scan(&id, &n))
		return id, n
	}
	anna, annaNum := newUser("Анна")
	boris, _ := newUser("Борис")
	eve, _ := newUser("Ева")
	ts := tasks.NewStore(pool)
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	bundle, err := i18n.Load("")
	must(err)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, s3),
		Feed: feed.NewStore(pool, ts), Notify: notify.NewStore(pool), Keys: apikeys.NewStore(pool), Company: company.NewStore(pool),
		CRM: crm.NewStore(pool), I18n: bundle, Sessions: auth.NewSessions(strings.Repeat("s", 32), false),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.BP = bp.NewStore(pool, ts, srv.CRM, srv.Company)
	srv.BP.Feed = srv.Feed
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who, method, path, ct string, body io.Reader) (int, string, string) {
		t.Helper()
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: who}))
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		req.AddCookie(rec.Result().Cookies()[0])
		res, err := noRedirect.Do(req)
		must(err)
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b), res.Header.Get("Location")
	}
	form := func(fields map[string]string, files map[string]string) (string, io.Reader) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for k, v := range fields {
			_ = mw.WriteField(k, v)
		}
		for k, name := range files {
			fw, _ := mw.CreateFormFile(k, name)
			_, _ = fw.Write([]byte("содержимое " + name))
		}
		_ = mw.Close()
		return mw.FormDataContentType(), &b
	}

	tpl, err := srv.BP.SaveTemplate(ctx, anna, bp.Template{Name: "Счёт", Active: true, Fields: []bp.Field{
		{Key: "doc", Label: "Счёт", Kind: "file", Required: true}, {Key: "sum", Label: "Сумма", Kind: "number"}}, Steps: []bp.Step{
		{Kind: "approve", To: boris, Text: "Оплатить {doc}?"},
		{Kind: "request", To: boris, Text: "Платёжка", Fields: []bp.Field{{Key: "paid", Label: "Платёжка", Kind: "file", Required: true}}},
	}})
	must(err)
	if _, page, _ := send(anna, "GET", "/bp/start/"+tpl, "", nil); !strings.Contains(page, `enctype="multipart/form-data"`) ||
		!strings.Contains(page, `type="file" name="file_doc"`) {
		t.Fatalf("the start form: %.2000s", page)
	}
	ct, body := form(map[string]string{"f_sum": "100"}, map[string]string{"file_doc": "счёт.pdf"})
	_, _, loc := send(anna, "POST", "/bp/start/"+tpl, ct, body)
	id, ok := strings.CutPrefix(loc, "/bp/i/")
	if !ok {
		t.Fatalf("started: %s", loc)
	}
	x, err := srv.BP.Instance(ctx, anna, id)
	must(err)
	doc := x.Values["doc"]
	if _, page, _ := send(anna, "GET", "/bp/i/"+id, "", nil); !strings.Contains(page, `href="/files/`+doc+`"`) || !strings.Contains(page, "счёт.pdf") {
		t.Fatalf("the process's page: %.3000s", page)
	}
	var kept int // a copy in «Загруженные файлы» of Анна's drive
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE name = 'счёт.pdf' AND drive = 'user' AND drive_owner = $1`, anna).Scan(&kept))
	if kept != 1 {
		t.Fatalf("uploaded files: %d", kept)
	}
	for who, want := range map[string]int{anna: 200, boris: 200, eve: 403} {
		if code, b, _ := send(who, "GET", "/files/"+doc, "", nil); code != want || want == 200 && b != "содержимое счёт.pdf" {
			t.Fatalf("download by %s: %d %q", who, code, b)
		}
	}
	// «Мои задания»: the approval shows the file
	if _, page, _ := send(boris, "GET", "/bp?tab=approvals", "", nil); !strings.Contains(page, `href="/files/`+doc+`"`) {
		t.Fatalf("the approvals: %.3000s", page)
	}
	list, err := srv.BP.MyApprovals(ctx, boris)
	must(err)
	must(srv.BP.Decide(ctx, boris, list[0].ID, "approve", ""))
	list, err = srv.BP.MyApprovals(ctx, boris)
	must(err)
	ct, body = form(map[string]string{"decision": "answer", "back": "/bp/i/" + id}, map[string]string{"file_paid": "платёжка.pdf"})
	if _, _, loc := send(boris, "POST", "/bp/approvals/"+list[0].ID, ct, body); strings.Contains(loc, "msg=") {
		t.Fatalf("the answer: %s", loc)
	}
	x, err = srv.BP.Instance(ctx, anna, id)
	must(err)
	if x.Status != "done" || x.Values["paid"] == "" {
		t.Fatalf("answered: %s %v", x.Status, x.Values)
	}
	if code, b, _ := send(anna, "GET", "/files/"+x.Values["paid"], "", nil); code != 200 || b != "содержимое платёжка.pdf" {
		t.Fatalf("the answer's file: %d %q", code, b)
	}

	// bizproc.workflow.start with [name, base64]
	token, err := srv.Keys.Create(ctx, anna, "n8n", apikeys.AllScopes, 0)
	must(err)
	var tnum int64
	must(pool.QueryRow(ctx, `SELECT num FROM bp_templates WHERE id = $1`, tpl).Scan(&tnum))
	in, _ := json.Marshal(map[string]any{"TEMPLATE_ID": tnum,
		"PARAMETERS": map[string]any{"doc": []string{"акт.txt", base64.StdEncoding.EncodeToString([]byte("акт"))}}})
	res, err := http.Post(hs.URL+"/rest/"+strconv.FormatInt(annaNum, 10)+"/"+token+"/bizproc.workflow.start.json", "application/json", bytes.NewReader(in))
	must(err)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var out struct{ Result string }
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode != 200 || out.Result == "" {
		t.Fatalf("bizproc.workflow.start: %d %s", res.StatusCode, raw)
	}
	x, err = srv.BP.Instance(ctx, anna, out.Result)
	must(err)
	if code, b, _ := send(boris, "GET", "/files/"+x.Values["doc"], "", nil); code != 200 || b != "акт" {
		t.Fatalf("the REST file: %d %q", code, b)
	}
}
