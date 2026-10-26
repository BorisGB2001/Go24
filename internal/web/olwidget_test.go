package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/openlines"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestWidgetFilesHTTP: the visitor sends a picture from the widget and gets it by its link (not by a wrong secret),
// the operator sees it and answers with a file; the widget shows the operator, «печатает…» and «прочитано».
func TestWidgetFilesHTTP(t *testing.T) {
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
	var op string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('op', 'Оля', 'op@t') RETURNING id`).Scan(&op))
	s3, err := storage.New(ctx, s3ep, "go24testkey", "go24testsecret", "go24test", false)
	must(err)
	ol := openlines.NewStore(pool, crm.NewStore(pool))
	ol.Tr = func(key string, _ ...any) string { return key }
	srv := &Server{DB: pool, Tasks: tasks.NewStore(pool), CRM: ol.CRM, OpenLines: ol, Storages: storages.NewStore(pool, nil, s3),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	wid, err := ol.SaveWidget(ctx, op, openlines.Widget{Name: "Сайт", Active: true, Operators: []string{op}})
	must(err)
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	rec := httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: op}))
	cookie := rec.Result().Cookies()[0]
	do := func(req *http.Request, want int) []byte {
		t.Helper()
		res, err := http.DefaultClient.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", req.Method, req.URL.Path, res.StatusCode, want, raw)
		}
		return raw
	}
	multi := func(fields map[string]string, name string, data []byte) (*bytes.Buffer, string) {
		var b bytes.Buffer
		mw := multipart.NewWriter(&b)
		for k, v := range fields {
			_ = mw.WriteField(k, v)
		}
		fw, _ := mw.CreateFormFile("file", name)
		_, _ = fw.Write(data)
		_ = mw.Close()
		return &b, mw.FormDataContentType()
	}
	api := hs.URL + "/w/" + wid + "/api/"
	req, _ := http.NewRequest("POST", api+"visit", strings.NewReader(`{}`))
	var v struct{ Token string }
	must(json.Unmarshal(do(req, 200), &v))
	visitor := func(method, op string, body io.Reader, ct string) *http.Request {
		req, _ := http.NewRequest(method, api+op, body)
		req.Header.Set("X-Go24-Visitor", v.Token)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		return req
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	b, ct := multi(map[string]string{"body": "Вот фото"}, "photo.png", png)
	do(visitor("POST", "file", b, ct), 200)

	type msg struct {
		ID       int64
		Operator string
		Body     string
		File     *struct {
			Name, URL string
			Image     bool
		}
	}
	var got struct {
		Messages []msg
		Operator struct {
			Name   string
			Typing bool
			Read   int64
		}
	}
	must(json.Unmarshal(do(visitor("GET", "messages", nil, ""), 200), &got))
	if len(got.Messages) != 1 || got.Messages[0].File == nil || got.Messages[0].Body != "Вот фото" {
		t.Fatalf("messages = %+v", got)
	}
	f := got.Messages[0].File
	if f.Name != "photo.png" || !strings.HasPrefix(f.URL, hs.URL+"/w/"+wid+"/file/") {
		t.Fatalf("file = %+v", f)
	}
	req, _ = http.NewRequest("GET", f.URL, nil)
	if raw := do(req, 200); !bytes.Equal(raw, png) {
		t.Fatalf("the visitor's file = %q", raw)
	}
	req, _ = http.NewRequest("GET", f.URL[:len(f.URL)-4]+"0000", nil)
	do(req, 404)

	// the operator: the page shows the picture, the file is theirs to get; they type and answer with a file
	var sid string
	must(pool.QueryRow(ctx, `SELECT id FROM ol_sessions`).Scan(&sid))
	user := func(method, path string, body io.Reader, ct string) *http.Request {
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(cookie)
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		return req
	}
	page := string(do(user("GET", "/openlines?s="+sid, nil, ""), 200))
	link := "/openlines/file/" + strconv.FormatInt(got.Messages[0].ID, 10)
	if !strings.Contains(page, `<img src="`+link+`"`) {
		t.Fatal("the operator's page has no picture")
	}
	if raw := do(user("GET", link, nil, ""), 200); !bytes.Equal(raw, png) {
		t.Fatal("the operator's file")
	}
	do(user("POST", "/openlines/s/"+sid+"/typing", nil, ""), 204)
	// a colleague who is not the line's operator sees neither the dialogue nor its file, nor may type in it
	var stranger string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('x', 'Чужой', 'x@t') RETURNING id`).Scan(&stranger))
	rec = httptest.NewRecorder()
	must(srv.Sessions.Set(rec, auth.Session{UserID: stranger}))
	other := rec.Result().Cookies()[0]
	as := func(method, path string) *http.Request {
		req, _ := http.NewRequest(method, hs.URL+path, nil)
		req.AddCookie(other)
		return req
	}
	do(as("GET", "/openlines?s="+sid), 404)
	if page := string(do(as("GET", "/openlines"), 200)); strings.Contains(page, sid) {
		t.Fatal("a stranger sees the dialogue in the list")
	}
	do(as("GET", link), 404)
	do(as("POST", "/openlines/s/"+sid+"/typing"), 404)
	// «Задача» of the dialogue: the form is filled with it for the operator, not for a stranger
	_, err = srv.Tasks.CreateProject(ctx, op, "OL", "Линии", "")
	must(err)
	if form := string(do(user("GET", "/tasks/new?ol="+sid, nil, ""), 200)); !strings.Contains(form, `name="ol" value="`+sid+`"`) || !strings.Contains(form, "Вот фото") {
		t.Fatalf("the task's form of the dialogue: %s", form)
	}
	if form := string(do(as("GET", "/tasks/new?ol="+sid), 200)); strings.Contains(form, `name="ol"`) {
		t.Fatal("a stranger's task of the dialogue")
	}
	do(user("POST", "/tasks/new", strings.NewReader(url.Values{"project": {"OL"}, "title": {"Перезвонить"}, "responsible_id": {op}, "ol": {sid}}.Encode()),
		"application/x-www-form-urlencoded"), 200)
	var note string
	must(pool.QueryRow(ctx, `SELECT body FROM ol_messages WHERE session_id = $1 AND kind = 'note' ORDER BY id DESC LIMIT 1`, sid).Scan(&note))
	if note != "ol.note_task" {
		t.Fatalf("the dialogue's note of the task %q", note)
	}
	must(json.Unmarshal(do(visitor("GET", "messages", nil, ""), 200), &got))
	if !got.Operator.Typing || got.Operator.Read == 0 {
		t.Fatalf("typing and read = %+v", got.Operator)
	}
	b, ct = multi(map[string]string{"op": "reply", "body": ""}, "bill.pdf", []byte("%PDF-1.4"))
	do(user("POST", "/openlines/s/"+sid, b, ct), 200) // the redirect to the dialogue, followed
	must(json.Unmarshal(do(visitor("GET", "messages?seen=1", nil, ""), 200), &got))
	last := got.Messages[len(got.Messages)-1]
	if last.Operator != "Оля" || last.File == nil || last.File.Name != "bill.pdf" || last.File.Image || got.Operator.Name != "Оля" || got.Operator.Typing {
		t.Fatalf("after the answer = %+v", got)
	}
	do(visitor("POST", "typing", strings.NewReader(`{}`), "application/json"), 200)
	page = string(do(user("GET", "/openlines?s="+sid, nil, ""), 200))
	if !strings.Contains(page, "olread") {
		t.Fatal("no «прочитано» under the operator's message")
	}

	// «Согласие на обработку персональных данных»: no message, file or contact form before it; given once, with the address
	_, err = ol.SaveWidget(ctx, op, openlines.Widget{ID: wid, Name: "Сайт", Active: true, Operators: []string{op}, ConsentOn: true})
	must(err)
	if wd, _ := ol.Widget(ctx, wid); wd.ConsentText != "ol.consent_default" {
		t.Fatalf("the default agreement %q", wd.ConsentText)
	}
	if js := string(do(must2(http.NewRequest("GET", hs.URL+"/w/"+wid+"/widget.js", nil)), 200)); !strings.Contains(js, `"consent":"ol.consent_default"`) {
		t.Fatal("the widget has no agreement")
	}
	do(visitor("POST", "say", strings.NewReader(`{"body":"ещё вопрос"}`), "application/json"), 422)
	do(visitor("POST", "contact", strings.NewReader(`{"name":"Иван"}`), "application/json"), 422)
	b, ct = multi(map[string]string{}, "a.png", png)
	do(visitor("POST", "file", b, ct), 422)
	var c struct{ Consent bool }
	must(json.Unmarshal(do(visitor("GET", "messages", nil, ""), 200), &c))
	if c.Consent {
		t.Fatal("consented before accepting")
	}
	do(visitor("POST", "consent", strings.NewReader(`{}`), "application/json"), 200)
	do(visitor("POST", "say", strings.NewReader(`{"body":"ещё вопрос"}`), "application/json"), 200)
	must(json.Unmarshal(do(visitor("GET", "messages", nil, ""), 200), &c))
	var ip string
	must(pool.QueryRow(ctx, `SELECT consent_ip FROM ol_sessions WHERE id = $1`, sid).Scan(&ip))
	if !c.Consent || ip == "" {
		t.Fatalf("consent %v from %q", c.Consent, ip)
	}
	if page := string(do(user("GET", "/openlines?s="+sid, nil, ""), 200)); !strings.Contains(page, "✓ ") {
		t.Fatal("the operator does not see the consent")
	}
}

func must2[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
