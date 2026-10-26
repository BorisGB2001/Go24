package kb

import (
	"errors"
	"strings"
	"testing"
)

// TestKBStructure: a page moves under another one, into another space with its sub-pages, never under itself; goes
// up and down among its neighbours; is copied right after itself; an earlier version comes back.
func TestKBStructure(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, other := newUser("boss"), newUser("other")
	s := NewStore(pool)
	a, err := s.CreateSpace(ctx, boss, "А", "", "")
	must(err)
	b, err := s.CreateSpace(ctx, boss, "Б", "", "")
	must(err)
	page := func(space, parent, title string) string {
		id, err := s.CreatePage(ctx, boss, space, parent, title, "текст "+title)
		must(err)
		return id
	}
	one, two, three := page(a, "", "Один"), page(a, "", "Два"), page(a, "", "Три")
	child := page(a, one, "Вложенная")
	titles := func(space string) (out []string) {
		tree, _ := s.Tree(ctx, space)
		for _, p := range tree {
			out = append(out, p.Title)
		}
		return out
	}
	eq := func(got []string, want ...string) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("pages = %v, want %v", got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("pages = %v, want %v", got, want)
			}
		}
	}

	must(s.MoveUp(ctx, boss, three, true))
	eq(titles(a), "Один", "Три", "Два")
	must(s.MoveUp(ctx, boss, one, true)) // already first
	must(s.MoveUp(ctx, boss, one, false))
	eq(titles(a), "Три", "Один", "Два")

	if err := s.MovePage(ctx, boss, one, a, child); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a page went under its own sub-page: %v", err)
	}
	if err := s.MovePage(ctx, boss, one, a, one); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a page went under itself: %v", err)
	}
	if err := s.MovePage(ctx, boss, one, b, two); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a parent of another space: %v", err)
	}
	must(s.MovePage(ctx, boss, two, a, three))
	eq(titles(a), "Три", "Один")
	if p, _, _ := s.Page(ctx, boss, two); p.ParentID != three {
		t.Fatalf("moved page = %+v", p)
	}
	must(s.MovePage(ctx, boss, one, b, ""))
	eq(titles(b), "Один")
	if p, sp, _ := s.Page(ctx, boss, child); sp.ID != b || p.ParentID != one {
		t.Fatalf("the sub-page did not go along: %+v in %s", p, sp.ID)
	}
	targets, err := s.Targets(ctx, boss, one)
	must(err)
	for _, tg := range targets {
		if tg.PageID == one || tg.PageID == child {
			t.Fatalf("the page itself is a target: %+v", targets)
		}
	}
	if len(targets) != 4 { // А, Три, Два (under Три), Б
		t.Fatalf("targets = %+v", targets)
	}

	must(s.SetRight(ctx, boss, b, "all", "", "read"))
	if err := s.MovePage(ctx, other, three, b, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("moved into a space one only reads: %v", err)
	}

	cp, err := s.CopyPage(ctx, boss, three, "Три (копия)")
	must(err)
	eq(titles(a), "Три", "Три (копия)")
	if p, _, _ := s.Page(ctx, boss, cp); p.Body != "текст Три" {
		t.Fatalf("copy = %+v", p)
	}

	must(s.SavePage(ctx, boss, three, "Три", "новый текст"))
	vs, _ := s.Versions(ctx, three)
	must(s.RestoreVersion(ctx, boss, three, vs[0].ID))
	if p, _, _ := s.Page(ctx, boss, three); p.Body != "текст Три" {
		t.Fatalf("restored = %+v", p)
	}
	if vs, _ := s.Versions(ctx, three); len(vs) != 2 {
		t.Fatalf("the text before the restore is not in the history: %+v", vs)
	}
}

// TestKBPlace: a page dragged in the tree — before a page, under one (last there), at the top; not under itself;
// the way down the tree for «хлебные крошки».
func TestKBPlace(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, reader := newUser("boss"), newUser("reader")
	s := NewStore(pool)
	a, err := s.CreateSpace(ctx, boss, "А", "", "")
	must(err)
	page := func(parent, title string) string {
		id, err := s.CreatePage(ctx, boss, a, parent, title, "")
		must(err)
		return id
	}
	one, two, three := page("", "Один"), page("", "Два"), page("", "Три")
	child := page(one, "Вложенная")
	shape := func() string { // the tree as «Один(Вложенная) Два …»
		tree, err := s.Tree(ctx, a)
		must(err)
		var walk func([]*Page) string
		walk = func(list []*Page) string {
			var out []string
			for _, p := range list {
				if p.Children != nil {
					out = append(out, p.Title+"("+walk(p.Children)+")")
				} else {
					out = append(out, p.Title)
				}
			}
			return strings.Join(out, " ")
		}
		return walk(tree)
	}
	want := func(w string) {
		t.Helper()
		if got := shape(); got != w {
			t.Fatalf("tree = %s, want %s", got, w)
		}
	}

	must(s.PlacePage(ctx, boss, three, "", one)) // before «Один»
	want("Три Один(Вложенная) Два")
	must(s.PlacePage(ctx, boss, two, one, child)) // under «Один», before «Вложенная»
	want("Три Один(Два Вложенная)")
	must(s.PlacePage(ctx, boss, three, two, "")) // under «Два», last
	want("Один(Два(Три) Вложенная)")
	must(s.PlacePage(ctx, boss, two, "", "")) // to the top, last, with «Три» under it
	want("Один(Вложенная) Два(Три)")
	must(s.PlacePage(ctx, boss, two, "", two)) // before itself: stays last
	want("Один(Вложенная) Два(Три)")

	if err := s.PlacePage(ctx, boss, two, three, ""); !errors.Is(err, ErrForbidden) {
		t.Fatalf("under its own subpage: %v", err)
	}
	if err := s.PlacePage(ctx, boss, two, "00000000-0000-0000-0000-000000000000", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("under a missing page: %v", err)
	}
	must(s.SetRight(ctx, boss, a, "user", reader, "read"))
	if err := s.PlacePage(ctx, reader, two, "", one); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a reader drags: %v", err)
	}

	tree, err := s.Tree(ctx, a)
	must(err)
	path := Path(tree, three)
	if len(path) != 2 || path[0].Title != "Два" || path[1].Title != "Три" {
		t.Fatalf("path = %+v", path)
	}
	if Path(tree, "x") != nil || Descendants(path[0]) != 1 || Descendants(tree[0]) != 1 {
		t.Fatal("path of a missing page or the count of subpages")
	}
}
