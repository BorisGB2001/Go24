package sites

import (
	"context"
	"errors"
	"os"
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
	if err := st.DeletePage(ctx, id, home); !isErr(err, "err.site_home_delete") {
		t.Fatalf("the home page deleted: %v", err)
	}
	must(st.SetHome(ctx, id, news))
	must(st.DeletePage(ctx, id, home))
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

// TestForm: the fields of a form block are cleaned by kind; the answers keep the asked fields only, need the required
// ones and a real e-mail; robots are caught by the trap and the time; one address is limited.
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
