package sites

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/db"
	"go24/internal/i18n"
	"go24/internal/tasks"
)

// TestBlocks: unknown types are dropped, fields are kept by their type and kind, dangerous links are emptied.
func TestBlocks(t *testing.T) {
	list := normalize([]Block{
		{Type: "nope", Data: map[string]string{"x": "y"}},
		{Type: "cover", Data: map[string]string{"title": "Привет\nмир", "link": "javascript:alert(1)", "image": "/sitefile/1",
			"color": "red", "extra": "x"}},
		{Type: "menu", Data: map[string]string{"links": "О нас | #about\n\nПлохо | javascript:x\n | ", "color": "#AABBCC"}},
	})
	if len(list) != 2 || list[0].ID == "" || list[0].ID == list[1].ID {
		t.Fatalf("normalized: %+v", list)
	}
	c := list[0].Data
	if c["title"] != "Привет мир" || c["link"] != "" || c["image"] != "/sitefile/1" || c["color"] != "" || c["extra"] != "" {
		t.Fatalf("cover: %+v", c)
	}
	pairs := list[1].Pairs("links")
	if len(pairs) != 2 || pairs[0] != (Pair{"О нас", "#about"}) || pairs[1].Link() != "" || list[1].Get("color") != "#AABBCC" {
		t.Fatalf("menu: %+v", pairs)
	}
	for in, want := range map[string]string{"https://x": "https://x", "//evil": "", "/a": "/a", "tel:+7": "tel:+7", "JavaScript:x": "", "data:x": ""} {
		if got := SafeURL(in); got != want {
			t.Errorf("SafeURL(%q) = %q", in, got)
		}
	}
	paras := Block{Data: map[string]string{"t": "a\nb\n\n\nc"}}.Paragraphs("t")
	if len(paras) != 2 || len(paras[0]) != 2 || paras[1][0] != "c" {
		t.Fatalf("paragraphs: %q", paras)
	}
	for in, want := range map[string]string{"Наша Компания!": "nasha-kompaniya", "Цены 2026": "tseny-2026", "  ": "", "Объём": "obem", "go24 — CRM": "go24-crm"} {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func testDB(t *testing.T) (context.Context, *pgxpool.Pool, func(error), func(string) string) {
	t.Helper()
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
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
	newUser := func(n string) string {
		var id string
		must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ($1, $1, $1 || '@t') RETURNING id`, n).Scan(&id))
		return id
	}
	return ctx, pool, must, newUser
}

func isErr(err error, key string) bool {
	var ie *i18n.Error
	return errors.As(err, &ie) && ie.Key == key
}

// TestSites: a site with its home page, pages and blocks, publishing, what a visitor sees, who may delete.
func TestSites(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, dev := newUser("boss"), newUser("dev")
	st := NewStore(pool)
	id, err := st.CreateSite(ctx, boss, "Наша компания", "", "Главная", []Block{{Type: "cover", Data: map[string]string{"title": "Привет"}}})
	must(err)
	x, err := st.Site(ctx, id)
	must(err)
	if x.Slug != "nasha-kompaniya" || x.Pages != 1 || x.HomeID == "" || x.Published {
		t.Fatalf("site: %+v", x)
	}
	if _, err := st.CreateSite(ctx, dev, "Другой", "nasha-kompaniya", "Главная", nil); !isErr(err, "err.site_slug_taken") {
		t.Fatalf("a taken address: %v", err)
	}
	if _, err := st.CreateSite(ctx, dev, "Другой", "Плохой адрес", "Главная", nil); !isErr(err, "err.site_slug") {
		t.Fatalf("a bad address: %v", err)
	}
	if _, err := st.CreateSite(tasks.WithViewer(ctx, dev, true), dev, "Гость", "", "Главная", nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a guest made a site: %v", err)
	}

	// blocks: add, edit, move, hide, delete
	home := x.HomeID
	b2, err := st.AddBlock(ctx, dev, id, home, "text", 99, map[string]string{"text": "Текст"})
	must(err)
	b0, err := st.AddBlock(ctx, dev, id, home, "menu", 0, map[string]string{"logo": "Лого"})
	must(err)
	if _, err := st.AddBlock(ctx, dev, id, home, "html", 0, nil); !isErr(err, "err.site_block") {
		t.Fatalf("an unknown block: %v", err)
	}
	must(st.SaveBlock(ctx, dev, id, home, b2, map[string]string{"text": "Новый", "link": "x"}))
	must(st.BlockAction(ctx, dev, id, home, b2, "up"))
	must(st.BlockAction(ctx, dev, id, home, b0, "hide"))
	p, err := st.Page(ctx, id, home)
	must(err)
	if len(p.Blocks) != 3 || p.Blocks[0].ID != b0 || !p.Blocks[0].Hidden || p.Blocks[1].ID != b2 || p.Blocks[1].Get("text") != "Новый" ||
		p.Blocks[1].Get("link") != "" || p.Blocks[2].Type != "cover" || p.Editor != "dev" {
		t.Fatalf("blocks: %+v", p.Blocks)
	}
	if err := st.BlockAction(ctx, dev, id, home, "nope", "up"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing block: %v", err)
	}

	// a visitor sees nothing until the site is published, then the visible blocks only
	if _, _, _, err := st.Public(ctx, "nasha-kompaniya", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unpublished site is open: %v", err)
	}
	prices, err := st.CreatePage(ctx, dev, id, "Цены", "", nil)
	must(err)
	must(st.PublishSite(ctx, id, true))
	_, pub, pages, err := st.Public(ctx, "nasha-kompaniya", "")
	must(err)
	if pub.ID != home || len(pub.Blocks) != 2 || len(pages) != 2 {
		t.Fatalf("public home: %+v, pages %d", pub, len(pages))
	}
	_, pub, _, err = st.Public(ctx, "nasha-kompaniya", "tseny")
	must(err)
	if pub.ID != prices {
		t.Fatal("the page by its address")
	}
	must(st.PublishPage(ctx, id, prices, false))
	if _, _, _, err := st.Public(ctx, "nasha-kompaniya", "tseny"); !errors.Is(err, ErrNotFound) {
		t.Fatal("a page taken off is open")
	}
	// a new page of a published site is published; publishing a page of an unpublished site publishes the site
	news, err := st.CreatePage(ctx, dev, id, "Новости", "news", nil)
	must(err)
	if _, _, _, err := st.Public(ctx, "nasha-kompaniya", "news"); err != nil {
		t.Fatal("a new page of a published site is not open")
	}
	must(st.PublishSite(ctx, id, false))
	must(st.PublishPage(ctx, id, prices, true))
	if _, _, _, err := st.Public(ctx, "nasha-kompaniya", "tseny"); err != nil {
		t.Fatal("publishing a page did not publish its site")
	}

	// the home page: another one becomes it; the home page cannot be deleted
	if err := st.DeletePage(ctx, dev, id, home); !isErr(err, "err.site_home_delete") {
		t.Fatalf("the home page deleted: %v", err)
	}
	must(st.SetHome(ctx, id, news))
	must(st.DeletePage(ctx, dev, id, home))
	if x, _ = st.Site(ctx, id); x.HomeID != news || x.Pages != 2 {
		t.Fatalf("after the home page moved: %+v", x)
	}
	if err := st.UpdatePage(ctx, dev, id, prices, "Цены", "news"); !isErr(err, "err.site_slug_taken") {
		t.Fatalf("two pages at one address: %v", err)
	}
	must(st.UpdateSite(ctx, id, "Компания", "company", "О нас"))
	if _, _, _, err := st.Public(ctx, "company", ""); err != nil {
		t.Fatal("the site by its new address")
	}

	// deleting: the creator or an administrator
	if err := st.DeleteSite(ctx, dev, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an employee deleted another's site: %v", err)
	}
	must(st.DeleteSite(tasks.WithAdmin(ctx), dev, id))
	if list, _ := st.Sites(ctx); len(list) != 0 {
		t.Fatal("the site is left")
	}
}

// TestTemplates: a site of the gallery with its pages and links between them, a page from a template framed by the
// home page's menu and footer; copies of pages and sites at free addresses with their links following, moving a page.
func TestTemplates(t *testing.T) {
	text := func(typ, variant, field string) (string, bool) {
		v, ok := map[string]string{"menu.links": "Главная | /site/{site}\nЗаявка | {form}\nЦены | {#prices}", "menu.logo": "{name}",
			"prices.title": "Цены", "footer.text": "© {name}"}[variant+"."+field]
		return v, ok
	}
	tpl := Template{"t", "file", []TemplatePage{{"index", []string{"menu", "features:prices", "footer"}}, {"contacts", []string{"menu", "form", "nope"}}}}
	pages := BuildSite(tpl, text, func(p string) string { return "T " + p }, "acme", "Акме")
	if len(pages) != 2 || pages[1].Slug != "contacts" || pages[1].Title != "T contacts" || len(pages[1].Blocks) != 2 {
		t.Fatalf("site template: %+v", pages)
	}
	form, prices := pages[1].Blocks[1].ID, pages[0].Blocks[1]
	if got := pages[0].Blocks[0].Get("links"); got != "Главная | /site/acme\nЗаявка | /site/acme/contacts#b-"+form+"\nЦены | #b-"+prices.ID {
		t.Fatalf("links: %q", got)
	}
	if pages[0].Blocks[0].Get("logo") != "Акме" || prices.Get("title") != "Цены" || pages[0].Blocks[2].Get("text") != "© Акме" {
		t.Fatalf("texts: %+v", pages[0].Blocks)
	}
	own := Build([]string{"form", "menu"}, text, "acme", "Акме")
	if !strings.Contains(own[1].Get("links"), "Заявка | #b-"+own[0].ID) {
		t.Fatalf("a page's own form: %q", own[1].Get("links"))
	}
	menu, footer := Chrome(pages[0].Blocks)
	if len(menu) != 1 || len(footer) != 1 || menu[0].ID == pages[0].Blocks[0].ID || menu[0].Get("logo") != "Акме" {
		t.Fatalf("chrome: %+v %+v", menu, footer)
	}
	menu[0].Data["logo"] = "x"
	if pages[0].Blocks[0].Get("logo") != "Акме" {
		t.Fatal("chrome shares the data")
	}
	if m, f := Chrome(append([]Block{{Type: "text"}}, pages[1].Blocks...)); len(m) != 1 || f != nil {
		t.Fatal("chrome without a footer")
	}
	if SiteTemplate("nope").Key != SiteTemplates[0].Key || PageTemplate("nope").Key != "blank" {
		t.Fatal("unknown templates")
	}

	ctx, pool, must, newUser := testDB(t)
	boss, dev := newUser("boss"), newUser("dev")
	st := NewStore(pool)
	id, err := st.CreateSiteFrom(ctx, boss, "Акме", "acme", pages)
	must(err)
	list, err := st.Pages(ctx, id)
	must(err)
	if len(list) != 2 || !list[0].Home || list[0].Slug != "index" || list[1].Slug != "contacts" || list[1].Title != "T contacts" {
		t.Fatalf("pages: %+v", list)
	}
	home, contacts := list[0], list[1]

	// copies of a page: in the site itself (a free address), to another site
	c1, err := st.CopyPage(ctx, dev, id, contacts.ID, id, "Копия")
	must(err)
	c2, err := st.CopyPage(ctx, dev, id, contacts.ID, id, "Копия 2")
	must(err)
	other, err := st.CreateSite(ctx, dev, "Другой", "other", "Главная", nil)
	must(err)
	c3, err := st.CopyPage(ctx, dev, id, home.ID, other, "Главная Акме")
	must(err)
	for _, c := range []struct{ site, id, slug string }{{id, c1, "contacts-2"}, {id, c2, "contacts-3"}, {other, c3, "t-index"}} {
		p, err := st.Page(ctx, c.site, c.id)
		must(err)
		if p.Slug != c.slug || p.Home || p.Published || len(p.Blocks) == 0 {
			t.Fatalf("a copy: %+v, want %s", p, c.slug)
		}
	}

	// moving: not the home page; the moved page is published only when the new site is
	if err := st.MovePage(ctx, dev, id, home.ID, other); !isErr(err, "err.site_home_move") {
		t.Fatalf("the home page moved: %v", err)
	}
	must(st.PublishSite(ctx, id, true))
	must(st.MovePage(ctx, dev, id, c1, other))
	p, err := st.Page(ctx, other, c1)
	must(err)
	if p.Slug != "contacts-2" || p.Published {
		t.Fatalf("moved: %+v", p)
	}
	if _, err := st.Page(ctx, id, c1); !errors.Is(err, ErrNotFound) {
		t.Fatal("the moved page stays in its site")
	}

	// a copy of a site: all pages, not published, links to itself at its address; a new address relinks the pages
	cp, err := st.CopySite(ctx, dev, id, "Акме (копия)")
	must(err)
	x, err := st.Site(ctx, cp)
	must(err)
	if x.Slug != "acme-copy" || x.Pages != 3 || x.Published || x.HomeID == "" || x.CreatedBy != dev {
		t.Fatalf("a copy of a site: %+v", x)
	}
	if cp2, err := st.CopySite(ctx, dev, id, "Ещё"); err != nil {
		t.Fatal(err)
	} else if y, _ := st.Site(ctx, cp2); y.Slug != "acme-copy-2" {
		t.Fatalf("second copy: %s", y.Slug)
	}
	links := func(site, page string) string {
		p, err := st.Page(ctx, site, page)
		must(err)
		return p.Blocks[0].Get("links")
	}
	if got := links(cp, x.HomeID); !strings.Contains(got, "| /site/acme-copy\n") || !strings.Contains(got, "/site/acme-copy/contacts#b-") {
		t.Fatalf("links of the copy: %q", got)
	}
	must(st.UpdateSite(ctx, id, "Акме", "acme-new", ""))
	if got := links(id, home.ID); !strings.Contains(got, "| /site/acme-new\n") || !strings.Contains(got, "/site/acme-new/contacts#b-") {
		t.Fatalf("links after a new address: %q", got)
	}

	// pictures a copy shows keep opening when the original site is deleted
	var file string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, content_type, size, site_id) VALUES ('k', 'a.png', 'image/png', 1, $1) RETURNING id`, id).Scan(&file))
	_, err = st.AddBlock(ctx, dev, cp, x.HomeID, "image", 0, map[string]string{"image": "/sitefile/" + file})
	must(err)
	must(st.DeleteSite(tasks.WithAdmin(ctx), boss, id))
	if !st.PublicFile(ctx, file) {
		t.Fatal("the picture of the copy is gone")
	}
}

// TestSettings: the settings of a site are checked (counters, domain, a 404 page of its own), a page keeps its SEO;
// blocks move between a page and the header or footer of the site, and a visitor sees them on every page; a site
// with its own domain is found by it only when published; a copy keeps the header and the 404 page.
func TestSettings(t *testing.T) {
	for in, want := range map[string]string{"https://WWW.Example.ru/x": "www.example.ru", "shop.example.com:8080": "shop.example.com", "": ""} {
		if got, ok := CleanDomain(in); !ok || got != want {
			t.Errorf("CleanDomain(%q) = %q %v", in, got, ok)
		}
	}
	for _, bad := range []string{"localhost", "a b.ru", "-x.ru", "x..ru", "192.168.1.1"} {
		if _, ok := CleanDomain(bad); ok {
			t.Errorf("CleanDomain(%q) passed", bad)
		}
	}
	ctx, pool, must, newUser := testDB(t)
	dev := newUser("dev")
	st := NewStore(pool)
	id, err := st.CreateSiteFrom(ctx, dev, "Акме", "acme", []NewPage{{Title: "Главная", Blocks: []Block{{Type: "menu", Data: map[string]string{"logo": "Акме"}},
		{Type: "text"}}}, {Slug: "oops", Title: "Нет такой", Blocks: []Block{{Type: "title"}}}})
	must(err)
	other, err := st.CreateSite(ctx, dev, "Другой", "other", "Главная", nil)
	must(err)
	x, err := st.Site(ctx, id)
	must(err)
	o, _ := st.Site(ctx, other)
	pages, _ := st.Pages(ctx, id)
	home, oops := pages[0], pages[1]

	for set, key := range map[Settings]string{{Metrika: "12ab"}: "err.site_metrika", {Analytics: "foo"}: "err.site_analytics", {Domain: "не домен"}: "err.site_domain"} {
		if err := st.SaveSettings(ctx, id, set); !isErr(err, key) {
			t.Errorf("%+v: %v", set, err)
		}
	}
	if err := st.SaveSettings(ctx, id, Settings{Page404: o.HomeID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a 404 page of another site: %v", err)
	}
	must(st.SaveSettings(ctx, id, Settings{Page404: oops.ID, Favicon: "javascript:x", Metrika: "123", Analytics: "g-abc123",
		Accent: "#112233", Font: "serif", Domain: "https://Acme.Example.ru/"}))
	if err := st.SaveSettings(ctx, other, Settings{Domain: "acme.example.ru"}); !isErr(err, "err.site_domain_taken") {
		t.Fatalf("a taken domain: %v", err)
	}
	must(st.SaveSettings(ctx, other, Settings{Font: "comic"}))
	x, _ = st.Site(ctx, id)
	if x.Settings != (Settings{Page404: oops.ID, Metrika: "123", Analytics: "G-ABC123", Accent: "#112233", Font: "serif", Domain: "acme.example.ru"}) {
		t.Fatalf("settings: %+v", x.Settings)
	}
	if o, _ = st.Site(ctx, other); o.Font != "" {
		t.Fatal("an unknown font")
	}
	must(st.SavePageSEO(ctx, dev, id, home.ID, SEO{Title: " Акме —\n лучшие ", Description: "Описание", Keywords: "a, b", NoIndex: true}))
	if p, _ := st.Page(ctx, id, home.ID); p.SEO != (SEO{"Акме — лучшие", "Описание", "a, b", true}) {
		t.Fatalf("seo: %+v", p.SEO)
	}

	// the header and the footer
	menu := home.Blocks[0].ID
	must(st.MoveBlock(ctx, dev, id, home.ID, menu, AreaHeader))
	fid, err := st.AddBlock(ctx, dev, id, AreaFooter, "footer", 0, map[string]string{"text": "© Акме"})
	must(err)
	if err := st.MoveBlock(ctx, dev, id, AreaHeader, menu, AreaFooter); !errors.Is(err, ErrNotFound) {
		t.Fatal("from an area to an area")
	}
	must(st.SaveBlock(ctx, dev, id, AreaHeader, menu, map[string]string{"logo": "АКМЕ"}))
	x, _ = st.Site(ctx, id)
	if len(x.Header) != 1 || x.Header[0].Get("logo") != "АКМЕ" || len(x.Footer) != 1 || x.Footer[0].ID != fid {
		t.Fatalf("areas: %+v %+v", x.Header, x.Footer)
	}
	if p, _ := st.Page(ctx, id, home.ID); len(p.Blocks) != 1 {
		t.Fatalf("the menu stays on the page: %+v", p.Blocks)
	}
	must(st.PublishSite(ctx, id, true))
	_, pub, _, err := st.Public(ctx, "acme", "oops")
	must(err)
	if len(pub.Blocks) != 3 || pub.Blocks[0].ID != menu || pub.Blocks[1].Type != "title" || pub.Blocks[2].ID != fid {
		t.Fatalf("a public page with the areas: %+v", pub.Blocks)
	}
	must(st.BlockAction(ctx, dev, id, AreaFooter, fid, "hide"))
	if _, pub, _, _ = st.Public(ctx, "acme", "oops"); len(pub.Blocks) != 2 {
		t.Fatal("a hidden footer block is shown")
	}

	// the own domain: only a published site
	if got := st.ByDomain(ctx, "acme.example.ru"); got != "acme" {
		t.Fatalf("by domain: %q", got)
	}
	must(st.PublishSite(ctx, id, false))
	if got := st.ByDomain(ctx, "acme.example.ru"); got != "" {
		t.Fatalf("an unpublished site by its domain: %q", got)
	}

	// a copy: the header, the settings but the domain, its own 404 page
	cp, err := st.CopySite(ctx, dev, id, "Копия")
	must(err)
	c, _ := st.Site(ctx, cp)
	cpages, _ := st.Pages(ctx, cp)
	if len(c.Header) != 1 || c.Domain != "" || c.Font != "serif" || c.Page404 == "" || c.Page404 == oops.ID || cpages[1].ID != c.Page404 || !cpages[0].SEO.NoIndex {
		t.Fatalf("a copy: %+v", c)
	}
}

// TestForm: the fields of a form block are cleaned by kind; the answers keep the asked fields only, need the required
// ones and a real e-mail; robots are caught by the trap and the time; one address is limited.
// TestBookingBlock: the block «Онлайн-запись» keeps the token of a link only (GO24DEV-688).
func TestBookingBlock(t *testing.T) {
	tok := "0123456789abcdef0123456789abcdef"
	list := normalize([]Block{{Type: "booking", Data: map[string]string{"title": "Запись", "booking": tok, "user": "x"}},
		{Type: "booking", Data: map[string]string{"booking": `"><script>`}}, {Type: "booking", Data: map[string]string{"booking": "ABC"}}})
	if len(list) != 3 || list[0].Get("booking") != tok || list[0].Get("title") != "Запись" || list[0].Get("user") != "" ||
		list[1].Get("booking") != "" || list[2].Get("booking") != "" {
		t.Fatalf("cleaned: %+v", list)
	}
}

func TestForm(t *testing.T) {
	b := normalize([]Block{{Type: "form", Data: map[string]string{"ask_name": "req", "ask_phone": "maybe", "ask_email": "on",
		"responsible": "'; DROP TABLE x", "button": "Жду"}}})[0]
	if b.Asks("name") != "req" || b.Asks("phone") != "" || b.Asks("email") != "on" || b.Get("responsible") != "" {
		t.Fatalf("cleaned: %+v", b.Data)
	}
	form := func(v map[string]string) func(string) string { return func(k string) string { return v[k] } }
	if _, err := b.Answer(form(map[string]string{"email": "a@b.ru"})); err == nil {
		t.Fatal("no required name")
	}
	if _, err := b.Answer(form(map[string]string{"name": "Ира", "email": "не адрес"})); err == nil {
		t.Fatal("a wrong e-mail")
	}
	ans, err := b.Answer(form(map[string]string{"name": " Ира\nПетрова ", "phone": "+7 999", "email": "ira@b.ru"}))
	if err != nil || ans["name"] != "Ира Петрова" || ans["phone"] != "" || ans["email"] != "ira@b.ru" {
		t.Fatalf("answer: %v %+v", err, ans)
	}
	p := Page{Blocks: []Block{b, {ID: "h", Type: "form", Hidden: true}, {ID: "t", Type: "text"}}}
	if _, ok := p.FormBlock(b.ID); !ok {
		t.Fatal("the form block")
	}
	for _, id := range []string{"h", "t", ""} {
		if _, ok := p.FormBlock(id); ok {
			t.Fatalf("FormBlock(%q): a hidden one or not a form", id)
		}
	}

	now := time.Now()
	for _, c := range []struct {
		trap, stamp string
		ok          bool
	}{{"", FormStamp(now.Add(-10 * time.Second)), true}, {"x", FormStamp(now.Add(-10 * time.Second)), false},
		{"", FormStamp(now), false}, {"", "", false}, {"", FormStamp(now.Add(-48 * time.Hour)), false}} {
		if Human(c.trap, c.stamp, now) != c.ok {
			t.Errorf("Human(%q, %q) != %v", c.trap, c.stamp, c.ok)
		}
	}
	h := &Hits{Max: 2, Per: time.Minute}
	if !h.Allow("a", now) || !h.Allow("a", now) || h.Allow("a", now) || !h.Allow("b", now) || !h.Allow("a", now.Add(2*time.Minute)) {
		t.Fatal("the limit of an address")
	}
}

// TestManage: the history of a page and of the header with rolling back, the trash of pages and sites (free addresses
// on the way back, purging by time), the rights of people and departments.
func TestManage(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, dev, olga, guest := newUser("boss"), newUser("dev"), newUser("olga"), newUser("guest")
	st := NewStore(pool)
	as := func(u string) context.Context { return tasks.WithViewer(ctx, u, false) }
	id, err := st.CreateSite(ctx, boss, "Акме", "acme", "Главная", []Block{{Type: "title", Data: map[string]string{"title": "Было"}}})
	must(err)
	x, _ := st.Site(ctx, id)
	home := x.HomeID

	// history: the state before the first change, then every change; a revert is a change of its own
	b, err := st.AddBlock(as(dev), dev, id, home, "text", 1, nil)
	must(err)
	must(st.SaveBlock(as(dev), dev, id, home, b, map[string]string{"text": "Новый текст"}))
	must(st.BlockAction(as(dev), dev, id, home, b, "hide"))
	must(st.BlockAction(as(dev), dev, id, home, b, "hide"))
	revs, err := st.Revisions(ctx, id, home)
	must(err)
	acts := []string{}
	for _, r := range revs {
		acts = append(acts, r.Act+":"+r.BlockType)
	}
	if strings.Join(acts, " ") != "show:text hide:text save:text add:text initial:" || revs[0].Author != "dev" {
		t.Fatalf("history: %v %+v", acts, revs[0])
	}
	must(st.Revert(as(dev), dev, id, home, revs[len(revs)-1].ID))
	if p, _ := st.Page(ctx, id, home); len(p.Blocks) != 1 || p.Blocks[0].Get("title") != "Было" {
		t.Fatalf("after the revert: %+v", p.Blocks)
	}
	if revs, _ = st.Revisions(ctx, id, home); revs[0].Act != "revert" || len(revs) != 6 {
		t.Fatalf("the revert in the history: %+v", revs[0])
	}
	_, err = st.AddBlock(ctx, dev, id, AreaHeader, "menu", 0, nil)
	must(err)
	if revs, _ := st.Revisions(ctx, id, AreaHeader); len(revs) != 2 || revs[1].Act != "initial" || revs[0].Count != 1 {
		t.Fatalf("the history of the header: %+v", revs)
	}
	if revs, _ := st.Revisions(ctx, id, home); len(revs) != 6 {
		t.Fatal("the header's history went to the page")
	}
	for i := 0; i < keepRevisions+5; i++ {
		must(st.BlockAction(ctx, dev, id, AreaHeader, firstID(t, st, id), "hide"))
	}
	if revs, _ := st.Revisions(ctx, id, AreaHeader); len(revs) != keepRevisions {
		t.Fatalf("history kept %d", len(revs))
	}

	// rights: everyone may publish by default; closed to all — only the creator, the people and departments given
	if x, _ := st.Site(as(dev), id); x.Role != RolePublish || !x.CanPublish() || x.CanManage() {
		t.Fatalf("role by default: %v", x.Role)
	}
	var sales, east string
	must(pool.QueryRow(ctx, `INSERT INTO departments (name) VALUES ('Продажи') RETURNING id`).Scan(&sales))
	must(pool.QueryRow(ctx, `INSERT INTO departments (name, parent_id) VALUES ('Восток', $1) RETURNING id`, sales).Scan(&east))
	_, err = pool.Exec(ctx, `INSERT INTO user_departments (user_id, department_id) VALUES ($1, $2)`, olga, east)
	must(err)
	if err := st.SetAccess(as(dev), dev, id, RoleNone, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a publisher changed the rights: %v", err)
	}
	must(st.SetAccess(as(boss), boss, id, RoleNone, map[Role][]string{RoleView: {dev, "dept:" + sales}, RoleEdit: {"dept:" + sales}, RoleFull: {boss}}))
	everyone, list, err := st.Access(as(boss), id)
	must(err)
	if everyone != RoleNone || len(list) != 2 || list[0].Role != RoleEdit || list[0].Name != "Продажи" || list[1].ID != dev {
		t.Fatalf("access: %v %+v", everyone, list)
	}
	if x, _ := st.Site(as(dev), id); x.Role != RoleView || x.CanEdit() {
		t.Fatalf("dev: %v", x.Role)
	}
	if x, _ := st.Site(as(olga), id); x.Role != RoleEdit || x.CanPublish() {
		t.Fatalf("olga of a sub-department: %v", x.Role)
	}
	if _, err := st.Site(as(guest), id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed site seen: %v", err)
	}
	if list, _ := st.Sites(as(guest)); len(list) != 0 {
		t.Fatal("closed site listed")
	}
	if _, err := st.AddBlock(as(dev), dev, id, home, "text", 0, nil); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a viewer edited: %v", err)
	}
	if err := st.PublishSite(as(olga), id, true); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an editor published: %v", err)
	}
	if _, err := st.Revisions(as(dev), id, home); err != nil {
		t.Fatalf("a viewer sees the history: %v", err)
	}
	if err := st.Revert(as(dev), dev, id, home, revs[1].ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a viewer rolled back: %v", err)
	}
	if err := st.DeleteSite(as(olga), olga, id); !errors.Is(err, ErrForbidden) {
		t.Fatalf("an editor deleted the site: %v", err)
	}
	page, err := st.CreatePage(as(olga), olga, id, "О нас", "about", nil)
	must(err)

	// the trash: a page, then the site; their addresses taken meanwhile are given up for free ones
	must(st.DeletePage(as(olga), olga, id, page))
	if _, err := st.Page(ctx, id, page); !errors.Is(err, ErrNotFound) {
		t.Fatal("a page in the trash opens")
	}
	again, err := st.CreatePage(as(olga), olga, id, "О нас", "about", nil)
	must(err)
	if tr, _ := st.Trash(as(dev)); len(tr) != 0 {
		t.Fatalf("a viewer sees the trash: %+v", tr)
	}
	tr, err := st.Trash(as(olga))
	must(err)
	if len(tr) != 1 || tr[0].PageID != page || tr[0].DeletedBy != "olga" {
		t.Fatalf("trash: %+v", tr)
	}
	if _, err := st.RestorePage(as(dev), dev, page); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a viewer restored: %v", err)
	}
	if site, err := st.RestorePage(as(olga), olga, page); err != nil || site != id {
		t.Fatalf("restore a page: %v", err)
	}
	if p, _ := st.Page(ctx, id, page); p.Slug != "about-2" {
		t.Fatalf("restored page at %q", p.Slug)
	}
	must(st.DeletePage(ctx, olga, id, again))
	must(st.PurgePage(as(olga), olga, again))

	must(st.PublishSite(ctx, id, true))
	must(st.SaveSettings(ctx, id, Settings{Domain: "acme.example.ru"}))
	must(st.DeleteSite(as(boss), boss, id))
	if _, _, _, err := st.Public(ctx, "acme", ""); !errors.Is(err, ErrNotFound) {
		t.Fatal("a site in the trash opens")
	}
	if st.ByDomain(ctx, "acme.example.ru") != "" {
		t.Fatal("a site in the trash opens on its domain")
	}
	other, err := st.CreateSite(ctx, dev, "Новый Акме", "acme", "Главная", nil)
	must(err)
	must(st.SaveSettings(ctx, other, Settings{Domain: "acme.example.ru"}))
	if tr, _ := st.Trash(as(olga)); len(tr) != 0 {
		t.Fatalf("an editor sees a deleted site: %+v", tr)
	}
	if tr, _ := st.Trash(as(boss)); len(tr) != 1 || tr[0].PageID != "" {
		t.Fatalf("the creator's trash: %+v", tr)
	}
	slug, err := st.RestoreSite(as(boss), boss, id)
	must(err)
	if x, _ := st.Site(ctx, id); slug != "acme-2" || x.Slug != slug || x.Domain != "" || x.Pages != 2 {
		t.Fatalf("restored site: %q %+v", slug, x)
	}
	// purging by time
	must(st.DeleteSite(ctx, dev, other))
	must(st.DeletePage(ctx, boss, id, page))
	if n, err := st.PurgeExpired(ctx, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("purged fresh ones: %d %v", n, err)
	}
	if n, err := st.PurgeExpired(ctx, time.Now().Add(time.Minute)); err != nil || n != 2 {
		t.Fatalf("purged %d %v", n, err)
	}
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM sites_all WHERE deleted_at IS NOT NULL`).Scan(&left))
	if left != 0 {
		t.Fatal("expired sites left")
	}
}

func firstID(t *testing.T, st *Store, site string) string {
	t.Helper()
	x, err := st.Site(context.Background(), site)
	if err != nil || len(x.Header) == 0 {
		t.Fatalf("header: %v", err)
	}
	return x.Header[0].ID
}
