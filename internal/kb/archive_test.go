package kb

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestNatLessParseMD(t *testing.T) {
	names := []string{"10 Итоги", "2 План", "01 Начало", "b", "A"}
	slices.SortFunc(names, func(a, b string) int {
		if natLess(a, b) {
			return -1
		}
		if natLess(b, a) {
			return 1
		}
		return 0
	})
	if strings.Join(names, "|") != "01 Начало|2 План|10 Итоги|A|b" {
		t.Fatalf("natural order = %q", names)
	}
	for _, c := range []struct{ in, title, body string }{
		{"---\ntitle: \"Про \\\"кавычки\\\"\"\n---\n\nтекст", `Про "кавычки"`, "текст"},
		{"---\ntitle: 'It''s'\ntags: [a]\n---\nтекст", "It's", "текст"},
		{"\ufeff# Заголовок\r\n\r\nтекст", "Заголовок", "текст"},
		{"просто текст", "имя", "просто текст"},
	} {
		if title, body := parseMD([]byte(c.in), "имя"); title != c.title || body != c.body {
			t.Errorf("parseMD(%q) = %q, %q", c.in, title, body)
		}
	}
}

// TestKBArchive: a base goes out as a ZIP of .md files in folders with its pictures, and the ZIP comes back into
// another base as the same tree with links to its new pages and files; separate .md files are imported too; one
// who does not write in the base imports nothing.
func TestKBArchive(t *testing.T) {
	ctx, pool, must, newUser := testDB(t)
	boss, other := newUser("boss"), newUser("other")
	s := NewStore(pool)

	src, err := s.CreateSpace(ctx, boss, "Отдел продаж", "", "")
	must(err)
	var file string
	must(pool.QueryRow(ctx, `INSERT INTO files (object_key, name, size, content_type, kb_space_id) VALUES ('t/plan', 'план.png', 3, 'image/png', $1)
		RETURNING id`, src).Scan(&file))
	one, err := s.CreatePage(ctx, boss, src, "", "Начало", "")
	must(err)
	two, err := s.CreatePage(ctx, boss, src, "", "Скрипты/звонки", "текст")
	must(err)
	_, err = s.CreatePage(ctx, boss, src, two, "Холодный звонок", "см. [начало](/kb/"+src+"/"+one+"#цели)")
	must(err)
	must(s.SavePage(ctx, boss, one, "Начало", "См. [скрипты](/kb/"+src+"/"+two+") и ![план](/files/"+file+"?inline=1)"))

	var buf bytes.Buffer
	open := func(_ context.Context, _ *string, key string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("PNG:" + key)), nil
	}
	must(s.Export(ctx, other, src, &buf, open))
	z, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	must(err)
	got := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		b, _ := io.ReadAll(r)
		got[f.Name] = string(b)
	}
	pic := "_files/" + file[:8] + "-план.png"
	if len(got) != 4 || got[pic] != "PNG:t/plan" {
		t.Fatalf("archive = %v", got)
	}
	if b := got["01 Начало.md"]; b != "---\ntitle: \"Начало\"\n---\n\nСм. [скрипты](02%20%D0%A1%D0%BA%D1%80%D0%B8%D0%BF%D1%82%D1%8B_%D0%B7%D0%B2%D0%BE%D0%BD%D0%BA%D0%B8.md) и ![план](_files/"+file[:8]+"-%D0%BF%D0%BB%D0%B0%D0%BD.png)" {
		t.Fatalf("01 Начало.md = %q", b)
	}
	if b := got["02 Скрипты_звонки/01 Холодный звонок.md"]; !strings.Contains(b, "](../01%20%D0%9D%D0%B0%D1%87%D0%B0%D0%BB%D0%BE.md#цели)") ||
		!strings.Contains(b, `title: "Холодный звонок"`) {
		t.Fatalf("a subpage = %q", b)
	}

	// Back into another base, under a page of it.
	dst, err := s.CreateSpace(ctx, boss, "Копия", "", "")
	must(err)
	under, err := s.CreatePage(ctx, boss, dst, "", "Архив", "")
	must(err)
	var put []string
	putBytes := func(_ context.Context, key string, r io.Reader, _ int64, ct string) error {
		b, _ := io.ReadAll(r)
		put = append(put, ct+":"+string(b))
		return nil
	}
	files, err := ReadArchive(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	must(err)
	if _, err := s.Import(ctx, other, dst, "", files, putBytes); err != nil {
		t.Fatalf("one who writes in a company base could not import: %v", err)
	}
	n, err := s.Import(ctx, boss, dst, under, files, putBytes)
	must(err)
	if n != 3 || len(put) != 2 || put[0] != "image/png:PNG:t/plan" {
		t.Fatalf("imported %d pages, stored %v", n, put)
	}
	tree, err := s.Tree(ctx, dst)
	must(err)
	var arch *Page
	for _, p := range tree {
		if p.ID == under {
			arch = p
		}
	}
	if arch == nil || len(arch.Children) != 2 || arch.Children[0].Title != "Начало" || arch.Children[1].Title != "Скрипты/звонки" ||
		len(arch.Children[1].Children) != 1 || arch.Children[1].Children[0].Title != "Холодный звонок" {
		t.Fatalf("tree = %+v", arch)
	}
	p, _, err := s.Page(ctx, boss, arch.Children[0].ID)
	must(err)
	var newFile string
	must(pool.QueryRow(ctx, `SELECT id FROM files WHERE kb_space_id = $1 ORDER BY created_at DESC LIMIT 1`, dst).Scan(&newFile))
	if p.Body != "См. [скрипты](/kb/"+dst+"/"+arch.Children[1].ID+") и ![план](/files/"+newFile+"?inline=1)" {
		t.Fatalf("body = %q", p.Body)
	}
	p, _, err = s.Page(ctx, boss, arch.Children[1].Children[0].ID)
	must(err)
	if p.Body != "см. [начало](/kb/"+dst+"/"+arch.Children[0].ID+"#цели)" {
		t.Fatalf("subpage body = %q", p.Body)
	}

	// Separate .md files; a folder without its .md is an empty page.
	n, err = s.Import(ctx, boss, dst, "", []ImportFile{{Name: "2 Б.md", Data: []byte("# Второй\n\nтекст")},
		{Name: "10 В/x.md", Data: []byte("икс")}}, putBytes)
	must(err)
	if n != 3 {
		t.Fatalf("imported %d", n)
	}
	if _, err := s.Import(ctx, boss, dst, "", []ImportFile{{Name: "a.png", Data: []byte("x")}}, putBytes); err == nil {
		t.Fatal("imported without pages")
	}

	// Read-only rights: the base is seen, nothing is imported.
	must(s.SetRight(ctx, boss, dst, "user", other, "read"))
	if _, err := s.Import(ctx, other, dst, "", files, putBytes); err != ErrForbidden {
		t.Fatalf("one who only reads imported: %v", err)
	}
}
