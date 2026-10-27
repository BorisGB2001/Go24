package kb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestKBPageTemplates: a page is made from a template of go24 with {{date}}, {{user}} and {{title}} filled in; a page
// saved as a template gives a new page its text and a copy of its picture; base templates and page templates do not
// mix; who may only read a base makes no page from a template.
func TestKBPageTemplates(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, reader := newUser("boss"), newUser("reader")
	s := NewStore(pool)
	tr := func(key string) string {
		switch key {
		case "kb.ptpl.meeting.title":
			return "Встреча {{date}}"
		case "kb.ptpl.meeting.body":
			return "Дата: {{date}}, ведёт {{user}}: {{title}}"
		}
		return "«" + key + "»"
	}
	var copied int
	copyBytes := func(context.Context, *string, string, string, int64, string) error { copied++; return nil }
	vars := PageVars{Date: "27.10.2026", User: "Борис"}

	list, err := s.PageTemplates(ctx, boss, tr)
	must(err)
	if len(list) != len(pageBuiltins) || list[0].ID != "p:meeting" || !list[0].Builtin || list[0].Name != "«kb.ptpl.meeting»" {
		t.Fatalf("page templates = %+v", list)
	}
	sp, err := s.CreateSpace(ctx, boss, "Отдел", "", "")
	must(err)
	id, err := s.CreatePageFromTemplate(ctx, boss, sp, "", "p:meeting", "", vars, tr, copyBytes)
	must(err)
	p, _, err := s.Page(ctx, boss, id)
	must(err)
	if p.Title != "Встреча 27.10.2026" || p.Body != "Дата: 27.10.2026, ведёт Борис: Встреча 27.10.2026" {
		t.Fatalf("page from go24's template = %q / %q", p.Title, p.Body)
	}
	// The title given; the user's name from go24 when not given.
	sub, err := s.CreatePageFromTemplate(ctx, boss, sp, id, "p:meeting", "Планёрка", PageVars{Date: "1"}, tr, copyBytes)
	must(err)
	if p, _, err = s.Page(ctx, boss, sub); err != nil || p.Title != "Планёрка" || p.ParentID != id || !strings.HasSuffix(p.Body, "ведёт boss: Планёрка") {
		t.Fatalf("subpage = %+v, %v", p, err)
	}
	if _, err := s.CreatePageFromTemplate(ctx, boss, sp, "", "p:nope", "", vars, tr, copyBytes); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an unknown template: %v", err)
	}

	// A page of the company with a picture becomes a template.
	var file string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, kb_space_id) VALUES ('t/'||gen_random_uuid(), 'схема.png', 3, 'image/png', $1)
		RETURNING id`, sp).Scan(&file))
	src, err := s.CreatePage(ctx, boss, sp, "", "Отчёт", "![схема](/files/"+file+"?inline=1) за {{date}}")
	must(err)
	must(s.SetRight(ctx, boss, sp, "all", "", "read"))
	if _, err := s.SavePageTemplate(ctx, reader, src, "", "", copyBytes); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a reader saved a template: %v", err)
	}
	tpl, err := s.SavePageTemplate(ctx, boss, src, "", "Еженедельный", copyBytes)
	must(err)
	list, err = s.PageTemplates(ctx, reader, tr)
	must(err)
	got := list[len(list)-1]
	if got.ID != tpl || got.Name != "Отчёт" || got.Description != "Еженедельный" || got.Builtin || got.CanDelete || got.Author != "boss" {
		t.Fatalf("saved page template = %+v", got)
	}
	bases, err := s.Templates(ctx, boss, tr)
	must(err)
	for _, b := range bases {
		if b.ID == tpl {
			t.Fatal("a page template among the templates of bases")
		}
	}
	if _, err := s.CreateFromTemplate(ctx, boss, tpl, "Х", "", "", tr, copyBytes); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a base from a page template: %v", err)
	}

	// The source page and its picture may go: the new page gets its own copy.
	if _, err := s.DeletePage(ctx, boss, src); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreatePageFromTemplate(ctx, reader, sp, "", tpl, "", vars, tr, copyBytes); !errors.Is(err, ErrForbidden) {
		t.Fatalf("a reader made a page: %v", err)
	}
	np, err := s.CreatePageFromTemplate(ctx, boss, sp, "", tpl, "", vars, tr, copyBytes)
	must(err)
	p, _, err = s.Page(ctx, boss, np)
	must(err)
	var nfile string
	must(pool.QueryRow(ctx, `SELECT id FROM files WHERE kb_space_id = $1 AND id <> $2`, sp, file).Scan(&nfile))
	if p.Title != "Отчёт" || p.Body != "![схема](/files/"+nfile+"?inline=1) за 27.10.2026" || copied != 2 {
		t.Fatalf("page from the company's template = %q / %q, copied %d", p.Title, p.Body, copied)
	}
	if err := s.DeleteTemplate(ctx, reader, tpl); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another one deleted the template: %v", err)
	}
	must(s.DeleteTemplate(ctx, boss, tpl))
	if _, err := s.CreatePageFromTemplate(ctx, boss, sp, "", tpl, "", vars, tr, copyBytes); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a page from a deleted template: %v", err)
	}
}
