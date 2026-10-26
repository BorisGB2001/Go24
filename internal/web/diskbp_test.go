package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/bp"
	"go24/internal/company"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestDiskCardBP: «Бизнес-процессы» on the card of a document of «Общий диск» — the templates of files to start, the
// process started from the card is listed there with its status and is stopped back to the card; a file of «Мой
// диск» has no such block; the template's editor offers the documents of «Общий диск».
func TestDiskCardBP(t *testing.T) {
	dbURL := os.Getenv("GO24_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
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
	var boss, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), Storages: storages.NewStore(pool, nil, nil), CRM: crm.NewStore(pool),
		Company: company.NewStore(pool), Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv.BP = bp.NewStore(pool, ts, srv.CRM, srv.Company)
	srv.BP.Files = srv.Drive
	doc, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "договор.docx", ContentType: "text/plain",
		UploadedBy: boss, Drive: "company"})
	must(err)
	mine, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "моё.docx", ContentType: "text/plain",
		UploadedBy: eve, Drive: "user", DriveOwner: eve})
	must(err)
	tpl, err := srv.BP.SaveTemplate(ctx, boss, bp.Template{Name: "Согласование договора", Entity: bp.EntityFile, Active: true,
		Steps: []bp.Step{{Kind: "approve", To: "responsible", Text: "Согласуйте {title}"}}})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string, admin bool) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id, Admin: admin, AdminMode: admin}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss, true), eve: cookie(eve, false)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) (string, string) {
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
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", method, path, res.StatusCode, want, raw)
		}
		return string(raw), res.Header.Get("Location")
	}

	start := `href="/bp/start/` + tpl + `?entity_id=` + doc + `"`
	card, _ := do(eve, "GET", "/files/"+doc+"/about", nil, 200)
	if !strings.Contains(card, start) {
		t.Fatal("the card of a document of «Общий диск» has no «Бизнес-процессы»")
	}
	if card, _ := do(eve, "GET", "/files/"+mine+"/about", nil, 200); strings.Contains(card, "/bp/start/") {
		t.Fatal("a file of «Мой диск» offers the processes of «Общий диск»")
	}
	_, loc := do(eve, "POST", "/bp/start/"+tpl, url.Values{"entity_id": {doc}}, 303)
	id, ok := strings.CutPrefix(loc, "/bp/i/")
	if !ok {
		t.Fatalf("started to %q", loc)
	}
	var who, text string
	must(pool.QueryRow(ctx, `SELECT user_id::text, text FROM bp_approvals WHERE instance_id::text = $1`, id).Scan(&who, &text))
	if who != boss || text != "Согласуйте договор.docx" {
		t.Fatalf("approval of %s: %q", who, text)
	}
	card, _ = do(boss, "GET", "/files/"+doc+"/about", nil, 200)
	if !strings.Contains(card, `href="/bp/i/`+id+`"`) || !strings.Contains(card, `action="/bp/i/`+id+`/stop"`) {
		t.Fatal("the card does not list the process to stop")
	}
	if _, loc := do(boss, "POST", "/bp/i/"+id+"/stop", url.Values{"back": {"/files/" + doc + "/about"}}, 303); loc != "/files/"+doc+"/about" {
		t.Fatalf("stopped back to %q", loc)
	}
	page, _ := do(boss, "GET", "/bp/i/"+id, nil, 200)
	if !strings.Contains(page, `href="/files/`+doc+`/about"`) {
		t.Fatal("the process does not link the file")
	}
	if page, _ := do(boss, "GET", "/bp/templates/"+tpl, nil, 200); !strings.Contains(page, `<option value="file" selected>`) {
		t.Fatal("the editor does not offer the documents of «Общий диск»")
	}
}
