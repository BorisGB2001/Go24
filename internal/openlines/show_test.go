package openlines

import (
	"context"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"go24/internal/crm"
	"go24/internal/db"
)

// TestShowsOn: the «Правила показа» pages — only the listed or all but the listed, «*» — any characters, as is
// ignoring the case, the «/» at the end and the anchor.
func TestShowsOn(t *testing.T) {
	only := Widget{PagesMode: "only", Pages: []string{"https://shop.ru/catalog/*", "https://shop.ru/about", "*?utm_source=*"}}
	for page, want := range map[string]bool{
		"https://shop.ru/catalog/": true, "https://shop.ru/catalog/shoes/1": true, "https://shop.ru/About/": true,
		"https://shop.ru/about#team": true, "https://shop.ru/": false, "https://shop.ru/about/us": false,
		"https://other.ru/?utm_source=ya": true, "https://shop.ru/catalog": false,
	} {
		if got := only.ShowsOn(page); got != want {
			t.Errorf("only: ShowsOn(%q) = %v, want %v", page, got, want)
		}
	}
	except := Widget{PagesMode: "except", Pages: []string{"*/cart*", "https://shop.ru/a*b*c"}}
	for page, want := range map[string]bool{"https://shop.ru/cart": false, "https://shop.ru/cart/2": false, "https://shop.ru/": true,
		"https://shop.ru/aXbYc": false, "https://shop.ru/abcd": true} {
		if got := except.ShowsOn(page); got != want {
			t.Errorf("except: ShowsOn(%q) = %v, want %v", page, got, want)
		}
	}
	if !(Widget{PagesMode: "all", Pages: []string{"x"}}).ShowsOn("https://any") {
		t.Error("«all» hides")
	}
	w := Widget{PagesMode: "only", Pages: []string{" ", ""}}
	if err := cleanShow(&w); err == nil {
		t.Error("«only» without pages is taken")
	}
	w = Widget{HelloOn: true, HelloText: "  "}
	if err := cleanShow(&w); err == nil {
		t.Error("an invitation without a text is taken")
	}
	w = Widget{Position: "middle"}
	if err := cleanShow(&w); err == nil {
		t.Error("a wrong corner is taken")
	}
	w = Widget{Pages: []string{" https://a ", ""}, ShowDelay: 5}
	if err := cleanShow(&w); err != nil || w.Position != "bottom-right" || w.PagesMode != "all" || !slices.Equal(w.Pages, []string{"https://a"}) {
		t.Fatalf("cleanShow = %v, %+v", err, w)
	}
}

// TestWidgetShow: the look and «Правила показа» are kept with the line; the photo of the inviting operator (the
// queue's first) is shown before they have answered.
func TestWidgetShow(t *testing.T) {
	url := os.Getenv("GO24_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GO24_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
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
	var boss, a string
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email) VALUES ('boss', 'boss', 'boss@t') RETURNING id`).Scan(&boss))
	must(pool.QueryRow(ctx, `INSERT INTO users (oidc_sub, name, email, photo_key, photo_type) VALUES ('a', 'a', 'a@t', 'k1', 'image/png') RETURNING id`).Scan(&a))
	s := NewStore(pool, crm.NewStore(pool))
	s.Tr = func(key string, args ...any) string { return key }
	wid, err := s.SaveWidget(ctx, boss, Widget{Name: "Сайт", Active: true, Operators: []string{a}, Position: "top-left", PagesMode: "except",
		Pages: []string{"*/cart*"}, ShowDelay: 3, HelloText: "Подсказать?", HelloDelay: 10, HideOffline: true})
	must(err)
	w, err := s.Widget(ctx, wid)
	must(err)
	if w.Position != "top-left" || w.PagesMode != "except" || !slices.Equal(w.Pages, []string{"*/cart*"}) || w.ShowDelay != 3 ||
		w.HelloOn || w.HelloText != "Подсказать?" || w.HelloDelay != 10 || !w.HideOffline {
		t.Fatalf("line = %+v", w)
	}
	if key, _, _ := s.OperatorPhoto(ctx, wid, a); key != "" {
		t.Fatal("the photo of an operator who has not answered and does not invite")
	}
	w.HelloOn = true
	_, err = s.SaveWidget(ctx, boss, w)
	must(err)
	if key, _, _ := s.OperatorPhoto(ctx, wid, a); key != "k1" {
		t.Fatalf("the inviting operator's photo = %q", key)
	}
	if key, _, _ := s.OperatorPhoto(ctx, wid, boss); key != "" {
		t.Fatal("the photo of someone else")
	}
}
