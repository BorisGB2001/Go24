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
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/db"
	"go24/internal/drive"
	"go24/internal/kb"
	"go24/internal/storage"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestKBHTTP: «База знаний» over HTTP — the settings and «Права доступа» of a base (a closed base is not found by
// the others, nor its pictures), a page moved and copied, an earlier version compared and restored, a picture
// uploaded into a page, the trash, a page dragged in the tree.
func TestKBHTTP(t *testing.T) {
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
	var boss, ann, eve string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('ann', 'Анна', 'ann@go24.test') RETURNING id`).Scan(&ann))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('eve', 'Ева', 'eve@go24.test') RETURNING id`).Scan(&eve))
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, Drive: drive.NewStore(pool, ts), KB: kb.NewStore(pool), Storages: storages.NewStore(pool, nil, nil),
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if ep := os.Getenv("GO24_TEST_S3"); ep != "" {
		s3, err := storage.New(ctx, ep, "go24testkey", "go24testsecret", "go24test", false)
		must(err)
		srv.Storages = storages.NewStore(pool, nil, s3)
	}
	space, err := srv.KB.CreateSpace(ctx, boss, "Регламенты", "", "")
	must(err)
	other, err := srv.KB.CreateSpace(ctx, boss, "Архив", "", "")
	must(err)
	pg, err := srv.KB.CreatePage(ctx, boss, space, "", "Отпуск", "за две недели")
	must(err)
	pic, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "схема.png", ContentType: "image/png",
		UploadedBy: boss, KBSpaceID: space})
	must(err)

	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss), ann: cookie(ann), eve: cookie(eve)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(who, method, path string, form url.Values, want int) string {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		req.AddCookie(as[who])
		req.Header.Set("X-Go24-Form", "1")
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
		return string(raw)
	}

	// the settings: only the one who manages the base has them
	if !strings.Contains(do(boss, "GET", "/kb/"+space+"/"+pg, nil, 200), `href="/kb/`+space+`/settings"`) {
		t.Fatal("the creator has no «Настройки базы»")
	}
	if strings.Contains(do(eve, "GET", "/kb/"+space+"/"+pg, nil, 200), `/kb/`+space+`/settings"`) {
		t.Fatal("an employee has «Настройки базы»")
	}
	do(eve, "GET", "/kb/"+space+"/settings", nil, 403)
	do(boss, "POST", "/kb/"+space+"/settings", url.Values{"name": {"Правила"}, "description": {"для всех"}}, 200)
	do(boss, "POST", "/kb/"+space+"/rights", url.Values{"whom": {"user:" + ann}, "access": {"edit"}}, 200)
	if page := do(boss, "GET", "/kb/"+space+"/settings", nil, 200); !strings.Contains(page, "Анна") || !strings.Contains(page, `value="Правила"`) {
		t.Fatal("the settings page has no right of Анна or the new name")
	}
	do(eve, "GET", "/kb/"+space+"/"+pg, nil, 404)
	do(eve, "GET", "/files/"+pic, nil, 403)
	if strings.Contains(do(eve, "GET", "/kb", nil, 200), "Правила") {
		t.Fatal("a closed base is in the list of one it is not open to")
	}
	do(ann, "GET", "/files/"+pic, nil, 200)
	if page := do(ann, "GET", "/kb/"+space+"/"+pg, nil, 200); !strings.Contains(page, `action="/kb/`+space+`/`+pg+`/move"`) {
		t.Fatal("an editor cannot move the page")
	}

	// a version: compared with the current text and restored
	do(ann, "POST", "/kb/"+space+"/"+pg, url.Values{"title": {"Отпуск"}, "body": {"за три недели"}}, 200)
	if out := do(ann, "POST", "/kb/"+space+"/preview", url.Values{"body": {"~~было~~ <b>"}}, 200); !strings.Contains(out, `\u003cs\u003eбыло\u003c/s\u003e \u0026lt;b\u0026gt;`) {
		t.Fatalf("preview: %s", out)
	}
	do(eve, "POST", "/kb/"+space+"/preview", url.Values{"body": {"x"}}, 404)
	vs, _ := srv.KB.Versions(ctx, pg)
	v := strconv.FormatInt(vs[0].ID, 10)
	if page := do(ann, "GET", "/kb/"+space+"/"+pg+"?v="+v+"&diff=1", nil, 200); !strings.Contains(page, `<div class="del">- за две недели</div>`) ||
		!strings.Contains(page, `<div class="add">&#43; за три недели</div>`) {
		t.Fatalf("no difference on the page:\n%s", page)
	}
	do(ann, "POST", "/kb/"+space+"/"+pg+"/restore", url.Values{"v": {v}}, 200)
	if p, _, _ := srv.KB.Page(ctx, ann, pg); p.Body != "за две недели" {
		t.Fatalf("restored = %+v", p)
	}

	// the search in one base marks what it found; «Содержание» of a page with headings
	if page := do(ann, "GET", "/kb/"+space+"?q=НЕДЕЛ", nil, 200); !strings.Contains(page, "Найдено страниц: 1") || !strings.Contains(page, "за две <mark>недел</mark>и") {
		t.Fatalf("the search in the base:\n%s", page)
	}
	if page := do(ann, "GET", "/kb/"+other+"?q=недел", nil, 200); !strings.Contains(page, "Найдено страниц: 0") {
		t.Fatal("the search in another base found the page")
	}
	do(eve, "GET", "/kb/"+space+"?q=недел", nil, 404)
	toc, err := srv.KB.CreatePage(ctx, ann, other, "", "Правила", "## Приход\nтекст\n### Опоздания\n## Уход")
	must(err)
	if page := do(ann, "GET", "/kb/"+other+"/"+toc, nil, 200); !strings.Contains(page, `<li class="l3"><a href="#%d0%be%d0%bf%d0%be%d0%b7%d0%b4%d0%b0%d0%bd%d0%b8%d1%8f">Опоздания</a></li>`) ||
		!strings.Contains(page, `<h3 id="приход">Приход</h3>`) {
		i := strings.Index(page, "kbtoc")
		t.Fatalf("no contents:\n%s", page[max(i-100, 0):])
	}
	if page := do(ann, "GET", "/kb/"+space+"/"+pg, nil, 200); strings.Contains(page, `class="kbtoc"`) {
		t.Fatal("contents of a page without headings")
	}

	// «В избранное», «Следить», «Недавние»
	if page := do(ann, "GET", "/kb/"+space+"/"+pg, nil, 200); !strings.Contains(page, `action="/kb/`+space+`/mark"`) || !strings.Contains(page, "Следить") {
		t.Fatal("no marks on the page")
	}
	do(ann, "POST", "/kb/"+space+"/mark?on=1", url.Values{"kind": {"fav"}, "page": {pg}}, 200)
	do(ann, "POST", "/kb/"+space+"/mark?on=1", url.Values{"kind": {"watch"}}, 200)
	if page := do(ann, "GET", "/kb/"+space+"/"+pg, nil, 200); !strings.Contains(page, `kbstar on`) || !strings.Contains(page, "Слежу") {
		t.Fatal("marks not shown")
	}
	if page := do(ann, "GET", "/kb", nil, 200); !strings.Contains(page, "Избранное</h3>") || !strings.Contains(page, "Недавние</h3>") ||
		strings.Count(page, `href="/kb/`+space+`/`+pg+`"`) != 2 {
		t.Fatal("no favorites or recent pages on the main page")
	}
	do(eve, "POST", "/kb/"+space+"/mark?on=1", url.Values{"kind": {"fav"}}, 404)
	do(ann, "POST", "/kb/"+space+"/mark?on=0", url.Values{"kind": {"fav"}, "page": {pg}}, 200)
	do(ann, "POST", "/kb/"+space+"/mark?on=0", url.Values{"kind": {"watch"}}, 200)
	if page := do(ann, "GET", "/kb", nil, 200); strings.Contains(page, "Избранное</h3>") {
		t.Fatal("unstarred page still in favorites")
	}

	// «Публикация»: a page with its subpages, then the whole base, opens by an outside link without signing in
	sub, err := srv.KB.CreatePage(ctx, boss, space, pg, "Больничный", "в тот же день")
	must(err)
	aside, err := srv.KB.CreatePage(ctx, boss, space, "", "Командировки", "по приказу")
	must(err)
	must(srv.KB.SavePage(ctx, boss, pg, "Отпуск", "за три недели ![схема](/files/"+pic+"?inline=1) [больничный](/kb/"+space+"/"+sub+")"))
	other2, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/" + randomHex(8), Name: "тайна.png", ContentType: "image/png",
		UploadedBy: boss, KBSpaceID: space})
	must(err)
	anon := func(path string, want int) string {
		t.Helper()
		res, err := client.Get(hs.URL + path)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("GET %s: %d, want %d: %.300s", path, res.StatusCode, want, raw)
		}
		return string(raw)
	}
	do(eve, "POST", "/kb/"+space+"/publish", url.Values{"page": {pg}}, 404)
	do(ann, "POST", "/kb/"+space+"/publish", nil, 403)
	do(ann, "POST", "/kb/"+space+"/publish", url.Values{"page": {pg}}, 200)
	tok, err := srv.KB.PageLink(ctx, space, pg)
	must(err)
	if tok == "" {
		t.Fatal("the page is not published")
	}
	if page := do(ann, "GET", "/kb/"+space+"/"+pg, nil, 200); !strings.Contains(page, `/kbp/`+tok+`"`) {
		t.Fatal("the page does not show its link")
	}
	if page := anon("/kbp/"+tok, 200); !strings.Contains(page, "за три недели") || !strings.Contains(page, `src="/kbp/`+tok+`/f/`+pic+`?inline=1"`) ||
		!strings.Contains(page, `href="/kbp/`+tok+`/`+sub+`"`) || strings.Contains(page, "Командировки") {
		t.Fatalf("the published page:\n%s", page)
	}
	if !strings.Contains(anon("/kbp/"+tok+"/"+sub, 200), "в тот же день") {
		t.Fatal("the subpage is not under the link")
	}
	anon("/kbp/"+tok+"/"+aside, 404)
	anon("/kbp/"+tok+"/"+toc, 404)
	anon("/kbp/"+tok+"/f/"+pic, 200)
	anon("/kbp/"+tok+"/f/"+other2, 404)
	anon("/kbp/nope", 404)
	do(boss, "POST", "/kb/"+space+"/publish", nil, 200)
	all, err := srv.KB.PageLink(ctx, space, "")
	must(err)
	if !strings.Contains(anon("/kbp/"+all+"/"+aside, 200), "по приказу") {
		t.Fatal("a page of the published base does not open")
	}
	if page := do(boss, "GET", "/kb/"+space+"/settings", nil, 200); !strings.Contains(page, "/kbp/"+all) || !strings.Contains(page, "/kbp/"+tok) {
		t.Fatal("the settings do not list the links")
	}
	do(ann, "POST", "/kb/"+space+"/publish", url.Values{"page": {pg}, "off": {"1"}}, 200)
	anon("/kbp/"+tok, 404)
	_, err = srv.KB.DeletePage(ctx, boss, aside)
	must(err)
	anon("/kbp/"+all+"/"+aside, 404)
	_, err = srv.KB.DeletePage(ctx, boss, sub)
	must(err)

	// copied and moved into another base
	do(ann, "POST", "/kb/"+space+"/"+pg+"/copy", nil, 200)
	if tree, _ := srv.KB.Tree(ctx, space); len(tree) != 2 || tree[1].Title != "Отпуск (копия)" {
		t.Fatalf("tree after the copy = %+v", tree)
	}
	// the copy dragged under the page: «хлебные крошки» above it, «Подстраницы» at the end of the page
	pages, _ := srv.KB.Tree(ctx, space)
	cp := pages[1].ID
	do(ann, "POST", "/kb/"+space+"/"+cp+"/place", url.Values{"parent": {pg}}, 200)
	if page := do(ann, "GET", "/kb/"+space+"/"+cp, nil, 200); !strings.Contains(page, `<a href="/kb/`+space+`/`+pg+`">Отпуск</a> `) {
		t.Fatalf("no crumbs:\n%s", page)
	}
	if page := do(ann, "GET", "/kb/"+space+"/"+pg, nil, 200); !strings.Contains(page, `class="kbchildren"`) || !strings.Contains(page, "подстраницами (1)") {
		t.Fatalf("no subpages at the end or in the warning of deleting:\n%s", page)
	}
	do(eve, "POST", "/kb/"+space+"/"+cp+"/place", url.Values{"parent": {""}}, 404)
	do(ann, "POST", "/kb/"+space+"/"+pg+"/place", url.Values{"parent": {cp}}, 403)
	do(ann, "POST", "/kb/"+space+"/"+pg+"/move", url.Values{"to": {other}}, 200)
	if _, sp, _ := srv.KB.Page(ctx, boss, pg); sp.ID != other {
		t.Fatalf("the page is in %s", sp.ID)
	}

	// the trash: a page and a base go there and come back
	do(ann, "POST", "/kb/"+other+"/"+pg+"/delete", nil, 200)
	do(ann, "GET", "/kb/"+other+"/"+pg, nil, 404)
	do(boss, "POST", "/kb/"+space+"/delete", nil, 200)
	if page := do(ann, "GET", "/kb/trash", nil, 200); !strings.Contains(page, "<b>Отпуск</b>") || strings.Contains(page, "Правила") {
		t.Fatalf("ann's trash:\n%s", page)
	}
	if page := do(boss, "GET", "/kb/trash", nil, 200); !strings.Contains(page, "<b>Правила</b>") {
		t.Fatalf("boss's trash:\n%s", page)
	}
	do(ann, "POST", "/kb/"+space+"/trash", url.Values{"act": {"restore"}}, 403)
	do(boss, "POST", "/kb/"+space+"/trash", url.Values{"act": {"restore"}}, 200)
	do(ann, "POST", "/kb/"+other+"/trash", url.Values{"page": {pg}, "act": {"restore"}}, 200)
	do(ann, "GET", "/kb/"+other+"/"+pg, nil, 200)

	// a picture uploaded into a page
	upload := func(who, name string, want int) map[string]any {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		h := make(map[string][]string)
		h["Content-Disposition"] = []string{`form-data; name="file"; filename="` + name + `"`}
		h["Content-Type"] = []string{"image/png"}
		fw, _ := mw.CreatePart(h)
		fw.Write([]byte("png"))
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/kb/"+other+"/files", &buf)
		req.AddCookie(as[who])
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Accept", "application/json")
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		if res.StatusCode != want {
			raw, _ := io.ReadAll(res.Body)
			t.Fatalf("upload %s: %d, want %d: %s", name, res.StatusCode, want, raw)
		}
		var out map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return out
	}
	must(srv.KB.SetRight(ctx, boss, other, "all", "", "read"))
	upload(eve, "x.png", 403) // reads only
	// «Шаблоны»: a base from a template of go24, a base saved as a template, deleted by who saved it
	if !strings.Contains(do(ann, "GET", "/kb", nil, 200), `value="b:onboarding"`) {
		t.Fatal("no templates in the form of a new base")
	}
	loc := do(ann, "POST", "/kb", url.Values{"name": {"Вопросы"}, "template": {"b:faq"}}, 200)
	var faq string
	must(pool.QueryRow(ctx, `SELECT id FROM kb_spaces WHERE name = 'Вопросы'`).Scan(&faq))
	var n int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM kb_pages WHERE space_id = $1`, faq).Scan(&n))
	if n != 4 {
		t.Fatalf("pages from the template = %d (%s)", n, loc)
	}
	do(eve, "POST", "/kb/"+faq+"/template", url.Values{"name": {"Шаблон Евы"}}, 403)
	do(ann, "POST", "/kb/"+faq+"/template", url.Values{"name": {"Наш FAQ"}}, 200)
	var tpl string
	must(pool.QueryRow(ctx, `SELECT id FROM kb_templates WHERE name = 'Наш FAQ'`).Scan(&tpl))
	if home := do(eve, "GET", "/kb", nil, 200); !strings.Contains(home, `value="`+tpl+`"`) || strings.Contains(home, "/kb/templates/"+tpl+"/delete") {
		t.Fatal("the company template is not in the form, or another one may delete it")
	}
	do(eve, "POST", "/kb/templates/"+tpl+"/delete", url.Values{}, 404)
	do(ann, "POST", "/kb/templates/"+tpl+"/delete", url.Values{}, 200)

	if os.Getenv("GO24_TEST_S3") == "" {
		t.Log("no GO24_TEST_S3: an upload into a page is not checked")
		return
	}
	got := upload(boss, "план [этаж].png", 200)
	if md, _ := got["md"].(string); !strings.HasPrefix(md, "![план (этаж).png](/files/") || !strings.HasSuffix(md, "?inline=1)") || got["image"] != true {
		t.Fatalf("upload = %v", got)
	}
	id := strings.TrimSuffix(strings.TrimPrefix(got["url"].(string), "/files/"), "?inline=1")
	if body := do(eve, "GET", "/files/"+id+"?inline=1", nil, 200); body != "png" {
		t.Fatalf("the picture = %q", body)
	}

	// «Экспорт» of the base with the picture, «Импорт» of the archive into another base
	_, err = srv.KB.CreatePage(ctx, boss, other, "", "План этажа", got["md"].(string))
	must(err)
	zipped := do(eve, "GET", "/kb/"+other+"/export", nil, 200)
	z, err := zip.NewReader(strings.NewReader(zipped), int64(len(zipped)))
	must(err)
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	if !slices.Contains(names, "03 План этажа.md") || !strings.HasPrefix(names[len(names)-1], "_files/"+id[:8]+"-") {
		t.Fatalf("the archive = %v", names)
	}
	importZip := func(who string, want int) {
		t.Helper()
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, _ := mw.CreateFormFile("file", "Архив.zip")
		fw.Write([]byte(zipped))
		mw.Close()
		req, _ := http.NewRequest("POST", hs.URL+"/kb/"+space+"/import", &buf)
		req.AddCookie(as[who])
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("X-Go24-Form", "1")
		res, err := client.Do(req)
		must(err)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("import as %s: %d, want %d", who, res.StatusCode, want)
		}
	}
	importZip(eve, 404) // does not see the base
	importZip(ann, 200)
	var imported, body string
	must(pool.QueryRow(ctx, `SELECT id, body FROM kb_pages WHERE space_id = $1 AND parent_id IS NULL AND title = 'План этажа'`, space).Scan(&imported, &body))
	if !strings.HasPrefix(body, "![план (этаж).png](/files/") || strings.Contains(body, id) {
		t.Fatalf("the imported page = %q", body)
	}
	if page := do(ann, "GET", "/kb/"+space+"/"+imported, nil, 200); !strings.Contains(page, `action="/kb/`+space+`/import"`) || !strings.Contains(page, `href="/kb/`+space+`/export"`) {
		t.Fatalf("no «Экспорт» or «Импорт» in the base:\n%s", page)
	}
}
