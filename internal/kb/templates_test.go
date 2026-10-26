package kb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestKBTemplates: a base is made from a template of go24 with its tree of pages; a base saved as a template gives
// a new base the same pages, copies of their files and links between the copies; the template is deleted by who
// saved it.
func TestKBTemplates(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, other := newUser("boss"), newUser("other")
	s := NewStore(pool)
	tr := func(key string) string { return "«" + key + "»" }
	var copied []string
	copyBytes := func(_ context.Context, _ *string, from, to string, _ int64, _ string) error {
		copied = append(copied, from+" → "+to)
		return nil
	}

	list, err := s.Templates(ctx, boss, tr)
	must(err)
	if len(list) != len(builtins) || list[0].ID != "b:onboarding" || list[0].Pages != 6 || !list[0].Builtin {
		t.Fatalf("templates = %+v", list)
	}
	sp, err := s.CreateFromTemplate(ctx, boss, "b:onboarding", "Новичкам", "", "", tr, copyBytes)
	must(err)
	tree, err := s.Tree(ctx, sp)
	must(err)
	if len(tree) != 4 || tree[0].Title != "«kb.tpl.onboarding.welcome.title»" || len(tree[0].Children) != 2 {
		t.Fatalf("tree = %+v", tree)
	}
	if _, err := s.CreateFromTemplate(ctx, boss, "b:nope", "Х", "", "", tr, copyBytes); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown template: %v", err)
	}

	// A base of its own: a page linking to another and to a picture of the base.
	src, err := s.CreateSpace(ctx, boss, "Отдел продаж", "Как мы продаём", "")
	must(err)
	var file string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, kb_space_id) VALUES ('t/'||gen_random_uuid(), 'план.png', 3, 'image/png', $1)
		RETURNING id`, src).Scan(&file))
	two, err := s.CreatePage(ctx, boss, src, "", "Скрипты", "текст")
	must(err)
	one, err := s.CreatePage(ctx, boss, src, "", "Начало", "")
	must(err)
	must(s.SavePage(ctx, boss, one, "Начало", "См. [скрипты](/kb/"+src+"/"+two+") и ![план](/files/"+file+"?inline=1)"))
	_, err = s.CreatePage(ctx, boss, src, two, "Холодный звонок", "")
	must(err)
	if _, err := s.SaveTemplate(ctx, other, src, "", "", copyBytes); err == nil {
		t.Fatal("one who does not manage the base saved it as a template")
	}
	tpl, err := s.SaveTemplate(ctx, boss, src, "", "", copyBytes)
	must(err)
	list, err = s.Templates(ctx, other, tr)
	must(err)
	got := list[len(list)-1]
	if got.ID != tpl || got.Name != "Отдел продаж" || got.Description != "Как мы продаём" || got.Pages != 3 || got.CanDelete || got.Builtin {
		t.Fatalf("saved template = %+v", got)
	}

	// Anyone makes a base from it; the source base may go.
	must(s.DeleteSpace(ctx, boss, src))
	nsp, err := s.CreateFromTemplate(ctx, other, tpl, "Продажи-2", "", "", tr, copyBytes)
	must(err)
	tree, err = s.Tree(ctx, nsp)
	must(err)
	if len(tree) != 2 || tree[0].Title != "Скрипты" || len(tree[0].Children) != 1 || tree[1].Title != "Начало" {
		t.Fatalf("tree from the template = %+v", tree)
	}
	p, _, err := s.Page(ctx, other, tree[1].ID)
	must(err)
	var nfile string
	must(pool.QueryRow(ctx, `SELECT id FROM files WHERE kb_space_id = $1`, nsp).Scan(&nfile))
	if want := "См. [скрипты](/kb/" + nsp + "/" + tree[0].ID + ") и ![план](/files/" + nfile + "?inline=1)"; p.Body != want {
		t.Fatalf("body = %q, want %q", p.Body, want)
	}
	if len(copied) != 2 || !strings.HasPrefix(copied[0], "t/") {
		t.Fatalf("copied = %v", copied)
	}

	if err := s.DeleteTemplate(ctx, other, tpl); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another one deleted the template: %v", err)
	}
	must(s.DeleteTemplate(ctx, boss, tpl))
	var left int
	must(pool.QueryRow(ctx, `SELECT count(*) FROM files WHERE kb_template_id IS NOT NULL OR kb_space_id = $1`, nsp).Scan(&left))
	if left != 1 { // the template's copy is gone, the new base's stays
		t.Fatalf("files left = %d", left)
	}

	// An empty base is not a template.
	empty, err := s.CreateSpace(ctx, boss, "Пусто", "", "")
	must(err)
	if _, err := s.SaveTemplate(ctx, boss, empty, "", "", copyBytes); err == nil {
		t.Fatal("an empty base saved as a template")
	}
}
