package web

import (
	"bytes"
	"context"
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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/auth"
	"go24/internal/booking"
	"go24/internal/crm"
	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/sites"
	"go24/internal/storages"
	"go24/internal/tasks"
)

// TestSitesHTTP: «Сайты» over HTTP — a new site comes with ready blocks, a block is added, edited (a dangerous link
// is dropped), hidden; the page opens to a visitor only when published, without hidden blocks; a guest has no
// sites; a picture of a site is open to anyone, other files are not.
func TestSitesHTTP(t *testing.T) {
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
	var boss, guest, dev string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'Босс', 'boss@go24.test') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('dev', 'Разработчик', 'dev@go24.test') RETURNING id`).Scan(&dev))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, guest) VALUES ('g', 'Гость', 'g@go24.test', true) RETURNING id`).Scan(&guest))
	bundle, err := i18n.Load("")
	must(err)
	pages, err := buildPages(bundle)
	must(err)
	ts := tasks.NewStore(pool)
	srv := &Server{DB: pool, Tasks: ts, CRM: crm.NewStore(pool), Sites: sites.NewStore(pool), Booking: booking.NewStore(pool), Storages: storages.NewStore(pool, nil, nil), I18n: bundle, pages: pages,
		Sessions: auth.NewSessions(strings.Repeat("s", 32), false), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	hs := httptest.NewServer(srv.Routes())
	defer hs.Close()
	cookie := func(id string) *http.Cookie {
		rec := httptest.NewRecorder()
		must(srv.Sessions.Set(rec, auth.Session{UserID: id}))
		return rec.Result().Cookies()[0]
	}
	as := map[string]*http.Cookie{boss: cookie(boss), guest: cookie(guest), dev: cookie(dev)}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	send := func(who string, req *http.Request, want int) (string, *http.Response) {
		t.Helper()
		if c := as[who]; c != nil {
			req.AddCookie(c)
		}
		req.Header.Set("Accept-Language", "ru")
		res, err := client.Do(req)
		must(err)
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		if res.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d: %.300s", req.Method, req.URL.Path, res.StatusCode, want, raw)
		}
		return string(raw), res
	}
	do := func(who, method, path string, form url.Values, want int) (string, *http.Response) {
		t.Helper()
		var body io.Reader
		if form != nil {
			body = strings.NewReader(form.Encode())
		}
		req, _ := http.NewRequest(method, hs.URL+path, body)
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		return send(who, req, want)
	}

	_, res := do(boss, "POST", "/sites", url.Values{"name": {"Ромашка"}}, 303)
	siteURL := res.Header.Get("Location")
	_, res = do(boss, "GET", siteURL, nil, 302)
	pageURL := res.Header.Get("Location")
	editor, _ := do(boss, "GET", pageURL, nil, 200)
	for _, want := range []string{"Почему мы", "Подробнее о нас", `class="lp-menu"`, "Ромашка", `name="type" value="features"`} {
		if !strings.Contains(editor, want) {
			t.Fatalf("the editor of a new site has no %q", want)
		}
	}
	x, err := srv.Sites.Sites(ctx)
	must(err)
	if len(x) != 1 || x[0].Slug != "romashka" {
		t.Fatalf("sites: %+v", x)
	}
	p, err := srv.Sites.Page(ctx, x[0].ID, x[0].HomeID)
	must(err)
	if len(p.Blocks) != 6 || x[0].Pages != 3 || !strings.Contains(p.Blocks[0].Get("links"), "О компании | /site/romashka/about") {
		t.Fatalf("starter blocks: %d", len(p.Blocks))
	}

	// a block: added first, edited, the other hidden
	_, res = do(boss, "POST", pageURL+"/blocks", url.Values{"type": {"button"}, "at": {"0"}}, 303)
	loc := res.Header.Get("Location")
	bid := loc[strings.Index(loc, "#b-")+3:]
	do(boss, "POST", pageURL+"/blocks/"+bid, url.Values{"button": {"Заказать звонок"}, "link": {"javascript:alert(1)"}}, 303)
	cover := p.Blocks[1].ID
	do(boss, "POST", pageURL+"/blocks/"+cover+"/hide", nil, 303)
	do(boss, "POST", pageURL+"/blocks/"+bid+"/nope", nil, 404)
	// with no place chosen a block goes last
	do(boss, "POST", pageURL+"/blocks", url.Values{"type": {"text"}}, 303)
	if p, err = srv.Sites.Page(ctx, x[0].ID, x[0].HomeID); err != nil || p.Blocks[len(p.Blocks)-1].Type != "text" || p.Blocks[0].Type != "button" {
		t.Fatalf("a block with no place: %v %+v", err, p.Blocks)
	}

	// the visitor: nothing before publishing, then the page without the hidden cover
	do("", "GET", "/site/romashka", nil, 404)
	preview, _ := do(boss, "GET", pageURL+"/preview", nil, 200)
	if !strings.Contains(preview, "Заказать звонок") || strings.Contains(preview, "Подробнее о нас") || strings.Contains(preview, "javascript:") {
		t.Fatal("the preview shows a hidden block or a dangerous link")
	}
	do(boss, "POST", siteURL+"/publish", url.Values{"on": {"1"}}, 303)
	public, _ := do("", "GET", "/site/romashka", nil, 200)
	if !strings.Contains(public, "Заказать звонок") || strings.Contains(public, "Подробнее о нас") || strings.Contains(public, "class=\"app\"") {
		t.Fatal("the published page")
	}
	do(boss, "POST", pageURL+"/unpublish", nil, 303)
	do("", "GET", "/site/romashka", nil, 404)

	// «CRM-форма»: on the editor it cannot be sent; a visitor's request becomes a lead of the chosen responsible
	// (source «Веб-сайт»), who is notified; a robot and a missing required field create nothing
	var manager string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('m', 'Менеджер', 'm@go24.test') RETURNING id`).Scan(&manager))
	_, res = do(boss, "POST", pageURL+"/blocks", url.Values{"type": {"form"}}, 303)
	loc = res.Header.Get("Location")
	fid := loc[strings.Index(loc, "#b-")+3:]
	if editor, _ = do(boss, "GET", pageURL, nil, 200); !strings.Contains(editor, "Оставьте заявку") || !strings.Contains(editor, `disabled title="Форма`) {
		t.Fatal("the form block in the editor")
	}
	do(boss, "POST", pageURL+"/blocks/"+fid, url.Values{"title": {"Заявка"}, "ask_name": {"req"}, "ask_phone": {"req"}, "ask_comment": {"on"},
		"button": {"Жду звонка"}, "thanks": {"Спасибо, Ира скоро позвонит"}, "responsible": {manager}}, 303)
	do(boss, "POST", pageURL+"/publish", nil, 303)
	public, _ = do("", "GET", "/site/romashka", nil, 200)
	if !strings.Contains(public, "Жду звонка") || !strings.Contains(public, `name="phone"`) || strings.Contains(public, `name="email"`) {
		t.Fatalf("the form on the page: %.2000s", public)
	}
	old := sites.FormStamp(time.Now().Add(-time.Minute))
	leads := func() (n int) {
		must(pool.QueryRow(ctx, `SELECT count(*) FROM crm_leads`).Scan(&n))
		return
	}
	_, res = do("", "POST", "/site/romashka", url.Values{"block": {fid}, "t": {sites.FormStamp(time.Now())}, "name": {"Бот"}, "phone": {"1"}}, 303)
	if !strings.Contains(res.Header.Get("Location"), "sent=1") || leads() != 0 {
		t.Fatal("a robot sent a form")
	}
	_, res = do("", "POST", "/site/romashka", url.Values{"block": {fid}, "t": {old}, "name": {"Ира"}}, 303)
	if loc = res.Header.Get("Location"); !strings.Contains(loc, "err=err.site_form_required") || leads() != 0 {
		t.Fatalf("no phone: %s", loc)
	}
	if body, _ := do("", "GET", loc, nil, 200); !strings.Contains(body, "Заполните обязательные поля") {
		t.Fatal("the refusal is not shown")
	}
	do("", "POST", "/site/romashka", url.Values{"block": {"nope"}, "t": {old}, "name": {"Ира"}}, 404)
	_, res = do("", "POST", "/site/romashka", url.Values{"block": {fid}, "t": {old}, "name": {"Ира"}, "phone": {"+7 900 000-00-00"},
		"email": {"x@y.ru"}, "comment": {"Перезвоните вечером"}}, 303)
	if loc = res.Header.Get("Location"); !strings.Contains(loc, "sent=1") {
		t.Fatalf("sent: %s", loc)
	}
	if body, _ := do("", "GET", loc, nil, 200); !strings.Contains(body, "Ира скоро позвонит") || strings.Contains(body, `name="phone"`) {
		t.Fatal("the thanks")
	}
	var title, name, phone, email, comment, resp, origin string
	must(pool.QueryRow(ctx, `SELECT l.title, l.name, l.phone, l.email, l.comment, l.responsible_id::text, d.code FROM crm_leads l
		JOIN crm_dict d ON d.id = l.source_id`).Scan(&title, &name, &phone, &email, &comment, &resp, &origin))
	if title != "Заявка с сайта «Ромашка» — Ира" || name != "Ира" || phone != "+7 900 000-00-00" || email != "" || resp != manager || origin != "WEB" ||
		!strings.HasPrefix(comment, "Перезвоните вечером") || !strings.Contains(comment, "/site/romashka") {
		t.Fatalf("the lead: %q %q %q %q %q %q %q", title, name, phone, email, comment, resp, origin)
	}
	var notes int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE user_id = $1 AND kind = 'site_form' AND url LIKE '/crm/leads/%'`, manager).Scan(&notes))
	if notes != 1 {
		t.Fatalf("notifications of the responsible: %d", notes)
	}
	do(boss, "POST", pageURL+"/unpublish", nil, 303)
	do("", "POST", "/site/romashka", url.Values{"block": {fid}, "t": {old}, "name": {"Ира"}, "phone": {"1"}}, 404)

	// pages: a new one, its address changed, the home page cannot be deleted
	_, res = do(boss, "POST", siteURL+"/pages", url.Values{"title": {"Цены"}}, 303)
	prices := res.Header.Get("Location")
	do(boss, "POST", prices, url.Values{"title": {"Прайс"}, "slug": {"price"}}, 303)
	do(boss, "POST", prices+"/publish", nil, 303)
	do("", "GET", "/site/romashka/price", nil, 200)
	_, res = do(boss, "POST", pageURL+"/delete", nil, 303)
	if !strings.Contains(res.Header.Get("Location"), "msg=") {
		t.Fatal("the home page was deleted")
	}

	// templates: the gallery; a page from a ready page gets the home page's menu and footer and the template's title;
	// a site of another template; copying a page and a site, moving a page
	if home, _ := do(boss, "GET", "/sites", nil, 200); !strings.Contains(home, `value="event"`) || !strings.Contains(home, "Мероприятие") {
		t.Fatal("the gallery of templates")
	}
	_, res = do(boss, "POST", siteURL+"/pages", url.Values{"tpl": {"contacts"}}, 303)
	cpage := res.Header.Get("Location")
	if body, _ := do(boss, "GET", cpage, nil, 200); !strings.Contains(body, `<h1 class="sitetitle">Контакты</h1>`) || !strings.Contains(body, "/site/romashka/about") ||
		!strings.Contains(body, "© Ромашка") || !strings.Contains(body, "Оставьте заявку") {
		t.Fatalf("a page from a template: %.3000s", body)
	}
	_, res = do(boss, "POST", "/sites", url.Values{"name": {"День открытых дверей"}, "tpl": {"event"}}, 303)
	event := strings.TrimPrefix(res.Header.Get("Location"), "/sites/")
	do(boss, "POST", "/sites/"+event+"/publish", url.Values{"on": {"1"}}, 303)
	if body, _ := do("", "GET", "/site/den-otkrytyh-dverey", nil, 200); !strings.Contains(body, "Программа") || !strings.Contains(body, `Регистрация</a>`) ||
		!strings.Contains(body, `href="#b-`) || strings.Contains(body, `name="comment"`) {
		t.Fatalf("an event site: %.3000s", body)
	}
	_, res = do(boss, "POST", cpage+"/copy", url.Values{"to": {event}}, 303)
	if loc = res.Header.Get("Location"); !strings.HasPrefix(loc, "/sites/"+event+"/p/") {
		t.Fatalf("a page copied to another site: %s", loc)
	}
	if body, _ := do(boss, "GET", loc, nil, 200); !strings.Contains(body, "Контакты (копия)") {
		t.Fatal("the copy's title")
	}
	_, res = do(boss, "POST", prices+"/move", url.Values{"to": {event}}, 303)
	if loc = res.Header.Get("Location"); loc != "/sites/"+event+strings.TrimPrefix(prices, siteURL) {
		t.Fatalf("moved: %s", loc)
	}
	do("", "GET", "/site/den-otkrytyh-dverey/price", nil, 200)
	if _, res = do(boss, "POST", pageURL+"/move", url.Values{"to": {event}}, 303); !strings.Contains(res.Header.Get("Location"), "msg=") {
		t.Fatal("the home page was moved")
	}
	_, res = do(boss, "POST", siteURL+"/copy", nil, 303)
	cp, err := srv.Sites.Site(ctx, strings.TrimPrefix(res.Header.Get("Location"), "/sites/"))
	must(err)
	if cp.Name != "Ромашка (копия)" || cp.Slug != "romashka-copy" || cp.Published || cp.Pages != 4 || cp.Domain != "" {
		t.Fatalf("a copy of a site: %+v", cp)
	}

	// settings: SEO of a page, the site's 404 page, favicon, counters, colour and font, the own domain; the header
	do(boss, "POST", cpage, url.Values{"title": {"Контакты"}, "slug": {"kontakty"}, "seo_title": {"Контакты Ромашки"},
		"seo_description": {"Как нас найти"}, "seo_keywords": {"ромашка, контакты"}}, 303)
	cid := cpage[strings.LastIndex(cpage, "/")+1:]
	if _, res = do(boss, "POST", siteURL+"/settings", url.Values{"name": {"Ромашка"}, "slug": {"romashka"}, "metrika": {"x1"}, "from": {cid}}, 303); !strings.Contains(res.Header.Get("Location"), "msg=") {
		t.Fatal("a bad counter saved")
	}
	_, res = do(boss, "POST", siteURL+"/settings", url.Values{"name": {"Ромашка"}, "slug": {"romashka"}, "from": {cid}, "page404": {cid},
		"favicon": {"/sitefile/x"}, "metrika": {"987654"}, "analytics": {"G-TEST123"}, "accent": {"#aa0000"}, "font": {"serif"}, "domain": {"romashka.example.ru"}}, 303)
	if res.Header.Get("Location") != cpage {
		t.Fatalf("settings go back to the page: %s", res.Header.Get("Location"))
	}
	home, err := srv.Sites.Page(ctx, x[0].ID, x[0].HomeID)
	must(err)
	menu := ""
	for _, b := range home.Blocks {
		if b.Type == "menu" && menu == "" {
			menu = b.ID
		}
	}
	_, res = do(boss, "POST", pageURL[:strings.LastIndex(pageURL, "/")]+"/"+x[0].HomeID+"/blocks/"+menu+"/toheader", nil, 303)
	if body, _ := do(boss, "GET", cpage, nil, 200); !strings.Contains(body, "Шапка сайта · Меню") || !strings.Contains(body, `/p/header/blocks/`+menu+`/topage`) ||
		!strings.Contains(body, "lp-font-serif") {
		t.Fatal("the header in the editor")
	}
	public, res = do("", "GET", "/site/romashka/kontakty", nil, 200)
	for _, want := range []string{"<title>Контакты Ромашки</title>", `content="Как нас найти"`, `content="ромашка, контакты"`, `rel="icon" href="/sitefile/x"`,
		"mc.yandex.ru/metrika/tag.js", `ym(Number("987654")`, "gtag/js?id=G-TEST123", "--lp-accent:#aa0000", `class="lp-menu"`} {
		if !strings.Contains(public, want) {
			t.Fatalf("the public page has no %q", want)
		}
	}
	if body, _ := do("", "GET", "/site/romashka/no-such-page", nil, 404); !strings.Contains(body, "<title>Контакты Ромашки</title>") {
		t.Fatal("the 404 page")
	}
	if body, _ := do(boss, "GET", cpage+"/preview", nil, 200); strings.Contains(body, "metrika") || !strings.Contains(body, `name="robots"`) {
		t.Fatal("counters in the preview")
	}
	onDomain := func(path string, want int) string {
		req, _ := http.NewRequest("GET", hs.URL+path, nil)
		req.Host = "romashka.example.ru"
		body, _ := send("", req, want)
		return body
	}
	if body := onDomain("/kontakty", 200); !strings.Contains(body, "Контакты Ромашки") {
		t.Fatal("the site on its domain")
	}
	onDomain("/sites", 404)
	onDomain("/crm/leads/1", 404)

	// «Онлайн-запись» (GO24DEV-688): the block shows the page of the chosen link in a frame, on the own domain too
	lid, err := srv.Booking.SaveLink(ctx, boss, booking.Link{Name: "Запись в Ромашку", DaysAhead: 14, Active: true})
	must(err)
	bl, err := srv.Booking.Link(ctx, lid)
	must(err)
	_, res = do(boss, "POST", cpage+"/blocks", url.Values{"type": {"booking"}}, 303)
	loc = res.Header.Get("Location")
	bkid := loc[strings.Index(loc, "#b-")+3:]
	if editor, _ := do(boss, "GET", cpage, nil, 200); !strings.Contains(editor, "Запишитесь онлайн") || !strings.Contains(editor, `<option value="`+bl.Token+`"`) ||
		!strings.Contains(editor, "Выберите ссылку для записи") {
		t.Fatal("the booking block in the editor")
	}
	do(boss, "POST", cpage+"/blocks/"+bkid, url.Values{"title": {"Запишитесь к нам"}, "booking": {bl.Token}}, 303)
	do(boss, "POST", cpage+"/publish", nil, 303)
	if body := onDomain("/kontakty", 200); !strings.Contains(body, "Запишитесь к нам") || !strings.Contains(body, `src="/bk/`+bl.Token+`?embed=1"`) {
		t.Fatalf("the booking block on the site: %.3000s", body)
	}
	if body := onDomain("/bk/"+bl.Token+"?embed=1", 200); !strings.Contains(body, "Запись в Ромашку") || !strings.Contains(body, "bkembed.js") {
		t.Fatal("the booking page on the site's domain")
	}

	// a guest of a collab has no «Сайты»: for them it does not exist
	do(guest, "GET", "/sites", nil, 404)
	do(guest, "GET", pageURL, nil, 404)
	// nobody signed in: to the sign-in
	if _, res = do("", "GET", "/sites", nil, 302); res.StatusCode != 302 {
		t.Fatal("the editor without signing in")
	}

	// pictures: only a picture is taken; a site's picture is for anyone, another file is not
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "a.txt")
	fw.Write([]byte("x"))
	mw.Close()
	req, _ := http.NewRequest("POST", hs.URL+siteURL+"/files", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if body, _ := send(boss, req, 422); !strings.Contains(body, "картинку") {
		t.Fatalf("a text file as a picture: %s", body)
	}
	other, err := srv.Storages.AddFile(ctx, storages.NewFile{Key: "k/1", Name: "секрет.png", ContentType: "image/png", UploadedBy: boss})
	must(err)
	do("", "GET", "/sitefile/"+other, nil, 404)

	// the history of a page: what was done by whom, an earlier state previewed and brought back
	siteID, pageID := strings.TrimPrefix(siteURL, "/sites/"), x[0].HomeID
	hist, _ := do(boss, "GET", pageURL+"/history", nil, 200)
	for _, want := range []string{"Изменён блок «Кнопка»", "Добавлен блок «Кнопка»", "Состояние до начала истории", "Босс"} {
		if !strings.Contains(hist, want) {
			t.Fatalf("the history has no %q", want)
		}
	}
	revs, err := srv.Sites.Revisions(ctx, siteID, pageID)
	must(err)
	first := revs[len(revs)-1]
	if body, _ := do(boss, "GET", pageURL+"/history/"+strconv.FormatInt(first.ID, 10), nil, 200); strings.Contains(body, "Заказать звонок") {
		t.Fatal("the earlier state shows a later block")
	}
	if _, res = do(boss, "POST", pageURL+"/history/"+strconv.FormatInt(first.ID, 10), url.Values{"from": {pageID}}, 303); !strings.HasPrefix(res.Header.Get("Location"), pageURL+"?msg=") {
		t.Fatalf("after bringing back: %s", res.Header.Get("Location"))
	}
	if p, _ := srv.Sites.Page(ctx, siteID, pageID); len(p.Blocks) != first.Count {
		t.Fatalf("brought back %d blocks, want %d", len(p.Blocks), first.Count)
	}
	do(boss, "GET", siteURL+"/p/header/history?from="+pageID, nil, 200)

	// rights: closed to the employees, dev may only look — the editor without its tools, no changes
	editor, _ = do(dev, "GET", pageURL, nil, 200)
	if !strings.Contains(editor, pageURL+"/blocks") || strings.Contains(editor, "siteaccessform") {
		t.Fatal("an employee's editor by default")
	}
	do(boss, "POST", siteURL+"/access", url.Values{"everyone": {"0"}, "role1": {dev}, "from": {pageID}}, 303)
	if editor, _ = do(boss, "GET", pageURL, nil, 200); !strings.Contains(editor, "siteaccessform") || !strings.Contains(editor, "Разработчик") {
		t.Fatal("the rights in the creator's editor")
	}
	editor, _ = do(dev, "GET", pageURL, nil, 200)
	if strings.Contains(editor, pageURL+"/blocks") || strings.Contains(editor, "sitesetform") || !strings.Contains(editor, "История изменений") {
		t.Fatal("a viewer's editor has its tools")
	}
	do(dev, "POST", pageURL+"/blocks", url.Values{"type": {"text"}}, 403)
	do(dev, "POST", siteURL+"/publish", url.Values{"on": {"0"}}, 403)
	do(boss, "POST", siteURL+"/access", url.Values{"everyone": {"0"}, "from": {pageID}}, 303)
	do(dev, "GET", pageURL, nil, 404)
	if body, _ := do(dev, "GET", "/sites", nil, 200); strings.Contains(body, siteURL) {
		t.Fatal("a closed site in the list")
	}
	do(boss, "POST", siteURL+"/access", url.Values{"everyone": {"3"}, "from": {pageID}}, 303)

	// the trash: a page and the site come back
	pg, err := srv.Sites.Pages(ctx, siteID)
	must(err)
	about := pg[1].ID
	do(boss, "POST", siteURL+"/p/"+about+"/delete", nil, 303)
	do(boss, "GET", siteURL+"/p/"+about, nil, 404)
	if body, _ := do(boss, "GET", "/sites/trash", nil, 200); !strings.Contains(body, pg[1].Title) || !strings.Contains(body, "Босс") {
		t.Fatal("the page in the trash")
	}
	do(boss, "POST", siteURL+"/trash", url.Values{"act": {"restore"}, "page": {about}}, 303)
	do(boss, "GET", siteURL+"/p/"+about, nil, 200)

	do(boss, "POST", siteURL+"/delete", nil, 303)
	do(boss, "GET", siteURL, nil, 404)
	do("", "GET", "/site/romashka", nil, 404)
	if body, _ := do(dev, "GET", "/sites/trash", nil, 200); strings.Contains(body, "Ромашка") {
		t.Fatal("another's site in the trash")
	}
	do(dev, "POST", siteURL+"/trash", url.Values{"act": {"restore"}}, 403)
	do(boss, "POST", siteURL+"/trash", url.Values{"act": {"restore"}}, 303)
	do(boss, "GET", siteURL, nil, 302)
	do(boss, "POST", siteURL+"/delete", nil, 303)
	do(boss, "POST", siteURL+"/trash", url.Values{"act": {"purge"}}, 303)
	if body, _ := do(boss, "GET", "/sites/trash", nil, 200); strings.Contains(body, "Ромашка") {
		t.Fatal("a purged site in the trash")
	}
}
